package crossing

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 直推入库（POST /ingest）：mode=direct 按 (team_id, session_id, crane_record_id) 幂等 upsert 进正式表
//（已存在更新全部业务列）；mode=staging 进暂存区（同 (team_id, session_id) 批次整批替换）。
// 可选 deletes（[{ crane_record_id }]）按 (team_id, crane_record_id) 定位（不限 session_id）命中即整条删除
//（与 DELETE /records/:id 同款墓碑删除），未命中/缺 id 计 skipped 不报错；与 records 允许同批并存。
// 交跨为纯文字台账：无辅证照片、无 COS 逻辑；direct 不发站内通知，仅 staging 进暂存区通知（同树障口径）。

// toIntOrNull 按 JS parseInt(String(v).trim(), 10) 口径：取前导整数，解析不出 → null。
func toIntOrNull(v any) *int64 {
	s := strings.TrimSpace(toString(v))
	sign := ""
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		sign = string(s[i])
		i++
	}
	digits := ""
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		digits += string(s[i])
		i++
	}
	if digits == "" {
		return nil
	}
	n, err := strconv.ParseInt(sign+digits, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// toString 把任意 JSON 值转文本（对齐 JS String(x) 的常用子集）。
func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// anyArray 解析 JSON 数组为对象列表（缺省/非数组 → 空列表）。
func anyArray(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

var dateRe = regexp.MustCompile(`^(\d{4})[-./](\d{1,2})[-./](\d{1,2})`)

// normDateStr 日期归一为 'yyyy-MM-dd'：兼容 'yyyy.MM.dd' / 'yyyy-MM-dd' / 'yyyy/M/D' 文本；无法识别 → ”。
func normDateStr(v any) string {
	s := strings.TrimSpace(toString(v))
	if s == "" {
		return ""
	}
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s-%02d-%02d", m[1], month, day)
}

var sectionPoleRe = regexp.MustCompile(`(\d+)\s*#?\s*[-—~～]\s*(\d+)\s*#?`)

// parseSectionPoles 区段文本（如「15#-16#」）尽力解析杆号区间 → [pole_from, pole_to]；解析不出 → [null, null]
// （暂存导入落库共用：暂存行不含 pole 列，按区段文本补便于按杆号排序）。
func parseSectionPoles(section string) (*int64, *int64) {
	m := sectionPoleRe.FindStringSubmatch(section)
	if m == nil {
		return nil, nil
	}
	from, err1 := strconv.ParseInt(m[1], 10, 64)
	to, err2 := strconv.ParseInt(m[2], 10, 64)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &from, &to
}

// ingestRecord 清洗后的直推记录（业务列，字段截断口径与 Node sanitizeRecord 一致）。
type ingestRecord struct {
	CraneRecordID   *string
	VoltageLevel    string
	LineName        string
	Section         string
	PoleFrom        *int64
	PoleTo          *int64
	CrossingType    string
	DistSmall       string
	DistBig         string
	CrossHorizontal string
	CrossVertical   string
	CrossAngle      string
	HasHazard       string
	FinderName      string
	InspectAt       *string
	Remark          string
}

// sanitizeRecord 直推记录清洗：缺 line_name 且缺 line_name_raw 的条目返回 nil（跳过计数）；
// line_name 空时回退 line_name_raw；crane_record_id 空存 NULL（uk_crane 不约束 NULL）。
func sanitizeRecord(raw map[string]any) *ingestRecord {
	if raw == nil {
		return nil
	}
	str := func(key string, max int) string {
		return truncate(strings.TrimSpace(toString(raw[key])), max)
	}
	lineName := str("line_name", 64)
	if lineName == "" {
		lineName = str("line_name_raw", 64)
	}
	if lineName == "" {
		return nil
	}
	rec := &ingestRecord{
		VoltageLevel:    str("voltage_level", 16),
		LineName:        lineName,
		Section:         str("section", 32),
		PoleFrom:        toIntOrNull(raw["pole_from"]),
		PoleTo:          toIntOrNull(raw["pole_to"]),
		CrossingType:    str("crossing_type", 128),
		DistSmall:       str("dist_small", 16),
		DistBig:         str("dist_big", 16),
		CrossHorizontal: str("cross_horizontal", 16),
		CrossVertical:   str("cross_vertical", 16),
		CrossAngle:      str("cross_angle", 16),
		HasHazard:       str("has_hazard", 16),
		FinderName:      str("finder_name", 32),
		Remark:          str("remark", 255),
	}
	if id := str("crane_record_id", 64); id != "" {
		rec.CraneRecordID = &id
	}
	if d := normDateStr(raw["inspect_at"]); d != "" {
		rec.InspectAt = &d
	}
	return rec
}

// businessSet 业务列全量更新集（direct 已存在记录按直推权威值覆盖；交跨无处理列）。
const businessSet = `voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?, crossing_type = ?,
  dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
  finder_name = ?, inspect_at = ?, remark = ?`

func (rec *ingestRecord) businessVals() []any {
	return []any{
		rec.VoltageLevel, rec.LineName, rec.Section, rec.PoleFrom, rec.PoleTo, rec.CrossingType,
		rec.DistSmall, rec.DistBig, rec.CrossHorizontal, rec.CrossVertical, rec.CrossAngle, rec.HasHazard,
		rec.FinderName, rec.InspectAt, rec.Remark,
	}
}

// importDirect 逐条 upsert（按 (team_id, session_id, crane_record_id) 幂等），返回 {inserted, updated}。
func (m *Module) importDirect(teamID int64, sessionID string, records []*ingestRecord) (inserted, updated int, err error) {
	for _, rec := range records {
		existingID := int64(0)
		found := false
		if rec.CraneRecordID != nil {
			err := m.DB.QueryRow(
				"SELECT id FROM crossing_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?",
				teamID, sessionID, *rec.CraneRecordID).Scan(&existingID)
			switch {
			case err == nil:
				found = true
			case errors.Is(err, sql.ErrNoRows):
			default:
				return inserted, updated, err
			}
		}
		if found {
			args := append(rec.businessVals(), existingID)
			if _, err := m.DB.Exec("UPDATE crossing_record SET "+businessSet+" WHERE id = ?", args...); err != nil {
				return inserted, updated, err
			}
			updated++
			continue
		}
		args := []any{
			teamID, rec.VoltageLevel, rec.LineName, rec.Section, rec.PoleFrom, rec.PoleTo, rec.CrossingType,
			rec.DistSmall, rec.DistBig, rec.CrossHorizontal, rec.CrossVertical, rec.CrossAngle, rec.HasHazard,
			rec.FinderName, rec.InspectAt, rec.Remark, sessionID, rec.CraneRecordID,
		}
		if _, err := m.DB.Exec(
			`INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
          dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
          finder_name, inspect_at, remark, source, crane_session_id, crane_record_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'crane', ?, ?)`, args...); err != nil {
			return inserted, updated, err
		}
		inserted++
	}
	return inserted, updated, nil
}

// saveBatchXlsx 随批 xlsx 落盘：data/crossing/inbox/{team_id}/{session_id}.xlsx（路径段过滤非法字符），
// 返回相对 server 目录的存储路径；空内容返回 ”。
func (m *Module) saveBatchXlsx(teamID int64, sessionID, xlsxBase64 string) (string, error) {
	buf, err := base64.StdEncoding.DecodeString(strings.TrimSpace(xlsxBase64))
	if err != nil || len(buf) == 0 {
		return "", nil
	}
	root := m.ServerDir
	if root == "" {
		root = "."
	}
	rel := filepath.ToSlash(filepath.Join("data", "crossing", "inbox", strconv.FormatInt(teamID, 10),
		safeFileName(sessionID)+".xlsx"))
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", apiErr(http.StatusInternalServerError, codeFile, "暂存 xlsx 落盘失败")
	}
	if err := os.WriteFile(abs, buf, 0o644); err != nil {
		return "", apiErr(http.StatusInternalServerError, codeFile, "暂存 xlsx 落盘失败")
	}
	return rel, nil
}

// importStaging 同 (team_id, session_id) 批次整批替换（含已导入批次被重推时复位为 pending），
// 返回 batch_id 与写入行数。
func (m *Module) importStaging(teamID int64, sessionID, groupName, pusherName string, reviewNote *string, xlsxPath string, records []*ingestRecord) (int64, int, error) {
	var batchID int64
	err := m.DB.QueryRow(
		"SELECT id FROM crossing_staging_batch WHERE team_id = ? AND crane_session_id = ?",
		teamID, sessionID).Scan(&batchID)
	switch {
	case err == nil:
		if _, err := m.DB.Exec("DELETE FROM crossing_staging_record WHERE batch_id = ?", batchID); err != nil {
			return 0, 0, err
		}
		if _, err := m.DB.Exec(
			`UPDATE crossing_staging_batch SET group_name = ?, pusher_name = ?, review_note = ?, xlsx_path = ?, status = 'pending', created_at = NOW()
       WHERE id = ?`, groupName, pusherName, reviewNote, xlsxPath, batchID); err != nil {
			return 0, 0, err
		}
	case errors.Is(err, sql.ErrNoRows):
		res, err := m.DB.Exec(
			"INSERT INTO crossing_staging_batch (team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path) VALUES (?, ?, ?, ?, ?, ?)",
			teamID, sessionID, groupName, pusherName, reviewNote, xlsxPath)
		if err != nil {
			return 0, 0, err
		}
		batchID, _ = res.LastInsertId()
	default:
		return 0, 0, err
	}
	for _, rec := range records {
		if _, err := m.DB.Exec(
			`INSERT INTO crossing_staging_record (batch_id, crane_record_id, voltage_level, line_name, section, crossing_type,
        dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
        finder_name, inspect_at, remark)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			batchID, rec.CraneRecordID, rec.VoltageLevel, rec.LineName, rec.Section, rec.CrossingType,
			rec.DistSmall, rec.DistBig, rec.CrossHorizontal, rec.CrossVertical, rec.CrossAngle, rec.HasHazard,
			rec.FinderName, rec.InspectAt, rec.Remark); err != nil {
			return 0, 0, err
		}
	}
	return batchID, len(records), nil
}

// ingestBody 直推请求体（松散解析）。
type ingestBody struct {
	SessionID  string
	TeamName   string
	Mode       string
	PusherName string
	GroupName  string
	ReviewNote *string
	XlsxBase64 string
	Records    []map[string]any
	Deletes    []map[string]any
}

// parseIngestBody 从松散 JSON 组装直推入参（session_id / team_name / mode 必填校验对齐 Node）。
func parseIngestBody(body map[string]json.RawMessage) (ingestBody, error) {
	var out ingestBody
	out.SessionID = truncate(rawString(body["session_id"]), 64)
	out.TeamName = strings.TrimSpace(rawString(body["team_name"]))
	out.Mode = strings.TrimSpace(rawString(body["mode"]))
	if out.SessionID == "" {
		return out, errors.New("参数错误：session_id 不能为空")
	}
	if out.TeamName == "" {
		return out, errors.New("参数错误：team_name 不能为空")
	}
	if out.Mode != "direct" && out.Mode != "staging" {
		return out, errors.New("参数错误：mode 应为 direct 或 staging")
	}
	if pusher, ok := body["pusher"]; ok {
		var p map[string]any
		if err := json.Unmarshal(pusher, &p); err == nil {
			out.PusherName = truncate(strings.TrimSpace(toString(p["name"])), 32)
		}
	}
	out.GroupName = truncate(strings.TrimSpace(rawString(body["group_name"])), 64)
	if note := truncate(strings.TrimSpace(rawString(body["review_note"])), 255); note != "" {
		out.ReviewNote = &note
	}
	out.XlsxBase64 = rawString(body["xlsx_base64"])
	out.Records = anyArray(body["records"])
	out.Deletes = anyArray(body["deletes"])
	return out, nil
}

// 直推可判定错误（HTTP 映射与进程内调用方判定共用）。
var (
	// ErrIngestNotConfigured 未配置对接密钥（CROSSING_INGEST_TOKEN）。
	ErrIngestNotConfigured = errors.New("未配置对接密钥")
	// ErrIngestToken 对接密钥错误。
	ErrIngestToken = errors.New("对接密钥错误")
	// ErrIngestBadJSON 请求体不是合法 JSON。
	ErrIngestBadJSON = errors.New("参数错误：请求体不是合法 JSON")
	// ErrIngestTeamNotFound 班组不存在（按 team_name 精确匹配不到启用班组）。
	ErrIngestTeamNotFound = errors.New("班组不存在")
)

// IngestParamError 参数错误（40001）。
type IngestParamError struct{ Msg string }

func (e *IngestParamError) Error() string { return e.Msg }

// IngestResult 直推结果：direct 模式看 Inserted/Updated/Skipped/Deleted；staging 模式看 BatchID/Count/Deleted。
// TeamName/PusherName/ReviewNote 为回传的清洗后入参（staging 站内通知用）。
type IngestResult struct {
	Mode       string
	TeamName   string
	PusherName string
	ReviewNote *string
	Inserted   int
	Updated    int
	Skipped    int
	Deleted    int
	BatchID    int64
	Count      int
}

// verifyIngestToken 校验对接密钥（未配置 → ErrIngestNotConfigured；不等 → ErrIngestToken）。
// 先比长度再恒定时间比较；任何入口（HTTP / 进程内）都必须经此校验。
func (m *Module) verifyIngestToken(ctx context.Context, provided string) error {
	token, err := m.ingestToken(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return ErrIngestNotConfigured
	}
	if !tokenEqual(provided, token) {
		return ErrIngestToken
	}
	return nil
}

// Ingest 直推主流程（crane-answer 的 POST /ingest 与单进程 wechatanswer 直推共用）。
// token 为对接密钥，与 X-Ingest-Token 同口径校验，任何调用方不得绕过；raw 为 /ingest 请求体原文。
// mode=direct：按 (team_id, session_id, crane_record_id) 幂等 upsert；mode=staging：进暂存区（同会话整批替换）；
// 可选 deletes 按 (team_id, crane_record_id) 命中即删并计数（与 records 允许同批并存）。
// 返回明确结果；参数/鉴权/班组错误以可判定错误返回（见 ErrIngest* 与 IngestParamError）。
func (m *Module) Ingest(ctx context.Context, token string, raw []byte) (*IngestResult, error) {
	if err := m.verifyIngestToken(ctx, token); err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, ErrIngestBadJSON
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	req, err := parseIngestBody(body)
	if err != nil {
		return nil, &IngestParamError{Msg: err.Error()}
	}
	// 按班组名精确匹配（仅启用班组）
	var teamID int64
	var teamName string
	if err := m.DB.QueryRowContext(ctx,
		"SELECT id, name FROM sys_team WHERE name = ? AND status = 1", req.TeamName).Scan(&teamID, &teamName); err != nil {
		return nil, ErrIngestTeamNotFound
	}

	// 清洗 + 跳过计数（缺线路名的条目不入库）
	records := make([]*ingestRecord, 0, len(req.Records))
	result := &IngestResult{Mode: req.Mode, TeamName: teamName, PusherName: req.PusherName, ReviewNote: req.ReviewNote}
	for _, rawRec := range req.Records {
		if rec := sanitizeRecord(rawRec); rec != nil {
			records = append(records, rec)
		} else {
			result.Skipped++
		}
	}

	// 可选 deletes：按 (team_id, crane_record_id) 定位（不限 session_id），命中整条删除并计 deleted；
	// 未命中/缺 id 计 skipped 不报错；与 records 允许同批并存
	for _, d := range req.Deletes {
		craneID := strings.TrimSpace(toString(d["crane_record_id"]))
		if craneID == "" {
			result.Skipped++
			continue
		}
		rows, err := m.DB.QueryContext(ctx,
			"SELECT id FROM crossing_record WHERE team_id = ? AND crane_record_id = ?", teamID, craneID)
		if err != nil {
			return nil, err
		}
		hit := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			hit = append(hit, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(hit) == 0 {
			result.Skipped++
			continue
		}
		for _, id := range hit { // 同 id 跨会话桶可能多行，一并删除
			if err := m.deleteRecord(id, teamID); err != nil {
				return nil, err
			}
		}
		result.Deleted++
	}

	if req.Mode == "direct" {
		inserted, updated, err := m.importDirect(teamID, req.SessionID, records)
		if err != nil {
			return nil, err
		}
		result.Inserted, result.Updated = inserted, updated
		return result, nil
	}

	xlsxPath, err := m.saveBatchXlsx(teamID, req.SessionID, req.XlsxBase64)
	if err != nil {
		return nil, err
	}
	batchID, count, err := m.importStaging(teamID, req.SessionID, req.GroupName, req.PusherName,
		req.ReviewNote, xlsxPath, records)
	if err != nil {
		return nil, err
	}
	result.BatchID, result.Count = batchID, count
	return result, nil
}

// handleIngest POST /ingest（鉴权凭 X-Ingest-Token 在本函数完成，不走 JWT；转调 Ingest 并映射错误到响应码）。
func (m *Module) handleIngest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, ErrIngestBadJSON.Error())
		return
	}
	res, err := m.Ingest(r.Context(), r.Header.Get("X-Ingest-Token"), raw)
	if err != nil {
		writeIngestFail(w, err)
		return
	}
	if res.Mode == "direct" {
		// direct 为逐条实时推送，不发站内通知避免刷屏；群内回执由 crane-answer 自行发送
		writeOK(w, map[string]any{
			"inserted": res.Inserted, "updated": res.Updated, "skipped": res.Skipped, "deleted": res.Deleted,
		})
		return
	}
	// staging 进暂存区：站内通知超管 + 本班班组管理员（失败仅跳过，不影响直推应答）
	m.notifyStaging(res.TeamName, res.Count, res.Skipped, res.PusherName, res.ReviewNote)
	writeOK(w, map[string]any{"batch_id": res.BatchID, "count": res.Count, "deleted": res.Deleted})
}

// writeIngestFail 直推可判定错误 → 响应码映射（与 Node 路由 fail 口径一致）。
func writeIngestFail(w http.ResponseWriter, err error) {
	var perr *IngestParamError
	switch {
	case errors.Is(err, ErrIngestNotConfigured):
		fail(w, http.StatusServiceUnavailable, codeNoToken, err.Error())
	case errors.Is(err, ErrIngestToken):
		fail(w, http.StatusUnauthorized, codeIngestTok, err.Error())
	case errors.Is(err, ErrIngestBadJSON):
		fail(w, http.StatusBadRequest, codeParam, err.Error())
	case errors.As(err, &perr):
		fail(w, http.StatusBadRequest, codeParam, perr.Error())
	case errors.Is(err, ErrIngestTeamNotFound):
		fail(w, http.StatusNotFound, codeMissing, err.Error())
	default:
		writeErr(w, err)
	}
}

// notifyStaging staging 进暂存区站内通知（超管 + 本班班组管理员；失败仅跳过，不影响直推应答）。
func (m *Module) notifyStaging(teamName string, count, skipped int, pusherName string, reviewNote *string) {
	if m.Notifier == nil {
		return
	}
	noteLine := ""
	if reviewNote != nil && *reviewNote != "" {
		text := *reviewNote
		if runes := []rune(text); len(runes) > 80 {
			text = string(runes[:80]) + "…"
		}
		noteLine = "\n备注：" + text
	}
	pusher := pusherName
	if pusher == "" {
		pusher = "未知"
	}
	content := fmt.Sprintf("班组：%s\n暂存：%d 条（跳过 %d 条），待确认导入\n推送人：%s%s",
		teamName, count, skipped, pusher, noteLine)
	_ = m.Notifier.Push(context.Background(), []string{"admin", "team_admin"}, "交跨记录进暂存区", content)
}
