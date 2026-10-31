// 水印照片：上传（multipart 二进制 + 可选水印渲染 + COS 落库）、Dify 验证流水线（重试 + 回写）、
// 手动修正 / 人名调整 / 删除 / 下载 / 范围列表 / 批量 PDF。日期/地点核验在后端 CheckWatermark 完成。
package worklog

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/apps/wmadd"
	"shade/internal/compat/imagepdf"
	"shade/internal/httpapi"
	"shade/internal/logging"
)

// Dots 把 'YYYY-MM-DD' 转 'YYYY.MM.DD'（水印日期比对口径）。
func Dots(dateStr string) string { return strings.ReplaceAll(dateStr, "-", ".") }

// rowQuerier 行查询接口（*sql.DB 与 *sql.Tx 通用）。
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// checkPhotoMembers 照片人名校验：⊆ 本卡用车人；非水印（isPlain）不占「每人限一张」名额。
func (m *Module) checkPhotoMembers(ctx context.Context, entryID int64, names []string, excludePhotoID int64, isPlain bool) (int, string) {
	return m.checkPhotoMembersWith(ctx, m.DB, entryID, names, excludePhotoID, isPlain)
}

// checkPhotoMembersWith 同 checkPhotoMembers，查询走调用方事务（上传入口在行锁内复查占位）。
func (m *Module) checkPhotoMembersWith(ctx context.Context, q rowQuerier, entryID int64, names []string, excludePhotoID int64, isPlain bool) (int, string) {
	rows, err := q.QueryContext(ctx,
		`SELECT m.name FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
		 WHERE em.entry_id = ?`, entryID)
	if err != nil {
		return codeInternal, "服务器开小差了，请稍后再试"
	}
	inEntry := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			inEntry[name] = true
		}
	}
	rows.Close()
	for _, n := range names {
		if !inEntry[n] {
			return codeNotCardMember, "「" + n + "」不是本卡用车人"
		}
	}
	if isPlain {
		return 0, ""
	}
	prows, err := q.QueryContext(ctx,
		`SELECT id, members FROM worklog_photo WHERE entry_id = ? AND is_watermark = 1`, entryID)
	if err != nil {
		return codeInternal, "服务器开小差了，请稍后再试"
	}
	used := map[string]bool{}
	for prows.Next() {
		var id int64
		var membersJSON string
		if prows.Scan(&id, &membersJSON) != nil {
			continue
		}
		if excludePhotoID > 0 && id == excludePhotoID {
			continue
		}
		for _, n := range parseNames(membersJSON) {
			used[n] = true
		}
	}
	prows.Close()
	for _, n := range names {
		if used[n] {
			return codeMemberUsed, "「" + n + "」已有水印照片，每人限一张"
		}
	}
	return 0, ""
}

// handlePhotoUpload POST /logs/{id}/photos：multipart 二进制照片上传后转存 COS。
// wm 字段可选：服务端渲染水印；plain=true 为非水印照片（免验证、不占每人一张）。
func (m *Module) handlePhotoUpload(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var entryTeamID, vehicleID int64
	var logDate, destName, teamName string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT e.team_id, COALESCE(e.vehicle_id, 0), DATE_FORMAT(e.log_date, '%Y-%m-%d'),
		        COALESCE(d.name, ''), COALESCE(t.name, '')
		 FROM worklog_entry e
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 LEFT JOIN sys_team t ON t.id = e.team_id
		 WHERE e.id = ? AND `+entryVisibleSQL,
		entryID, team.ID, team.ID).
		Scan(&entryTeamID, &vehicleID, &logDate, &destName, &teamName)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if vehicleID == 0 {
		fail(w, http.StatusBadRequest, codeInput, "未出车不可上传照片")
		return
	}
	if m.SgccOps != nil && m.SgccOps.ActivePhotoOp(entryTeamID, entryID) {
		fail(w, http.StatusConflict, codeOpRunning, "该卡片有商旅同步任务进行中，请稍后再传")
		return
	}
	upload, err := httpapi.ReadMultipartImage(w, r, "image", map[string]bool{"image/jpeg": true, "image/png": true})
	if err != nil {
		switch {
		case errors.Is(err, httpapi.ErrImageTooLarge):
			fail(w, http.StatusRequestEntityTooLarge, codePhotoSize, "照片大小应在 50MB 以内")
		case errors.Is(err, httpapi.ErrImageFormat):
			fail(w, http.StatusBadRequest, codePhotoFormat, "照片格式应为 jpeg/png")
		case errors.Is(err, httpapi.ErrMultipart):
			fail(w, http.StatusBadRequest, codeInput, "请使用 multipart/form-data 上传照片")
		default:
			fail(w, http.StatusBadRequest, codeInput, "请选择要上传的照片")
		}
		return
	}
	isPlain := rawBool(json.RawMessage(upload.Value("plain")))
	membersRaw := strings.TrimSpace(upload.Value("members"))
	names := parseNameList(json.RawMessage(membersRaw))
	if len(names) == 0 && membersRaw != "" && !strings.HasPrefix(membersRaw, "[") {
		names = []string{membersRaw}
	}
	if len(names) == 0 {
		fail(w, http.StatusBadRequest, codePickMembers, "请选择照片所属人名")
		return
	}
	if code, msg := m.checkPhotoMembers(r.Context(), entryID, names, 0, isPlain); code != 0 {
		fail(w, http.StatusBadRequest, code, msg)
		return
	}
	buf, contentType := upload.Buf, upload.ContentType
	// 需要加水印时：服务端渲染（方向矫正 + 防伪码校验），产物统一为 JPEG；非水印跳过渲染
	if !isPlain {
		if wmRaw := strings.TrimSpace(upload.Value("wm")); wmRaw != "" && wmRaw != "null" {
			var wm wmFields
			if err := jsonUnmarshal(json.RawMessage(wmRaw), &wm); err == nil {
				rendered, err := renderWMPicture(buf, wm)
				if err != nil {
					fail(w, http.StatusBadRequest, codeWM, "水印渲染失败，请重试")
					return
				}
				buf = rendered
				contentType = "image/jpeg"
			}
		}
	}

	ext := "jpg"
	if contentType == "image/png" {
		ext = "png"
	}
	// 商旅远端先行 + 本地收尾：SgccRemote+PhotoOps 均注入时任务化异步（响应带 opId，端侧轮询收尾），
	// 否则同步执行（远端失败本地零写入）
	var photoID int64
	var photoOut string
	run := func(ctx context.Context, rec PhotoOpRec) error {
		prefix := m.configValue(ctx, "COS_WORKLOG_PREFIX")
		if prefix == "" {
			prefix = "worklog/"
		}
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		key := fmt.Sprintf("%s%s/%s/%d-%d.%s", prefix, teamName, Dots(logDate), entryID, nowMillis(), ext)
		links := ""
		if m.SgccRemote != nil {
			var err error
			links, err = m.SgccRemote.UploadToMembers(ctx, entryTeamID, logDate, names, buf)
			if err != nil {
				return &remoteFail{msg: fmt.Sprintf("商旅同步失败（%v），本地未上传", err)}
			}
		}
		if m.Store != nil {
			if err := m.Store.PutBuffer(ctx, key, buf, contentType); err != nil {
				return err
			}
		}
		imgMD5 := md5.Sum(buf)
		membersJSON, _ := json.Marshal(names)
		// 原子占位：锁卡片行后在事务内复查「每人一张」，再写入照片；并发请求不会同时越过名额检查。
		stored := m.Store != nil
		cleanup := func() {
			if stored {
				_ = m.Store.DeleteObject(ctx, key)
			}
		}
		tx, err := m.DB.BeginTx(ctx, nil)
		if err != nil {
			cleanup()
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT id FROM worklog_entry WHERE id = ? FOR UPDATE`, entryID); err != nil {
			_ = tx.Rollback()
			cleanup()
			return err
		}
		if code, msg := m.checkPhotoMembersWith(ctx, tx, entryID, names, 0, isPlain); code != 0 {
			_ = tx.Rollback()
			cleanup()
			return &slotFail{code: code, msg: msg}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO worklog_photo (entry_id, cos_key, url, members, md5, sgcc_img_id, sgcc_synced)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			entryID, key, m.photoURL(key, ""), string(membersJSON), hex.EncodeToString(imgMD5[:]),
			links, boolToInt(m.SgccRemote != nil))
		if err != nil {
			_ = tx.Rollback()
			cleanup()
			return err
		}
		photoID, _ = res.LastInsertId()
		if isPlain {
			if _, err := tx.ExecContext(ctx,
				`UPDATE worklog_photo SET is_watermark = 0, verify_status = 'skipped' WHERE id = ?`, photoID); err != nil {
				_ = tx.Rollback()
				cleanup()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			cleanup()
			return err
		}
		photoOut = m.photoURL(key, "")
		if rec != nil {
			rec.SetPhoto(photoID, photoOut)
		}
		if isPlain {
			return nil
		}
		m.runVerify(ctx, photoID, ident.Username, logDate, destName, photoOut)
		return nil
	}
	if m.PhotoOps != nil && m.SgccRemote != nil {
		opID := m.PhotoOps.StartPhotoOp("upload", entryTeamID, entryID, 0,
			func(ctx context.Context, rec PhotoOpRec) error { return run(ctx, rec) })
		writeOK(w, map[string]any{"opId": opID}, "已发起上传，商旅同步中")
		return
	}
	if err := run(r.Context(), nil); err != nil {
		writeRunErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": photoID}, "照片已上传")
}

// runVerify 触发验证流水线（DisableAsync 同步执行，测试确定性；生产异步）。
func (m *Module) runVerify(parent context.Context, photoID int64, username, logDate, destName, url string) {
	if parent == nil {
		parent = context.Background()
	}
	ctx := logging.WithNewTraceID(context.WithoutCancel(parent))
	if m.DisableAsync {
		m.VerifyAndWriteBack(ctx, photoID, username, logDate, destName, url)
		return
	}
	go m.VerifyAndWriteBack(ctx, photoID, username, logDate, destName, url)
}

// wmFields 手动水印字段（渲染与修正共用）。
type wmFields struct {
	Content     string `json:"content"`
	Time        string `json:"time"`
	Weather     string `json:"weather"`
	Location    string `json:"location"`
	Longitude   string `json:"longitude"`
	Latitude    string `json:"latitude"`
	Lng         string `json:"lng"`
	Lat         string `json:"lat"`
	AntiCode    string `json:"antiCode"`
	Orientation string `json:"orientation"`
}

// wmRenderer 水印渲染器：与「水印添加」共用同一实现（Node 侧 worklog/render-photo.js 两应用同口径）；
// 测试可替换为桩。
var wmRenderer wmadd.PhotoRenderer = &wmadd.WatermarkRenderer{}

// renderWMPicture 水印渲染：字段清洗（截断 + 防伪码字符集校验/服务端重生成）后走共享渲染器；
// 方向按小程序 wx.getImageInfo 标签（wm.orientation）摆正，产物 JPEG。
func renderWMPicture(buf []byte, wm wmFields) ([]byte, error) {
	lng := wm.Longitude
	if lng == "" {
		lng = wm.Lng
	}
	lat := wm.Latitude
	if lat == "" {
		lat = wm.Lat
	}
	fields := wmadd.SanitizeWm(map[string]string{
		"content":   wm.Content,
		"time":      wm.Time,
		"weather":   wm.Weather,
		"location":  wm.Location,
		"longitude": lng,
		"latitude":  lat,
		"antiCode":  wm.AntiCode,
	})
	return wmRenderer.Render(buf, fields, wm.Orientation)
}

// VerifyAndWriteBack Dify 识别（含重试）→ 回写（写库前重查目的地规避竞态）→ 必要时通知人工审核。
// 同时是 sgccclockin.PhotoVerifier 钩子（费用照片拉下入库后异步触发，main 生产装配注入）。
// 入口统一占用全局 4 路信号量并在飞集合去重：本地上传、失败重验与商旅回调共用一个上限。
func (m *Module) VerifyAndWriteBack(ctx context.Context, photoID int64, username, logDate, destName, url string) {
	release, ok := m.beginVerify(ctx, photoID)
	if !ok {
		return
	}
	defer release()
	m.verifyAndWriteBack(ctx, photoID, username, logDate, destName, url)
}

// verifyConcurrency 验证（Dify 识别）全局并发上限。
const verifyConcurrency = 4

// beginVerify 登记在飞照片并占用一路验证信号量；同一照片已在飞或等待期 ctx 取消时返回 ok=false。
func (m *Module) beginVerify(ctx context.Context, photoID int64) (release func(), ok bool) {
	m.verifyMu.Lock()
	if m.verifySem == nil {
		m.verifySem = make(chan struct{}, verifyConcurrency)
		m.verifyInFlight = map[int64]struct{}{}
	}
	if _, exists := m.verifyInFlight[photoID]; exists {
		m.verifyMu.Unlock()
		return nil, false
	}
	m.verifyInFlight[photoID] = struct{}{}
	m.verifyMu.Unlock()
	select {
	case m.verifySem <- struct{}{}:
		return func() {
			<-m.verifySem
			m.verifyMu.Lock()
			delete(m.verifyInFlight, photoID)
			m.verifyMu.Unlock()
		}, true
	case <-ctx.Done():
		m.verifyMu.Lock()
		delete(m.verifyInFlight, photoID)
		m.verifyMu.Unlock()
		return nil, false
	}
}

// verifyAndWriteBack 验证流水线本体（并发与去重由 VerifyAndWriteBack 入口统一收口）。
func (m *Module) verifyAndWriteBack(ctx context.Context, photoID int64, username, logDate, destName, url string) {
	if m.Dify == nil {
		m.markPhotoFailed(ctx, photoID)
		return
	}
	vr := recognize(ctx, m.Dify, username, Dots(logDate), destName, url)
	var curDate, curDest string
	err := m.DB.QueryRowContext(ctx,
		`SELECT DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(d.name, '')
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE p.id = ?`, photoID).Scan(&curDate, &curDest)
	if err != nil {
		return // 照片已删除
	}
	if !verifyUsable(vr) {
		m.markPhotoFailed(ctx, photoID)
		if vr.OK && m.Notifier != nil {
			_ = m.Notifier.NotifyManualReview(ctx, photoID)
		}
		return
	}
	chk := CheckWatermark(vr.Time, vr.Location, Dots(curDate), curDest)
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_photo SET verify_status = ?, work_content = ?, shot_time = ?, weather = ?,
		 location = ?, lng = ?, lat = ?, date_ok = ?, dest_ok = ? WHERE id = ?`,
		chk.Status, vr.WorkContent, vr.Time, vr.Weather, vr.Location, vr.Lng, vr.Lat,
		boolToInt(chk.DateOK), boolToInt(chk.DestOK), photoID)
}

// markPhotoFailed 置验证失败（清空识别字段）。
func (m *Module) markPhotoFailed(ctx context.Context, photoID int64) {
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_photo SET verify_status = 'failed', work_content = '', shot_time = '', weather = '',
		 location = '', lng = '', lat = '', date_ok = NULL, dest_ok = NULL WHERE id = ?`, photoID)
}

// handlePhotoVerify POST /photos/{id}/verify：验证失败后重新验证（重置 pending 并重调 Dify）。
func (m *Module) handlePhotoVerify(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var status, logDate, destName, cosKey, url string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.verify_status, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(d.name, ''), p.cos_key, p.url
		 FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE p.id = ? AND `+entryVisibleSQL,
		photoID, team.ID, team.ID).Scan(&status, &logDate, &destName, &cosKey, &url)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	if status != "failed" {
		fail(w, http.StatusBadRequest, codeVerifyBusy, "仅验证失败的照片可重新验证")
		return
	}
	_, _ = m.DB.ExecContext(r.Context(),
		`UPDATE worklog_photo SET verify_status = 'pending', work_content = '', shot_time = '',
		 weather = '', location = '', lng = '', lat = '', date_ok = NULL, dest_ok = NULL WHERE id = ?`, photoID)
	m.runVerify(r.Context(), photoID, ident.Username, logDate, destName, m.photoURL(cosKey, url))
	writeOK(w, map[string]any{"verify_status": "pending"})
}

// handlePhotoUpdateWM PUT /photos/{id}/wm：手动修正水印识别信息（仅水印照片、非识别中可改）。
func (m *Module) handlePhotoUpdateWM(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var status string
	var isWM int
	var logDate, destName string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.verify_status, p.is_watermark, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(d.name, '')
		 FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE p.id = ? AND `+entryVisibleSQL,
		photoID, team.ID, team.ID).Scan(&status, &isWM, &logDate, &destName)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	if isWM == 0 {
		fail(w, http.StatusBadRequest, codeVerifyBusy, "非水印照片不参与验证")
		return
	}
	if status == "pending" {
		fail(w, http.StatusBadRequest, codeVerifyBusy, "照片识别中，请稍后再修正")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	workContent := truncate(strings.TrimSpace(rawString(body["workContent"])), 512)
	if workContent == "" {
		workContent = truncate(strings.TrimSpace(rawString(body["work_content"])), 512)
	}
	shotTime := truncate(strings.TrimSpace(rawString(body["shotTime"])), 32)
	if shotTime == "" {
		shotTime = truncate(strings.TrimSpace(rawString(body["shot_time"])), 32)
	}
	// 拍摄时间归一：YYYY-MM-DD → YYYY.MM.DD（核验为点分口径）
	shotTime = normalizeShotTime(shotTime)
	if workContent == "" {
		fail(w, http.StatusBadRequest, codeVerifyBusy, "施工内容不能为空")
		return
	}
	if shotTime == "" {
		fail(w, http.StatusBadRequest, codeVerifyBusy, "拍摄时间不能为空")
		return
	}
	weather := truncate(strings.TrimSpace(rawString(body["weather"])), 64)
	location := truncate(strings.TrimSpace(rawString(body["location"])), 255)
	lng := truncate(strings.TrimSpace(rawString(body["lng"])), 32)
	lat := truncate(strings.TrimSpace(rawString(body["lat"])), 32)

	chk := CheckWatermark(shotTime, location, Dots(logDate), destName)
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_photo SET verify_status = ?, work_content = ?, shot_time = ?, weather = ?,
		 location = ?, lng = ?, lat = ?, date_ok = ?, dest_ok = ? WHERE id = ?`,
		chk.Status, workContent, shotTime, weather, location, lng, lat,
		boolToInt(chk.DateOK), boolToInt(chk.DestOK), photoID); err != nil {
		writeErr(w, err)
		return
	}
	msg := "已保存，仍未通过核验"
	if chk.Status == "passed" {
		msg = "已保存，照片核验通过"
	}
	writeOK(w, map[string]any{
		"id": photoID, "verify_status": chk.Status,
		"date_ok": boolToInt(chk.DateOK), "dest_ok": boolToInt(chk.DestOK),
	}, msg)
}

// normalizeShotTime 拍摄时间归一：全文把 YYYY-M-D 归一为 YYYY.MM.DD（月日补零；Node replace 全量同口径）。
func normalizeShotTime(s string) string {
	pad2 := func(v string) string {
		if len(v) == 1 {
			return "0" + v
		}
		return v
	}
	return shotTimeDateRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := shotTimeDateRe.FindStringSubmatch(m)
		return parts[1] + "." + pad2(parts[2]) + "." + pad2(parts[3])
	})
}

// shotTimeDateRe 拍摄时间中的年-月-日片段（月/日 1~2 位）。
var shotTimeDateRe = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})`)

// handlePhotoUpdateMembers PUT /photos/{id}/members：调整照片所属人名。
func (m *Module) handlePhotoUpdateMembers(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var entryID, entryTeamID int64
	var isWM int
	var membersRaw string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.entry_id, p.members, e.team_id, p.is_watermark FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE p.id = ? AND `+entryVisibleSQL, photoID, team.ID, team.ID).
		Scan(&entryID, &membersRaw, &entryTeamID, &isWM)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	names := parseNameList(body["members"])
	if len(names) == 0 {
		fail(w, http.StatusBadRequest, codePickMembers, "请选择照片所属人名")
		return
	}
	if code, msg := m.checkPhotoMembers(r.Context(), entryID, names, photoID, isWM == 0); code != 0 {
		fail(w, http.StatusBadRequest, code, msg)
		return
	}
	oldNames := parseNames(membersRaw)
	// 先校验未绑定/登录过期成员的人名状态不可更改（增减均拒绝；此类成员的商旅联动无法进行）
	if m.SgccRemote != nil {
		if msg := m.checkSgccBound(r, entryID, oldNames, names); msg != "" {
			fail(w, http.StatusBadRequest, codeBoundMember, msg)
			return
		}
	}
	if m.SgccOps != nil && m.SgccOps.ActivePhotoOp(entryTeamID, entryID) {
		fail(w, http.StatusConflict, codeOpRunning, "该卡片有商旅同步任务进行中，请稍后再试")
		return
	}
	removed := diffNames(oldNames, names)
	// 变更双向远端先行——剔除人名先从其商旅费用照片移除、新增人名先补传进商旅费用照片，全部成功后才变更本地；
	// 任务化（双注入）异步执行登记后返回 opId，端侧轮询 /op/status 收尾
	membersJSON, _ := json.Marshal(names)
	run := func(ctx context.Context, rec PhotoOpRec) error {
		if m.SgccRemote != nil {
			if len(removed) > 0 {
				if err := m.SgccRemote.RemoveMembers(ctx, photoID, removed); err != nil {
					if rec != nil {
						rec.SetFailedName(failedNameOf(err))
					}
					return &remoteFail{msg: fmt.Sprintf("商旅费用照片删除失败（%v），本地未修改", err)}
				}
			}
			if err := m.SgccRemote.SyncMembers(ctx, photoID, names); err != nil {
				if rec != nil {
					rec.SetFailedName(failedNameOf(err))
				}
				return &remoteFail{msg: fmt.Sprintf("商旅费用照片同步失败（%v），本地未修改", err)}
			}
		}
		_, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_photo SET members = ? WHERE id = ?`, string(membersJSON), photoID)
		return err
	}
	if m.PhotoOps != nil && m.SgccRemote != nil {
		opID := m.PhotoOps.StartPhotoOp("members", entryTeamID, entryID, photoID,
			func(ctx context.Context, rec PhotoOpRec) error { return run(ctx, rec) })
		writeOK(w, map[string]any{"opId": opID}, "已发起人名修改，商旅同步中")
		return
	}
	if err := run(r.Context(), nil); err != nil {
		writeRunErr(w, err)
		return
	}
	writeOK(w, map[string]any{"members": names})
}

// diffNames 返回在 old 中、不在 next 的人名（剔除名单）。
func diffNames(old, next []string) []string {
	in := map[string]bool{}
	for _, n := range next {
		in[n] = true
	}
	out := make([]string, 0)
	for _, n := range old {
		if !in[n] {
			out = append(out, n)
		}
	}
	return out
}

// failedNameOf 从远端错误中取失败人名（实现侧可包装带人名的错误；无法识别返回空串）。
type namedError interface{ FailedName() string }

func failedNameOf(err error) string {
	var ne namedError
	if errors.As(err, &ne) {
		return ne.FailedName()
	}
	return ""
}

// checkSgccBound 调人名前校验：状态变化的成员须绑定商旅且登录态有效（token_status=1），否则返回提示（空串=通过）。
func (m *Module) checkSgccBound(r *http.Request, entryID int64, oldNames, names []string) string {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT m.name, a.token_status FROM worklog_entry_member em
		 JOIN worklog_member m ON m.id = em.member_id
		 LEFT JOIN worklog_sgcc_account a ON a.member_id = m.id
		 WHERE em.entry_id = ?`, entryID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	now := map[string]bool{}
	for _, n := range names {
		now[n] = true
	}
	was := map[string]bool{}
	for _, n := range oldNames {
		was[n] = true
	}
	for rows.Next() {
		var name string
		var tokenStatus sql.NullInt64
		if rows.Scan(&name, &tokenStatus) != nil {
			continue
		}
		if tokenStatus.Valid && tokenStatus.Int64 == 1 {
			continue // 绑定且登录态有效
		}
		if was[name] != now[name] {
			reason := "未绑定商旅账号"
			if tokenStatus.Valid && tokenStatus.Int64 == 0 {
				reason = "商旅登录已过期"
			}
			return "成员「" + name + "」" + reason + "，人名状态不可更改"
		}
	}
	return ""
}

// handlePhotoDelete DELETE /photos/{id}：删除照片（远端先解除商旅费用照片关联，成功后才删 COS 对象与库记录）。
func (m *Module) handlePhotoDelete(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var cosKey string
	var entryTeamID, entryID int64
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.cos_key, p.entry_id, e.team_id FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE p.id = ? AND `+entryVisibleSQL, photoID, team.ID, team.ID).Scan(&cosKey, &entryID, &entryTeamID)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	if m.SgccOps != nil && m.SgccOps.ActivePhotoOp(entryTeamID, entryID) {
		fail(w, http.StatusConflict, codeOpRunning, "该卡片有商旅同步任务进行中，请稍后再试")
		return
	}
	run := func(ctx context.Context, rec PhotoOpRec) error {
		if m.SgccRemote != nil {
			if err := m.SgccRemote.Unlink(ctx, photoID); err != nil {
				if rec != nil {
					rec.SetFailedName(failedNameOf(err))
				}
				return &remoteFail{msg: fmt.Sprintf("商旅费用照片解除失败（%v），本地未删除", err)}
			}
		}
		if m.Store != nil && cosKey != "" {
			_ = m.Store.DeleteObject(ctx, cosKey)
		}
		_, err := m.DB.ExecContext(ctx, `DELETE FROM worklog_photo WHERE id = ?`, photoID)
		return err
	}
	if m.PhotoOps != nil && m.SgccRemote != nil {
		opID := m.PhotoOps.StartPhotoOp("delete", entryTeamID, entryID, photoID,
			func(ctx context.Context, rec PhotoOpRec) error { return run(ctx, rec) })
		writeOK(w, map[string]any{"opId": opID}, "已发起删除，解除商旅关联中")
		return
	}
	if err := run(r.Context(), nil); err != nil {
		writeRunErr(w, err)
		return
	}
	writeOK(w, nil)
}

// handlePhotoDownload GET /photos/{id}/download：单张照片下载代理（COS 跨域无 CORS，网页端经本接口回源并附下载文件名）。
func (m *Module) handlePhotoDownload(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var cosKey, url, logDate string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.cos_key, p.url, DATE_FORMAT(e.log_date, '%Y-%m-%d')
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE p.id = ? AND `+entryVisibleSQL, photoID, team.ID, team.ID).Scan(&cosKey, &url, &logDate)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	buf, err := fetchObjectBytes(r.Context(), m.Store, cosKey, m.photoURL(cosKey, url))
	if err != nil {
		fail(w, http.StatusBadGateway, codeUpstream, "照片回源失败（"+err.Error()+"）")
		return
	}
	// 文件名沿用打包下载规则：日期-照片id.jpg；RFC5987 编码，附 ASCII fallback
	name := (logDate + "-" + itoa(photoID) + ".jpg")
	contentType := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(cosKey), ".png") {
		contentType = "image/png"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.Header().Set("Content-Disposition",
		`attachment; filename="photo-`+itoa(photoID)+`.jpg"; filename*=UTF-8''`+encURI(name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// handlePhotoList GET /photos?from=&to=：日期范围内的全部照片（批量下载用；仅 PDF 组合过滤水印）。
func (m *Module) handlePhotoList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeDateRange, "开始日期不能晚于结束日期")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT p.id, p.cos_key, p.url, DATE_FORMAT(e.log_date, '%Y-%m-%d'), DAYOFMONTH(e.log_date), p.members
		 FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? ORDER BY e.log_date, p.id`,
		from, to, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID      int64    `json:"id"`
		CosKey  string   `json:"cos_key"`
		URL     string   `json:"url"`
		LogDate string   `json:"log_date"`
		Month   string   `json:"month"`
		Day     int      `json:"day"`
		Members []string `json:"members"`
	}
	list := make([]item, 0)
	base := m.urlBase()
	for rows.Next() {
		var it item
		var cosKey, url, membersJSON string
		if rows.Scan(&it.ID, &cosKey, &url, &it.LogDate, &it.Day, &membersJSON) == nil {
			it.CosKey = cosKey
			it.URL = base.join(cosKey, url)
			it.Month = it.LogDate[:7]
			it.Members = parseNames(membersJSON)
			list = append(list, it)
		}
	}
	writeOK(w, map[string]any{"list": list})
}

// handlePhotosPDF GET /photos.pdf?from=&to=：范围照片组合 PDF（复用 compat/imagepdf.BuildPhotosPDF）；
// 带 job_id 时改为取后台任务缓存 PDF（GET /photos.pdf?job_id=...，完成后取件）。
func (m *Module) handlePhotosPDF(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if jobID := strings.TrimSpace(r.URL.Query().Get("job_id")); jobID != "" {
		var teamID int64
		if team != nil {
			teamID = team.ID
		}
		m.servePDFJob(w, ident.ID, teamID, jobID)
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeDateRange, "开始日期不能晚于结束日期")
		return
	}
	if err := m.checkPDFRange(from, to); err != nil {
		writePDFBudgetErr(w, err)
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	release, ok := m.tryBeginPDFJob()
	if !ok {
		fail(w, http.StatusConflict, codeOpRunning, pdfBusyMsg)
		return
	}
	// 名额只覆盖生成阶段：生成一返回立即释放，慢客户端写响应不占名额（panic 也 exactly once）。
	pdf, failed, err := func() ([]byte, int, error) {
		defer release()
		return m.buildRangePhotosPDF(r.Context(), team.ID, from, to)
	}()
	if err != nil {
		if writePDFBudgetErr(w, err) {
			return
		}
		writeErr(w, err)
		return
	}
	if len(pdf) == 0 {
		fail(w, http.StatusNotFound, codeNoPhotos, "该日期范围没有水印照片")
		return
	}
	_ = failed
	fileName := "水印照片-" + from + ".pdf"
	if from != to {
		fileName = "水印照片-" + from + "至" + to + ".pdf"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition",
		`attachment; filename="photos-`+from+`.pdf"; filename*=UTF-8''`+encURI(fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// handlePhotosPDFSaveNetdisk POST /photos-pdf/save-netdisk {from,to,dir?,space?}：
// 组合 PDF 保存到网盘（space: my 我的空间（缺省）/ public 公共区；dir 缺省 出工日志/）。
func (m *Module) handlePhotosPDFSaveNetdisk(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	from, to := rawString(body["from"]), rawString(body["to"])
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeDateRange, "开始日期不能晚于结束日期")
		return
	}
	if err := m.checkPDFRange(from, to); err != nil {
		writePDFBudgetErr(w, err)
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	release, ok := m.tryBeginPDFJob()
	if !ok {
		fail(w, http.StatusConflict, codeOpRunning, pdfBusyMsg)
		return
	}
	// 名额只覆盖生成阶段：生成一返回立即释放，慢客户端写响应不占名额（panic 也 exactly once）。
	pdf, failed, err := func() ([]byte, int, error) {
		defer release()
		return m.buildRangePhotosPDF(r.Context(), team.ID, from, to)
	}()
	if err != nil {
		if writePDFBudgetErr(w, err) {
			return
		}
		writeErr(w, err)
		return
	}
	if len(pdf) == 0 {
		fail(w, http.StatusNotFound, codeNoPhotos, "该日期范围没有水印照片")
		return
	}
	fileName := "水印照片-" + from + ".pdf"
	if from != to {
		fileName = "水印照片-" + from + "至" + to + ".pdf"
	}
	space := "my"
	if rawString(body["space"]) == "public" {
		space = "public"
	}
	dir := "出工日志"
	if raw, has := body["dir"]; has && string(raw) != "null" {
		dir = rawString(raw)
	}
	path, err := m.saveToNetdisk(r.Context(), ident, NetdiskSaveRequest{
		Space: space, Dir: dir, Name: fileName, Body: pdf,
	})
	if err != nil {
		writeNetdiskErr(w, err)
		return
	}
	writeOK(w, map[string]any{"path": path, "failed": failed}, "已保存到网盘")
}

// photoGroup 一张卡片的照片回源引用组（组合 PDF 按卡分页）。
type photoGroup struct {
	entryID int64
	refs    []photoRef
}

// 组合 PDF 资源预算默认值（字段为 0 时生效）。
const (
	pdfDefaultMaxRangeDays = 92
	pdfDefaultMaxPhotos    = 300
	pdfDefaultMaxBytes     = int64(512 << 20)
	pdfJobConcurrency      = 2
)

var (
	// errPDFRangeTooLong 日期跨度超出组合 PDF 预算。
	errPDFRangeTooLong = errors.New("日期范围超出组合 PDF 上限")
	// errPDFTooManyPhotos 照片张数超出组合 PDF 预算。
	errPDFTooManyPhotos = errors.New("照片数量超出组合 PDF 上限")
	// errPDFBudgetExceeded 回源累计字节超出组合 PDF 预算。
	errPDFBudgetExceeded = errors.New("照片累计大小超出组合 PDF 上限")
	// errPDFTimeout 组合 PDF 生成总超时。
	errPDFTimeout = errors.New("组合 PDF 生成超时，请缩小日期范围后重试")
)

const pdfDefaultTimeout = 5 * time.Minute

func (m *Module) pdfBuildTimeout() time.Duration {
	if m.PDFTimeout > 0 {
		return m.PDFTimeout
	}
	return pdfDefaultTimeout
}

func (m *Module) pdfMaxRangeDays() int {
	if m.PDFMaxRangeDays > 0 {
		return m.PDFMaxRangeDays
	}
	return pdfDefaultMaxRangeDays
}

func (m *Module) pdfMaxPhotos() int {
	if m.PDFMaxPhotos > 0 {
		return m.PDFMaxPhotos
	}
	return pdfDefaultMaxPhotos
}

func (m *Module) pdfMaxBytes() int64 {
	if m.PDFMaxBytes > 0 {
		return m.PDFMaxBytes
	}
	return pdfDefaultMaxBytes
}

// checkPDFRange 校验日期跨度（含首尾；from/to 已通过格式与先后校验）。
func (m *Module) checkPDFRange(from, to string) error {
	f, err1 := time.Parse("2006-01-02", from)
	t, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return nil // 格式错误由调用方既有分支处理
	}
	days := int(t.Sub(f)/(24*time.Hour)) + 1
	if max := m.pdfMaxRangeDays(); days > max {
		return fmt.Errorf("%w（最多 %d 天，本次 %d 天）", errPDFRangeTooLong, max, days)
	}
	return nil
}

// writePDFBudgetErr 组合 PDF 预算错误出口（可辨识业务码）；非预算错误返回 false。
func writePDFBudgetErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errPDFRangeTooLong):
		fail(w, http.StatusBadRequest, codeDateRange, err.Error())
	case errors.Is(err, errPDFTooManyPhotos):
		fail(w, http.StatusBadRequest, codeFileCount, err.Error())
	case errors.Is(err, errPDFBudgetExceeded):
		fail(w, http.StatusBadRequest, codePhotoSize, err.Error())
	case errors.Is(err, errPDFTimeout):
		fail(w, http.StatusInternalServerError, codeInternal, err.Error())
	default:
		return false
	}
	return true
}

// ensurePDFJobSem 取（或懒初始化）组合 PDF 全局任务并发预算。
func (m *Module) ensurePDFJobSem() chan struct{} {
	m.pdfJobMu.Lock()
	defer m.pdfJobMu.Unlock()
	if m.pdfJobSem == nil {
		m.pdfJobSem = make(chan struct{}, pdfJobConcurrency)
	}
	return m.pdfJobSem
}

// tryBeginPDFJob 非阻塞占用一路组合 PDF 预算：占满立即返回 false（不排队等待）。
func (m *Module) tryBeginPDFJob() (release func(), ok bool) {
	sem := m.ensurePDFJobSem()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true
	default:
		return nil, false
	}
}

// pdfBusyMsg 组合 PDF 并发预算占满时的可读提示。
const pdfBusyMsg = "组合 PDF 任务繁忙，请稍后重试"

// listRangePhotoGroups 范围内水印照片按卡分组（同步生成与后台任务共用一条查询口径）。
func (m *Module) listRangePhotoGroups(ctx context.Context, teamID int64, from, to string) ([]photoGroup, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT p.entry_id, p.cos_key, p.url FROM worklog_photo p JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND p.is_watermark = 1
		 ORDER BY e.log_date, e.created_at, e.id, p.id`,
		from, to, teamID)
	if err != nil {
		return nil, err
	}
	byEntry := map[int64][]photoRef{}
	order := make([]int64, 0)
	base := m.urlBase()
	for rows.Next() {
		var entryID int64
		var cosKey, url string
		if rows.Scan(&entryID, &cosKey, &url) == nil {
			if _, ok := byEntry[entryID]; !ok {
				order = append(order, entryID)
			}
			byEntry[entryID] = append(byEntry[entryID], photoRef{cosKey: cosKey, url: base.join(cosKey, url)})
		}
	}
	rows.Close()
	groups := make([]photoGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, photoGroup{entryID: id, refs: byEntry[id]})
	}
	total := 0
	for _, g := range groups {
		total += len(g.refs)
	}
	if max := m.pdfMaxPhotos(); total > max {
		return nil, fmt.Errorf("%w（最多 %d 张，本次 %d 张）", errPDFTooManyPhotos, max, total)
	}
	return groups, nil
}

// buildPhotosPDFFromGroups 按卡组装 PDF（每卡两张一页，复用 compat/imagepdf.BuildPhotosPDF）；
// 有界并发回源（fetchObjectsOrdered），onProgress 每处理完一张照片回调
// （回源成功/失败均推进实际进度，可为 nil）。
// groups 的访问地址由 listRangePhotoGroups 按请求解析一次前缀后带入，这里不再重算。
// 无可用照片返回空 buf；failed 为回源失败的照片数（记名跳过，不中断）。
func (m *Module) buildPhotosPDFFromGroups(ctx context.Context, groups []photoGroup, onProgress func(cur, total int)) (pdf []byte, failed int, err error) {
	flat := make([]photoRef, 0)
	for _, g := range groups {
		for _, ref := range g.refs {
			flat = append(flat, photoRef{cosKey: ref.cosKey, url: ref.url})
		}
	}
	bufs := make([][]byte, len(flat))
	budget := m.pdfMaxBytes()
	used := int64(0)
	var budgetErr error
	buildCtx, cancelBuild := context.WithTimeout(ctx, m.pdfBuildTimeout())
	defer cancelBuild()
	fetchCtx, cancel := context.WithCancel(buildCtx)
	defer cancel()
	fetchObjectsOrdered(fetchCtx, m.Store, flat, fetchConcurrency, onProgress,
		func(i int, buf []byte, err error) {
			if err != nil || len(buf) == 0 || int64(len(buf)) > zipPhotoMaxLen {
				failed++
				return
			}
			if budget > 0 && used+int64(len(buf)) > budget {
				if budgetErr == nil {
					budgetErr = fmt.Errorf("%w（累计 %d > %d 字节）", errPDFBudgetExceeded, used+int64(len(buf)), budget)
				}
				cancel() // 停止后续批次回源，避免继续放大内存
				return
			}
			used += int64(len(buf))
			bufs[i] = buf
		})
	if budgetErr != nil {
		return nil, failed, budgetErr
	}
	if buildCtx.Err() != nil {
		return nil, failed, errPDFTimeout
	}
	cards := make([]imagepdf.PhotoCard, 0, len(groups))
	idx := 0
	for _, g := range groups {
		photos := make([][]byte, 0, len(g.refs))
		for range g.refs {
			if bufs[idx] != nil {
				photos = append(photos, bufs[idx])
			}
			idx++
		}
		if len(photos) > 0 {
			cards = append(cards, imagepdf.PhotoCard{Photos: photos})
		}
	}
	if len(cards) == 0 {
		return nil, failed, nil
	}
	built, err := imagepdf.BuildPhotosPDF(cards)
	if err != nil {
		return nil, failed, err
	}
	if buildCtx.Err() != nil {
		return nil, failed, errPDFTimeout
	}
	return built, failed, nil
}

// buildRangePhotosPDF 组合范围内水印照片为 PDF（按卡片分组，每卡两张一页）；
// 无照片返回空 buf；failed 为回源失败的照片数（记名跳过，不中断）。
func (m *Module) buildRangePhotosPDF(ctx context.Context, teamID int64, from, to string) (pdf []byte, failed int, err error) {
	groups, err := m.listRangePhotoGroups(ctx, teamID, from, to)
	if err != nil {
		return nil, 0, err
	}
	return m.buildPhotosPDFFromGroups(ctx, groups, nil)
}

// photoRef 照片回源引用。
type photoRef struct {
	cosKey string
	url    string
}

// ---- 组合 PDF 后台任务（内存任务表，TTL 过期即弃、不持久化）----

const (
	// pdfJobTTL 任务保留时长（创建起算；完成后缓存 PDF 也在该窗口内可取）。
	pdfJobTTL = 10 * time.Minute
	// pdfJobRunning / pdfJobDone / pdfJobFailed 任务状态字面量（GET /photos.pdf/jobs/{id} 下发）。
	pdfJobRunning = "running"
	pdfJobDone    = "done"
	pdfJobFailed  = "failed"
)

// pdfJob 组合 PDF 后台任务（仅存内存，进程重启即失）；pdf 为完成后缓存产物。
type pdfJob struct {
	id      string
	userID  int64 // 创建人（仅本人可见可取）
	teamID  int64 // 生效班组
	from    string
	to      string
	status  string // running / done / failed
	current int    // 已实际下载/处理的照片数
	total   int    // 范围内照片总数
	errCode int    // 失败业务码（无照片 40402，其余 50000）
	errMsg  string // 失败原因（running/done 为空串）
	pdf     []byte // 完成后的缓存 PDF（失败为空）
	created time.Time
	doneAt  time.Time
}

// newPDFJobID 生成任务 id（随机十六进制，不可猜测）。
func newPDFJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// purgeExpiredPDFJobs 清理超期任务（持锁调用）。
func (m *Module) purgeExpiredPDFJobs() {
	now := time.Now()
	for id, job := range m.pdfJobs {
		if now.Sub(job.created) > pdfJobTTL {
			delete(m.pdfJobs, id)
		}
	}
}

// putPDFJob 登记新任务（顺手清理过期任务）。
func (m *Module) putPDFJob(job *pdfJob) {
	m.pdfJobMu.Lock()
	defer m.pdfJobMu.Unlock()
	if m.pdfJobs == nil {
		m.pdfJobs = map[string]*pdfJob{}
	}
	m.purgeExpiredPDFJobs()
	m.pdfJobs[job.id] = job
}

// pdfJobByID 取任务快照（仅创建人 + 生效班组可见；超期/他人的视同不存在）。
func (m *Module) pdfJobByID(id string, userID, teamID int64) *pdfJob {
	m.pdfJobMu.Lock()
	defer m.pdfJobMu.Unlock()
	m.purgeExpiredPDFJobs()
	job := m.pdfJobs[id]
	if job == nil || job.userID != userID || job.teamID != teamID {
		return nil
	}
	snapshot := *job
	return &snapshot
}

// updatePDFJob 更新任务字段（持锁执行 fn）。
func (m *Module) updatePDFJob(id string, fn func(job *pdfJob)) {
	m.pdfJobMu.Lock()
	defer m.pdfJobMu.Unlock()
	if job := m.pdfJobs[id]; job != nil {
		fn(job)
	}
}

// finishPDFJob 落任务终态：err 优先；产物为空按无照片（40402）失败；否则缓存 PDF 取件。
func (m *Module) finishPDFJob(id string, pdf []byte, err error) {
	m.updatePDFJob(id, func(job *pdfJob) {
		job.doneAt = time.Now()
		switch {
		case err != nil:
			job.status = pdfJobFailed
			switch {
			case errors.Is(err, errPDFBudgetExceeded):
				job.errCode = codePhotoSize
				job.errMsg = err.Error()
			case errors.Is(err, errPDFTooManyPhotos):
				job.errCode = codeFileCount
				job.errMsg = err.Error()
			case errors.Is(err, errPDFRangeTooLong):
				job.errCode = codeDateRange
				job.errMsg = err.Error()
			case errors.Is(err, errPDFTimeout):
				job.errCode = codeInternal
				job.errMsg = err.Error()
			default:
				job.errCode = codeInternal
				job.errMsg = "服务器开小差了，请稍后再试"
			}
		case len(pdf) == 0:
			job.status = pdfJobFailed
			job.errCode = codeNoPhotos
			job.errMsg = "该日期范围没有水印照片"
		default:
			job.status = pdfJobDone
			job.pdf = pdf
		}
	})
}

// handlePhotosPDFJobCreate POST /photos.pdf/jobs {from,to}：发起组合 PDF 后台任务。
// 返回 {job_id,total}；进度按实际下载/处理照片推进，完成后 GET /photos.pdf?job_id= 取缓存 PDF。
// 无照片不建任务，按现有口径 40402。DisableAsync 时同步跑完（测试确定性）。
func (m *Module) handlePhotosPDFJobCreate(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	from, to := rawString(body["from"]), rawString(body["to"])
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeDateRange, "开始日期不能晚于结束日期")
		return
	}
	if err := m.checkPDFRange(from, to); err != nil {
		writePDFBudgetErr(w, err)
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	groups, err := m.listRangePhotoGroups(r.Context(), team.ID, from, to)
	if err != nil {
		if writePDFBudgetErr(w, err) {
			return
		}
		writeErr(w, err)
		return
	}
	total := 0
	for _, g := range groups {
		total += len(g.refs)
	}
	if total == 0 {
		fail(w, http.StatusNotFound, codeNoPhotos, "该日期范围没有水印照片")
		return
	}
	// 先占全局并发预算：占满立即拒绝，避免无界 goroutine 排队。
	release, ok := m.tryBeginPDFJob()
	if !ok {
		fail(w, http.StatusConflict, codeOpRunning, pdfBusyMsg)
		return
	}
	job := &pdfJob{
		id: newPDFJobID(), userID: ident.ID, teamID: team.ID,
		from: from, to: to, status: pdfJobRunning, total: total, created: time.Now(),
	}
	m.putPDFJob(job)
	run := func(ctx context.Context) {
		// 名额只覆盖生成阶段，完成后立即释放；产物写入任务表与后续取件不占名额。
		pdf, _, err := func() ([]byte, int, error) {
			defer release()
			return m.buildPhotosPDFFromGroups(ctx, groups, func(cur, _ int) {
				m.updatePDFJob(job.id, func(j *pdfJob) { j.current = cur })
			})
		}()
		m.finishPDFJob(job.id, pdf, err)
	}
	if m.DisableAsync {
		run(r.Context())
	} else {
		go run(logging.WithNewTraceID(context.WithoutCancel(r.Context())))
	}
	writeOK(w, map[string]any{"job_id": job.id, "total": total})
}

// handlePhotosPDFJobStatus GET /photos.pdf/jobs/{id}：任务进度 {id,status,current,total,error}。
func (m *Module) handlePhotosPDFJobStatus(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	var teamID int64
	if team != nil {
		teamID = team.ID
	}
	job := m.pdfJobByID(chi.URLParam(r, "id"), ident.ID, teamID)
	if job == nil {
		fail(w, http.StatusNotFound, codeMissing, "任务不存在或已过期")
		return
	}
	writeOK(w, map[string]any{
		"id": job.id, "status": job.status, "current": job.current,
		"total": job.total, "error": job.errMsg,
	})
}

// servePDFJob GET /photos.pdf?job_id=：取后台任务缓存 PDF（完成才可取件）；
// 进行中 40909，失败按任务错误码下发（无照片 40402 与同步口径一致）。
func (m *Module) servePDFJob(w http.ResponseWriter, userID, teamID int64, jobID string) {
	job := m.pdfJobByID(jobID, userID, teamID)
	if job == nil {
		fail(w, http.StatusNotFound, codeMissing, "任务不存在或已过期")
		return
	}
	switch job.status {
	case pdfJobDone:
		fileName := "水印照片-" + job.from + ".pdf"
		if job.from != job.to {
			fileName = "水印照片-" + job.from + "至" + job.to + ".pdf"
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition",
			`attachment; filename="photos-`+job.from+`.pdf"; filename*=UTF-8''`+encURI(fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(job.pdf)
	case pdfJobFailed:
		status := http.StatusInternalServerError
		switch job.errCode {
		case codeNoPhotos:
			status = http.StatusNotFound
		case codeDateRange, codeFileCount, codePhotoSize:
			status = http.StatusBadRequest
		}
		fail(w, status, job.errCode, job.errMsg)
	default:
		fail(w, http.StatusConflict, codeOpRunning, "任务进行中，请稍后取件")
	}
}

// parseNameList 解析人名数组字段（去空白去重保序）。
func parseNameList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := jsonUnmarshal(raw, &arr); err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(arr))
	for _, n := range arr {
		n = strings.TrimSpace(n)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
