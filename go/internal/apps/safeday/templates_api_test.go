package safeday

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shade/internal/auth"
)

func newTemplateServer(t *testing.T, ident auth.Identity) (*testEnv, *TemplateStore) {
	t.Helper()
	env := newTestServer(t, ident)
	ts, err := NewTemplateStore(t.TempDir(), testCipher(t))
	if err != nil {
		t.Fatalf("构造模板库失败: %v", err)
	}
	env.module.Templates = ts
	return env, ts
}

func newGenerateServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	env, ts := newTemplateServer(t, ident)
	if _, err := ts.Create("测试用户模板", testSections(), "test-template-key"); err != nil {
		t.Fatalf("创建测试用户模板失败: %v", err)
	}
	return env
}

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

func TestTemplateAdminPermissions(t *testing.T) {
	envUser, _ := newTemplateServer(t, identTeamA)
	rec := envUser.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问管理模板应 403: %d %s", rec.Code, rec.Body.String())
	}
	rec = envUser.do(t, http.MethodGet, "/api/v1/safeday/templates", nil)
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("生成模板列表应成功: %d %v", rec.Code, body)
	}
	if list, _ := body["templates"].([]any); len(list) != 0 {
		t.Fatalf("初始普通模板列表应为空: %v", body)
	}
	envUser.module.Permit = fakePermit{allow: false}
	rec = envUser.do(t, http.MethodGet, "/api/v1/safeday/templates", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无应用权限应 403: %d %s", rec.Code, rec.Body.String())
	}

	envAdmin, _ := newTemplateServer(t, identAdmin)
	rec = envAdmin.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	body = decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["defaultId"] != "" {
		t.Fatalf("初始管理列表/defaultId 应为空: %d %v", rec.Code, body)
	}
}

func TestTemplateAdminCRUD(t *testing.T) {
	env, ts := newTemplateServer(t, identAdmin)
	rec := env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "自定义模板", "apiKey": "app-admin-secret", "sections": testSectionsJSON,
	}, "", "", nil)
	body := decodeBodyMap(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("无 docx 新建模板应成功: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "app-admin-secret") || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Fatalf("新建响应不得包含密钥: %s", rec.Body.String())
	}
	tplMap, _ := body["template"].(map[string]any)
	id, _ := tplMap["id"].(string)
	if id == "" || tplMap["hasApiKey"] != true || tplMap["mode"] != TemplateModeDynamic || tplMap["isDefault"] != true {
		t.Fatalf("新建模板响应不符: %v", tplMap)
	}
	if _, hasSHA := tplMap["sha256"]; hasSHA {
		t.Fatalf("新模板不应返回 docx SHA: %v", tplMap)
	}

	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates", nil)
	if strings.Contains(rec.Body.String(), "app-admin-secret") || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Fatalf("管理列表不得包含明文/密文: %s", rec.Body.String())
	}
	listBody := decodeBodyMap(t, rec)
	if list, _ := listBody["templates"].([]any); len(list) != 1 {
		t.Fatalf("列表应只有用户模板: %v", listBody)
	}
	rec = env.do(t, http.MethodGet, "/api/v1/safeday/templates", nil)
	ordinary := decodeBodyMap(t, rec)
	list, _ := ordinary["templates"].([]any)
	first, _ := list[0].(map[string]any)
	if _, has := first["hasApiKey"]; has {
		t.Fatalf("普通列表不应暴露 hasApiKey: %v", first)
	}

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

	rec = env.do(t, http.MethodPost, "/api/v1/admin/safeday/templates/"+id+"/default", nil)
	if body := decodeBodyMap(t, rec); rec.Code != http.StatusOK || body["defaultId"] != id {
		t.Fatalf("设默认应成功: %d %v", rec.Code, body)
	}
	for _, path := range []string{"/docx", "/download"} {
		rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/"+id+path, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("旧 docx 接口应移除 %s: %d", path, rec.Code)
		}
	}
	rec = env.do(t, http.MethodGet, "/api/v1/admin/safeday/templates/sample", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("样例接口应移除: %d", rec.Code)
	}

	rec = env.do(t, http.MethodDelete, "/api/v1/admin/safeday/templates/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除模板应成功: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := ts.Get(id); ok {
		t.Fatalf("删除后不应可查")
	}
	rec = env.do(t, http.MethodPut, "/api/v1/admin/safeday/templates/tpl-missing", map[string]any{"name": "X"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知模板编辑应 404: %d %s", rec.Code, rec.Body.String())
	}
	rec = env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "缺 key", "sections": testSectionsJSON,
	}, "", "", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "API Key") {
		t.Fatalf("缺 key 应 400: %d %s", rec.Code, rec.Body.String())
	}
	env.module.Templates = nil
	rec = env.doMultipartFile(t, http.MethodPost, "/api/v1/admin/safeday/templates", map[string]string{
		"name": "X", "apiKey": "k", "sections": testSectionsJSON,
	}, "", "", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "模板存储未接入") {
		t.Fatalf("未接入应明确报错: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTemplateAdminUpdateParsesStringSections(t *testing.T) {
	env, ts := newTemplateServer(t, identAdmin)
	tpl, err := ts.Create("X", testSections(), "app-k")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	rec := env.do(t, http.MethodPut, "/api/v1/admin/safeday/templates/"+tpl.ID, map[string]any{
		"name": "字符串章节", "sections": testSectionsJSON,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("字符串 sections 应兼容: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := ts.Get(tpl.ID)
	if got.Name != "字符串章节" || len(got.Sections) != 2 || got.Sections[0].Key != "part_a" {
		t.Fatalf("字符串 sections 解析不符: %+v", got)
	}
}
