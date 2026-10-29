package treehazard

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/httpapi"
)

// ObjectStore 对象存储适配层（COS）：真实外部服务必须经此注入，测试不得实际调用。
type ObjectStore interface {
	// Put 上传对象，返回公开访问地址。
	Put(ctx context.Context, key string, data []byte, contentType string) (url string, err error)
	// PublicURL 按对象键生成当前访问地址（兼容域名配置变更前写入的历史 url）。
	PublicURL(key string) string
	// Delete 删除对象。
	Delete(ctx context.Context, key string) error
}

// BatchFileStore 随批 xlsx 落盘（data/treehazard/inbox/）；测试可注入内存实现。
type BatchFileStore interface {
	// Save 保存 xlsx，返回相对 server 目录的存储路径（如 data/treehazard/inbox/1/abc.xlsx）。
	Save(ctx context.Context, teamID int64, sessionID string, data []byte) (relPath string, err error)
	// Remove 删除已落盘文件（不存在视为成功）。
	Remove(ctx context.Context, relPath string) error
}

const (
	photoMaxBytes       = 50 * 1024 * 1024
	photoMaxPerRecord   = 9
	defaultObjectPrefix = "treehazard/"
)

// dataURLPhoto dataURL 解析产物（jpeg/png）。
type dataURLPhoto struct {
	buf         []byte
	contentType string
	ext         string
}

var dataURLRe = regexp.MustCompile(`^data:image/(jpeg|jpg|png);base64,(.+)$`)

// parseDataUrlPhoto 解析 jpeg/png base64 dataURL；非法返回 nil（同 Node utils/photo.js）。
func parseDataUrlPhoto(image string) *dataURLPhoto {
	m := dataURLRe.FindStringSubmatch(strings.TrimSpace(image))
	if m == nil {
		return nil
	}
	buf, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return nil
	}
	ext := "jpg"
	contentType := "image/jpeg"
	if m[1] == "png" {
		ext = "png"
		contentType = "image/png"
	}
	return &dataURLPhoto{buf: buf, contentType: contentType, ext: ext}
}

// objectPrefix 取 COS 键前缀（COS_TREEHAZARD_PREFIX，缺省 treehazard/，保证尾随 /）。
func (m *Module) objectPrefix(ctx context.Context) string {
	p := m.configValue(ctx, "COS_TREEHAZARD_PREFIX")
	if p == "" {
		return defaultObjectPrefix
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// currentPhotoUrl 按 cos_key 生成当前访问地址，无 key 时保留旧地址。
func (m *Module) currentPhotoUrl(cosKey, url string) string {
	if cosKey == "" {
		return url
	}
	if m.Objects == nil {
		return url
	}
	return m.Objects.PublicURL(cosKey)
}

// photoItem 照片 JSON（与 Node treehazard_photo 行同形）。
type photoItem struct {
	ID         int64     `json:"id"`
	RecordID   int64     `json:"record_id"`
	Kind       string    `json:"kind"`
	CosKey     string    `json:"cos_key"`
	URL        string    `json:"url"`
	UploadedBy *int64    `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// handlePhotoUpload POST /records/:id/photos?kind=before|after：body { image: dataURL }。
func (m *Module) handlePhotoUpload(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配", nil)
		return
	}
	ident, _ := identityFrom(r.Context())
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "before" && kind != "after" {
		writeFail(w, http.StatusBadRequest, codeParam, "kind 应为 before 或 after", nil)
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
	if m.Objects == nil {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, "对象存储未接入", nil)
		return
	}
	upload, err := httpapi.ReadMultipartImage(w, r, "image", map[string]bool{"image/jpeg": true, "image/png": true})
	if err != nil {
		switch {
		case errors.Is(err, httpapi.ErrImageTooLarge):
			writeFail(w, http.StatusRequestEntityTooLarge, codePhotoSize, "照片大小应在 50MB 以内", nil)
		case errors.Is(err, httpapi.ErrImageFormat):
			writeFail(w, http.StatusBadRequest, codePhotoFormat, "照片格式应为 jpeg/png", nil)
		default:
			writeFail(w, http.StatusBadRequest, codeParam, "请使用 multipart/form-data 上传照片", nil)
		}
		return
	}
	ext := "jpg"
	if upload.ContentType == "image/png" {
		ext = "png"
	}
	// COS key 后缀随实际格式（png 用 .png），供按扩展名识别格式。
	key := fmt.Sprintf("%s%s/%d/%s-%d.%s",
		m.objectPrefix(r.Context()), safeFileName(team.Name), recordID, kind, time.Now().UnixMilli(), ext)
	url, err := m.Objects.Put(r.Context(), key, upload.Buf, upload.ContentType)
	if err != nil {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, "照片上传失败："+err.Error(), nil)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO treehazard_photo (record_id, kind, cos_key, url, uploaded_by) VALUES (?, ?, ?, ?, ?)`,
		recordID, kind, key, url, ident.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "照片保存失败", nil)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, photoItem{
		ID: id, RecordID: recordID, Kind: kind, CosKey: key, URL: url,
		UploadedBy: &ident.ID, CreatedAt: time.Now(),
	}, "")
}

// handlePhotoDelete DELETE /records/:id/photos/:photoId：上传者本人或班管/超管；删 COS 对象 + 表行。
func (m *Module) handlePhotoDelete(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配", nil)
		return
	}
	ident, _ := identityFrom(r.Context())
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	photoID, err := strconv.ParseInt(chi.URLParam(r, "photoId"), 10, 64)
	if err != nil || photoID <= 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "photoId 无效", nil)
		return
	}
	var (
		cosKey     string
		uploadedBy sql.NullInt64
	)
	err = m.DB.QueryRowContext(r.Context(),
		`SELECT p.id, p.cos_key, p.uploaded_by FROM treehazard_photo p
		 JOIN treehazard_record r ON r.id = p.record_id
		 WHERE p.id = ? AND p.record_id = ? AND r.team_id = ?`,
		photoID, recordID, team.ID).Scan(new(int64), &cosKey, &uploadedBy)
	if err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "照片不存在", nil)
		return
	}
	isManager := requireManager(ident, team)
	if !(uploadedBy.Valid && uploadedBy.Int64 == ident.ID) && !isManager {
		writeFail(w, http.StatusForbidden, codeForbidden, "仅上传者本人或管理员可删除", nil)
		return
	}
	if cosKey != "" && m.Objects != nil {
		if err := m.Objects.Delete(r.Context(), cosKey); err != nil {
			// 与 Node 一致：COS 删除失败仅记原因，继续删表行。
			fmt.Printf("[树障] 删除 COS 对象失败（继续删表行）：%v\n", err)
		}
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM treehazard_photo WHERE id = ?`, photoID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "照片删除失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": photoID}, "")
}

// replaceEvidencePhotos direct 模式整组替换某记录的 evidence 照片（先删旧对象与表行再逐张上传）。
// 返回 (stored, failed)；单张失败不阻断记录入库。
func (m *Module) replaceEvidencePhotos(ctx context.Context, teamName string, recordID int64, photos []dataURLPhoto) (stored, failed int) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, cos_key FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'`, recordID)
	if err == nil {
		type oldPhoto struct {
			id     int64
			cosKey string
		}
		olds := make([]oldPhoto, 0)
		for rows.Next() {
			var p oldPhoto
			if rows.Scan(&p.id, &p.cosKey) == nil {
				olds = append(olds, p)
			}
		}
		rows.Close()
		for _, p := range olds {
			if m.Objects != nil && p.cosKey != "" {
				if err := m.Objects.Delete(ctx, p.cosKey); err != nil {
					fmt.Printf("[树障] 重推清理旧辅证 COS 对象失败（继续）：%v\n", err)
				}
			}
		}
		if len(olds) > 0 {
			_, _ = m.DB.ExecContext(ctx, `DELETE FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'`, recordID)
		}
	}
	for i, photo := range photos {
		key := fmt.Sprintf("%s%s/%d/evidence-%d-%d.%s",
			m.objectPrefix(ctx), safeFileName(teamName), recordID, time.Now().UnixMilli(), i, photo.ext)
		if m.Objects == nil {
			failed++
			continue
		}
		url, err := m.Objects.Put(ctx, key, photo.buf, photo.contentType)
		if err != nil {
			failed++
			fmt.Printf("[树障] 直推辅证照片入 COS 失败（记录已入库，照片缺失）：%v\n", err)
			continue
		}
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_photo (record_id, kind, cos_key, url, uploaded_by) VALUES (?, 'evidence', ?, ?, NULL)`,
			recordID, key, url); err != nil {
			failed++
			continue
		}
		stored++
	}
	return stored, failed
}

// storeStagingPhotos staging：照片随批入暂存照片表，按暂存记录 insertId 归挂。
func (m *Module) storeStagingPhotos(ctx context.Context, teamName string, batchID, stagingRecordID int64, craneRecordID string, photos []dataURLPhoto) (stored, failed int) {
	for i, photo := range photos {
		key := fmt.Sprintf("%s%s/staging-%d/%s-%d-%d.%s",
			m.objectPrefix(ctx), safeFileName(teamName), batchID, safeFileName(craneRecordID)+"-", time.Now().UnixMilli(), i, photo.ext)
		if craneRecordID == "" {
			key = fmt.Sprintf("%s%s/staging-%d/rec-%d-%d.%s",
				m.objectPrefix(ctx), safeFileName(teamName), batchID, time.Now().UnixMilli(), i, photo.ext)
		}
		if m.Objects == nil {
			failed++
			continue
		}
		url, err := m.Objects.Put(ctx, key, photo.buf, photo.contentType)
		if err != nil {
			failed++
			fmt.Printf("[树障] 暂存辅证照片入 COS 失败（不影响批次入库）：%v\n", err)
			continue
		}
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_staging_photo (batch_id, staging_record_id, crane_record_id, cos_key, url) VALUES (?, ?, ?, ?, ?)`,
			batchID, stagingRecordID, craneRecordID, key, url); err != nil {
			failed++
			continue
		}
		stored++
	}
	return stored, failed
}

// clearStagingPhotos 批次废弃/整批替换时连带清理暂存照片（COS 对象 + 表行）。
func (m *Module) clearStagingPhotos(ctx context.Context, batchID int64) int {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, cos_key FROM treehazard_staging_photo WHERE batch_id = ?`, batchID)
	if err != nil {
		return 0
	}
	type sp struct {
		id     int64
		cosKey string
	}
	items := make([]sp, 0)
	for rows.Next() {
		var p sp
		if rows.Scan(&p.id, &p.cosKey) == nil {
			items = append(items, p)
		}
	}
	rows.Close()
	for _, p := range items {
		if m.Objects != nil && p.cosKey != "" {
			if err := m.Objects.Delete(ctx, p.cosKey); err != nil {
				fmt.Printf("[树障] 清理暂存辅证 COS 对象失败（继续）：%v\n", err)
			}
		}
	}
	if len(items) > 0 {
		_, _ = m.DB.ExecContext(ctx, `DELETE FROM treehazard_staging_photo WHERE batch_id = ?`, batchID)
	}
	return len(items)
}
