// 安全日模板 API：
//   - GET  /api/v1/safeday/templates            登录 + safe-day 权限，生成表单可选模板
//   - GET  /api/v1/admin/safeday/templates      管理列表（仅超管；只暴露 hasApiKey）
//   - POST /api/v1/admin/safeday/templates      新建（multipart：name/sections/apiKey/file）
//   - GET  /api/v1/admin/safeday/templates/{id} 详情
//   - PUT  /api/v1/admin/safeday/templates/{id} 编辑名称/章节/API Key（空值保留）
//   - POST /api/v1/admin/safeday/templates/{id}/docx     替换 docx
//   - POST|PUT /api/v1/admin/safeday/templates/{id}/default 设默认
//   - DELETE /api/v1/admin/safeday/templates/{id}        删除（内容文件保留）
//   - GET  /api/v1/admin/safeday/templates/{id}/download 下载模板 docx
//   - GET  /api/v1/admin/safeday/templates/sample        下载动态样例 docx
//
// 所有响应保持安全日 { ok, error, ... } 信封；任何接口绝不返回 API Key 明文/密文。
package safeday

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// registerTemplateAdminRoutes 注册 /api/v1/admin/safeday 下模板管理路由（挂载在 /api/v1 之下）。
func (m *Module) registerTemplateAdminRoutes(r chi.Router) {
	r.Route("/admin/safeday", func(r chi.Router) {
		r.Get("/templates", m.withAdmin(m.adminTemplateList))
		r.Post("/templates", m.withAdmin(m.adminTemplateCreate))
		r.Get("/templates/sample", m.withAdmin(m.adminTemplateSample))
		r.Get("/templates/{id}", m.withAdmin(m.adminTemplateGet))
		r.Put("/templates/{id}", m.withAdmin(m.adminTemplateUpdate))
		r.Post("/templates/{id}/docx", m.withAdmin(m.adminTemplateReplaceDocx))
		r.Post("/templates/{id}/default", m.withAdmin(m.adminTemplateSetDefault))
		r.Put("/templates/{id}/default", m.withAdmin(m.adminTemplateSetDefault))
		r.Delete("/templates/{id}", m.withAdmin(m.adminTemplateDelete))
		r.Get("/templates/{id}/download", m.withAdmin(m.adminTemplateDownload))
	})
}

// withAdmin 超管门控（生产鉴权中间件已按 /api/v1/admin/* 拦截，此处再做模块级兜底）。
func (m *Module) withAdmin(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			writeFail(w, http.StatusUnauthorized, "未登录或登录已过期", nil)
			return
		}
		if ident.Role != "admin" {
			writeFail(w, http.StatusForbidden, "仅超级管理员可执行此操作", nil)
			return
		}
		next(w, r, ident)
	}
}

// templateOptions 普通模板列表（生成表单选择用，不含管理字段）。
func (m *Module) templateOptions(w http.ResponseWriter, _ *http.Request, _ auth.Identity) {
	if m.Templates == nil {
		writeOK(w, map[string]any{
			"templates": []TemplateOption{{
				ID: BuiltinTemplateID, Name: builtinTemplateName, Mode: TemplateModeStatic,
				Sections: copySections(builtinSections), IsDefault: true,
			}},
			"defaultId": BuiltinTemplateID,
		})
		return
	}
	writeOK(w, map[string]any{
		"templates": m.Templates.Options(),
		"defaultId": m.Templates.DefaultID(),
	})
}

// adminTemplateList 管理列表（含 hasApiKey/sha/大小，不含密钥）。
func (m *Module) adminTemplateList(w http.ResponseWriter, _ *http.Request, _ auth.Identity) {
	if m.Templates == nil {
		writeOK(w, map[string]any{
			"templates": []Template{{
				ID: BuiltinTemplateID, Name: builtinTemplateName, Mode: TemplateModeStatic,
				Sections: copySections(builtinSections), IsDefault: true,
			}},
			"defaultId": BuiltinTemplateID,
		})
		return
	}
	writeOK(w, map[string]any{
		"templates": m.Templates.List(),
		"defaultId": m.Templates.DefaultID(),
	})
}

// adminTemplateGet 模板详情。
func (m *Module) adminTemplateGet(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	tpl, found := store.Get(chi.URLParam(r, "id"))
	if !found {
		writeFail(w, http.StatusNotFound, ErrTemplateNotFound.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

// writeTemplateMultipartErr 模板 multipart 解析错误：超总量 400 明确提示，其余格式错误同原口径。
func writeTemplateMultipartErr(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large") {
		writeFail(w, http.StatusBadRequest, "请求体过大（超出模板上传限制）", nil)
		return
	}
	writeFail(w, http.StatusBadRequest, "请求格式不正确，应为 multipart/form-data", nil)
}

// adminTemplateCreate 新建自定义动态模板（multipart）。
func (m *Module) adminTemplateCreate(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateFileSize+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeTemplateMultipartErr(w, err)
		return
	}
	sections, err := parseSectionsField(r.FormValue("sections"))
	if err != nil {
		writeFail(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if sections == nil {
		sections = copySections(builtinSections)
	}
	docx, err := formFileBytes(r, "file", "docx", "files")
	if err != nil {
		writeFail(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	tpl, err := store.Create(r.FormValue("name"), sections, r.FormValue("apiKey"), docx)
	if err != nil {
		writeFail(w, templateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

// adminTemplateUpdate 编辑名称/章节/API Key（空字段保持原值）。
func (m *Module) adminTemplateUpdate(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeFail(w, http.StatusBadRequest, "请求体不是合法 JSON", nil)
		return
	}
	var sections []TemplateSection
	if raw, has := body["sections"]; has {
		sections, err = parseSectionsRaw(raw)
		if err != nil {
			writeFail(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
	}
	tpl, err := store.Update(chi.URLParam(r, "id"), TemplateUpdate{
		Name:     rawString(body["name"]),
		Sections: sections,
		APIKey:   rawString(body["apiKey"]),
	})
	if err != nil {
		writeFail(w, adminTemplateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

// adminTemplateReplaceDocx 替换模板 docx（multipart）。
func (m *Module) adminTemplateReplaceDocx(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateFileSize+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeTemplateMultipartErr(w, err)
		return
	}
	docx, err := formFileBytes(r, "file", "docx", "files")
	if err != nil {
		writeFail(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	tpl, err := store.ReplaceDocx(chi.URLParam(r, "id"), docx)
	if err != nil {
		writeFail(w, adminTemplateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

// adminTemplateSetDefault 设默认模板。
func (m *Module) adminTemplateSetDefault(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := store.SetDefault(id); err != nil {
		writeFail(w, adminTemplateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"defaultId": id})
}

// adminTemplateDelete 删除自定义模板（内容文件保留，在途记录仍可渲染）。
func (m *Module) adminTemplateDelete(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	if err := store.Delete(chi.URLParam(r, "id")); err != nil {
		writeFail(w, adminTemplateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, nil)
}

// adminTemplateDownload 下载模板当前 docx。
func (m *Module) adminTemplateDownload(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	name, data, err := store.Download(chi.URLParam(r, "id"))
	if err != nil {
		status := templateErrorStatus(err)
		if errors.Is(err, ErrTemplateNotFound) || errors.Is(err, ErrTemplateFileMissing) {
			status = http.StatusNotFound
		}
		writeFail(w, status, err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// adminTemplateSample 下载动态模板样例 docx。
func (m *Module) adminTemplateSample(w http.ResponseWriter, _ *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	data, err := store.SampleDocx()
	if err != nil {
		writeFail(w, templateErrorStatus(err), err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape("安全日动态模板样例.docx")))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// adminTemplateErrorStatus 管理端模板错误状态：未知模板 404，其余沿用通用映射。
func adminTemplateErrorStatus(err error) int {
	if errors.Is(err, ErrTemplateNotFound) {
		return http.StatusNotFound
	}
	return templateErrorStatus(err)
}

// requireTemplates 模板库未接入时明确报错。
func (m *Module) requireTemplates(w http.ResponseWriter) (*TemplateStore, bool) {
	if m.Templates == nil {
		writeFail(w, http.StatusInternalServerError, "模板存储未接入", nil)
		return nil, false
	}
	return m.Templates, true
}

// parseSectionsField 解析表单 sections 字段（JSON 数组或包了一层的 JSON 字符串；空串返回 nil）。
func parseSectionsField(raw string) ([]TemplateSection, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal([]byte(raw), &text); err == nil {
			raw = strings.TrimSpace(text)
		}
	}
	var sections []TemplateSection
	if err := json.Unmarshal([]byte(raw), &sections); err != nil {
		return nil, invalidTemplate("章节配置不是合法 JSON")
	}
	return sections, nil
}

// parseSectionsRaw 解析 JSON 请求体中的 sections（数组 / JSON 字符串；null 视为不修改）。
func parseSectionsRaw(raw json.RawMessage) ([]TemplateSection, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return parseSectionsField(text)
	}
	var sections []TemplateSection
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, invalidTemplate("章节配置不是合法 JSON")
	}
	return sections, nil
}

// formFileBytes 取 multipart 上传的 docx（依次尝试字段名；未上传明确报错）。
func formFileBytes(r *http.Request, fields ...string) ([]byte, error) {
	if r.MultipartForm == nil {
		return nil, invalidTemplate("请上传模板 docx 文件")
	}
	for _, field := range fields {
		files := r.MultipartForm.File[field]
		if len(files) == 0 {
			continue
		}
		f, err := files[0].Open()
		if err != nil {
			return nil, invalidTemplate("读取上传文件失败：%v", err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxTemplateFileSize+1))
		f.Close()
		if err != nil {
			return nil, invalidTemplate("读取上传文件失败：%v", err)
		}
		if len(data) > maxTemplateFileSize {
			return nil, invalidTemplate("模板文件过大（最大 %dMB）", maxTemplateFileSize>>20)
		}
		return data, nil
	}
	return nil, invalidTemplate("请上传模板 docx 文件")
}
