// 备注附件：multipart 上传（图片/视频/Office 文档，落 COS）、Office 在线预览地址、下载代理。
// 格式白名单与 Node REMARK_EXTS 同口径；单卡附件上限 9；COS 路径按归属班组（跨班卡可被别班上传）。
package worklog

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/httpapi"
)

// 备注附件类型与扩展名白名单（remark_files.type 取 image/video/doc）。
var remarkExts = map[string][]string{
	"image": {"jpg", "jpeg", "png", "gif", "webp", "bmp"},
	"video": {"mp4", "mov", "m4v"},
	"doc":   {"doc", "docx", "xls", "xlsx", "ppt", "pptx", "pdf"},
}

// remarkMIME 附件上传 ContentType（按扩展名；缺省 octet-stream）。
var remarkMIME = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"gif":  "image/gif",
	"webp": "image/webp",
	"bmp":  "image/bmp",
	"mp4":  "video/mp4",
	"mov":  "video/quicktime",
	"m4v":  "video/x-m4v",
	"doc":  "application/msword",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xls":  "application/vnd.ms-excel",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"ppt":  "application/vnd.ms-powerpoint",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"pdf":  "application/pdf",
}

// remarkTypeOf 扩展名 → 附件类型（白名单外返回空串）。
func remarkTypeOf(ext string) string {
	for t, list := range remarkExts {
		for _, e := range list {
			if e == ext {
				return t
			}
		}
	}
	return ""
}

// remarkUploadMaxBytes 单附件大小上限（Node multer 同口径 50MB）。
const remarkUploadMaxBytes = 50 << 20

// handleRemarkUpload POST /logs/{id}/remark-files：上传单个备注附件（multipart 字段 file；表单 name 可覆盖文件名）。
// 上传成功返回 {name,url,cos_key,type,size}，由前端 PUT /logs/{id} 挂入 remark_files。
func (m *Module) handleRemarkUpload(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	// multipart 表单体在 withContext 之后才可读：表单携带 team_id 时重新解析生效班组（超管跨班上传）
	r.Body = http.MaxBytesReader(w, r.Body, remarkUploadMaxBytes+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeFileTooLarge, "附件大小应在 50MB 以内")
		return
	}
	if v := strings.TrimSpace(r.FormValue("team_id")); v != "" {
		if wanted, err := strconv.ParseInt(v, 10, 64); err == nil && wanted > 0 {
			if t, err := httpapi.ResolveTeam(r.Context(), m.DB,
				httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, wanted); err == nil {
				team = &t
			}
		}
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var entryTeamID int64
	var logDate, teamName, remarkFiles string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT e.team_id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(t.name, ''), COALESCE(e.remark_files, '')
		 FROM worklog_entry e
		 LEFT JOIN sys_team t ON t.id = e.team_id
		 WHERE e.id = ? AND `+entryVisibleSQL,
		entryID, team.ID, team.ID).
		Scan(&entryTeamID, &logDate, &teamName, &remarkFiles)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	// 单卡附件总数上限 9（同 PUT /logs/{id} 存 remark_files 口径）：按库内现有数校验，防重复上传突破
	if len(parseRemarkFiles(remarkFiles)) >= 9 {
		fail(w, http.StatusBadRequest, codeFileCount, "备注附件最多 9 个")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, codeFileBad, "请选择要上传的附件")
		return
	}
	defer file.Close()
	buf, err := io.ReadAll(io.LimitReader(file, remarkUploadMaxBytes+1))
	if err != nil || len(buf) == 0 {
		fail(w, http.StatusBadRequest, codeFileBad, "请选择要上传的附件")
		return
	}
	if len(buf) > remarkUploadMaxBytes {
		fail(w, http.StatusBadRequest, codeFileTooLarge, "附件大小应在 50MB 以内")
		return
	}
	// 文件名：表单 name 优先（小程序 chooseMedia 临时文件名无意义）；回退原始名并修正 latin1 乱码
	fallback := strings.TrimSpace(fixLatin1Name(header.Filename))
	name := truncate(strings.TrimSpace(r.FormValue("name")), 128)
	if name == "" {
		name = truncate(fallback, 128)
	}
	if name == "" {
		name = "附件"
	}
	ext := getFileExt(name)
	fileType := remarkTypeOf(ext)
	if fileType == "" {
		fail(w, http.StatusBadRequest, codeFileBad,
			"仅支持图片、视频或 Office 文档（doc/docx/xls/xlsx/ppt/pptx/pdf）")
		return
	}
	if m.Store == nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "对象存储未接入，无法上传附件")
		return
	}
	// COS 路径按归属班组（跨班卡可被别班上传）
	prefix := m.configValue(r.Context(), "COS_WORKLOG_PREFIX")
	if prefix == "" {
		prefix = "worklog/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	key := prefix + "remark/" + teamName + "/" + Dots(logDate) + "/" +
		strconv.FormatInt(entryID, 10) + "-" + strconv.FormatInt(nowMillis(), 10) + "." + ext
	if err := m.Store.PutBuffer(r.Context(), key, buf, remarkMIME[ext]); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"name": name, "url": m.photoURL(key, ""), "cos_key": key,
		"type": fileType, "size": len(buf),
	})
}

// findRemarkFile 按 entryId + cos_key 定位备注附件（预览/下载共用）。
// exists=false 表示卡片不存在或不可见；found=false 表示附件不在该卡 remark_files 内。
func (m *Module) findRemarkFile(r *http.Request, entryID int64, key string, teamID int64) (file RemarkFile, found, exists bool) {
	var raw string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(e.remark_files, '') FROM worklog_entry e WHERE e.id = ? AND `+entryVisibleSQL,
		entryID, teamID, teamID).Scan(&raw)
	if err != nil {
		return RemarkFile{}, false, false
	}
	for _, f := range parseRemarkFiles(raw) {
		if f.CosKey == key {
			return f, true, true
		}
	}
	return RemarkFile{}, false, true
}

// handleRemarkPreview GET /logs/{id}/remark-preview?key=：拼在线预览地址（仅 Office 文档）——
// OO 启用（OO_PUBLIC_URL）时返回 /office.html?src=worklog-remark 承载页地址（office 签发端点按 id+key 定位附件回源）；
// 否则微软 Office 查看器地址（COS 公共读，查看器直接回源 COS）。
func (m *Module) handleRemarkPreview(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	file, found, exists := m.findRemarkFile(r, entryID, key, team.ID)
	if !exists {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if !found {
		fail(w, http.StatusNotFound, codeMissing, "附件不存在")
		return
	}
	if file.Type != "doc" {
		fail(w, http.StatusBadRequest, codePreviewOnly, "仅 Office 文档支持在线预览")
		return
	}
	if m.configValue(r.Context(), "OO_PUBLIC_URL") != "" {
		writeOK(w, map[string]any{
			"url": "/office.html?src=worklog-remark&id=" + encURI(strconv.FormatInt(entryID, 10)) +
				"&key=" + encURI(file.CosKey) + "&name=" + encURI(file.Name) + "&back=/worklog.html",
		})
		return
	}
	viewer := m.configValue(r.Context(), "PREVIEW_VIEWER_URL")
	if viewer == "" {
		viewer = "https://view.officeapps.live.com/op/view.aspx"
	}
	writeOK(w, map[string]any{"url": viewer + "?src=" + encURI(m.photoURL(file.CosKey, file.URL))})
}

// handleRemarkDownload GET /logs/{id}/remark-download?key=：附件下载代理（COS 跨域无 CORS，网页端经本接口回源并附下载文件名）。
func (m *Module) handleRemarkDownload(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	file, found, exists := m.findRemarkFile(r, entryID, key, team.ID)
	if !exists {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if !found {
		fail(w, http.StatusNotFound, codeMissing, "附件不存在")
		return
	}
	buf, err := fetchObjectBytes(r.Context(), m.Store, file.CosKey, m.photoURL(file.CosKey, file.URL))
	if err != nil {
		fail(w, http.StatusBadGateway, codeUpstream, "附件回源失败（"+err.Error()+"）")
		return
	}
	contentType := remarkMIME[getFileExt(file.Name)]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	name := file.Name
	if name == "" {
		name = "附件"
	}
	w.Header().Set("Content-Type", contentType)
	// RFC5987 编码中文文件名，附 ASCII fallback（同 ZIP 下载口径）
	w.Header().Set("Content-Disposition",
		`attachment; filename="file"; filename*=UTF-8''`+encURI(name))
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}
