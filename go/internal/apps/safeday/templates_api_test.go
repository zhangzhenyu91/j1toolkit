package safeday

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shade/internal/auth"
	"shade/internal/compat/exceldoc"
)

// newTemplateServer 构造带模板库的测试服务。
func newTemplateServer(t *testing.T, ident auth.Identity) (*testEnv, *TemplateStore) {
	t.Helper()
	env := newTestServer(t, ident)
	ts, err := NewTemplateStore(t.TempDir(), testCipher(t))
	if err != nil {
		t.Fatalf("构造模板库失败: %v", err)
	}
	ts.BuiltinPath = writeBytesFile(t, staticDocxBytes(t))
	ts.SamplePath = writeBytesFile(t, dynamicDocxBytes(t))
	env.module.Templates = ts
	return env, ts
}

// doMultipartFile 构造 multipart 请求（自定义文件字段名）。
func (e *testEnv) doMultipartFile(t *testing.T, method, path string, fields map[string]string, fileField, fileName string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileField != "" {
		part, err := mw.CreateFormFile(fileField, fileName)
		if err != nil {
			t.Fatalf("建表单文件失败: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("写表单文件失败: %v", err)
		}
	}
	_ = mw.Close()
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

const testSectionsJSON = `[{"key":"part_a","title":"章节甲"},{"key":"part_b","title":"章节乙"}]`

// TestTemplateAdminPermissions 管理 API 仅 admin；普通 GET 供生成选择且不含密钥字段。
func TestTemplateAdminPermissions(t *testing.T) {
	// 普通用户访问管理路径被超管门控拦截（生产同一中间件）
	envUser, _ := newTemplateServer(t, identTeamA)
	rec := envUser.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问管理模板应 403: %d %s", rec.Code, rec.Body.String())
	}
	// 普通用户可读生成选择列表
	rec = envUser.do(t, http.MethodGet, "/api/v1/safeday/templates", nil)
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("生成模板列表应成功: %d %v", rec.Code, body)
	}
	list, _ := body["templates"].([]any)
	if len(list) != 1 {
		t.Fatalf("初始应只有内置模板: %v", body)
	}
	first, _ := list[0].(map[string]any)
	if first["id"] != BuiltinTemplateID || first["name"] == "" || first["mode"] != TemplateModeStatic {
		t.Fatalf("内置模板选项不符: %v", first)
	}
	if _, has := first["hasApiKey"]; has {
		t.Fatalf("普通列表不应暴露 hasApiKey: %v", first)
	}
	if strings.Contains(rec.Body.String(), "enc:v1:") || strings.Contains(rec.Body.String(), "apiKeyEnc") {
		t.Fatalf("普通列表不得包含密钥字段: %s", rec.Body.String())
	}
	// 无应用权限时普通列表拒绝
	envUser.module.Permit = fakePermit{allow: false}
	rec = envUser.do(t, http.MethodGet, "/api/v1/safeday/templates", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无应用权限应 403: %d %s", rec.Code, rec.Body.String())
	}

	// 管理员可读管理列表
	envAdmin, _ := newTemplateServer(t, identAdmin)
	rec = envAdmin.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员列表应成功: %d %s", rec.Code, rec.Body.String())
	}
	body = decodeBodyMap(t, rec)
	if body["defaultId"] != BuiltinTemplateID {
		t.Fatalf("默认模板 id 不符: %v", body)
	}
}

// TestTemplateAdminCRUD 管理模板：新建/编辑/替换 docx/设默认/下载/样例/删除。
func TestTemplateAdminCRUD(t *testing.T) {
	env, ts := newTemplateServer(t, identAdmin)
	docx := dynamicDocxBytes(t)

	// 新建（multipart）
	rec := env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "自定义模板", "apiKey": "app-admin-secret", "sections": testSectionsJSON,
	}, "file", "custom.docx", docx)
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("新建模板应成功: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "app-admin-secret") || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Fatalf("新建响应不得包含密钥: %s", rec.Body.String())
	}
	tplMap, _ := body["template"].(map[string]any)
	id, _ := tplMap["id"].(string)
	if id == "" || tplMap["hasApiKey"] != true || tplMap["mode"] != TemplateModeDynamic {
		t.Fatalf("新建模板响应不符: %v", tplMap)
	}
	sha1, _ := tplMap["sha256"].(string)

	// 列表 + 详情：不含密钥字段
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	if strings.Contains(rec.Body.String(), "app-admin-secret") || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Fatalf("管理列表不得包含明文/密文: %s", rec.Body.String())
	}
	listBody := decodeBodyMap(t, rec)
	if list, _ := listBody["templates"].([]any); len(list) != 2 {
		t.Fatalf("列表应有内置 + 自定义: %v", listBody)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("详情应成功: %d %s", rec.Code, rec.Body.String())
	}

	// 编辑名称/章节（key 留空保留）
	rec = env.do(t, http.MethodPut, "/api/v1/admin/safeday/templates/"+id, map[string]any{
		"name": "改名模板", "sections": []map[string]string{{"key": "part_c", "title": "章节丙"}},
	})
	if body := decodeBodyMap(t, rec); rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("编辑模板应成功: %d %v", rec.Code, body)
	}
	if got, _ := ts.Get(id); got.Name != "改名模板" || len(got.Sections) != 1 || !got.HasAPIKey {
		t.Fatalf("编辑结果不符: %+v", got)
	}
	if key, _ := ts.WorkflowAPIKey(id); key != "app-admin-secret" {
		t.Fatalf("留空 key 应保留: %q", key)
	}

	// 替换 docx
	newDocx := buildTestDocx(t, append(headerParas(), `<w:p><w:r><w:t>{@章节区}</w:t></w:r></w:p>`, `<w:p><w:r><w:t>附加段落</w:t></w:r></w:p>`))
	rec = env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates/"+id+"/docx",
		map[string]string{}, "file", "new.docx", newDocx)
	body = decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("替换 docx 应成功: %d %s", rec.Code, rec.Body.String())
	}
	tplMap, _ = body["template"].(map[string]any)
	sha2, _ := tplMap["sha256"].(string)
	if sha2 == "" || sha2 == sha1 {
		t.Fatalf("替换后 sha 应变化: %s -> %s", sha1, sha2)
	}

	// 设默认
	rec = env.do(t, http.MethodPost, "/api/v1/admin/safeday/templates/"+id+"/default", nil)
	if body := decodeBodyMap(t, rec); rec.Code != http.StatusOK || body["defaultId"] != id {
		t.Fatalf("设默认应成功: %d %v", rec.Code, body)
	}
	if ts.DefaultID() != id {
		t.Fatalf("默认未生效")
	}

	// 下载（返回替换后的 docx）
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/"+id+"/download", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), newDocx) {
		t.Fatalf("下载内容不符: %d len=%d", rec.Code, rec.Body.Len())
	}

	// 内置模板可下载（走后端解析路径）
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/"+BuiltinTemplateID+"/download", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), staticDocxBytes(t)) {
		t.Fatalf("内置模板下载内容不符: %d len=%d", rec.Code, rec.Body.Len())
	}

	// 动态样例（独占 {@章节区}）
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/sample", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("样例下载应成功: %d %s", rec.Code, rec.Body.String())
	}
	parts, err := exceldoc.ReadDocx(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("样例应为合法 docx: %v", err)
	}
	if !exceldoc.HasExclusiveRawPlaceholder(string(parts["word/document.xml"]), sectionsPlaceholder) {
		t.Fatalf("样例必须含独占 {@章节区}")
	}

	// 删除自定义模板；内置模板不可删
	rec = env.do(t, http.MethodDelete, "/api/v1/admin/safeday/templates/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除模板应成功: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := ts.Get(id); ok {
		t.Fatalf("删除后不应可查")
	}
	rec = env.do(t, http.MethodDelete, "/api/v1/admin/safeday/templates/"+BuiltinTemplateID, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "内置模板") {
		t.Fatalf("内置模板不可删: %d %s", rec.Code, rec.Body.String())
	}

	// 管理端操作未知模板 → 404
	rec = env.do(t, http.MethodPut, "/api/v1/admin/safeday/templates/tpl-missing", map[string]any{"name": "X"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知模板编辑应 404: %d %s", rec.Code, rec.Body.String())
	}

	// 新建缺 key → 400
	rec = env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "缺 key", "sections": testSectionsJSON,
	}, "file", "custom.docx", docx)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "API Key") {
		t.Fatalf("缺 key 应 400: %d %s", rec.Code, rec.Body.String())
	}

	// 未接入模板库时管理写操作明确报错
	env.module.Templates = nil
	rec = env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "X", "apiKey": "k", "sections": testSectionsJSON,
	}, "file", "custom.docx", docx)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "模板存储未接入") {
		t.Fatalf("未接入应明确报错: %d %s", rec.Code, rec.Body.String())
	}
}

// TestTemplateAdminUpdateParsesStringSections 编辑接口兼容 sections 为 JSON 字符串。
func TestTemplateAdminUpdateParsesStringSections(t *testing.T) {
	env, ts := newTemplateServer(t, identAdmin)
	tpl, err := ts.Create("X", testSections(), "app-k", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	raw, _ := json.Marshal(testSections())
	rec := env.do(t, http.MethodPut, "/api/v1/admin/safeday/templates/"+tpl.ID, map[string]any{
		"name": "字符串章节", "sections": string(raw),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("字符串 sections 应可解析: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := ts.Get(tpl.ID); got.Name != "字符串章节" || len(got.Sections) != 2 {
		t.Fatalf("更新结果不符: %+v", got)
	}
}
