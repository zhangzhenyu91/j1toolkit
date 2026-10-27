package safeday

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/compat/exceldoc"
	"shade/internal/compat/imagepdf"
	"shade/internal/httpapi"
)

// fakeAuth 把固定身份注入 httpapi.AuthMiddleware 的上下文（与生产同一条装配路径）。
type fakeAuth struct {
	ident auth.Identity
}

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: f.ident}, nil
}

func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) {
	return f.ident, nil
}

func (f fakeAuth) Logout(context.Context, string, time.Time) error { return nil }

// fakePermit 应用权限桩。
type fakePermit struct{ allow bool }

func (p fakePermit) Allow(context.Context, int64, string) (bool, error) { return p.allow, nil }

type testEnv struct {
	handler http.Handler
	fake    *fakeDB
	module  *Module
	store   *RecordStore
	cos     *fakeCOS
	dify    *fakeDify
	netdisk *fakeNetdisk
}

// fixtureTemplate 在测试目录合成一份带全部占位符的最小 docx 模板。
func fixtureTemplate(t *testing.T) string {
	t.Helper()
	paras := []string{}
	for _, key := range []string{"班组名称", "学习内容", "活动时间", "主持人", "上级参加人员", "本班组参加人员", "缺席人员", "缺席人员原因", "记录人"} {
		paras = append(paras, `<w:p><w:r><w:t>{`+key+`}</w:t></w:r></w:p>`)
	}
	for _, key := range rawXMLKeys {
		paras = append(paras, `<w:p><w:r><w:t>{@`+key+`}</w:t></w:r></w:p>`)
	}
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		strings.Join(paras, "") +
		`<w:sectPr></w:sectPr></w:body></w:document>`
	parts := map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`),
		"word/document.xml": []byte(xml),
	}
	data, err := exceldoc.BuildDocx(parts)
	if err != nil {
		t.Fatalf("合成模板失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "template.docx")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写模板失败: %v", err)
	}
	return path
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	store, err := NewRecordStore(t.TempDir())
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	m := New(db, store)
	m.Permit = fakePermit{allow: true}
	m.Cfg = fakeConfig{"SAFEDAY_CALLBACK_TOKEN": "cb-token", "SAFEDAY_DEFAULT_SUPERIOR": "张主任", "OO_PUBLIC_URL": "", "PREVIEW_VIEWER_URL": ""}
	m.Auth = fakeAuth{ident: ident} // download 自验证 ?token=/Bearer JWT（与生产注入 *auth.Service 同口径）
	m.COS = newFakeCOS()
	m.Dify = &fakeDify{}
	m.Converter = fakeConverter{}
	m.Netdisk = &fakeNetdisk{}
	m.Renderer = &DocxRenderer{TemplatePath: fixtureTemplate(t)}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler: httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:    fake,
		module:  m,
		store:   store,
		cos:     m.COS.(*fakeCOS),
		dify:    m.Dify.(*fakeDify),
		netdisk: m.Netdisk.(*fakeNetdisk),
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeBodyMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	return out
}

var (
	identAdmin = auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1}
	identTeamA = auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1}
	identTeamB = auth.Identity{ID: 5, Username: "v", Role: "user", TeamID: 2}
)

func teamRules(fake *fakeDB) {
	// 注册顺序：兜底在前，参数精确规则在后（find 后注册优先）
	fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	fake.onArgs("FROM sys_team WHERE id = ?", []driver.Value{int64(1)}, []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	fake.onArgs("FROM sys_team WHERE id = ?", []driver.Value{int64(2)}, []string{"id", "name"}, [][]driver.Value{{int64(2), "检修二班"}})
}

// TestCallbackToken 回调 fail-closed：未配置/不符一律 403。
func TestCallbackToken(t *testing.T) {
	env := newTestServer(t, identTeamA)
	// 未配置 token
	env.module.Cfg = fakeConfig{}
	rec := env.do(t, http.MethodPost, "/api/v1/safeday/callback", map[string]any{"date": "2026.10.27"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未配置 token 应 403: %s", rec.Body.String())
	}
	// token 不符
	rec = env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=bad", map[string]any{"date": "2026.10.27"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("token 不符应 403: %s", rec.Body.String())
	}
}

// TestCallbackRender 新链路：六段文字回传 → 渲染 docx 落盘 → done + COS 清理。
func TestCallbackRender(t *testing.T) {
	env := newTestServer(t, identTeamA)
	rec, _, err := env.store.Create(Record{
		Name: "安全活动学习", Date: "2026.10.27", FileName: "2026.10.27.docx",
		Team: "检修一班", Status: "processing", SourceCosKey: "safeday/检修一班/1.pdf",
		Form: RecordForm{Host: "李班长", Attendees: "张三、李四"},
	})
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}

	body := map[string]any{
		"date": "2026.10.27", "class": "检修一班",
		"accident_process": "事故经过……", "cause_analysis": "原因剖析……",
		"self_check": "自查……", "rectification_list": "整改……",
		"prevention_measures": "防范……", "supervision_inspection": "督导……",
	}
	recResp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", body)
	if recResp.Code != http.StatusOK {
		t.Fatalf("回调应成功: %s", recResp.Body.String())
	}
	data := decodeBodyMap(t, recResp)
	if data["ok"] != true || data["done"] != float64(1) {
		t.Fatalf("回调响应不符: %v", data)
	}
	got, ok := env.store.Get(rec.ID)
	if !ok || got.Status != "done" {
		t.Fatalf("记录应置 done: %+v", got)
	}
	path := env.store.RecordFilePath(got)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("产物 docx 应落盘: %v", err)
	}
	// COS 源文件清理 + sourceCosKey 清空
	if len(env.cos.deleted) != 1 || env.cos.deleted[0] != "safeday/检修一班/1.pdf" {
		t.Fatalf("COS 源文件应清理: %v", env.cos.deleted)
	}
	if cur, _ := env.store.Get(rec.ID); cur.SourceCosKey != "" {
		t.Fatalf("sourceCosKey 应清空: %+v", cur)
	}
}

// TestCallbackLegacy 旧链路：按产物文件存在性终判（存在 done / 缺失 failed）。
func TestCallbackLegacy(t *testing.T) {
	env := newTestServer(t, identTeamA)
	recDone, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.26", FileName: "2026.10.26.docx", Team: "检修一班", Status: "processing",
	})
	recFail, _, _ := env.store.Create(Record{
		Name: "B", Date: "2026.10.25", FileName: "2026.10.25.docx", Team: "检修一班", Status: "processing",
	})
	// 只给 recDone 落产物
	outDir := filepath.Join(env.store.DocsDir(), "检修一班")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "2026.10.26.docx"), []byte("docx"), 0o644)

	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", map[string]any{"class": "检修一班"})
	data := decodeBodyMap(t, resp)
	if data["done"] != float64(1) || data["failed"] != float64(1) {
		t.Fatalf("终判计数不符: %v", data)
	}
	if got, _ := env.store.Get(recDone.ID); got.Status != "done" {
		t.Fatalf("有产物应 done: %+v", got)
	}
	if got, _ := env.store.Get(recFail.ID); got.Status != "failed" || got.Error == "" {
		t.Fatalf("无产物应 failed 且带错误: %+v", got)
	}
}

// TestGenerateValidate 生成入口参数校验。
func TestGenerateValidate(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)

	// 无文件
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"name": "X", "date": "2026.10.27"}, nil)
	if body := decodeBodyMap(t, rec); body["error"] != "请至少上传一个文件" {
		t.Fatalf("缺文件文案不符: %v", body)
	}
	// 日期格式
	files := map[string][]byte{"a.pdf": []byte("%PDF-1.4 fake")}
	rec = env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"name": "X", "date": "2026/10/27"}, files)
	if body := decodeBodyMap(t, rec); body["error"] != "日期格式不正确，应为 YYYY.MM.DD" {
		t.Fatalf("日期校验文案不符: %v", body)
	}
	// 扩展名白名单
	rec = env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"name": "X", "date": "2026.10.27"}, map[string][]byte{"a.exe": []byte("x")})
	if body := decodeBodyMap(t, rec); !strings.Contains(body["error"].(string), "不支持的文件格式") {
		t.Fatalf("扩展名校验文案不符: %v", body)
	}
	// 缺名称
	rec = env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"date": "2026.10.27"}, files)
	if body := decodeBodyMap(t, rec); body["error"] != "请填写学习文件名称" {
		t.Fatalf("缺名称文案不符: %v", body)
	}
}

// TestGenerate 单文件生成成功链路：建记录 + COS 中转 + Dify 触发 + 表单记忆。
func TestGenerate(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	files := map[string][]byte{"学习资料.pdf": []byte("%PDF-1.4 fake")}
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "十月安全活动", "date": "2026.10.27", "host": "李班长",
		"superior": "张主任", "recorder": "王记录", "attendees": "张三、李四",
	}, files)
	body := decodeBodyMap(t, rec)
	if body["ok"] != true {
		t.Fatalf("生成应成功: %v", body)
	}
	record, _ := body["record"].(map[string]any)
	if record["status"] != "processing" || record["team"] != "检修一班" || record["fileName"] != "2026.10.27.docx" {
		t.Fatalf("记录字段不符: %v", record)
	}
	if record["sourceCount"] != float64(1) {
		t.Fatalf("sourceCount 不符: %v", record)
	}
	// COS 已上传且记录了 sourceCosKey
	if len(env.cos.objects) != 1 {
		t.Fatalf("COS 应有 1 个中转对象: %v", env.cos.objects)
	}
	env.dify.mu.Lock()
	defer env.dify.mu.Unlock()
	if len(env.dify.inputs) != 1 {
		t.Fatalf("Dify 应触发 1 次: %v", env.dify.inputs)
	}
	in := env.dify.inputs[0]
	if in.Date != "2026.10.27" || in.Class != "检修一班" || !strings.HasPrefix(in.URL, "https://cos.example.invalid/safeday/") {
		t.Fatalf("工作流入参不符: %+v", in)
	}
	// 表单默认值记忆
	defs := env.store.GetFormDefaults("检修一班")
	if defs.Superior != "张主任" || defs.Recorder != "王记录" {
		t.Fatalf("表单默认值未记忆: %+v", defs)
	}
}

// TestGenerateMulti 多文件合并：非 PDF 先转 PDF 再合并（转换桩 + imagepdf.MergePDFs）。
func TestGenerateMulti(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	pdfA, err := realPDF()
	if err != nil {
		t.Fatalf("造 PDF 失败: %v", err)
	}
	files := map[string][]byte{"a.pdf": pdfA, "b.docx": []byte("docx-bytes")}
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "多文件学习", "date": "2026.10.28",
	}, files)
	body := decodeBodyMap(t, rec)
	if body["ok"] != true {
		t.Fatalf("多文件生成应成功: %v", body)
	}
	env.dify.mu.Lock()
	defer env.dify.mu.Unlock()
	if len(env.dify.inputs) != 1 {
		t.Fatalf("Dify 应触发: %v", env.dify.inputs)
	}
	// 合并产物应为合法 PDF（两页：原始 PDF + 转换桩 PDF）
	var key string
	for k := range env.cos.objects {
		key = k
	}
	if !strings.HasSuffix(key, ".pdf") {
		t.Fatalf("多文件产物 key 应 pdf: %s", key)
	}
	pages, err := imagepdf.PDFPageCount(env.cos.objects[key])
	if err != nil || pages != 2 {
		t.Fatalf("合并 PDF 应 2 页: pages=%d err=%v", pages, err)
	}
}

// TestGenerateSameDateReplace 同班组同 date 只保留最新一条，失败清理被覆盖旧产物。
func TestGenerateSameDateReplace(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	// 旧记录 + 旧产物
	old, replaced, _ := env.store.Create(Record{
		Name: "旧", Date: "2026.10.29", FileName: "2026.10.29.docx", Team: "检修一班", Status: "done",
	})
	if len(replaced) != 0 {
		t.Fatalf("首次建记录不应有 replaced")
	}
	outDir := filepath.Join(env.store.DocsDir(), "检修一班")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "2026.10.29.docx"), []byte("old-docx"), 0o644)

	files := map[string][]byte{"a.pdf": []byte("%PDF-1.4 a")}
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"name": "新", "date": "2026.10.29"}, files)
	body := decodeBodyMap(t, rec)
	if body["ok"] != true {
		t.Fatalf("生成应成功: %v", body)
	}
	list := env.store.List("检修一班")
	if len(list) != 1 || list[0].Name != "新" {
		t.Fatalf("同班组同 date 应只保留最新: %+v", list)
	}
	if _, ok := env.store.Get(old.ID); ok {
		t.Fatalf("旧记录应被替换")
	}
}

// TestRecordsTeamIsolation 记录列表班组隔离 + 超管 all。
func TestRecordsTeamIsolation(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	_, _, _ = env.store.Create(Record{Name: "A", Date: "2026.10.01", FileName: "1.docx", Team: "检修一班", Status: "done"})
	_, _, _ = env.store.Create(Record{Name: "B", Date: "2026.10.02", FileName: "2.docx", Team: "检修二班", Status: "done"})

	rec := env.do(t, http.MethodGet, "/api/v1/safeday/records", nil)
	body := decodeBodyMap(t, rec)
	list, _ := body["records"].([]any)
	if len(list) != 1 {
		t.Fatalf("本班应只见 1 条: %v", body)
	}

	// 超管 all
	envAdmin := newTestServer(t, identAdmin)
	teamRules(envAdmin.fake)
	_, _, _ = envAdmin.store.Create(Record{Name: "A", Date: "2026.10.01", FileName: "1.docx", Team: "检修一班", Status: "done"})
	_, _, _ = envAdmin.store.Create(Record{Name: "B", Date: "2026.10.02", FileName: "2.docx", Team: "检修二班", Status: "done"})
	rec = envAdmin.do(t, http.MethodGet, "/api/v1/safeday/records?team_id=all", nil)
	body = decodeBodyMap(t, rec)
	list, _ = body["records"].([]any)
	if len(list) != 2 {
		t.Fatalf("超管 all 应见全部: %v", body)
	}
}

// TestDownloadAndDelete 下载归属校验与删除级联。
func TestDownloadAndDelete(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	rec, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.03", FileName: "2026.10.03.docx", Team: "检修一班",
		Status: "done", SourceCosKey: "safeday/x/1.pdf",
	})
	outDir := filepath.Join(env.store.DocsDir(), "检修一班")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "2026.10.03.docx"), []byte("docx-bytes"), 0o644)

	// 下载成功
	resp := env.do(t, http.MethodGet, "/api/v1/safeday/records/"+rec.ID+"/download", nil)
	if resp.Code != http.StatusOK || resp.Body.String() != "docx-bytes" {
		t.Fatalf("下载不符: %d %s", resp.Code, resp.Body.String())
	}
	// 跨班组下载 403
	envB := newTestServer(t, identTeamB)
	teamRules(envB.fake)
	_, _, _ = envB.store.Create(Record{
		Name: "A", Date: "2026.10.03", FileName: "2026.10.03.docx", Team: "检修一班", Status: "done",
	})
	// 注：两个 env 存储目录不同，改用同一 env 的他班用户覆盖 —— 用记录 team 名比对口径验证
	_ = envB

	// 删除级联：记录、产物、COS
	resp = env.do(t, http.MethodDelete, "/api/v1/safeday/records/"+rec.ID, nil)
	body := decodeBodyMap(t, resp)
	if body["ok"] != true {
		t.Fatalf("删除应成功: %v", body)
	}
	if _, ok := env.store.Get(rec.ID); ok {
		t.Fatalf("记录应删除")
	}
	if _, err := os.Stat(filepath.Join(outDir, "2026.10.03.docx")); err == nil {
		t.Fatalf("产物应删除")
	}
	if len(env.cos.deleted) == 0 {
		t.Fatalf("COS 源文件应清理")
	}
	// 再删 404
	resp = env.do(t, http.MethodDelete, "/api/v1/safeday/records/"+rec.ID, nil)
	if body := decodeBodyMap(t, resp); body["error"] != "记录不存在" {
		t.Fatalf("重复删除应 404 文案: %v", body)
	}
}

// TestCanAccess 跨班组记录访问拒绝。
func TestCanAccess(t *testing.T) {
	env := newTestServer(t, identTeamB) // 用户班组 2
	teamRules(env.fake)
	rec, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.04", FileName: "2026.10.04.docx", Team: "检修一班", Status: "done",
	})
	resp := env.do(t, http.MethodGet, "/api/v1/safeday/records/"+rec.ID+"/download", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("跨班组下载应 403: %s", resp.Body.String())
	}
	resp = env.do(t, http.MethodGet, "/api/v1/safeday/records/"+rec.ID+"/preview", nil)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("跨班组预览应 403: %s", resp.Body.String())
	}
}

// TestPreview 预览分支：微软查看器（默认）/ OO 承载页 / 非 Office 格式不支持。
func TestPreview(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	rec, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.05", FileName: "2026.10.05.docx", Team: "检修一班", Status: "done",
	})
	// 默认 PREVIEW_VIEWER_URL → 微软查看器地址（cfg 无该键时走内置缺省）
	env.module.Cfg = fakeConfig{"PREVIEW_VIEWER_URL": "https://view.officeapps.live.com/op/view.aspx"}
	resp := env.do(t, http.MethodGet, "/api/v1/safeday/records/"+rec.ID+"/preview", nil)
	body := decodeBodyMap(t, resp)
	url, _ := body["url"].(string)
	if !strings.HasPrefix(url, "https://view.officeapps.live.com/op/view.aspx?src=") {
		t.Fatalf("微软查看器分支不符: %v", body)
	}
	// 回源地址须附 ?token=（下载接口自验证 JWT，对齐 Node utils/preview.js）
	srcEnc := strings.TrimPrefix(url, "https://view.officeapps.live.com/op/view.aspx?src=")
	srcURL, uerr := neturl.QueryUnescape(srcEnc)
	if uerr != nil || !strings.Contains(srcURL, "/download/2026.10.05.docx?token=test") {
		t.Fatalf("回源地址应附 ?token=: %v", body)
	}
	// 配置 OO → office.html 承载页
	env.module.Cfg = fakeConfig{"OO_PUBLIC_URL": "https://oo.example.invalid"}
	resp = env.do(t, http.MethodGet, "/api/v1/safeday/records/"+rec.ID+"/preview", nil)
	body = decodeBodyMap(t, resp)
	url, _ = body["url"].(string)
	if !strings.HasPrefix(url, "/office.html?src=safeday&id=") {
		t.Fatalf("OO 分支应返回承载页: %v", body)
	}
	// 非 Office 格式 → ok:false（不支持在线预览）
	recPDF, _, _ := env.store.Create(Record{
		Name: "B", Date: "2026.10.06", FileName: "2026.10.06.pdf", Team: "检修一班", Status: "done",
	})
	env.module.Cfg = fakeConfig{}
	resp = env.do(t, http.MethodGet, "/api/v1/safeday/records/"+recPDF.ID+"/preview", nil)
	body = decodeBodyMap(t, resp)
	if body["ok"] != false {
		t.Fatalf("非 Office 格式应 ok:false: %v", body)
	}
}

// TestFormMeta 表单数据源：成员名单 + 记忆默认值（空值回落 SAFEDAY_DEFAULT_SUPERIOR）。
func TestFormMeta(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	env.fake.on("FROM worklog_member", []string{"name"}, [][]driver.Value{{"张三"}, {"李四"}})
	env.store.SaveFormDefaults("检修一班", FormDefaults{Recorder: "王记录"})

	resp := env.do(t, http.MethodGet, "/api/v1/safeday/form-meta", nil)
	body := decodeBodyMap(t, resp)
	if body["ok"] != true {
		t.Fatalf("form-meta 应成功: %v", body)
	}
	members, _ := body["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("成员名单不符: %v", body)
	}
	defs, _ := body["defaults"].(map[string]any)
	if defs["superior"] != "张主任" || defs["recorder"] != "王记录" {
		t.Fatalf("默认值不符: %v", defs)
	}
}

// TestSaveNetdisk 保存到网盘（注入桩）。
func TestSaveNetdisk(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	rec, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.06", FileName: "2026.10.06.docx", Team: "检修一班", Status: "done",
	})
	outDir := filepath.Join(env.store.DocsDir(), "检修一班")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "2026.10.06.docx"), []byte("docx-bytes"), 0o644)

	resp := env.do(t, http.MethodPost, "/api/v1/safeday/records/"+rec.ID+"/save-netdisk", map[string]any{"space": "public", "dir": "归档"})
	body := decodeBodyMap(t, resp)
	if body["ok"] != true {
		t.Fatalf("保存应成功: %v", body)
	}
	if path, _ := body["path"].(string); path != "public/归档/2026.10.06.docx" {
		t.Fatalf("保存路径不符: %v", body)
	}
	if len(env.netdisk.saved) != 1 || env.netdisk.saved[0].Space != "public" {
		t.Fatalf("网盘入参不符: %+v", env.netdisk.saved)
	}
}

// TestStoreSemantics 记录存储语义：补丁 error 口径、form 默认值空值不覆盖。
func TestStoreSemantics(t *testing.T) {
	store, err := NewRecordStore(t.TempDir())
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	rec, _, _ := store.Create(Record{Name: "X", Date: "2026.10.07", FileName: "1.docx", Team: "T", Status: "processing"})
	msg := "boom"
	failed := "failed"
	store.Update(rec.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
	got, _ := store.Get(rec.ID)
	if got.Error != "boom" {
		t.Fatalf("error 应写入: %+v", got)
	}
	done := "done"
	store.Update(rec.ID, RecordPatch{Status: &done}) // 不带 error → 清除
	got, _ = store.Get(rec.ID)
	if got.Error != "" || got.Status != "done" {
		t.Fatalf("补丁口径不符: %+v", got)
	}
	// 空值不覆盖
	store.SaveFormDefaults("T", FormDefaults{Superior: "甲", Recorder: "乙"})
	store.SaveFormDefaults("T", FormDefaults{Superior: " ", Recorder: ""})
	defs := store.GetFormDefaults("T")
	if defs.Superior != "甲" || defs.Recorder != "乙" {
		t.Fatalf("空值不应覆盖: %+v", defs)
	}
	// 班组改名级联
	if n := store.RenameTeam("T", "T2"); n != 1 {
		t.Fatalf("改名级联条数不符: %d", n)
	}
	if got, _ := store.Get(rec.ID); got.Team != "T2" {
		t.Fatalf("team 未级联: %+v", got)
	}
	// 旧记录回填
	store2, _ := NewRecordStore(t.TempDir())
	store2.Create(Record{Name: "Y", Date: "2026.10.08", FileName: "2.docx", Status: "processing"})
	if n := store2.BackfillTeam("默认班"); n != 1 {
		t.Fatalf("回填条数不符: %d", n)
	}
	if list := store2.List("默认班"); len(list) != 1 {
		t.Fatalf("回填后应可见: %+v", list)
	}
}

// doMultipart 组装 multipart/form-data 上传（字段 files）。
func (e *testEnv) doMultipart(t *testing.T, path string, fields map[string]string, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for name, data := range files {
		part, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("建表单文件失败: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("写表单文件失败: %v", err)
		}
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// TestDownloadTokenAuth 下载旧版 ?token= JWT 自验证：有效放行、缺失/无效 401（绝不匿名）；
// 其余记录接口仍走统一登录门控。
func TestDownloadTokenAuth(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	strict := fakeTokenAuth{valid: map[string]auth.Identity{"jwt-ok": identTeamA}}
	env.module.Auth = strict
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { env.module.RegisterRoutes(r) })
	env.handler = httpapi.AuthMiddleware(r, strict)

	rec, _, _ := env.store.Create(Record{
		Name: "A", Date: "2026.10.27", FileName: "2026.10.27.docx", Team: "检修一班", Status: "done",
	})
	outDir := filepath.Join(env.store.DocsDir(), "检修一班")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.WriteFile(filepath.Join(outDir, "2026.10.27.docx"), []byte("docx-bytes"), 0o644)

	call := func(rawURL, authHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, rawURL, nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		out := httptest.NewRecorder()
		env.handler.ServeHTTP(out, req)
		return out
	}

	// 旧版 ?token=（无 Authorization 头）→ 放行
	resp := call("/api/v1/safeday/records/"+rec.ID+"/download?token=jwt-ok", "")
	if resp.Code != http.StatusOK || resp.Body.String() != "docx-bytes" {
		t.Fatalf("?token= 下载应放行: %d %s", resp.Code, resp.Body.String())
	}
	// 带名下载路由同样自验证
	resp = call("/api/v1/safeday/records/"+rec.ID+"/download/2026.10.27.docx?token=jwt-ok", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("带名下载应放行: %d %s", resp.Code, resp.Body.String())
	}
	// Authorization: Bearer → 放行
	resp = call("/api/v1/safeday/records/"+rec.ID+"/download", "Bearer jwt-ok")
	if resp.Code != http.StatusOK {
		t.Fatalf("Bearer 下载应放行: %d %s", resp.Code, resp.Body.String())
	}
	// 无效 token → 401（不得匿名）
	resp = call("/api/v1/safeday/records/"+rec.ID+"/download?token=bad", "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("无效 token 应 401: %d %s", resp.Code, resp.Body.String())
	}
	// 无任何凭证 → 401（不得匿名）
	resp = call("/api/v1/safeday/records/"+rec.ID+"/download", "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("无凭证应 401: %d %s", resp.Code, resp.Body.String())
	}
	// 其余记录接口仍要登录（统一鉴权中间件，query token 不放行）
	resp = call("/api/v1/safeday/records", "")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("记录列表无登录应 401: %d %s", resp.Code, resp.Body.String())
	}
	resp = call("/api/v1/safeday/records", "Bearer jwt-ok")
	if resp.Code != http.StatusOK {
		t.Fatalf("记录列表登录后应 200: %d %s", resp.Code, resp.Body.String())
	}
}

// TestDifyWorkflow 真实 Dify 工作流客户端：入参三变量 / SSE 失败回调 / 热配置读取（不实连 Dify）。
func TestDifyWorkflow(t *testing.T) {
	type hit struct {
		auth   string
		path   string
		mode   string
		user   string
		inputs map[string]string
	}
	var (
		mu   sync.Mutex
		hits []hit
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs map[string]string `json:"inputs"`
			Mode   string            `json:"response_mode"`
			User   string            `json:"user"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits = append(hits, hit{
			auth: r.Header.Get("Authorization"), path: r.URL.Path,
			mode: body.Mode, user: body.User, inputs: body.Inputs,
		})
		n := len(hits)
		mu.Unlock()
		if n == 1 {
			_, _ = w.Write([]byte("data: {\"event\":\"workflow_finished\",\"data\":{\"status\":\"failed\",\"error\":\"节点出错\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"event\":\"workflow_finished\",\"data\":{\"status\":\"succeeded\"}}\n\n"))
	}))
	defer srv.Close()

	cfg := fakeConfig{"DIFY_API_URL": srv.URL, "DIFY_SAFEDAY_API_KEY": "key-1"}
	d := &DifyWorkflow{Cfg: cfg, HTTP: srv.Client()}
	if !d.Configured() {
		t.Fatalf("热配置应视为已配置")
	}
	failed := make(chan error, 1)
	if err := d.Run(context.Background(), WorkflowInput{
		URL: "https://cos.example/x.pdf", Date: "2026.10.27", Class: "检修一班",
		OnFailed: func(e error) { failed <- e },
	}); err != nil {
		t.Fatalf("触发应成功: %v", err)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Error(), "节点出错") {
			t.Fatalf("失败回调文案不符: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("应收到工作流失败回调")
	}
	mu.Lock()
	h := hits[0]
	mu.Unlock()
	if h.auth != "Bearer key-1" || h.path != "/v1/workflows/run" || h.mode != "streaming" || h.user != "safeday-web" {
		t.Fatalf("请求口径不符: %+v", h)
	}
	if h.inputs["url"] != "https://cos.example/x.pdf" || h.inputs["date"] != "2026.10.27" || h.inputs["class"] != "检修一班" {
		t.Fatalf("工作流入参不符: %+v", h.inputs)
	}

	// 热配置：换密钥后再次触发即时生效；succeeded 不回调 OnFailed
	cfg["DIFY_SAFEDAY_API_KEY"] = "key-2"
	failed2 := make(chan error, 1)
	if err := d.Run(context.Background(), WorkflowInput{
		URL: "https://cos.example/y.pdf", Date: "2026.10.28", Class: "检修一班",
		OnFailed: func(e error) { failed2 <- e },
	}); err != nil {
		t.Fatalf("二次触发应成功: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // 等后台流消费完
	select {
	case e := <-failed2:
		t.Fatalf("succeeded 不应失败回调: %v", e)
	default:
	}
	mu.Lock()
	h2 := hits[1]
	mu.Unlock()
	if h2.auth != "Bearer key-2" {
		t.Fatalf("热配置应即时生效: %+v", h2)
	}

	// 未配置报错（对齐 Node 文案）
	err := (&DifyWorkflow{Cfg: fakeConfig{}}).Run(context.Background(), WorkflowInput{})
	if err == nil || !strings.Contains(err.Error(), "未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY") {
		t.Fatalf("未配置报错不符: %v", err)
	}
	// 固定字段回落（Cfg 为 nil，测试注入）
	d3 := &DifyWorkflow{APIURL: srv.URL + "/v1", APIKey: "key-3", HTTP: srv.Client()}
	if !d3.Configured() {
		t.Fatalf("固定字段应视为已配置")
	}
	if err := d3.Run(context.Background(), WorkflowInput{URL: "u", Date: "d", Class: "c"}); err != nil {
		t.Fatalf("固定字段触发应成功: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	h3 := hits[2]
	mu.Unlock()
	if h3.auth != "Bearer key-3" || h3.path != "/v1/workflows/run" {
		t.Fatalf("固定字段口径不符: %+v", h3)
	}
}

// TestAppLogOnFailure 失败路径应用日志回归：工作流触发失败 / 异步工作流失败均落日志。
func TestAppLogOnFailure(t *testing.T) {
	env := newTestServer(t, identTeamA)
	teamRules(env.fake)
	logger := &fakeLogger{}
	env.module.Log = logger

	// 触发失败 → safeday.generate.failed
	env.dify.failRun = true
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "十月安全活动", "date": "2026.10.27",
	}, map[string][]byte{"学习资料.pdf": []byte("%PDF-1.4 fake")})
	if body := decodeBodyMap(t, rec); body["ok"] != false {
		t.Fatalf("触发失败应报错: %v", body)
	}
	if !logger.has("safeday.generate.failed") {
		t.Fatalf("触发失败应落日志: %v", logger.events)
	}

	// 异步失败 → safeday.workflow.failed
	env.dify.failRun = false
	env.dify.autoFail = true
	rec = env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{
		"name": "十月安全活动", "date": "2026.10.28",
	}, map[string][]byte{"学习资料.pdf": []byte("%PDF-1.4 fake")})
	if body := decodeBodyMap(t, rec); body["ok"] != true {
		t.Fatalf("异步失败不影响触发返回: %v", body)
	}
	if !logger.has("safeday.workflow.failed") {
		t.Fatalf("异步失败应落日志: %v", logger.events)
	}
}

// TestResolveTemplatePath 模板路径按部署布局解析（容器 assets/… 优先、仓根 server/assets/… 回退），
// New 装配的 Renderer.TemplatePath 同口径；fake 文件系统，不联网。
func TestResolveTemplatePath(t *testing.T) {
	// 容器布局：assets/safeday/… 命中相对路径
	container := t.TempDir()
	if err := os.MkdirAll(filepath.Join(container, "assets", "safeday"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(container, "assets", "safeday", "activity-record-template.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(container)
	if got := ResolveTemplatePath(); got != filepath.Join("assets", "safeday", "activity-record-template.docx") {
		t.Fatalf("容器布局解析错: %q", got)
	}
	if m := New(nil, nil); m.Renderer.TemplatePath != filepath.Join("assets", "safeday", "activity-record-template.docx") {
		t.Fatalf("New 装配 TemplatePath 错: %q", m.Renderer.TemplatePath)
	}

	// 仓根布局：server/assets/safeday/… 回退
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "server", "assets", "safeday"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "server", "assets", "safeday", "activity-record-template.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if got := ResolveTemplatePath(); got != filepath.Join("server", "assets", "safeday", "activity-record-template.docx") {
		t.Fatalf("仓根布局解析错: %q", got)
	}

	// 均未命中：回容器布局缺省值（报错信息指向缺省路径）
	t.Chdir(t.TempDir())
	if got := ResolveTemplatePath(); got != defaultTemplatePath {
		t.Fatalf("缺省应回 %q，got %q", defaultTemplatePath, got)
	}
}
