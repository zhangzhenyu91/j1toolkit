// 安全日用户模板 API：模板只维护名称、章节和每模板 Dify API Key，不上传或下载 docx。
// 所有响应保持安全日 { ok, error, ... } 信封；任何接口绝不返回 API Key 明文/密文。
package safeday

import (
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

func (m *Module) registerTemplateAdminRoutes(r chi.Router) {
	r.Route("/admin/safeday", func(r chi.Router) {
		r.Get("/templates", m.withAdmin(m.adminTemplateList))
		r.Post("/templates", m.withAdmin(m.adminTemplateCreate))
		r.Get("/templates/{id}", m.withAdmin(m.adminTemplateGet))
		r.Put("/templates/{id}", m.withAdmin(m.adminTemplateUpdate))
		r.Post("/templates/{id}/default", m.withAdmin(m.adminTemplateSetDefault))
		r.Put("/templates/{id}/default", m.withAdmin(m.adminTemplateSetDefault))
		r.Delete("/templates/{id}", m.withAdmin(m.adminTemplateDelete))
	})
}

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

func (m *Module) templateOptions(w http.ResponseWriter, _ *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	writeOK(w, map[string]any{"templates": store.Options(), "defaultId": store.DefaultID()})
}

func (m *Module) adminTemplateList(w http.ResponseWriter, _ *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	writeOK(w, map[string]any{"templates": store.List(), "defaultId": store.DefaultID()})
}

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

func writeTemplateMultipartErr(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || errors.Is(err, multipart.ErrMessageTooLarge) || strings.Contains(err.Error(), "request body too large") {
		writeFail(w, http.StatusBadRequest, "请求体过大（超出模板请求限制）", nil)
		return
	}
	writeFail(w, http.StatusBadRequest, "请求格式不正确，应为 multipart/form-data", nil)
}

// adminTemplateCreate 新建用户模板（multipart：name/sections/apiKey）。
func (m *Module) adminTemplateCreate(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	store, ok := m.requireTemplates(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateRequestSize)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeTemplateMultipartErr(w, err)
		return
	}
	sections, err := parseSectionsField(r.FormValue("sections"))
	if err != nil {
		writeFail(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	tpl, err := store.Create(r.FormValue("name"), sections, r.FormValue("apiKey"))
	if err != nil {
		writeFail(w, templateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

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
		Name: rawString(body["name"]), Sections: sections, APIKey: rawString(body["apiKey"]),
	})
	if err != nil {
		writeFail(w, adminTemplateErrorStatus(err), err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"template": tpl})
}

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

func adminTemplateErrorStatus(err error) int {
	if errors.Is(err, ErrTemplateNotFound) {
		return http.StatusNotFound
	}
	return templateErrorStatus(err)
}

func (m *Module) requireTemplates(w http.ResponseWriter) (*TemplateStore, bool) {
	if m.Templates == nil {
		writeFail(w, http.StatusInternalServerError, "模板存储未接入", nil)
		return nil, false
	}
	return m.Templates, true
}

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
