package treehazard

// crane-answer 直推入库（与 server/src/treehazard/ingest.js 对齐）：
// mode=direct：按 (team_id, session_id, crane_record_id) 幂等 upsert 进正式表（已存在仅更新发现侧列，不动处理列/状态）；
// mode=staging：进暂存区（同 (team_id, session_id) 批次整批替换），随批 xlsx 落盘供事后核对。
// 可选 deletes（[{ crane_record_id }]）按 (team_id, crane_record_id) 命中即整条删除，与 records 允许同批并存。
// 对外暴露可复用的 Ingest 方法（输入 context 与原始 JSON），crane-answer 的 HTTP 路由与单进程
// wechatanswer 直推共用同一核心；对接密钥（X-Ingest-Token 同口径）一律校验，不提供绕过入口。

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 直推可判定错误（HTTP 映射与进程内调用方判定共用）。
var (
	// ErrIngestNotConfigured 未配置对接密钥（TREEHAZARD_INGEST_TOKEN）。
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

// IngestResult 直推结果：direct 模式看 Inserted/Updated/Skipped/Photos/PhotosFailed/Deleted；
// staging 模式看 BatchID/Count/Photos/PhotosFailed/Deleted。
type IngestResult struct {
	Mode         string
	Inserted     int
	Updated      int
	Skipped      int
	Deleted      int
	Photos       int
	PhotosFailed int
	BatchID      int64
	Count        int
}

// ingestRecord 清洗后的直推记录。
type ingestRecord struct {
	CraneRecordID *string
	VoltageLevel  string
	LineName      string
	Section       string
	PoleFrom      *int64
	PoleTo        *int64
	PositionNote  string
	TreeSpecies   string
	TreeCount     string
	DistSmall     string
	DistBig       string
	Diameter      string
	Horizontal    string
	Vertical      string
	VerticalNum   *float64
	FinderName    string
	FoundAt       string
	Photos        []dataURLPhoto // nil=旧版直推不动既有辅证；非 nil（含空）为权威值整组替换
}

// toIntOrNull 整数解析（非法 → nil）。
func toIntOrNull(v any) *int64 {
	n, ok := asInt64(v)
	if !ok {
		return nil
	}
	return &n
}

// parseEvidencePhotos 解析直推记录附带的辅证照片：字段缺省返回 nil；数组为权威值（可空=清除）；
// 单张非法（非 jpeg/png dataURL、超 15MB）跳过，每条最多 9 张。
func parseEvidencePhotos(raw map[string]any) []dataURLPhoto {
	field, present := raw["evidence_photos"]
	if !present {
		return nil
	}
	list, ok := field.([]any)
	if !ok {
		return nil
	}
	photos := make([]dataURLPhoto, 0)
	for i, item := range list {
		if i >= photoMaxPerRecord {
			break
		}
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		photo := parseDataUrlPhoto(asString(obj["image"]))
		if photo == nil || len(photo.buf) == 0 || len(photo.buf) > photoMaxBytes {
			continue
		}
		photos = append(photos, *photo)
	}
	return photos
}

// sanitizeRecord 直推记录清洗：缺 line_name 且缺 line_name_raw → nil（跳过计数）。
func sanitizeRecord(raw map[string]any) *ingestRecord {
	if raw == nil {
		return nil
	}
	lineName := truncateRunes(strings.TrimSpace(asString(raw["line_name"])), 64)
	if lineName == "" {
		lineName = truncateRunes(strings.TrimSpace(asString(raw["line_name_raw"])), 64)
	}
	if lineName == "" {
		return nil
	}
	craneID := truncateRunes(strings.TrimSpace(asString(raw["crane_record_id"])), 64)
	rec := &ingestRecord{
		VoltageLevel: truncateRunes(strings.TrimSpace(asString(raw["voltage_level"])), 16),
		LineName:     lineName,
		Section:      truncateRunes(strings.TrimSpace(asString(raw["section"])), 32),
		PoleFrom:     toIntOrNull(raw["pole_from"]),
		PoleTo:       toIntOrNull(raw["pole_to"]),
		PositionNote: truncateRunes(strings.TrimSpace(asString(raw["position_note"])), 32),
		TreeSpecies:  truncateRunes(strings.TrimSpace(asString(raw["tree_species"])), 32),
		TreeCount:    truncateRunes(strings.TrimSpace(asString(raw["tree_count"])), 16),
		DistSmall:    truncateRunes(strings.TrimSpace(asString(raw["dist_small"])), 16),
		DistBig:      truncateRunes(strings.TrimSpace(asString(raw["dist_big"])), 16),
		Diameter:     truncateRunes(strings.TrimSpace(asString(raw["diameter"])), 16),
		Horizontal:   truncateRunes(strings.TrimSpace(asString(raw["horizontal"])), 16),
		Vertical:     truncateRunes(strings.TrimSpace(asString(raw["vertical"])), 16),
		FinderName:   truncateRunes(strings.TrimSpace(asString(raw["finder_name"])), 32),
	}
	rec.VerticalNum = parseVerticalNum(rec.Vertical)
	rec.FoundAt = normDateStr(asString(raw["found_at"]))
	if craneID != "" {
		rec.CraneRecordID = &craneID // 空存 NULL（uk_crane 不约束 NULL）
	}
	return rec
}

// discoverySet 发现侧列（direct 已存在记录仅更新这些列，处理三列与状态不动）。
const discoverySet = `voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?, position_note = ?,
  tree_species = ?, tree_count = ?, dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?,
  vertical_num = ?, finder_name = ?, found_at = ?`

func (r *ingestRecord) discoveryVals() []any {
	return []any{
		r.VoltageLevel, r.LineName, r.Section, r.PoleFrom, r.PoleTo, r.PositionNote,
		r.TreeSpecies, r.TreeCount, r.DistSmall, r.DistBig, r.Diameter, r.Horizontal, r.Vertical,
		r.VerticalNum, r.FinderName, nullIfEmpty(r.FoundAt),
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// importDirect 逐条 upsert（附带辅证照片时整组替换 evidence 组）；数据库错误原样上抛。
func (m *Module) importDirect(ctx context.Context, teamID int64, teamName, sessionID string, records []*ingestRecord) (inserted, updated, photos, photosFailed int, err error) {
	for _, rec := range records {
		var existingID int64
		found := false
		if rec.CraneRecordID != nil {
			qErr := m.DB.QueryRowContext(ctx,
				`SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?`,
				teamID, sessionID, *rec.CraneRecordID).Scan(&existingID)
			if qErr == nil {
				found = true
			} else if !errors.Is(qErr, sql.ErrNoRows) {
				return inserted, updated, photos, photosFailed, qErr
			}
		}
		var recordID int64
		if found {
			if _, err := m.DB.ExecContext(ctx,
				`UPDATE treehazard_record SET `+discoverySet+` WHERE id = ?`,
				append(rec.discoveryVals(), existingID)...); err != nil {
				return inserted, updated, photos, photosFailed, err
			}
			updated++
			recordID = existingID
		} else {
			res, err := m.DB.ExecContext(ctx,
				`INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to, position_note,
				  tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
				  finder_name, found_at, status, source, crane_session_id, crane_record_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unhandled', 'crane', ?, ?)`,
				teamID, rec.VoltageLevel, rec.LineName, rec.Section, rec.PoleFrom, rec.PoleTo, rec.PositionNote,
				rec.TreeSpecies, rec.TreeCount, rec.DistSmall, rec.DistBig, rec.Diameter, rec.Horizontal,
				rec.Vertical, rec.VerticalNum, rec.FinderName, nullIfEmpty(rec.FoundAt), sessionID, rec.CraneRecordID)
			if err != nil {
				return inserted, updated, photos, photosFailed, err
			}
			recordID, _ = res.LastInsertId()
			inserted++
		}
		// Photos 为 nil（旧版直推未上报）不动既有辅证；切片（含空）为权威值整组替换。
		if rec.Photos != nil && recordID > 0 {
			stored, failed := m.replaceEvidencePhotos(ctx, teamName, recordID, rec.Photos)
			photos += stored
			photosFailed += failed
		}
	}
	return inserted, updated, photos, photosFailed, nil
}

// importStaging 同 (team_id, session_id) 批次整批替换（含已导入批次被重推时复位为 pending）。
func (m *Module) importStaging(ctx context.Context, teamID int64, teamName, sessionID, groupName, pusherName, reviewNote, xlsxBase64 string, records []*ingestRecord) (batchID int64, count, photos, photosFailed int, err error) {
	xlsxPath := ""
	if xlsxBase64 != "" && m.Files != nil {
		data, decErr := decodeBase64(xlsxBase64)
		if decErr == nil && len(data) > 0 {
			if p, saveErr := m.Files.Save(ctx, teamID, sessionID, data); saveErr == nil {
				xlsxPath = p
			}
		}
	}
	var existingID int64
	findErr := m.DB.QueryRowContext(ctx,
		`SELECT id FROM treehazard_staging_batch WHERE team_id = ? AND crane_session_id = ?`,
		teamID, sessionID).Scan(&existingID)
	if findErr == nil {
		batchID = existingID
		if _, err := m.DB.ExecContext(ctx,
			`DELETE FROM treehazard_staging_record WHERE batch_id = ?`, batchID); err != nil {
			return 0, 0, 0, 0, err
		}
		m.clearStagingPhotos(ctx, batchID) // 整批替换：旧暂存照片连带清理
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE treehazard_staging_batch SET group_name = ?, pusher_name = ?, review_note = ?, xlsx_path = ?, status = 'pending', created_at = NOW()
			 WHERE id = ?`, groupName, pusherName, nullIfEmpty(reviewNote), xlsxPath, batchID); err != nil {
			return 0, 0, 0, 0, err
		}
	} else {
		res, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_staging_batch (team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path) VALUES (?, ?, ?, ?, ?, ?)`,
			teamID, sessionID, groupName, pusherName, nullIfEmpty(reviewNote), xlsxPath)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		batchID, _ = res.LastInsertId()
	}
	for _, rec := range records {
		res, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_staging_record (batch_id, crane_record_id, voltage_level, line_name, section,
			  tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
			  finder_name, found_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			batchID, rec.CraneRecordID, rec.VoltageLevel, rec.LineName, rec.Section, rec.TreeSpecies,
			rec.TreeCount, rec.DistSmall, rec.DistBig, rec.Diameter, rec.Horizontal, rec.Vertical,
			rec.VerticalNum, rec.FinderName, nullIfEmpty(rec.FoundAt))
		if err != nil {
			return 0, 0, 0, 0, err
		}
		stagingRecordID, _ := res.LastInsertId()
		if len(rec.Photos) > 0 {
			craneID := ""
			if rec.CraneRecordID != nil {
				craneID = *rec.CraneRecordID
			}
			stored, failed := m.storeStagingPhotos(ctx, teamName, batchID, stagingRecordID, craneID, rec.Photos)
			photos += stored
			photosFailed += failed
		}
	}
	return batchID, len(records), photos, photosFailed, nil
}

// decodeBase64 解码标准 base64（随批 xlsx）。
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// verifyIngestToken 校验对接密钥（未配置 → ErrIngestNotConfigured；不等 → ErrIngestToken）。
// 先比长度再恒定时间比较；任何入口（HTTP / 进程内）都必须经此校验。
func (m *Module) verifyIngestToken(ctx context.Context, token string) error {
	expect := m.configValue(ctx, "TREEHAZARD_INGEST_TOKEN")
	if expect == "" {
		return ErrIngestNotConfigured
	}
	provided := []byte(token)
	expected := []byte(expect)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
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
	var body struct {
		SessionID  string           `json:"session_id"`
		TeamName   string           `json:"team_name"`
		Mode       string           `json:"mode"`
		GroupName  string           `json:"group_name"`
		ReviewNote string           `json:"review_note"`
		XLSXBase64 string           `json:"xlsx_base64"`
		Pusher     map[string]any   `json:"pusher"`
		Records    []map[string]any `json:"records"`
		Deletes    []map[string]any `json:"deletes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, ErrIngestBadJSON
	}
	sessionID := truncateRunes(strings.TrimSpace(body.SessionID), 64)
	teamName := strings.TrimSpace(body.TeamName)
	mode := strings.TrimSpace(body.Mode)
	if sessionID == "" {
		return nil, &IngestParamError{Msg: "参数错误：session_id 不能为空"}
	}
	if teamName == "" {
		return nil, &IngestParamError{Msg: "参数错误：team_name 不能为空"}
	}
	if mode != "direct" && mode != "staging" {
		return nil, &IngestParamError{Msg: "参数错误：mode 应为 direct 或 staging"}
	}
	// 按班组名精确匹配（仅启用班组）。
	var teamID int64
	var matchedName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE name = ? AND status = 1`, teamName).Scan(&teamID, &matchedName); err != nil {
		return nil, ErrIngestTeamNotFound
	}

	pusherName := truncateRunes(strings.TrimSpace(asString(body.Pusher["name"])), 32)
	groupName := truncateRunes(strings.TrimSpace(body.GroupName), 64)
	reviewNote := truncateRunes(strings.TrimSpace(body.ReviewNote), 255)

	// 清洗 + 跳过计数（缺线路名的条目不入库）；辅证照片随记录解析。
	records := make([]*ingestRecord, 0, len(body.Records))
	result := &IngestResult{Mode: mode}
	for _, rawRec := range body.Records {
		rec := sanitizeRecord(rawRec)
		if rec == nil {
			result.Skipped++
			continue
		}
		rec.Photos = parseEvidencePhotos(rawRec)
		records = append(records, rec)
	}

	// 可选 deletes：按 (team_id, crane_record_id) 定位（不限 session_id），命中整条删除；
	// 未命中/缺 id 计 skipped 不报错。
	for _, d := range body.Deletes {
		craneID := strings.TrimSpace(asString(d["crane_record_id"]))
		if craneID == "" {
			result.Skipped++
			continue
		}
		rows, err := m.DB.QueryContext(ctx,
			`SELECT id FROM treehazard_record WHERE team_id = ? AND crane_record_id = ?`, teamID, craneID)
		if err != nil {
			return nil, err
		}
		hit := make([]int64, 0)
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				hit = append(hit, id)
			}
		}
		rows.Close()
		if len(hit) == 0 {
			result.Skipped++
			continue
		}
		for _, id := range hit { // 同 id 跨会话桶可能多行，一并删除
			if err := m.deleteRecord(ctx, id, teamID); err != nil {
				return nil, err
			}
		}
		result.Deleted++
	}

	if mode == "direct" {
		inserted, updated, photos, photosFailed, err := m.importDirect(ctx, teamID, matchedName, sessionID, records)
		if err != nil {
			return nil, err
		}
		result.Inserted, result.Updated = inserted, updated
		result.Photos, result.PhotosFailed = photos, photosFailed
		return result, nil
	}

	batchID, count, photos, photosFailed, err := m.importStaging(ctx, teamID, matchedName,
		sessionID, groupName, pusherName, reviewNote, body.XLSXBase64, records)
	if err != nil {
		return nil, fmt.Errorf("暂存写入失败：%w", err)
	}
	result.BatchID, result.Count = batchID, count
	result.Photos, result.PhotosFailed = photos, photosFailed
	return result, nil
}

// handleIngest POST /ingest（鉴权凭 X-Ingest-Token，转调 Ingest 并映射错误到响应码）。
// direct 为 crane-answer 逐条实时推送（落库/更正/辅证变更各一次），不发站内通知避免刷屏；
// staging 进暂存区的站内通知依赖 notice 模块，尚未在 Go 侧接入（见迁移报告）。
func (m *Module) handleIngest(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, ErrIngestBadJSON.Error(), nil)
		return
	}
	res, err := m.Ingest(r.Context(), r.Header.Get("X-Ingest-Token"), raw)
	if err != nil {
		writeIngestFail(w, err)
		return
	}
	if res.Mode == "direct" {
		writeOK(w, map[string]any{
			"inserted": res.Inserted, "updated": res.Updated, "skipped": res.Skipped,
			"photos": res.Photos, "photos_failed": res.PhotosFailed, "deleted": res.Deleted,
		}, "")
		return
	}
	writeOK(w, map[string]any{
		"batch_id": res.BatchID, "count": res.Count, "photos": res.Photos,
		"photos_failed": res.PhotosFailed, "deleted": res.Deleted,
	}, "")
}

// writeIngestFail 直推可判定错误 → 响应码映射（与 Node 路由 fail 口径一致）。
func writeIngestFail(w http.ResponseWriter, err error) {
	var perr *IngestParamError
	switch {
	case errors.Is(err, ErrIngestNotConfigured):
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, err.Error(), nil)
	case errors.Is(err, ErrIngestToken):
		writeFail(w, http.StatusUnauthorized, codeIngestToken, err.Error(), nil)
	case errors.Is(err, ErrIngestBadJSON):
		writeFail(w, http.StatusBadRequest, codeParam, err.Error(), nil)
	case errors.As(err, &perr):
		writeFail(w, http.StatusBadRequest, codeParam, perr.Error(), nil)
	case errors.Is(err, ErrIngestTeamNotFound):
		writeFail(w, http.StatusNotFound, codeNotFound, err.Error(), nil)
	default:
		writeFail(w, http.StatusInternalServerError, codeParam, err.Error(), nil)
	}
}
