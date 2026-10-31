package treehazard

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"
)

// Record treehazard_record 全列（JSON 与 Node 行同形；日期列为 'YYYY-MM-DD' 文本）。
type Record struct {
	ID             int64       `json:"id"`
	TeamID         int64       `json:"team_id"`
	VoltageLevel   string      `json:"voltage_level"`
	LineName       string      `json:"line_name"`
	Section        string      `json:"section"`
	PoleFrom       *int64      `json:"pole_from"`
	PoleTo         *int64      `json:"pole_to"`
	PositionNote   string      `json:"position_note"`
	TreeSpecies    string      `json:"tree_species"`
	TreeCount      string      `json:"tree_count"`
	DistSmall      string      `json:"dist_small"`
	DistBig        string      `json:"dist_big"`
	Diameter       string      `json:"diameter"`
	Horizontal     string      `json:"horizontal"`
	Vertical       string      `json:"vertical"`
	VerticalNum    *float64    `json:"vertical_num"`
	FinderName     string      `json:"finder_name"`
	FoundAt        string      `json:"found_at"`
	HandlerName    string      `json:"handler_name"`
	HandleNote     string      `json:"handle_note"`
	HandledAt      string      `json:"handled_at"`
	Status         string      `json:"status"`
	Source         string      `json:"source"`
	CraneSessionID *string     `json:"crane_session_id"`
	CraneRecordID  *string     `json:"crane_record_id"`
	SyncedAt       *time.Time  `json:"synced_at"`
	CreatedBy      *int64      `json:"created_by"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Photos         []photoItem `json:"photos"`
}

// recordCols 记录全列查询（日期列格式化为 'YYYY-MM-DD' 文本，与 Node RECORD_COLS 一致）。
const recordCols = `id, team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
  tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
  finder_name, DATE_FORMAT(found_at, '%Y-%m-%d') AS found_at,
  handler_name, handle_note, DATE_FORMAT(handled_at, '%Y-%m-%d') AS handled_at,
  status, source, crane_session_id, crane_record_id, synced_at, created_by, created_at, updated_at`

func scanRecord(rows *sql.Rows) (Record, error) {
	var rec Record
	var poleFrom, poleTo sql.NullInt64
	var verticalNum sql.NullFloat64
	var foundAt, handledAt sql.NullString
	var sessionID, recordID sql.NullString
	var createdBy sql.NullInt64
	err := rows.Scan(
		&rec.ID, &rec.TeamID, &rec.VoltageLevel, &rec.LineName, &rec.Section, &poleFrom, &poleTo,
		&rec.PositionNote, &rec.TreeSpecies, &rec.TreeCount, &rec.DistSmall, &rec.DistBig,
		&rec.Diameter, &rec.Horizontal, &rec.Vertical, &verticalNum, &rec.FinderName, &foundAt,
		&rec.HandlerName, &rec.HandleNote, &handledAt, &rec.Status, &rec.Source, &sessionID, &recordID,
		&rec.SyncedAt, &createdBy, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		return rec, err
	}
	if poleFrom.Valid {
		rec.PoleFrom = &poleFrom.Int64
	}
	if poleTo.Valid {
		rec.PoleTo = &poleTo.Int64
	}
	if verticalNum.Valid {
		rec.VerticalNum = &verticalNum.Float64
	}
	rec.FoundAt = foundAt.String
	rec.HandledAt = handledAt.String
	if sessionID.Valid {
		rec.CraneSessionID = &sessionID.String
	}
	if recordID.Valid {
		rec.CraneRecordID = &recordID.String
	}
	if createdBy.Valid {
		rec.CreatedBy = &createdBy.Int64
	}
	rec.Photos = []photoItem{}
	return rec, nil
}

// loadTeamRecords 读取本班全部记录。
func (m *Module) loadTeamRecords(ctx context.Context, teamID int64) ([]Record, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT `+recordCols+` FROM treehazard_record WHERE team_id = ?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Record, 0)
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// attachPhotos 批量装配照片（一次 IN 查询）。
func (m *Module) attachPhotos(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]any, 0, len(records))
	placeholders := make([]string, 0, len(records))
	for i := range records {
		ids = append(ids, records[i].ID)
		placeholders = append(placeholders, "?")
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, record_id, kind, cos_key, url, uploaded_by, created_at FROM treehazard_photo
		 WHERE record_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byRecord := map[int64][]photoItem{}
	base := m.urlBase()
	for rows.Next() {
		var p photoItem
		var uploadedBy sql.NullInt64
		if err := rows.Scan(&p.ID, &p.RecordID, &p.Kind, &p.CosKey, &p.URL, &uploadedBy, &p.CreatedAt); err != nil {
			return err
		}
		if uploadedBy.Valid {
			p.UploadedBy = &uploadedBy.Int64
		}
		p.URL = base.join(p.CosKey, p.URL)
		byRecord[p.RecordID] = append(byRecord[p.RecordID], p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range records {
		records[i].Photos = byRecord[records[i].ID]
		if records[i].Photos == nil {
			records[i].Photos = []photoItem{}
		}
	}
	return nil
}

// fetchThreshold 本班垂直距离阈值（未配置 → nil）。
func (m *Module) fetchThreshold(ctx context.Context, teamID int64) (*float64, error) {
	var threshold sql.NullFloat64
	err := m.DB.QueryRowContext(ctx,
		`SELECT vertical_threshold FROM treehazard_team_setting WHERE team_id = ?`, teamID).Scan(&threshold)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if !threshold.Valid {
		return nil, nil
	}
	return &threshold.Float64, nil
}

// handleDashboard GET /dashboard：未处理 / 本周新增 / 超阈值 / 已处理 + threshold 与计数。
func (m *Module) handleDashboard(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeOK(w, map[string]any{
			"unhandled": 0, "week_new": 0, "over_threshold": 0, "handled": 0,
			"threshold": nil, "pending_conflicts": 0, "staging_batches": 0,
		}, "")
		return
	}
	teamID := team.ID
	threshold, err := m.fetchThreshold(r.Context(), teamID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "看板读取失败", nil)
		return
	}
	count := func(query string, args ...any) int64 {
		var n int64
		if err := m.DB.QueryRowContext(r.Context(), query, args...).Scan(&n); err != nil {
			return 0
		}
		return n
	}
	unhandled := count(`SELECT COUNT(*) FROM treehazard_record WHERE team_id = ? AND status = 'unhandled'`, teamID)
	weekNew := count(`SELECT COUNT(*) FROM treehazard_record WHERE team_id = ? AND created_at >= ?`,
		teamID, weekStartText(time.Now()))
	over := int64(0)
	if threshold != nil {
		over = count(`SELECT COUNT(*) FROM treehazard_record WHERE team_id = ? AND status = 'unhandled' AND vertical_num IS NOT NULL AND vertical_num <= ?`,
			teamID, *threshold)
	}
	handled := count(`SELECT COUNT(*) FROM treehazard_record WHERE team_id = ? AND status = 'handled'`, teamID)
	conflicts := count(`SELECT COUNT(*) FROM treehazard_conflict WHERE team_id = ? AND status = 'pending'`, teamID)
	staging := count(`SELECT COUNT(*) FROM treehazard_staging_batch WHERE team_id = ? AND status = 'pending'`, teamID)
	writeOK(w, map[string]any{
		"unhandled":         unhandled,
		"week_new":          weekNew,
		"over_threshold":    over,
		"handled":           handled,
		"threshold":         threshold,
		"pending_conflicts": conflicts,
		"staging_batches":   staging,
	}, "")
}

// handleRecords GET /records：按运维线路分组（team_line 导入序，未匹配排尾），组内 未处理在前 >
// 区段升序 > pole_from > created_at；运维线路全部成组下发（含 0 记录线路）。
func (m *Module) handleRecords(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeOK(w, map[string]any{"threshold": nil, "lines": []any{}, "unmatched": []any{}}, "")
		return
	}
	teamID := team.ID
	threshold, err := m.fetchThreshold(r.Context(), teamID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	lineRows, err := m.fetchLineOrder(r.Context(), teamID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	records, err := m.loadTeamRecords(r.Context(), teamID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	if err := m.attachPhotos(r.Context(), records); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	lineOrder := map[string]int{}
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	sorted := sortRecords(records, makeLineGroupComparator(lineOrder), false)
	byLine := map[string][]Record{}
	unmatched := make([]Record, 0)
	for _, rec := range sorted {
		if _, known := lineOrder[rec.LineName]; known {
			byLine[rec.LineName] = append(byLine[rec.LineName], rec)
		} else {
			unmatched = append(unmatched, rec)
		}
	}
	lines := make([]map[string]any, 0, len(lineRows))
	for _, l := range lineRows {
		recs := byLine[l.LineName]
		if recs == nil {
			recs = []Record{}
		}
		lines = append(lines, map[string]any{
			"voltage_level": l.VoltageLevel, "line_name": l.LineName, "records": recs,
		})
	}
	if unmatched == nil {
		unmatched = []Record{}
	}
	writeOK(w, map[string]any{"threshold": threshold, "lines": lines, "unmatched": unmatched}, "")
}

// handleRecordGet GET /records/:id：单条记录（含 photos），仅本班。
func (m *Module) handleRecordGet(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配", nil)
		return
	}
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT `+recordCols+` FROM treehazard_record WHERE id = ? AND team_id = ?`, recordID, team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		writeFail(w, http.StatusNotFound, codeNotFound, "记录不存在", nil)
		return
	}
	rec, err := scanRecord(rows)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	records := []Record{rec}
	if err := m.attachPhotos(r.Context(), records); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "记录读取失败", nil)
		return
	}
	writeOK(w, records[0], "")
}

// handleBody 处理入参（handle_note 必填；handler_name 默认当前用户昵称；handled_at 默认当日北京时间）。
type handleBody struct {
	HandlerName string
	Note        string
	HandledAt   string
}

// handleRecordHandle POST /records/:id/handle：单条处理。
func (m *Module) handleRecordHandle(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配", nil)
		return
	}
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	parsed, ok := readHandleBody(w, r)
	if !ok {
		return
	}
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?`, recordID, team.ID).Scan(&exist); err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "记录不存在", nil)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE treehazard_record SET status = 'handled', handler_name = ?, handle_note = ?, handled_at = ? WHERE id = ?`,
		parsed.HandlerName, parsed.Note, parsed.HandledAt, recordID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "处理失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": recordID}, "")
}

// handleRecordHandleBatch POST /records/handle-batch：批量处理（同事务逐条更新，仅本班 + 仅 unhandled）。
func (m *Module) handleRecordHandleBatch(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配", nil)
		return
	}
	// 一次解码完整结构（ids + 处理字段）：请求体只能读一次，先读处理字段再解码 ids 会读到 EOF。
	var body struct {
		IDs         []any  `json:"ids"`
		HandleNote  string `json:"handle_note"`
		HandlerName string `json:"handler_name"`
		HandledAt   string `json:"handled_at"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "ids 应为非空记录 id 数组", nil)
		return
	}
	parsed, ok := normalizeHandleBody(w, r, body.HandleNote, body.HandlerName, body.HandledAt)
	if !ok {
		return
	}
	ids := make([]int64, 0, len(body.IDs))
	for _, raw := range body.IDs {
		if n, ok := asInt64(raw); ok && n > 0 {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "ids 应为非空记录 id 数组", nil)
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "处理失败", nil)
		return
	}
	var updated int64
	for _, id := range ids {
		res, err := tx.ExecContext(r.Context(),
			`UPDATE treehazard_record SET status = 'handled', handler_name = ?, handle_note = ?, handled_at = ?
			 WHERE id = ? AND team_id = ? AND status = 'unhandled'`,
			parsed.HandlerName, parsed.Note, parsed.HandledAt, id, team.ID)
		if err != nil {
			_ = tx.Rollback()
			writeFail(w, http.StatusInternalServerError, codeParam, "处理失败", nil)
			return
		}
		n, _ := res.RowsAffected()
		updated += n
	}
	if err := tx.Commit(); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "处理失败", nil)
		return
	}
	writeOK(w, map[string]any{"updated": updated}, "")
}

// readHandleBody 解析处理入参并写失败响应；返回 false 表示已应答。
func readHandleBody(w http.ResponseWriter, r *http.Request) (handleBody, bool) {
	var body struct {
		HandleNote  string `json:"handle_note"`
		HandlerName string `json:"handler_name"`
		HandledAt   string `json:"handled_at"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "处理情况不能为空", nil)
		return handleBody{}, false
	}
	return normalizeHandleBody(w, r, body.HandleNote, body.HandlerName, body.HandledAt)
}

// normalizeHandleBody 校验/补齐处理字段（单条与批量共用同一口径）。
func normalizeHandleBody(w http.ResponseWriter, r *http.Request, noteRaw, nameRaw, atRaw string) (handleBody, bool) {
	note := strings.TrimSpace(noteRaw)
	if note == "" {
		writeFail(w, http.StatusBadRequest, codeParam, "处理情况不能为空", nil)
		return handleBody{}, false
	}
	ident, _ := identityFrom(r.Context())
	handlerName := strings.TrimSpace(nameRaw)
	if handlerName == "" {
		handlerName = ident.Nickname
	}
	handledAt := strings.TrimSpace(atRaw)
	if handledAt == "" {
		handledAt = beijingToday()
	}
	if !dateRe.MatchString(handledAt) {
		writeFail(w, http.StatusBadRequest, codeParam, "处理时间格式应为 YYYY-MM-DD", nil)
		return handleBody{}, false
	}
	return handleBody{
		HandlerName: truncateRunes(handlerName, 32),
		Note:        truncateRunes(note, 64),
		HandledAt:   handledAt,
	}, true
}

// handleRecordDelete DELETE /records/:id：班管/超管；删照片对象与表行、删记录、写删除墓碑。
func (m *Module) handleRecordDelete(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?`, recordID, team.ID).Scan(&exist); err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "记录不存在", nil)
		return
	}
	if err := m.deleteRecord(r.Context(), recordID, team.ID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "删除失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": recordID}, "")
}

// deleteRecord 整条删除（DELETE /records/:id 与直推 deletes 共用）：
// 删照片 COS 对象（失败仅记日志）→ 事务删照片表行 + 记录行 + 写 treehazard_deleted_id 墓碑。
func (m *Module) deleteRecord(ctx context.Context, recordID, teamID int64) error {
	rows, err := m.DB.QueryContext(ctx, `SELECT cos_key FROM treehazard_photo WHERE record_id = ?`, recordID)
	if err != nil {
		return err
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}
	rows.Close()
	for _, key := range keys {
		if m.Objects != nil && key != "" {
			if err := m.Objects.Delete(ctx, key); err != nil {
				log.Printf("[树障] 删除 COS 对象失败（继续删记录）：%v\n", err)
			}
		}
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM treehazard_photo WHERE record_id = ?`, recordID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM treehazard_record WHERE id = ?`, recordID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT IGNORE INTO treehazard_deleted_id (id, team_id) VALUES (?, ?)`, recordID, teamID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// handleSettingsGet GET /settings：{ vertical_threshold }（未配置 → null）。
func (m *Module) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeOK(w, map[string]any{"vertical_threshold": nil}, "")
		return
	}
	threshold, err := m.fetchThreshold(r.Context(), team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "设置读取失败", nil)
		return
	}
	writeOK(w, map[string]any{"vertical_threshold": threshold}, "")
}

// handleSettingsPut PUT /settings：班管/超管；body { vertical_threshold }（空 = 清除）。
func (m *Module) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	team, ident, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	var body struct {
		VerticalThreshold any `json:"vertical_threshold"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "阈值应为非负数字", nil)
		return
	}
	var val *float64
	if body.VerticalThreshold != nil {
		s := strings.TrimSpace(asString(body.VerticalThreshold))
		if s != "" {
			n, err := parseFloat(s)
			if err != nil || n < 0 {
				writeFail(w, http.StatusBadRequest, codeParam, "阈值应为非负数字", nil)
				return
			}
			val = &n
		}
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO treehazard_team_setting (team_id, vertical_threshold, updated_by) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE vertical_threshold = VALUES(vertical_threshold), updated_by = VALUES(updated_by)`,
		team.ID, val, ident.ID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "设置保存失败", nil)
		return
	}
	writeOK(w, map[string]any{"vertical_threshold": val}, "")
}
