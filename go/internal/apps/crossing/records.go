package crossing

import (
	"database/sql"
	"net/http"
	"time"
)

// Record 交跨记录行（JSON 字段与 Node mysql2 行对象 1:1；日期列已格式化为 YYYY-MM-DD 文本）。
type Record struct {
	ID              int64      `json:"id"`
	TeamID          int64      `json:"team_id"`
	VoltageLevel    string     `json:"voltage_level"`
	LineName        string     `json:"line_name"`
	Section         string     `json:"section"`
	PoleFrom        *int64     `json:"pole_from"`
	PoleTo          *int64     `json:"pole_to"`
	CrossingType    string     `json:"crossing_type"`
	DistSmall       string     `json:"dist_small"`
	DistBig         string     `json:"dist_big"`
	CrossHorizontal string     `json:"cross_horizontal"`
	CrossVertical   string     `json:"cross_vertical"`
	CrossAngle      string     `json:"cross_angle"`
	HasHazard       string     `json:"has_hazard"`
	FinderName      string     `json:"finder_name"`
	InspectAt       *string    `json:"inspect_at"`
	Remark          string     `json:"remark"`
	Source          string     `json:"source"`
	CraneSessionID  *string    `json:"crane_session_id"`
	CraneRecordID   *string    `json:"crane_record_id"`
	SyncedAt        *time.Time `json:"synced_at"`
	CreatedBy       *int64     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// recordCols 记录全列查询（日期列格式化为 'YYYY-MM-DD' 文本），与 Node RECORD_COLS 一致。
const recordCols = `id, team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
  dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
  finder_name, DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark,
  source, crane_session_id, crane_record_id, synced_at, created_by, created_at, updated_at`

// scanRecord 扫描一行完整记录。
func scanRecord(rows *sql.Rows) (Record, error) {
	var rec Record
	var poleFrom, poleTo sql.NullInt64
	var inspectAt sql.NullString
	var sessionID, recordID sql.NullString
	var syncedAt sql.NullTime
	var createdBy sql.NullInt64
	err := rows.Scan(&rec.ID, &rec.TeamID, &rec.VoltageLevel, &rec.LineName, &rec.Section,
		&poleFrom, &poleTo, &rec.CrossingType, &rec.DistSmall, &rec.DistBig,
		&rec.CrossHorizontal, &rec.CrossVertical, &rec.CrossAngle, &rec.HasHazard,
		&rec.FinderName, &inspectAt, &rec.Remark, &rec.Source, &sessionID, &recordID,
		&syncedAt, &createdBy, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	if poleFrom.Valid {
		rec.PoleFrom = &poleFrom.Int64
	}
	if poleTo.Valid {
		rec.PoleTo = &poleTo.Int64
	}
	if inspectAt.Valid {
		rec.InspectAt = &inspectAt.String
	}
	if sessionID.Valid {
		rec.CraneSessionID = &sessionID.String
	}
	if recordID.Valid {
		rec.CraneRecordID = &recordID.String
	}
	if syncedAt.Valid {
		rec.SyncedAt = &syncedAt.Time
	}
	if createdBy.Valid {
		rec.CreatedBy = &createdBy.Int64
	}
	return rec, nil
}

// loadTeamRecords 取本班全部记录。
func (m *Module) loadTeamRecords(teamID int64) ([]Record, error) {
	rows, err := m.DB.Query("SELECT "+recordCols+" FROM crossing_record WHERE team_id = ?", teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// weekStartText 北京时间本周一 00:00 的库内墙钟文本（库内 created_at 为 UTC 墙钟，北京周界减 8h 后比较）。
func weekStartText() string { return weekStartTextAt(time.Now()) }

// weekStartTextAt 同 weekStartText，时间源可注入（测试用）。
func weekStartTextAt(now time.Time) string {
	cn := now.UTC().Add(8 * time.Hour) // UTC 字段即北京墙钟（TZ 无关，同 Node getUTC* 口径）
	dow := (int(cn.Weekday()) + 6) % 7
	monday := time.Date(cn.Year(), cn.Month(), cn.Day()-dow, 0, 0, 0, 0, time.UTC)
	utcWall := monday.Add(-8 * time.Hour)
	return utcWall.Format("2006-01-02 15:04:05")
}

// dashboard GET /dashboard：记录总数 / 本周新增 / 存在隐患数 / 待确认线路数，
// 顺带返回 pending_conflicts / staging_batches 计数（无班组全 0）。
func (m *Module) dashboard(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{
			"total": 0, "week_new": 0, "hazard": 0, "pending_lines": 0,
			"pending_conflicts": 0, "staging_batches": 0,
		})
		return
	}
	teamID := team.ID
	var total, weekNew, hazard, pendingLines, conflicts, staging int64
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ?", teamID).Scan(&total); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ? AND created_at >= ?",
		teamID, weekStartText()).Scan(&weekNew); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS n FROM crossing_record WHERE team_id = ? AND has_hazard <> ''",
		teamID).Scan(&hazard); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(DISTINCT s.line_name) AS n FROM crossing_staging_record s
       JOIN crossing_staging_batch b ON b.id = s.batch_id
       WHERE b.team_id = ? AND b.status = 'pending'`, teamID).Scan(&pendingLines); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS n FROM crossing_conflict WHERE team_id = ? AND status = 'pending'",
		teamID).Scan(&conflicts); err != nil {
		writeErr(w, err)
		return
	}
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS n FROM crossing_staging_batch WHERE team_id = ? AND status = 'pending'",
		teamID).Scan(&staging); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"total": total, "week_new": weekNew, "hazard": hazard, "pending_lines": pendingLines,
		"pending_conflicts": conflicts, "staging_batches": staging,
	})
}

// recordList GET /records：本班全部记录，按运维线路（team_line 导入序）分组，未匹配排尾；
// 组内 区段升序 → pole_from 升序 → created_at；运维线路全部成组下发（含 0 记录线路）。
func (m *Module) recordList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"lines": []any{}, "unmatched": []any{}})
		return
	}
	lineRows, err := fetchLineOrder(m.DB, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	records, err := m.loadTeamRecords(team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	lineOrder := map[string]int{}
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	sorted := sortRecords(records, makeLineGroupComparator(lineOrder))
	byLine := map[string][]Record{}
	unmatched := []Record{}
	for _, rec := range sorted {
		if _, ok := lineOrder[rec.LineName]; ok {
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
			"voltage_level": l.VoltageLevel,
			"line_name":     l.LineName,
			"records":       recs,
		})
	}
	if unmatched == nil {
		unmatched = []Record{}
	}
	writeOK(w, map[string]any{"lines": lines, "unmatched": unmatched})
}

// recordDetail GET /records/:id：单条记录（仅本班；未分配班组 40310）。
func (m *Module) recordDetail(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeamDeep, "未分配班组，请联系管理员分配")
		return
	}
	id := pathID(r)
	rows, err := m.DB.QueryContext(r.Context(),
		"SELECT "+recordCols+" FROM crossing_record WHERE id = ? AND team_id = ?", id, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		fail(w, http.StatusNotFound, codeMissing, "记录不存在")
		return
	}
	rec, err := scanRecord(rows)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rec)
}

// recordDelete DELETE /records/:id：班管/超管；删记录 + 写 crossing_deleted_id 墓碑
// （与直推 deletes 共用 deleteRecord）。
func (m *Module) recordDelete(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeamDeep, "未分配班组，请联系管理员分配")
		return
	}
	id := pathID(r)
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT id FROM crossing_record WHERE id = ? AND team_id = ?", id, team.ID).Scan(&exist); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "记录不存在")
		return
	}
	if err := m.deleteRecord(id, team.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// deleteRecord 按记录 id 整条删除（DELETE /records/:id 与直推 deletes 共用）：
// 事务删记录行 + 写 crossing_deleted_id 墓碑（交跨无辅证照片，无 COS 清理）。
func (m *Module) deleteRecord(recordID, teamID int64) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作
	if _, err := tx.Exec("DELETE FROM crossing_record WHERE id = ?", recordID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT IGNORE INTO crossing_deleted_id (id, team_id) VALUES (?, ?)",
		recordID, teamID); err != nil {
		return err
	}
	return tx.Commit()
}
