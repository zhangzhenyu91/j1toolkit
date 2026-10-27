package treehazard

// 树障统计表内网 U 盘同步（与 server/src/treehazard/sync.js 同口径，编排层可注入）：
// 同步对象：TREEHAZARD_SYNC_TEAM 指定班组的全量记录 ↔ 被控机导出的「{班组名}树木统计表.xlsx」
// （Q 列记录ID 为行级比对锚点）。轮次：准备（snapshot 指令 + 挂载 U 盘）→ 等指令窗口 → 取件
// （下载内网表 → diff 比对回写：updates 覆盖 / news 插入 source='intranet' / 双边都改 → 冲突留档；
// 裁决 keep=server 的旧值不回灌防反转）→ 视差异清盘收尾或下发最新表 + replace 指令 → 同步日志。
// 设备链路经 UsbLink 注入（真实现由主装配对接 GLKVM fileshare；nil/未配置明确报错，不假成功）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrSyncNotConfigured 内网 U 盘同步未配置/链路未接入（明确返回，不假成功）。
var ErrSyncNotConfigured = errors.New("未配置同步设备（TREEHAZARD_SYNC_DEVICE_DDNS）")

// UsbLink 内网 U 盘同步设备链路（可注入；对应 GLKVM fileshare 的 mount/download/list/delete/push）。
type UsbLink interface {
	// MountToHost 挂载（共享）U 盘至被控机端。
	MountToHost(ctx context.Context) error
	// DownloadFile 下载 U 盘内文件；文件不存在返回 (nil, nil)（区别于链路故障报错）。
	DownloadFile(ctx context.Context, name string) ([]byte, error)
	// ListFiles 列 U 盘根目录文件名。
	ListFiles(ctx context.Context) ([]string, error)
	// DeleteFiles 删除 U 盘内文件（names 一次删多个）。
	DeleteFiles(ctx context.Context, names []string) error
	// PushFile 推送文件到 U 盘。
	PushFile(ctx context.Context, name string, data []byte) error
}

// SyncNotifier 同步结果站内通知（可注入；nil 跳过通知不影响同步）。
type SyncNotifier interface {
	Push(ctx context.Context, targets []string, title, content string) error
}

// 同步指令文件与命名常量（与 sync.js 一致）。
const (
	syncCmdFile        = "tree_sync_cmd.json"
	syncLockReason     = "树障统计表同步中（预计 10 分钟内恢复）"
	syncKindNameManual = "手动"
)

// IntranetSyncCounts 轮次计数（供摘要与 /sync/status 展示）。
type IntranetSyncCounts struct {
	Updates     int  `json:"updates"`
	News        int  `json:"news"`
	Conflicts   int  `json:"conflicts"`
	Skips       int  `json:"skips"`
	Adjudicated int  `json:"adjudicated"`
	Pushed      bool `json:"pushed"`
	NoFile      bool `json:"no_file"`
}

// IntranetSyncResult 一轮同步结果。
type IntranetSyncResult struct {
	OK      bool               `json:"ok"`
	Message string             `json:"message"`
	Counts  IntranetSyncCounts `json:"counts"`
}

// newSyncCycleID 同步轮次标识：yyyyMMdd-HHmm-xxxx（北京时间 + 4 位随机）。
func newSyncCycleID(now time.Time) string {
	cn := now.UTC().Add(8 * time.Hour)
	return fmt.Sprintf("%s-%04x", cn.Format("20060102-1504"), cn.UnixNano()&0xffff)
}

// syncCmdPayload 指令文件内容：snapshot 告知被控机导出内网表；replace 告知 U 盘表已是最新取用覆盖。
func syncCmdPayload(cycle, kind, fileName string, now time.Time) []byte {
	cn := now.UTC().Add(8 * time.Hour)
	payload := map[string]any{
		"version":   1,
		"cycle_id":  cycle,
		"kind":      kind,
		"file":      fileName,
		"issued_at": cn.Format("2006-01-02T15:04:05+08:00"),
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// syncRecordMap 服务端记录 → diff 比对 map（15 业务列 + status + 同步点时间戳）。
func syncRecordMap(r Record) map[string]any {
	m := map[string]any{
		"id": r.ID, "voltage_level": r.VoltageLevel, "line_name": r.LineName, "section": r.Section,
		"tree_species": r.TreeSpecies, "tree_count": r.TreeCount, "dist_small": r.DistSmall,
		"dist_big": r.DistBig, "diameter": r.Diameter, "horizontal": r.Horizontal, "vertical": r.Vertical,
		"vertical_num": r.VerticalNum, "finder_name": r.FinderName, "found_at": r.FoundAt,
		"handler_name": r.HandlerName, "handle_note": r.HandleNote, "handled_at": r.HandledAt,
		"status": r.Status,
	}
	if r.SyncedAt != nil {
		m["synced_at"] = r.SyncedAt.Format("2006-01-02 15:04:05")
	} else {
		m["synced_at"] = ""
	}
	m["updated_at"] = r.UpdatedAt.Format("2006-01-02 15:04:05")
	return m
}

// applyIntranetDiff 差异应用：
// updates 内网值覆盖 15 业务列（+vertical_num/status/synced_at，WHERE 带 team_id 防越班）；
// news 以 source='intranet' 插入（crane 双 NULL，pole 从区段文本解析，synced_at=NOW）；
// conflicts 落 treehazard_conflict 留档（人工二选一解决，本轮不改记录）。
func (m *Module) applyIntranetDiff(ctx context.Context, teamID int64, result DiffResult) error {
	for _, u := range result.Updates {
		f := u.Fields
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, tree_species = ?, tree_count = ?,
			   dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?, finder_name = ?, found_at = ?,
			   handler_name = ?, handle_note = ?, handled_at = ?, vertical_num = ?, status = ?, synced_at = NOW()
			 WHERE id = ? AND team_id = ?`,
			f["voltage_level"], f["line_name"], f["section"], f["tree_species"], f["tree_count"],
			f["dist_small"], f["dist_big"], f["diameter"], f["horizontal"], f["vertical"], f["finder_name"], f["found_at"],
			f["handler_name"], f["handle_note"], f["handled_at"], f["vertical_num"], f["status"], u.ID, teamID); err != nil {
			return err
		}
	}
	for _, row := range result.News {
		f := intranetFields(row)
		poleFrom, poleTo := parseSectionPoles(normStr(row["section"]))
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to,
			   tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
			   finder_name, found_at, handler_name, handle_note, handled_at, status, source, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'intranet', NOW())`,
			teamID, f["voltage_level"], f["line_name"], f["section"], poleFrom, poleTo,
			f["tree_species"], f["tree_count"], f["dist_small"], f["dist_big"], f["diameter"], f["horizontal"],
			f["vertical"], f["vertical_num"], f["finder_name"], f["found_at"], f["handler_name"], f["handle_note"],
			f["handled_at"], f["status"]); err != nil {
			return err
		}
	}
	for _, c := range result.Conflicts {
		serverSnap, _ := json.Marshal(c.Server)
		intranetSnap, _ := json.Marshal(c.Intranet)
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_conflict (team_id, record_id, server_snapshot, intranet_snapshot) VALUES (?, ?, ?, ?)`,
			teamID, c.ID, string(serverSnap), string(intranetSnap)); err != nil {
			return err
		}
	}
	return nil
}

// decideIntranetAction 下发判定（纯函数）：吸收了内网改动（更新/新增）、有已裁决驳回待收敛的行，
// 或服务端存在「同步点之后又改过 / 从未同步」的记录 → 需下发最新表。
func decideIntranetAction(result DiffResult, serverRecords []map[string]any) (needPush bool, reason string) {
	if absorbed := len(result.Updates) + len(result.News); absorbed > 0 {
		return true, fmt.Sprintf("吸收内网改动 %d 条", absorbed)
	}
	if result.Adjudicated > 0 {
		return true, fmt.Sprintf("裁决待收敛 %d 条", result.Adjudicated)
	}
	changed := 0
	for _, r := range serverRecords {
		syncedAt := diffParseDateMillis(r["synced_at"])
		if syncedAt == 0 {
			changed++ // 从未同步
			continue
		}
		if updatedAt := diffParseDateMillis(r["updated_at"]); updatedAt > syncedAt {
			changed++
		}
	}
	if changed > 0 {
		return true, fmt.Sprintf("服务端变更 %d 条", changed)
	}
	return false, "无差异"
}

// markSyncedAfterPush 下发后落同步点：全量记录 synced_at=NOW()；pending 冲突行除外
// （保人工裁决语义——代价是冲突未决每轮仍判服务端变更触发下发，可接受）。
func (m *Module) markSyncedAfterPush(ctx context.Context, teamID int64) error {
	_, err := m.DB.ExecContext(ctx,
		`UPDATE treehazard_record SET synced_at = NOW()
		 WHERE team_id = ? AND id NOT IN (
		   SELECT record_id FROM treehazard_conflict WHERE team_id = ? AND status = 'pending' AND record_id IS NOT NULL)`,
		teamID, teamID)
	return err
}

// cleanupUsb 取件收尾清扫（同步完不保留本操作产物）：删 U 盘根目录全部树表残留
// （任意「*树木统计表.xlsx」与指令文件）；best-effort，失败仅返回错误不阻断同步。
func cleanupUsb(ctx context.Context, link UsbLink) error {
	files, err := link.ListFiles(ctx)
	if err != nil {
		return err
	}
	victims := make([]string, 0)
	for _, n := range files {
		if strings.HasSuffix(n, "树木统计表.xlsx") || n == syncCmdFile {
			victims = append(victims, n)
		}
	}
	if len(victims) == 0 {
		return nil
	}
	return link.DeleteFiles(ctx, victims)
}

// pushLatest 下发最新表：单表口径排序 → push 表文件 + replace 指令 → 重挂回共享态（不等被控机取件确认）。
func (m *Module) pushLatest(ctx context.Context, link UsbLink, teamName, cycle string, records []Record) error {
	fileName := exportFileName(teamName, "")
	buf, err := buildWorkbook(teamName, records, false, nil)
	if err != nil {
		return err
	}
	if err := link.PushFile(ctx, fileName, buf); err != nil {
		return err
	}
	if err := link.PushFile(ctx, syncCmdFile, syncCmdPayload(cycle, "replace", fileName, time.Now())); err != nil {
		return err
	}
	return link.MountToHost(ctx)
}

// fetchIntranetPhase 取件与比对回写：下载内网表 → diff（带已裁决驳回快照防反转）→ 应用 →
// 收尾清盘 → 视差异决定收尾或下发最新表（收尾顺序统一「先清扫、后下发」）。
func (m *Module) fetchIntranetPhase(ctx context.Context, link UsbLink, teamID int64, teamName, cycle string) (IntranetSyncCounts, error) {
	counts := IntranetSyncCounts{}
	fileName := exportFileName(teamName, "")
	records, err := m.loadTeamRecords(ctx, teamID)
	if err != nil {
		return counts, err
	}
	serverRecords := make([]map[string]any, 0, len(records))
	for _, r := range records {
		serverRecords = append(serverRecords, syncRecordMap(r))
	}
	deletedIDs := make([]int64, 0)
	if rows, err := m.DB.QueryContext(ctx,
		`SELECT id FROM treehazard_deleted_id WHERE team_id = ?`, teamID); err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				deletedIDs = append(deletedIDs, id)
			}
		}
		rows.Close()
	}

	buf, err := link.DownloadFile(ctx, fileName)
	if err != nil {
		return counts, err
	}
	if buf == nil {
		// 内网无表（首次同步/被控机未导出）：清残留后直接下发最新表（含空表），内网以服务端为准起步
		_ = cleanupUsb(ctx, link)
		if err := m.pushLatest(ctx, link, teamName, cycle, records); err != nil {
			return counts, err
		}
		if err := m.markSyncedAfterPush(ctx, teamID); err != nil {
			return counts, err
		}
		counts.Pushed, counts.NoFile = true, true
		return counts, nil
	}
	rows, err := ParseIntranetSheet(buf)
	if err != nil {
		return counts, err
	}
	// 已裁决驳回（keep=server）的冲突快照：裁决后内网旧表若未收敛（行与快照全等），驳回旧值不回灌防反转裁决。
	adjudicated := make([]Adjudicated, 0)
	if aRows, err := m.DB.QueryContext(ctx,
		`SELECT record_id, intranet_snapshot FROM treehazard_conflict
		 WHERE team_id = ? AND status = 'resolved' AND resolution = 'server' AND record_id IS NOT NULL`, teamID); err == nil {
		for aRows.Next() {
			var rid int64
			var snap string
			if aRows.Scan(&rid, &snap) == nil {
				if parsed := parseSnapshot(snap); len(parsed) > 0 {
					adjudicated = append(adjudicated, Adjudicated{RecordID: rid, Intranet: parsed})
				}
			}
		}
		aRows.Close()
	}
	result := DiffRecords(serverRecords, rows, deletedIDs, adjudicated)
	if err := m.applyIntranetDiff(ctx, teamID, result); err != nil {
		return counts, err
	}
	needPush, reason := decideIntranetAction(result, serverRecords)
	counts = IntranetSyncCounts{
		Updates: len(result.Updates), News: len(result.News), Conflicts: len(result.Conflicts),
		Skips: result.Skips, Adjudicated: result.Adjudicated,
	}
	if err := cleanupUsb(ctx, link); err != nil {
		return counts, err
	}
	if !needPush {
		// 无差异：消费完的表连同残留一并清盘后重挂回共享态收尾
		return counts, link.MountToHost(ctx)
	}
	fresh, err := m.loadTeamRecords(ctx, teamID) // 回写后的最新全量
	if err != nil {
		return counts, err
	}
	if err := m.pushLatest(ctx, link, teamName, cycle, fresh); err != nil {
		return counts, err
	}
	if err := m.markSyncedAfterPush(ctx, teamID); err != nil {
		return counts, err
	}
	counts.Pushed = true
	_ = reason
	return counts, nil
}

// buildIntranetSummary 结果摘要：'新增n 更新n 冲突n [裁决收敛n ]已下发' / '… 无差异'；失败 '失败：原因'。
func buildIntranetSummary(counts IntranetSyncCounts, err error) string {
	if err != nil {
		return "失败：" + err.Error()
	}
	parts := []string{fmt.Sprintf("新增%d", counts.News), fmt.Sprintf("更新%d", counts.Updates), fmt.Sprintf("冲突%d", counts.Conflicts)}
	if counts.Adjudicated > 0 {
		parts = append(parts, fmt.Sprintf("裁决收敛%d", counts.Adjudicated))
	}
	if counts.Pushed {
		if counts.NoFile {
			parts = append(parts, "内网无表·已下发")
		} else {
			parts = append(parts, "已下发")
		}
	} else {
		parts = append(parts, "无差异")
	}
	return strings.Join(parts, " ")
}

// syncLogAndNotify 写同步日志 + 结果通知（超管 + 本班班组管理员）；通知/日志失败仅记录不影响结果。
func (m *Module) syncLogAndNotify(ctx context.Context, teamID int64, teamName, kind string, ok bool, text string) {
	if _, err := m.DB.ExecContext(ctx,
		`INSERT INTO treehazard_sync_log (team_id, kind, result) VALUES (?, ?, ?)`, teamID, kind, text); err != nil {
		_ = err
	}
	if m.SyncNotifier == nil {
		return
	}
	title := fmt.Sprintf("树障统计表同步完成（%s）", teamName)
	if !ok {
		title = "树障统计表同步失败"
	}
	kindName := syncKindNameManual
	if kind != "manual" {
		kindName = kind
	}
	_ = m.SyncNotifier.Push(ctx, []string{"admin", "team_admin"}, title,
		fmt.Sprintf("方式：%s\n%s", kindName, text))
}

// RunIntranetCycle 完整同步轮次（手动/空闲/夜间共用）：准备（snapshot 指令 + 挂载）→ 等指令窗口 →
// 取件比对回写/下发 → 日志通知。未配置/链路未接入返回 ErrSyncNotConfigured（明确返回而非假成功）。
func (m *Module) RunIntranetCycle(ctx context.Context, kind string) (IntranetSyncResult, error) {
	if kind == "" {
		kind = "manual"
	}
	if m.Usb == nil {
		return IntranetSyncResult{OK: false, Message: "同步链路未接入（UsbLink 未注入）"},
			fmt.Errorf("%w：同步链路未接入", ErrSyncNotConfigured)
	}
	ddns := m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS")
	if ddns == "" {
		return IntranetSyncResult{OK: false, Message: ErrSyncNotConfigured.Error()}, ErrSyncNotConfigured
	}
	teamName := m.configValue(ctx, "TREEHAZARD_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var teamID int64
	var matchedName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE name = ? AND status = 1`, teamName).Scan(&teamID, &matchedName); err != nil {
		e := fmt.Errorf("未找到同步班组「%s」（未建或已停用）", teamName)
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	cycle := newSyncCycleID(time.Now())
	fileName := exportFileName(matchedName, "")
	// 准备：push snapshot 指令 → 挂载 U 盘至被控机端。
	if err := m.Usb.PushFile(ctx, syncCmdFile, syncCmdPayload(cycle, "snapshot", fileName, time.Now())); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if err := m.Usb.MountToHost(ctx); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	// 等指令窗口（TREEHAZARD_SYNC_CMD_WINDOW_MIN 分钟，默认 5；测试注入 0）。
	window := 5
	if v := m.configValue(ctx, "TREEHAZARD_SYNC_CMD_WINDOW_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			window = n
		}
	}
	if window > 0 {
		timer := time.NewTimer(time.Duration(window) * time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			e := ctx.Err()
			return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
		case <-timer.C:
		}
	}
	// 取件比对回写/下发。
	counts, err := m.fetchIntranetPhase(ctx, m.Usb, teamID, matchedName, cycle)
	text := buildIntranetSummary(counts, err)
	m.syncLogAndNotify(ctx, teamID, matchedName, kind, err == nil, text)
	if err != nil {
		return IntranetSyncResult{OK: false, Message: text, Counts: counts}, err
	}
	return IntranetSyncResult{OK: true, Message: text, Counts: counts}, nil
}

// IntranetSyncStatus 同步状态（enabled：已配置设备且链路已接入；running：轮次进行中；
// last：本班最近一条 sync_log；nextNight：下次夜间取件时间（北京时间 ISO），未启用为 nil）。
func (m *Module) IntranetSyncStatus(ctx context.Context) map[string]any {
	enabled := m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS") != "" && m.Usb != nil
	m.syncMu.Lock()
	running := m.syncRunning
	m.syncMu.Unlock()
	out := map[string]any{"enabled": enabled, "running": running, "last": nil, "nextNight": nil}
	if !enabled {
		return out
	}
	teamName := m.configValue(ctx, "TREEHAZARD_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var kind, result, createdAt string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT l.kind, l.result, DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i:%s') FROM treehazard_sync_log l
		 JOIN sys_team t ON t.id = l.team_id WHERE t.name = ? ORDER BY l.id DESC LIMIT 1`, teamName).
		Scan(&kind, &result, &createdAt); err == nil {
		out["last"] = map[string]any{"kind": kind, "result": result, "created_at": createdAt}
	}
	// 下次夜间取件时间（按 TREEHAZARD_SYNC_FETCH_TIME 排程口径换算为北京时间 ISO）。
	fetchTime := m.configValue(ctx, "TREEHAZARD_SYNC_FETCH_TIME")
	if fetchTime == "" {
		fetchTime = "00:00"
	}
	hh, mm := 0, 0
	fmt.Sscanf(fetchTime, "%d:%d", &hh, &mm)
	now := time.Now()
	cn := now.UTC().Add(8 * time.Hour)
	next := time.Date(cn.Year(), cn.Month(), cn.Day(), hh, mm, 0, 0, time.UTC)
	if !next.After(cn) {
		next = next.Add(24 * time.Hour)
	}
	out["nextNight"] = next.Format("2006-01-02T15:04:05+08:00")
	return out
}

// handleSyncPushNow POST /sync/push-now：手动触发一轮完整同步（异步执行：含指令窗口等待，
// 不宜挂 HTTP 等待；结果走通知与 /sync/status）。未配置明确 50301，不假成功。
func (m *Module) handleSyncPushNow(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := requireManagerNow(w, r); !ok {
		return
	}
	ctx := r.Context()
	if m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS") == "" {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, "未配置同步设备", nil)
		return
	}
	m.syncMu.Lock()
	if m.syncRunning {
		m.syncMu.Unlock()
		writeFail(w, http.StatusLocked, 42301, "同步进行中，请稍后再试", nil)
		return
	}
	m.syncRunning = true
	m.syncMu.Unlock()
	go func() {
		defer func() {
			m.syncMu.Lock()
			m.syncRunning = false
			m.syncMu.Unlock()
		}()
		_, _ = m.RunIntranetCycle(context.Background(), "manual") // 内部已全 catch + 通知
	}()
	writeOK(w, map[string]any{"started": true}, "同步已开始")
}

// handleSyncStatus GET /sync/status：{enabled, running, last, nextNight}（登录即可，供工作台展示）。
func (m *Module) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	writeOK(w, m.IntranetSyncStatus(r.Context()), "")
}
