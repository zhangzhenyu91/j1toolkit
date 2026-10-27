package crossing

// 内网交跨统计表解析与行级差异比对（与 server/src/crossing/diff.js 对齐，纯函数，不触库；
// 供内网 U 盘同步与冲突裁决复用）。内网表模板即导出件：R1 合并标题、R2 表头、O 列隐藏记录ID。

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// 比对列集：交跨业务 13 列（与 crossing_record / 内网表列序一致；交跨无处理三列）。
var (
	diffDiscoveryCols = []string{
		"voltage_level", "line_name", "section", "crossing_type", "dist_small", "dist_big",
		"cross_horizontal", "cross_vertical", "cross_angle", "has_hazard", "finder_name",
		"inspect_at", "remark",
	}
	// 数字类列：「3」与「3.0」视为相同（数值等值比较，任一侧空时按字符串比较）。
	diffNumericCols = map[string]bool{
		"voltage_level": true, "dist_small": true, "dist_big": true,
		"cross_horizontal": true, "cross_vertical": true, "cross_angle": true,
	}
	// 日期列：统一归一为 yyyy-MM-dd 比较。
	diffDateCols = map[string]bool{"inspect_at": true}
)

// normStr 字符串归一（nil → ”，trim）。
func normStr(v any) string {
	return strings.TrimSpace(toString(v))
}

// diffSameCell 单元格等值判定（按列类型走数字/日期/字符串口径）。
func diffSameCell(col string, a, b any) bool {
	if diffDateCols[col] {
		return normDateStr(normStr(a)) == normDateStr(normStr(b))
	}
	sa, sb := normStr(a), normStr(b)
	if sa == sb {
		return true
	}
	if diffNumericCols[col] && sa != "" && sb != "" {
		na, ea := strconv.ParseFloat(sa, 64)
		nb, eb := strconv.ParseFloat(sb, 64)
		if ea == nil && eb == nil {
			return na == nb
		}
	}
	return false
}

// toIntOr0 取整数值（缺失/非法 → 0）。
func toIntOr0(v any) int64 {
	if p := toIntOrNull(v); p != nil {
		return *p
	}
	return 0
}

// snapshotOf 服务端记录快照（冲突留档与 intranet 快照同形：id + 13 业务列）。
func snapshotOf(record map[string]any) map[string]any {
	snap := map[string]any{"id": toIntOr0(record["id"])}
	for _, c := range diffDiscoveryCols {
		if diffDateCols[c] {
			snap[c] = normDateStr(normStr(record[c]))
		} else {
			snap[c] = normStr(record[c])
		}
	}
	return snap
}

// intranetFields 内网行 → 回写服务端字段集（13 业务列；交跨无处理列/状态）。
func intranetFields(row map[string]any) map[string]any {
	fields := map[string]any{}
	for _, c := range diffDiscoveryCols {
		if diffDateCols[c] {
			fields[c] = nullIfEmpty(normDateStr(normStr(row[c])))
		} else {
			fields[c] = normStr(row[c])
		}
	}
	return fields
}

// bizKeySame 无 ID 行业务键比对：line_name + section + crossing_type + inspect_at 全等视为疑似重复。
func bizKeySame(serverRecord, row map[string]any) bool {
	for _, c := range []string{"line_name", "section", "crossing_type", "inspect_at"} {
		if !diffSameCell(c, serverRecord[c], row[c]) {
			return false
		}
	}
	return true
}

// diffParseDateMillis 时间戳比较（synced_at / updated_at 文本 → 序数；解析不出 → 0）。
var diffTimeRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})`)

func diffParseDateMillis(v any) int64 {
	s := normStr(v)
	m := diffTimeRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	h, _ := strconv.Atoi(m[4])
	mi, _ := strconv.Atoi(m[5])
	sec, _ := strconv.Atoi(m[6])
	return int64(((((y*12+mo)*31+d)*24+h)*60+mi)*60 + sec)
}

// DiffUpdate 内网改动吸收项（fields 为 intranetFields 产物）。
type DiffUpdate struct {
	ID     int64
	Fields map[string]any
}

// DiffConflict 双边都改/疑似重复冲突（server 快照 + intranet 行）。
type DiffConflict struct {
	ID       int64
	Server   map[string]any
	Intranet map[string]any
}

// Adjudicated 已裁决驳回（resolution='server'）的冲突快照。
type Adjudicated struct {
	RecordID int64
	Intranet map[string]any
}

// DiffResult 行级差异比对结果（Skips/Adjudicated 为计数）。
type DiffResult struct {
	Updates     []DiffUpdate
	News        []map[string]any
	Conflicts   []DiffConflict
	Skips       int
	Adjudicated int
}

// DiffRecords 行级差异比对（与 diff.js diffRecords 同规则）：
// 有 record_id 且服务端存在——列值相同 → skip；不同且（从未同步或服务端同步后又改）→ 冲突；
// 否则仅内网改过 → update；update 前行与任一已裁决驳回快照全等 → 驳回旧值不回灌。
// 有 record_id 服务端不存在——在墓碑 → skip；否则按无 ID 处理。同 record_id 多行：首行按上规则，其余按无 ID 处理。
// 无 record_id——业务键命中服务端 → 冲突（疑似重复）；否则 → news。
func DiffRecords(serverRecords, intranetRows []map[string]any, deletedIds []int64, adjudicated []Adjudicated) DiffResult {
	delSet := map[int64]bool{}
	for _, id := range deletedIds {
		delSet[id] = true
	}
	serverByID := map[int64]map[string]any{}
	for _, r := range serverRecords {
		serverByID[toIntOr0(r["id"])] = r
	}
	adjMap := map[int64][]map[string]any{}
	for _, a := range adjudicated {
		if a.RecordID <= 0 || a.Intranet == nil {
			continue
		}
		adjMap[a.RecordID] = append(adjMap[a.RecordID], a.Intranet)
	}
	res := DiffResult{
		Updates:   make([]DiffUpdate, 0),
		News:      make([]map[string]any, 0),
		Conflicts: make([]DiffConflict, 0),
	}
	seenIDs := map[int64]bool{}

	isAdjudicated := func(rid int64, row map[string]any) bool {
		for _, snap := range adjMap[rid] {
			allSame := true
			for _, c := range diffDiscoveryCols {
				if !diffSameCell(c, snap[c], row[c]) {
					allSame = false
					break
				}
			}
			if allSame {
				return true
			}
		}
		return false
	}
	handleNoID := func(row map[string]any) {
		for _, s := range serverRecords {
			if bizKeySame(s, row) {
				res.Conflicts = append(res.Conflicts, DiffConflict{ID: toIntOr0(s["id"]), Server: snapshotOf(s), Intranet: row})
				return
			}
		}
		res.News = append(res.News, row)
	}

	for _, row := range intranetRows {
		rid := toIntOr0(row["record_id"])
		if rid < 0 {
			rid = 0
		}
		if rid > 0 && !seenIDs[rid] {
			seenIDs[rid] = true
			srv, exists := serverByID[rid]
			if exists {
				changed := false
				for _, c := range diffDiscoveryCols {
					if !diffSameCell(c, srv[c], row[c]) {
						changed = true
						break
					}
				}
				if !changed {
					res.Skips++
					continue
				}
				syncedAt := diffParseDateMillis(srv["synced_at"])
				updatedAt := diffParseDateMillis(srv["updated_at"])
				// synced_at 空 = 从未同步（不一致即冲突）；updated_at>synced_at = 双边都改 → 冲突
				if syncedAt == 0 || (updatedAt != 0 && updatedAt > syncedAt) {
					res.Conflicts = append(res.Conflicts, DiffConflict{ID: rid, Server: snapshotOf(srv), Intranet: row})
				} else if isAdjudicated(rid, row) {
					res.Skips++
					res.Adjudicated++
				} else {
					res.Updates = append(res.Updates, DiffUpdate{ID: rid, Fields: intranetFields(row)})
				}
				continue
			}
			if delSet[rid] {
				res.Skips++
				continue
			}
			handleNoID(row) // 服务端不存在且不在墓碑：按无 ID 处理
			continue
		}
		handleNoID(row) // 无 record_id，或同 record_id 的重复行
	}
	return res
}

// ParseIntranetSheet 解析内网交跨统计表（buffer → 行对象数组）：
// 首个 sheet → 定位「序号」表头行（兼容两行表头模板）→ 数据行按列序映射；
// record_id 读 O 列（第 15 列），空/非法 → 缺省；日期归一 yyyy-MM-dd；整行业务列为空跳过。
func ParseIntranetSheet(data []byte) ([]map[string]any, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, nil
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	headIdx := -1
	for i, r := range rows {
		for _, c := range r {
			if strings.TrimSpace(c) == "序号" {
				headIdx = i
				break
			}
		}
		if headIdx >= 0 {
			break
		}
	}
	if headIdx < 0 {
		return nil, nil
	}
	cell := func(r []string, j int) string {
		if j < len(r) {
			return strings.TrimSpace(r[j])
		}
		return ""
	}
	kvTrim := regexp.MustCompile(`(?i)\s*kv$`)
	out := make([]map[string]any, 0)
	for i := headIdx + 1; i < len(rows); i++ {
		r := rows[i]
		anyBiz := false
		for j := 1; j <= 13; j++ {
			if cell(r, j) != "" {
				anyBiz = true
				break
			}
		}
		if !anyBiz {
			continue
		}
		row := map[string]any{
			"row_no": cell(r, 0),
			// 电压等级导出时拼了 kV 后缀，读回时剥掉与服务端原始值对齐
			"voltage_level":    kvTrim.ReplaceAllString(cell(r, 1), ""),
			"line_name":        cell(r, 2),
			"section":          cell(r, 3),
			"crossing_type":    cell(r, 4),
			"dist_small":       cell(r, 5),
			"dist_big":         cell(r, 6),
			"cross_horizontal": cell(r, 7),
			"cross_vertical":   cell(r, 8),
			"cross_angle":      cell(r, 9),
			"has_hazard":       cell(r, 10),
			"finder_name":      cell(r, 11),
			"inspect_at":       normDateStr(cell(r, 12)),
			"remark":           cell(r, 13),
		}
		if rid, err := strconv.ParseInt(cell(r, 14), 10, 64); err == nil && rid > 0 {
			row["record_id"] = rid
		}
		out = append(out, row)
	}
	return out, nil
}
