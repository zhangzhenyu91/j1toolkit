// 运维线路关联：把同塔双回等始终同塔、杆号一致的线路并入一个统一线路名。
// 关联后 team_line 只保留统一线路，team_line_link 记录原线路名 → 统一线路名；
// 树障/交跨历史记录同步改名，后续录入、更正、导出与内网/知识库同步均按统一线路处理。
package teamdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"shade/internal/linecatalog"
)

// oplineRow 关联用线路行（id + 电压 + 名称 + 排序）。
type oplineRow struct {
	ID      int64
	Voltage string
	Name    string
	Sort    int64
}

// rowQueryer *sql.DB / *sql.Tx 共用的查询接口。
type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// parseLineIDs 解析 ids 数组（数字；去重保序；非正整数忽略）。
func parseLineIDs(raw json.RawMessage) ([]int64, error) {
	if len(raw) == 0 {
		return nil, errors.New("ids 缺失")
	}
	var nums []float64
	if err := json.Unmarshal(raw, &nums); err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(nums))
	for _, f := range nums {
		if f <= 0 || f != float64(int64(f)) {
			continue
		}
		id := int64(f)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("ids 无效")
	}
	return out, nil
}

// loadOplinesByIDs 按 id 读取本班线路（sort/id 序）。
func loadOplinesByIDs(ctx context.Context, q rowQueryer, teamID int64, ids []int64) ([]oplineRow, error) {
	marks := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, teamID)
	for _, id := range ids {
		marks = append(marks, "?")
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id, voltage_level, line_name, sort FROM team_line
		 WHERE team_id = ? AND id IN (`+strings.Join(marks, ",")+`) ORDER BY sort, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]oplineRow, 0, len(ids))
	for rows.Next() {
		var r oplineRow
		if err := rows.Scan(&r.ID, &r.Voltage, &r.Name, &r.Sort); err != nil {
			return nil, err
		}
		r.Name = strings.TrimSpace(r.Name)
		out = append(out, r)
	}
	return out, rows.Err()
}

// oplineByName 按统一线路名精确读取本班线路；不存在返回 (nil, nil)。
func oplineByName(ctx context.Context, q rowQueryer, teamID int64, name string) (*oplineRow, error) {
	var r oplineRow
	err := q.QueryRowContext(ctx,
		`SELECT id, voltage_level, line_name, sort FROM team_line WHERE team_id = ? AND line_name = ?`,
		teamID, name).Scan(&r.ID, &r.Voltage, &r.Name, &r.Sort)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Name = strings.TrimSpace(r.Name)
	return &r, nil
}

// commonLineVoltage 选中线路的公共电压：返回首个非空原文；归一后不一致 → ok=false。
func commonLineVoltage(rows []oplineRow) (voltage string, ok bool) {
	norm := ""
	for _, r := range rows {
		raw := strings.TrimSpace(r.Voltage)
		n := linecatalog.NormalizeVoltage(raw)
		if n == "" {
			continue
		}
		if norm == "" {
			norm, voltage = n, raw
			continue
		}
		if n != norm {
			return "", false
		}
	}
	return voltage, true
}

// suggestLinkedName 由所选线路自动推导统一名称：
// 最长公共前缀之后必须是纯罗马数字（可带「线」），按罗马数字顺序拼接。
// 例：峰义Ⅰ线 + 峰义Ⅱ线 → 峰义ⅠⅡ线；推导不出返回空串（要求手填）。
func suggestLinkedName(rows []oplineRow) string {
	if len(rows) < 2 {
		return ""
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		n := linecatalog.Normalize(r.Name)
		if n == "" {
			return ""
		}
		names = append(names, n)
	}
	prefix := commonRunePrefix(names)
	parts := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		rest := strings.TrimPrefix(n, prefix)
		rest = strings.TrimSuffix(rest, "线")
		if rest == "" || !isRomanRunes(rest) {
			return ""
		}
		if !seen[rest] {
			seen[rest] = true
			parts = append(parts, rest)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.SliceStable(parts, func(i, j int) bool {
		return romanCompare(parts[i], parts[j]) < 0
	})
	return prefix + strings.Join(parts, "") + "线"
}

// commonRunePrefix 多个字符串的最长公共前缀（按 rune）。
func commonRunePrefix(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	prefix := []rune(vals[0])
	for _, v := range vals[1:] {
		r := []rune(v)
		n := 0
		for n < len(prefix) && n < len(r) && prefix[n] == r[n] {
			n++
		}
		prefix = prefix[:n]
		if len(prefix) == 0 {
			return ""
		}
	}
	return string(prefix)
}

var romanRuneRank = map[rune]int{
	'Ⅰ': 1, 'Ⅱ': 2, 'Ⅲ': 3, 'Ⅳ': 4, 'Ⅴ': 5,
	'Ⅵ': 6, 'Ⅶ': 7, 'Ⅷ': 8, 'Ⅸ': 9, 'Ⅹ': 10,
	'I': 1, 'V': 5, 'X': 10,
}

// isRomanRunes 是否全部为罗马数字字符。
func isRomanRunes(s string) bool {
	for _, r := range s {
		if _, ok := romanRuneRank[r]; !ok {
			return false
		}
	}
	return true
}

// romanCompare 罗马数字串按数值序列比较（Ⅰ < Ⅱ < ⅠⅡ < Ⅲ）。
func romanCompare(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if romanRuneRank[ra[i]] != romanRuneRank[rb[i]] {
			return romanRuneRank[ra[i]] - romanRuneRank[rb[i]]
		}
	}
	return len(ra) - len(rb)
}

// handleOplineLinkPreview POST /oplines/link/preview：返回关联建议名称与公共电压。
func (m *Module) oplineLinkPreview(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	ids, err := parseLineIDs(body["ids"])
	if err != nil || len(ids) < 2 {
		fail(w, http.StatusBadRequest, codeInput, "请选择至少两条线路")
		return
	}
	rows, err := loadOplinesByIDs(r.Context(), m.DB, team.ID, ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(rows) != len(ids) {
		fail(w, http.StatusNotFound, codeMissing, "部分线路不存在")
		return
	}
	voltage, ok := commonLineVoltage(rows)
	if !ok {
		fail(w, http.StatusBadRequest, codeInput, "所选线路电压等级不一致，无法关联")
		return
	}
	writeOK(w, map[string]any{"line_name": suggestLinkedName(rows), "voltage_level": voltage})
}

// handleOplineLink POST /oplines/link：关联线路并统一历史记录。
// body { ids, line_name? }；line_name 留空自动推导（推导不出则要求手填）。
func (m *Module) oplineLink(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	ids, err := parseLineIDs(body["ids"])
	if err != nil || len(ids) < 2 {
		fail(w, http.StatusBadRequest, codeInput, "请选择至少两条线路")
		return
	}
	target := truncate(linecatalog.Normalize(rawString(body["line_name"])), 64)

	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作

	rows, err := loadOplinesByIDs(r.Context(), tx, team.ID, ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(rows) != len(ids) {
		fail(w, http.StatusNotFound, codeMissing, "部分线路不存在")
		return
	}
	selVolt, ok := commonLineVoltage(rows)
	if !ok {
		fail(w, http.StatusBadRequest, codeInput, "所选线路电压等级不一致，无法关联")
		return
	}
	if target == "" {
		target = suggestLinkedName(rows)
	}
	if target == "" {
		fail(w, http.StatusBadRequest, codeInput, "无法自动生成统一线路名称，请手动填写")
		return
	}
	// 输入的是历史别名时归到已有关联线路；已有同归一名称的线路时沿用其规范名。
	cat, err := linecatalog.Load(r.Context(), tx, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if canonical, ok := cat.AliasTarget(target); ok {
		target = canonical
	} else if match := cat.ResolveExact(target); match.Status == "ok" {
		target = match.Name
	}

	existing, err := oplineByName(r.Context(), tx, team.ID, target)
	if err != nil {
		writeErr(w, err)
		return
	}
	finalVolt := selVolt
	if existing != nil && strings.TrimSpace(existing.Voltage) != "" {
		if selVolt != "" && linecatalog.NormalizeVoltage(existing.Voltage) != linecatalog.NormalizeVoltage(selVolt) {
			fail(w, http.StatusBadRequest, codeInput, "统一线路名称已存在且电压等级不一致")
			return
		}
		finalVolt = strings.TrimSpace(existing.Voltage)
	}
	if finalVolt == "" {
		fail(w, http.StatusBadRequest, codeInput, "所选线路缺少电压等级，请先补齐")
		return
	}

	targetID := int64(0)
	if existing == nil {
		sortVal := rows[0].Sort
		for _, row := range rows[1:] {
			if row.Sort < sortVal {
				sortVal = row.Sort
			}
		}
		res, err := tx.ExecContext(r.Context(),
			`INSERT INTO team_line (team_id, voltage_level, line_name, sort) VALUES (?, ?, ?, ?)`,
			team.ID, finalVolt, target, sortVal)
		if err != nil {
			if isDupEntry(err) {
				fail(w, http.StatusConflict, codeDup, "「"+target+"」已存在")
				return
			}
			writeErr(w, err)
			return
		}
		targetID, _ = res.LastInsertId()
	} else {
		targetID = existing.ID
		if strings.TrimSpace(existing.Voltage) == "" {
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE team_line SET voltage_level = ? WHERE id = ? AND team_id = ?`,
				finalVolt, targetID, team.ID); err != nil {
				writeErr(w, err)
				return
			}
		}
	}

	// 统一名称本身是规范线路，清掉历史上把它当别名的旧映射。
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM team_line_link WHERE team_id = ? AND alias_name = ?`, team.ID, target); err != nil {
		writeErr(w, err)
		return
	}
	// 选中线路原名称全部作为别名指向统一名称；已指向选中线路的旧别名一并改挂。
	selectedNames := make([]string, 0, len(rows))
	selectedIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		selectedNames = append(selectedNames, row.Name)
		if row.ID != targetID {
			selectedIDs = append(selectedIDs, row.ID)
		}
	}
	marks := make([]string, 0, len(selectedNames))
	args := make([]any, 0, len(selectedNames)+2)
	args = append(args, target, team.ID)
	for _, name := range selectedNames {
		marks = append(marks, "?")
		args = append(args, name)
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE team_line_link SET line_name = ? WHERE team_id = ? AND line_name IN (`+strings.Join(marks, ",")+`)`,
		args...); err != nil {
		writeErr(w, err)
		return
	}
	for _, row := range rows {
		if row.Name == target {
			continue
		}
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO team_line_link (team_id, alias_name, line_name) VALUES (?, ?, ?)
			 ON DUPLICATE KEY UPDATE line_name = VALUES(line_name)`,
			team.ID, row.Name, target); err != nil {
			writeErr(w, err)
			return
		}
	}
	if len(selectedIDs) > 0 {
		delMarks := make([]string, 0, len(selectedIDs))
		delArgs := make([]any, 0, len(selectedIDs)+1)
		delArgs = append(delArgs, team.ID)
		for _, id := range selectedIDs {
			delMarks = append(delMarks, "?")
			delArgs = append(delArgs, id)
		}
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM team_line WHERE team_id = ? AND id IN (`+strings.Join(delMarks, ",")+`)`,
			delArgs...); err != nil {
			writeErr(w, err)
			return
		}
	}

	// 关联生效后重载目录，把历史记录里指向原线路/别名的名称统一改写为规范名。
	cat, err = linecatalog.Load(r.Context(), tx, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	updated, err := canonicalizeRecordLines(r.Context(), tx, team.ID, cat)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"line_id": targetID, "line_name": target, "records_updated": updated,
	}, "已关联")
}

// canonicalizeRecordLines 树障/交跨正式记录按线路目录批量改名，返回更新行数。
// 只做精确/归一/别名匹配（不做前缀猜测），避免把短名称误并入其他线路。
func canonicalizeRecordLines(ctx context.Context, tx *sql.Tx, teamID int64, cat *linecatalog.Catalog) (int64, error) {
	var updated int64
	for _, table := range []string{"treehazard_record", "crossing_record"} {
		rows, err := tx.QueryContext(ctx,
			`SELECT DISTINCT line_name FROM `+table+` WHERE team_id = ?`, teamID)
		if err != nil {
			return updated, err
		}
		names := make([]string, 0)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return updated, err
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return updated, err
		}
		rows.Close()
		for _, name := range names {
			match := cat.ResolveExact(name)
			if match.Status != "ok" || match.Name == "" || match.Name == name {
				continue
			}
			res, err := tx.ExecContext(ctx,
				`UPDATE `+table+` SET line_name = ? WHERE team_id = ? AND line_name = ?`,
				match.Name, teamID, name)
			if err != nil {
				return updated, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				updated += n
			}
		}
	}
	return updated, nil
}
