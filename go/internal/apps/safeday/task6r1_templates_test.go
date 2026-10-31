package safeday

// Task 6 fix round 1：模板 multipart 上传在解析前限总量（巨大无关字段也不能绕过）。

import (
	"net/http"
	"strings"
	"testing"
)

// TestTemplateCreateBodyLimited 请求体超模板总量预算时在表单解析前拒绝。
func TestTemplateCreateBodyLimited(t *testing.T) {
	env, _ := newTemplateServer(t, identAdmin)
	rec := env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name":     "新模板",
		"sections": testSectionsJSON,
		"apiKey":   "app-admin-secret",
		"padding":  strings.Repeat("x", maxTemplateFileSize+(1<<20)),
	}, "file", "a.docx", dynamicDocxBytes(t))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请求体过大") {
		t.Fatalf("超大请求体应明确 400/请求体过大，实际 %d %s", rec.Code, rec.Body.String())
	}
}
