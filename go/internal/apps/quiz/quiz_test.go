package quiz

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"

	"shade/internal/auth"
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

type testEnv struct {
	handler  http.Handler
	fake     *fakeDB
	module   *Module
	analyzer *fakeAnalyzer
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	m := New(db)
	m.Permit = fakePermit{allow: true}
	an := &fakeAnalyzer{configured: false}
	m.SetAnalyzer(an)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler:  httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:     fake,
		module:   m,
		analyzer: an,
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

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string, map[string]any) {
	t.Helper()
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	data := map[string]any{}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		_ = json.Unmarshal(env.Data, &data)
	}
	return env.Code, env.Message, data
}

var (
	identAdmin     = auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1}
	identTeamAdmin = auth.Identity{ID: 2, Username: "tm", Role: "team_admin", TeamID: 1}
	identOtherTM   = auth.Identity{ID: 3, Username: "tm2", Role: "team_admin", TeamID: 2}
	identUser      = auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1}
)

func teamRules(fake *fakeDB) {
	fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	fake.onArgs("FROM sys_team WHERE id = ?", []driver.Value{int64(1)}, []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	fake.onArgs("FROM sys_team WHERE id = ?", []driver.Value{int64(2)}, []string{"id", "name"}, [][]driver.Value{{int64(2), "检修二班"}})
}

// ===== 归一与校验 =====

// TestNormalize 题型/答案/作答归一口径。
func TestNormalize(t *testing.T) {
	if normalizeType("单选题") != "single" || normalizeType(" multiple ") != "multiple" || normalizeType("判断") != "judge" {
		t.Fatalf("题型归一不符")
	}
	if normalizeType("简答") != "" {
		t.Fatalf("非法题型应空串")
	}
	if r := normalizeAnswer("single", "a", 4); r.Answer != "A" || r.Error != "" {
		t.Fatalf("single 归一不符: %+v", r)
	}
	if r := normalizeAnswer("multiple", "cb a", 4); r.Answer != "ABC" {
		t.Fatalf("multiple 归一应字母序去重: %+v", r)
	}
	if r := normalizeAnswer("multiple", "a", 4); r.Error == "" {
		t.Fatalf("multiple 少于 2 字母应报错")
	}
	if r := normalizeAnswer("single", "e", 4); r.Error == "" {
		t.Fatalf("超范围应报错")
	}
	if r := normalizeAnswer("judge", "对", 2); r.Answer != "A" || normalizeAnswer("judge", "×", 2).Answer != "B" {
		t.Fatalf("judge 归一不符")
	}
	if normalizeUserAnswer("bca a") != "ABC" {
		t.Fatalf("用户作答归一不符")
	}
	body := map[string]json.RawMessage{
		"type":    json.RawMessage(`"多选题"`),
		"content": json.RawMessage(`"题干"`),
		"options": json.RawMessage(`["甲","乙","丙"]`),
		"answer":  json.RawMessage(`"acb"`),
	}
	v, errMsg := validateQuestionPayload(body)
	if errMsg != "" || v.Type != "multiple" || v.Answer != "ABC" || len(v.Options) != 3 {
		t.Fatalf("题目校验不符: %+v err=%s", v, errMsg)
	}
	// 判断题固定两项
	body = map[string]json.RawMessage{
		"type": json.RawMessage(`"判断题"`), "content": json.RawMessage(`"题干"`), "answer": json.RawMessage(`"错"`),
	}
	v, errMsg = validateQuestionPayload(body)
	if errMsg != "" || len(v.Options) != 2 || v.Answer != "B" {
		t.Fatalf("判断题校验不符: %+v err=%s", v, errMsg)
	}
}

// ===== 题库管理 =====

// TestBankCreate 权限与重名校验。
func TestBankCreate(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	teamRules(env.fake)

	// 全部池仅超管
	rec := env.do(t, http.MethodPost, "/api/v1/quiz/banks", map[string]any{"name": "全部题库", "scope": "all"})
	if code, _, _ := decodeEnvelope(t, rec); code != codeNotAdmin {
		t.Fatalf("班组管理员建全部池应 40304: %s", rec.Body.String())
	}
	// 无班组（超管建班组池）
	envNone := newTestServer(t, identAdmin)
	envNone.fake.onErr("FROM sys_team", errNoRowsSentinel())
	rec = envNone.do(t, http.MethodPost, "/api/v1/quiz/banks", map[string]any{"name": "班组题库"})
	if code, _, _ := decodeEnvelope(t, rec); code != codeParam {
		t.Fatalf("无班组应 40030: %s", rec.Body.String())
	}
	// 成功
	rec = env.do(t, http.MethodPost, "/api/v1/quiz/banks", map[string]any{"name": "班组题库", "description": "简介"})
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 || data["id"] == nil {
		t.Fatalf("建题库应成功: %s", rec.Body.String())
	}
	// 全部池重名应用层校验
	envAdmin := newTestServer(t, identAdmin)
	teamRules(envAdmin.fake)
	envAdmin.fake.on("SELECT id FROM quiz_bank WHERE scope = 'all'", []string{"id"}, [][]driver.Value{{int64(9)}})
	rec = envAdmin.do(t, http.MethodPost, "/api/v1/quiz/banks", map[string]any{"name": "公共题库", "scope": "all"})
	if code, _, _ := decodeEnvelope(t, rec); code != codeDup {
		t.Fatalf("全部池重名应 40900: %s", rec.Body.String())
	}
}

// TestBankManageGating 管理接口粗筛 + 归属细筛（他班班管不得改本班班组池）。
func TestBankManageGating(t *testing.T) {
	env := newTestServer(t, identOtherTM) // 班管，班组 2
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(5), int64(1), "team"}}) // 归属班组 1
	rec := env.do(t, http.MethodPut, "/api/v1/quiz/banks/5", map[string]any{"name": "改名"})
	code, _, _ := decodeEnvelope(t, rec)
	if code != codeMissing {
		t.Fatalf("他班班管应 40400（findManageBank 拒绝）: %s", rec.Body.String())
	}
	// 普通用户连管理粗筛都过不去
	envUser := newTestServer(t, identUser)
	teamRules(envUser.fake)
	rec = envUser.do(t, http.MethodPost, "/api/v1/quiz/banks", map[string]any{"name": "x"})
	if code, _, _ := decodeEnvelope(t, rec); code != codeNotAdmin {
		t.Fatalf("普通用户建题库应 40304: %s", rec.Body.String())
	}
}

// TestBankPool 题库池可见性（全部池 + 本班班组池）与订阅标记。
func TestBankPool(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank b", []string{"id", "name", "description", "scope", "team_name", "created_at"},
		[][]driver.Value{
			{int64(1), "全部池", "", "all", nil, ""},
			{int64(2), "班组池", "", "team", "检修一班", ""},
		})
	env.fake.on("FROM quiz_question WHERE bank_id IN", []string{"bank_id", "total", "none_cnt", "pending_cnt", "failed_cnt", "done_cnt"},
		[][]driver.Value{{int64(1), int64(10), int64(2), int64(0), int64(1), int64(7)}})
	env.fake.on("FROM quiz_user_bank WHERE user_id", []string{"bank_id"}, [][]driver.Value{{int64(2)}})

	rec := env.do(t, http.MethodGet, "/api/v1/quiz/banks/pool", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("题库池应成功: %s", rec.Body.String())
	}
	list, _ := data["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("应 2 个题库: %v", data)
	}
	first := list[0].(map[string]any)
	if first["id"] != float64(1) || first["analysis"].(map[string]any)["done"] != float64(7) {
		t.Fatalf("统计不符: %v", first)
	}
	second := list[1].(map[string]any)
	if second["added"] != float64(1) {
		t.Fatalf("订阅标记不符: %v", second)
	}
}

// TestPracticeVisibility 刷题可见性与答案防泄漏。
func TestPracticeVisibility(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	// 班组池归属班组 2 → 对班组 1 用户不可见
	env.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(2), "team"}})
	rec := env.do(t, http.MethodGet, "/api/v1/quiz/practice/questions?mode=seq&bankId=3", nil)
	if code, _, _ := decodeEnvelope(t, rec); code != codeMissing {
		t.Fatalf("跨班组题库应 40400: %s", rec.Body.String())
	}

	// 可见题库默认不带答案
	env.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_question", []string{"total"}, [][]driver.Value{{int64(1)}})
	env.fake.on("SELECT id, type, content, options FROM quiz_question", []string{"id", "type", "content", "options"},
		[][]driver.Value{{int64(31), "single", "题干1", `[\"甲\",\"乙\"]`}})
	rec = env.do(t, http.MethodGet, "/api/v1/quiz/practice/questions?mode=seq&bankId=3", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("取题应成功: %s", rec.Body.String())
	}
	list := data["list"].([]any)
	item := list[0].(map[string]any)
	if _, has := item["answer"]; has {
		t.Fatalf("默认出参不应含答案: %v", item)
	}
	if item["content"] != "题干1" {
		t.Fatalf("题干不符: %v", item)
	}
}

// TestPracticeAnswer 判分 + 错题本 upsert + 连对移出。
func TestPracticeAnswer(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env.fake.on("SELECT bank_id, answer, analysis FROM quiz_question", []string{"bank_id", "answer", "analysis"},
		[][]driver.Value{{int64(3), "A", "解析文本"}})

	// 答错 → 错题本 upsert
	rec := env.do(t, http.MethodPost, "/api/v1/quiz/practice/answer", map[string]any{"questionId": 31, "answer": "B"})
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("作答应成功: %s", rec.Body.String())
	}
	if data["right"] != false {
		t.Fatalf("答对应判错: %v", data)
	}
	if env.fake.execCount("INSERT INTO quiz_record") != 1 {
		t.Fatalf("应写答题记录")
	}
	if env.fake.execCount("INSERT INTO quiz_wrong") != 1 {
		t.Fatalf("答错应 upsert 错题本")
	}
	wrong := data["wrong"].(map[string]any)
	if wrong["inBook"] != true {
		t.Fatalf("错题本状态不符: %v", wrong)
	}

	// 答对 + 连对达 3 → 移出
	env.fake.on("SELECT id, right_streak FROM quiz_wrong", []string{"id", "right_streak"},
		[][]driver.Value{{int64(7), int64(2)}})
	rec = env.do(t, http.MethodPost, "/api/v1/quiz/practice/answer", map[string]any{"questionId": 31, "answer": "A"})
	code, _, data = decodeEnvelope(t, rec)
	if code != 0 || data["right"] != true {
		t.Fatalf("答对应成功: %s", rec.Body.String())
	}
	if env.fake.execCount("DELETE FROM quiz_wrong") != 1 {
		t.Fatalf("连对达 3 应移出错题本")
	}
	wrong = data["wrong"].(map[string]any)
	if wrong["removed"] != true {
		t.Fatalf("移出标记不符: %v", wrong)
	}
}

// TestPracticeReset 断点上报与清空记录。
func TestPracticeReset(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})

	rec := env.do(t, http.MethodPost, "/api/v1/quiz/practice/progress", map[string]any{"bankId": 3, "idx": 5})
	if code, _, _ := decodeEnvelope(t, rec); code != 0 {
		t.Fatalf("断点上报应成功: %s", rec.Body.String())
	}
	if e, ok := env.fake.lastExec("INSERT INTO quiz_user_bank"); !ok || len(e.args) < 3 {
		t.Fatalf("断点应 upsert 订阅行: %+v", e)
	}

	rec = env.do(t, http.MethodPost, "/api/v1/quiz/practice/reset", map[string]any{"bankId": 3})
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("清空应成功: %s", rec.Body.String())
	}
	if env.fake.execCount("DELETE FROM quiz_record") != 1 || env.fake.execCount("seq_idx = NULL") != 1 {
		t.Fatalf("清空口径不符")
	}
	if data["cleared"] == nil {
		t.Fatalf("应返回 cleared: %v", data)
	}
}

// TestBankImport 导入逐行校验与全量替换。
func TestBankImport(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})

	// 含错误行 → 40031 + errors 明细
	bad := importXLSX(t, [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
		{"单选题", "合法题", "甲", "乙", "", "", "", "", "A", ""},
		{"简答题", "非法题型", "甲", "乙", "", "", "", "", "A", ""},
		{"单选题", "", "甲", "乙", "", "", "", "", "A", ""},
	})
	rec := env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", bad)
	code, message, data := decodeEnvelope(t, rec)
	if code != codeRows {
		t.Fatalf("错误行应 40031: %s", rec.Body.String())
	}
	if !strings.Contains(message, "校验未通过") {
		t.Fatalf("错误文案不符: %s", message)
	}
	if errors, ok := data["errors"].([]any); !ok || len(errors) != 2 {
		t.Fatalf("应返回 2 条行错误: %v", data)
	}

	// 全部合法 → 全量替换 + 入 AI 队列
	env.analyzer.configured = true
	good := importXLSX(t, [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
		{"单选题", "题一", "甲", "乙", "", "", "", "", "A", "手填解析"},
		{"多选题", "题二", "甲", "乙", "丙", "", "", "", "AC", ""},
		{"判断题", "题三", "", "", "", "", "", "", "对", ""},
	})
	rec = env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", good)
	code, message, data = decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("导入应成功: %s", rec.Body.String())
	}
	if data["imported"] != float64(3) || !strings.Contains(message, "已导入 3 道题目") {
		t.Fatalf("导入计数不符: %v %s", data, message)
	}
	if env.fake.execCount("DELETE FROM quiz_question") != 1 {
		t.Fatalf("应全量替换旧题目")
	}
	if env.fake.execCount("INSERT INTO quiz_question") != 1 {
		t.Fatalf("应批量插入新题目")
	}
	e, _ := env.fake.lastExec("INSERT INTO quiz_question")
	// 3 行 × 8 参数
	if len(e.args) != 24 {
		t.Fatalf("插入参数应 24 个（3 行 × 8 列），实际 %d", len(e.args))
	}
}

// TestBankTemplate 模板下载（xlsx 可解析）。
func TestBankTemplate(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	rec := env.do(t, http.MethodGet, "/api/v1/quiz/banks/template", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("模板下载应 200: %s", rec.Body.String())
	}
	wb, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("模板应为合法 xlsx: %v", err)
	}
	defer wb.Close()
	rows, _ := wb.GetRows("题目")
	if len(rows) < 2 || rows[0][0] != "题型" {
		t.Fatalf("模板表头不符: %v", rows)
	}
}

// TestAnalyzerQueue 解析队列：未配置跳过、成功置 done、失败置 failed、去重入队。
func TestAnalyzerQueue(t *testing.T) {
	db, fake := openFake(t)
	m := New(db)
	an := &fakeAnalyzer{configured: true, analysis: "AI 解析"}
	m.SetAnalyzer(an)
	queue := m.Queue()

	// 未配置时 Enqueue 返回 0
	m2 := New(db)
	m2.SetAnalyzer(&fakeAnalyzer{configured: false})
	if n := m2.Queue().Enqueue([]int64{1}); n != 0 {
		t.Fatalf("未配置应跳过入队，实际 %d", n)
	}

	// 成功路径
	fake.on("SELECT type, content, options, answer", []string{"type", "content", "options", "answer", "analysis", "analysis_status"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A", "", "none"}})
	if n := queue.Enqueue([]int64{101, 101}); n != 1 {
		t.Fatalf("去重入队应计 1，实际 %d", n)
	}
	waitFor(t, func() bool { return fake.execCount("analysis_status = 'done'") == 1 })
	if fake.execCount("SET analysis_status = 'pending'") != 1 {
		t.Fatalf("应先置 pending")
	}

	// 失败路径
	an.mu.Lock()
	an.err = errString("Dify 挂了")
	an.mu.Unlock()
	fake.on("SELECT type, content, options, answer", []string{"type", "content", "options", "answer", "analysis", "analysis_status"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A", "", "none"}})
	queue.Enqueue([]int64{202})
	waitFor(t, func() bool { return fake.execCount("analysis_status = 'failed'") == 1 })
}

// waitFor 轮询等待条件成立（异步 worker 收敛）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待条件超时")
}

// TestDifyAnalyzer Dify 解析客户端（httptest，不实连）。
func TestDifyAnalyzer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflows/run" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Inputs map[string]string `json:"inputs"`
			User   string            `json:"user"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Inputs["type"] != "判断题" || body.Inputs["answer"] != "B（错误）" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"inputs 不符"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"outputs":{"analysis":"这是解析"}}}`))
	}))
	defer srv.Close()

	d := &DifyAnalyzer{APIURL: srv.URL, APIKey: "k", HTTP: srv.Client()}
	if !d.Configured() {
		t.Fatalf("应已配置")
	}
	text, err := d.Analyze(context.Background(), QuestionForAI{
		ID: 7, Type: "judge", Content: "题干", Options: []string{"正确", "错误"}, Answer: "B",
	})
	if err != nil || text != "这是解析" {
		t.Fatalf("解析产物不符: %q err=%v", text, err)
	}
	// 未配置报错
	_, err = (&DifyAnalyzer{}).Analyze(context.Background(), QuestionForAI{})
	if err == nil {
		t.Fatalf("未配置应报错")
	}
	// outputs 缺 analysis 时取首个非空字符串
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"outputs":{"other":"兜底解析"}}}`))
	}))
	defer srv2.Close()
	d2 := &DifyAnalyzer{APIURL: srv2.URL + "/v1", APIKey: "k", HTTP: srv2.Client()}
	text, err = d2.Analyze(context.Background(), QuestionForAI{ID: 1, Type: "single", Content: "c", Options: []string{"甲", "乙"}, Answer: "A"})
	if err != nil || text != "兜底解析" {
		t.Fatalf("容错取值不符: %q err=%v", text, err)
	}
}

// ===== 工具 =====

// importXLSX 构造导入用 xlsx 字节。
func importXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetSheetName("Sheet1", "题目")
	for i, row := range rows {
		for j, cell := range row {
			addr, _ := excelize.CoordinatesToCellName(j+1, i+1)
			_ = f.SetCellValue("题目", addr, cell)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("造 xlsx 失败: %v", err)
	}
	return buf.Bytes()
}

// doImport 上传导入文件（multipart 字段 file）。
func (e *testEnv) doImport(t *testing.T, path, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("建表单文件失败: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("写表单文件失败: %v", err)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// errNoRowsSentinel sql.ErrNoRows 语义桩错误（无记录）。
func errNoRowsSentinel() error { return sql.ErrNoRows }

// TestRightRateRounding 正确率取整对齐 Node Math.round（2/3→67、3/8→38，截断口径会得 66/37）。
func TestRightRateRounding(t *testing.T) {
	// 总览：答 3 对 2 → 67
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	env.fake.on("FROM quiz_record WHERE user_id", []string{"total", "rights"},
		[][]driver.Value{{int64(3), int64(2)}})
	env.fake.on("FROM quiz_wrong WHERE user_id", []string{"cnt"}, [][]driver.Value{{int64(1)}})
	rec := env.do(t, http.MethodGet, "/api/v1/quiz/overview", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("总览应成功: %s", rec.Body.String())
	}
	if data["rightRate"] != float64(67) {
		t.Fatalf("总览正确率应 Math.round(2/3)=67: %v", data)
	}

	// 作答统计：本人 2/3→67、全员 3/8→38
	env2 := newTestServer(t, identUser)
	teamRules(env2.fake)
	env2.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env2.fake.on("SELECT bank_id, answer, analysis FROM quiz_question", []string{"bank_id", "answer", "analysis"},
		[][]driver.Value{{int64(3), "A", "解析"}})
	env2.fake.on("FROM quiz_record WHERE question_id = ?", []string{"total", "rights"},
		[][]driver.Value{{int64(8), int64(3)}})
	env2.fake.on("FROM quiz_record WHERE question_id = ? AND user_id = ?", []string{"total", "rights"},
		[][]driver.Value{{int64(3), int64(2)}})
	rec = env2.do(t, http.MethodPost, "/api/v1/quiz/practice/answer", map[string]any{"questionId": 31, "answer": "A"})
	code, _, data = decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("作答应成功: %s", rec.Body.String())
	}
	stats, _ := data["stats"].(map[string]any)
	if stats["myRightRate"] != float64(67) || stats["allRightRate"] != float64(38) {
		t.Fatalf("作答正确率应 Math.round: %v", stats)
	}
}

// TestPracticeParamClamp 查询参数钳制对齐 Node parseInt||默认值 + Math.max/min（负值落 1 而非默认值）。
func TestPracticeParamClamp(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_question", []string{"total"}, [][]driver.Value{{int64(9)}})
	env.fake.on("SELECT id, type, content, options FROM quiz_question", []string{"id", "type", "content", "options"}, nil)
	// size=-5 → Math.max(1,-5)=1；page=-3 → 1 → OFFSET 0
	rec := env.do(t, http.MethodGet, "/api/v1/quiz/banks/3/questions?page=-3&size=-5", nil)
	if code, _, _ := decodeEnvelope(t, rec); code != 0 {
		t.Fatalf("题目列表应成功: %s", rec.Body.String())
	}
	if q, ok := env.fake.lastQuery("ORDER BY sort, id LIMIT"); !ok || !strings.Contains(q.query, "LIMIT 1 OFFSET 0") {
		t.Fatalf("size/page 负值应钳制为 LIMIT 1 OFFSET 0: %+v", q)
	}
	// 非数字 → 缺省 20/1
	rec = env.do(t, http.MethodGet, "/api/v1/quiz/banks/3/questions?page=abc&size=abc", nil)
	if code, _, _ := decodeEnvelope(t, rec); code != 0 {
		t.Fatalf("题目列表应成功: %s", rec.Body.String())
	}
	if q, ok := env.fake.lastQuery("ORDER BY sort, id LIMIT"); !ok || !strings.Contains(q.query, "LIMIT 20 OFFSET 0") {
		t.Fatalf("size/page 非数字应落缺省 20: %+v", q)
	}
}

// TestBankUpdateScopeNull body.scope 显式 null 按 Node `!== undefined` 口径视同 'team'（可触发题库池变更）。
func TestBankUpdateScopeNull(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(5), nil, "all"}})
	rec := env.do(t, http.MethodPut, "/api/v1/quiz/banks/5", map[string]any{"name": "改名", "scope": nil})
	code, _, _ := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("改名应成功: %s", rec.Body.String())
	}
	e, ok := env.fake.lastExec("UPDATE quiz_bank SET name")
	if !ok || len(e.args) < 4 {
		t.Fatalf("应更新题库: %+v", e)
	}
	if e.args[2] != "team" || e.args[3] != int64(1) {
		t.Fatalf("scope=null 应视同 'team' 并挂生效班组: %+v", e.args)
	}
}
