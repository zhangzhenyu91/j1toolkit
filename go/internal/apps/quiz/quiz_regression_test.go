package quiz

// 本文件是 Excel 导入与 AI 解析队列的既有行为回归（对齐 server/src/quiz）：
// 题型别名/答案归一/选项按序取非空列/空行跳过/解析列口径/全量替换级联/行错误上限/入队口径；
// 全部使用本地假依赖，不连接真实数据库与 Dify。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// emptyAnalyzer 返回空解析（回归：空解析归 failed）。
type emptyAnalyzer struct{ configured bool }

func (a emptyAnalyzer) Configured() bool { return a.configured }
func (a emptyAnalyzer) Analyze(context.Context, QuestionForAI) (string, error) {
	return "", nil
}

// seqAnalyzer 按题号给结果：ID=1 报错，其余返回解析（回归：单题失败不中断 worker）。
type seqAnalyzer struct{ configured bool }

func (a seqAnalyzer) Configured() bool { return a.configured }
func (a seqAnalyzer) Analyze(_ context.Context, q QuestionForAI) (string, error) {
	if q.ID == 1 {
		return "", errString("Dify 挂了")
	}
	return "解析-" + fmt.Sprint(q.ID), nil
}

// TestImportExcelRegression 导入主链路回归：题型别名、答案归一、选项按序取非空列、
// 空行跳过、解析列口径（none/done）、sort 按行序、全量替换级联删除、入队计数。
func TestImportExcelRegression(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	// 导入后按状态入队用（3 条无解析的题）
	env.fake.on("FROM quiz_question WHERE bank_id = ? AND status = 1", []string{"id"},
		[][]driver.Value{{int64(21)}, {int64(22)}, {int64(23)}})
	env.analyzer.configured = true

	xlsx := importXLSX(t, [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
		{"single", "题一", "甲", "乙", "", "", "", "", "a", "手填解析"},
		{"Multiple", "题二", "甲", "乙", "丙", "", "", "", "c a b", ""},
		{"判断", "题三", "", "", "", "", "", "", "对", ""},
		{"", "", "", "", "", "", "", "", "", ""},         // 空行跳过
		{"单选题", "题四", "甲", "", "丙", "", "", "", "B", ""}, // 选项 B 空 → 按序取非空列
	})
	rec := env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", xlsx)
	code, message, data := decodeEnvelope(t, rec)
	if code != 0 || data["imported"] != float64(4) || !strings.Contains(message, "已导入 4 道题目") {
		t.Fatalf("导入计数不符（空行应跳过）: %s", rec.Body.String())
	}
	if data["queued"] != float64(3) {
		t.Fatalf("无解析的 3 题应入队（有解析的 1 题不入队）: %v", data)
	}
	// 全量替换级联：旧答题记录/错题/收藏/题目均删
	for _, stmt := range []string{"DELETE FROM quiz_record WHERE bank_id", "DELETE FROM quiz_wrong WHERE bank_id", "DELETE FROM quiz_favorite WHERE bank_id", "DELETE FROM quiz_question"} {
		if env.fake.execCount(stmt) != 1 {
			t.Fatalf("应级联删除: %s", stmt)
		}
	}
	e, ok := env.fake.lastExec("INSERT INTO quiz_question")
	if !ok || len(e.args) != 32 {
		t.Fatalf("插入参数应 32 个（4 行 × 8 列）: %+v", e)
	}
	row := func(j int) map[string]string {
		return map[string]string{
			"type":     fmt.Sprint(e.args[8*j+1]),
			"content":  fmt.Sprint(e.args[8*j+2]),
			"options":  fmt.Sprint(e.args[8*j+3]),
			"answer":   fmt.Sprint(e.args[8*j+4]),
			"analysis": fmt.Sprint(e.args[8*j+5]),
			"status":   fmt.Sprint(e.args[8*j+6]),
			"sort":     fmt.Sprint(e.args[8*j+7]),
		}
	}
	r0, r1, r2, r3 := row(0), row(1), row(2), row(3)
	if r0["type"] != "single" || r0["answer"] != "A" || r0["status"] != "done" || r0["analysis"] != "手填解析" {
		t.Fatalf("题一（别名 single + 小写答案 + 手填解析）不符: %v", r0)
	}
	// 多选 "c a b" → 字母序去重 ABC
	if r1["type"] != "multiple" || r1["answer"] != "ABC" || r1["status"] != "none" || r1["analysis"] != "<nil>" {
		t.Fatalf("题二（Multiple 别名 + 答案归一 ABC + 无解析置 none）不符: %v", r1)
	}
	// 判断题固定两项，对 → A
	if r2["type"] != "judge" || r2["answer"] != "A" || r2["options"] != `["正确","错误"]` {
		t.Fatalf("题三（判断别名 + 固定选项）不符: %v", r2)
	}
	// 选项 B 列为空 → 按序取非空列 [甲,丙]，答案 B 指第二项
	if r3["options"] != `["甲","丙"]` || r3["answer"] != "B" {
		t.Fatalf("题四（选项按序取非空列）不符: %v", r3)
	}
	for i, r := range []map[string]string{r0, r1, r2, r3} {
		if r["sort"] != fmt.Sprint(i+1) {
			t.Fatalf("sort 应按行序: %v", r)
		}
	}
}

// TestImportExcelRowErrors 行校验回归：逐行错误文案、有错整体失败不入库、错误上限 20、
// 非 xlsx / 无文件 / 仅表头口径。
func TestImportExcelRowErrors(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})

	rows := [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
		{"多选题", "多选单字母", "甲", "乙", "", "", "", "", "A", ""},  // 多选 <2 字母
		{"单选题", "单选双字母", "甲", "乙", "", "", "", "", "AB", ""}, // 单选 ≠1 字母
		{"判断题", "判断非法", "", "", "", "", "", "", "X", ""},     // 判断答案非法
		{"单选题", "超范围", "甲", "乙", "", "", "", "", "E", ""},    // 答案超出选项范围
		{"单选题", "少于两选项", "甲", "", "", "", "", "", "A", ""},   // 选项 <2
	}
	rec := env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", importXLSX(t, rows))
	code, message, data := decodeEnvelope(t, rec)
	if code != codeRows {
		t.Fatalf("应 40031: %s", rec.Body.String())
	}
	errs, _ := data["errors"].([]any)
	if len(errs) != 5 {
		t.Fatalf("应 5 条行错误: %v", data)
	}
	wantMsg := []string{
		"多选题答案应至少 2 个字母",
		"单选题答案应为 1 个字母",
		"判断题答案应为 对/错（或 A/B）",
		"答案超出选项范围",
		"选项至少填写 2 个",
	}
	for i, w := range wantMsg {
		row := errs[i].(map[string]any)
		if !strings.Contains(fmt.Sprint(row["message"]), w) {
			t.Fatalf("行错误文案不符（%d）: %v", i, row)
		}
	}
	// 有错整体失败不入库
	if env.fake.execCount("DELETE FROM quiz_question") != 0 || env.fake.execCount("INSERT INTO quiz_question") != 0 {
		t.Fatalf("有错应整体失败不入库")
	}

	// 错误上限 20 条
	bad := [][]string{{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"}}
	for i := 0; i < 25; i++ {
		bad = append(bad, []string{"简答题", "非法", "甲", "乙", "", "", "", "", "A", ""})
	}
	rec = env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", importXLSX(t, bad))
	code, message, data = decodeEnvelope(t, rec)
	if code != codeRows || !strings.Contains(message, "共 20 行校验未通过") {
		t.Fatalf("错误上限口径不符: %s", rec.Body.String())
	}
	if errs, _ = data["errors"].([]any); len(errs) != 20 {
		t.Fatalf("行错误最多 20 条: %d", len(errs))
	}

	// 非 xlsx 拒绝
	rec = env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xls", []byte("x"))
	if _, message, _ = decodeEnvelope(t, rec); !strings.Contains(message, "仅支持 .xlsx 文件") {
		t.Fatalf("非 xlsx 口径不符: %s", rec.Body.String())
	}
	// 仅表头 → 无有效题目行
	rec = env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", importXLSX(t, [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析"},
	}))
	if _, message, _ = decodeEnvelope(t, rec); !strings.Contains(message, "未识别到有效题目行") {
		t.Fatalf("仅表头口径不符: %s", rec.Body.String())
	}
}

// TestImportRowContentBeyondJ 第 11 列起有内容的行不判空行（对齐 Node 整行判空口径）：
// 按题型无法识别出行错误，而非静默跳过。
func TestImportRowContentBeyondJ(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	rows := [][]string{
		{"题型", "题干", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "答案", "解析", "备注"},
		{"", "", "", "", "", "", "", "", "", "", "只有第 11 列有内容"},
	}
	rec := env.doImport(t, "/api/v1/quiz/banks/3/import", "q.xlsx", importXLSX(t, rows))
	code, _, data := decodeEnvelope(t, rec)
	if code != codeRows {
		t.Fatalf("第 11 列起有内容的行应按行错误处理: %s", rec.Body.String())
	}
	errs, _ := data["errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("应 1 条行错误: %v", data)
	}
	row, _ := errs[0].(map[string]any)
	if row["row"] != float64(2) || !strings.Contains(fmt.Sprint(row["message"]), "题型无法识别") {
		t.Fatalf("行错误不符: %v", row)
	}
	if env.fake.execCount("INSERT INTO quiz_question") != 0 {
		t.Fatalf("有错应整体失败不入库")
	}
}

// TestAnalyzerQueueRegression 队列既有行为回归：题被删除静默跳过、空解析归 failed、
// 单题失败不中断 worker、处理完可再入队、EnqueueBank 状态过滤与缺省 none。
func TestAnalyzerQueueRegression(t *testing.T) {
	db, fake := openFake(t)
	m := New(db)
	m.SetAnalyzer(seqAnalyzer{configured: true})
	queue := m.Queue()

	// 题被删除（如导入全量替换后）→ 静默跳过，不置 pending/failed
	if n := queue.Enqueue([]int64{500}); n != 1 {
		t.Fatalf("应入队 1: %d", n)
	}
	waitFor(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.inSet) == 0
	})
	if fake.execCount("analysis_status") != 0 {
		t.Fatalf("题不存在应静默跳过: %+v", fake.execs)
	}

	// 空解析归 failed
	m2 := New(db)
	m2.SetAnalyzer(emptyAnalyzer{configured: true})
	fake.on("SELECT type, content, options, answer FROM quiz_question", []string{"type", "content", "options", "answer"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A"}})
	m2.Queue().Enqueue([]int64{501})
	waitFor(t, func() bool { return fake.execCount("analysis_status = 'failed'") >= 1 })

	// 单题失败不中断 worker：1 号失败、2 号成功（且 pending 先于 done/failed）
	beforeDone := fake.execCount("analysis_status = 'done'")
	beforeFailed := fake.execCount("analysis_status = 'failed'")
	fake.on("SELECT type, content, options, answer FROM quiz_question", []string{"type", "content", "options", "answer"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A"}})
	queue.Enqueue([]int64{1, 2})
	waitFor(t, func() bool {
		return fake.execCount("analysis_status = 'failed'") > beforeFailed &&
			fake.execCount("analysis_status = 'done'") > beforeDone
	})

	// 处理完释放去重锁：同一 id 可再入队（重试语义）
	waitFor(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.inSet) == 0
	})
	fake.on("SELECT type, content, options, answer FROM quiz_question", []string{"type", "content", "options", "answer"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A"}})
	if n := queue.Enqueue([]int64{2}); n != 1 {
		t.Fatalf("处理完应可再入队: %d", n)
	}
	waitFor(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.inSet) == 0
	})

	// EnqueueBank：status=1 且按状态过滤；缺省状态 none
	fake.on("FROM quiz_question WHERE bank_id = ? AND status = 1", []string{"id"},
		[][]driver.Value{{int64(7)}, {int64(8)}})
	if n := queue.EnqueueBank(context.Background(), 3, []string{"none", "failed"}); n != 2 {
		t.Fatalf("EnqueueBank 应入 2: %d", n)
	}
	waitFor(t, func() bool {
		queue.mu.Lock()
		defer queue.mu.Unlock()
		return len(queue.inSet) == 0
	})
	if n := queue.EnqueueBank(context.Background(), 3, nil); n != 2 {
		t.Fatalf("缺省状态应 none 口径入队: %d", n)
	}
}

// TestAnalyzeRetryEndpoint analyze-retry 接口回归：none/failed 全部重入队；未配置时 queued=0。
func TestAnalyzeRetryEndpoint(t *testing.T) {
	env := newTestServer(t, identAdmin)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ?", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env.fake.on("FROM quiz_question WHERE bank_id = ? AND status = 1", []string{"id"},
		[][]driver.Value{{int64(11)}, {int64(12)}, {int64(13)}})

	// 未配置 → 静默跳过 queued=0
	rec := env.do(t, http.MethodPost, "/api/v1/quiz/banks/3/analyze-retry", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 || data["queued"] != float64(0) {
		t.Fatalf("未配置应 queued=0: %s", rec.Body.String())
	}

	// 已配置 → queued=3
	env.analyzer.configured = true
	rec = env.do(t, http.MethodPost, "/api/v1/quiz/banks/3/analyze-retry", nil)
	code, _, data = decodeEnvelope(t, rec)
	if code != 0 || data["queued"] != float64(3) {
		t.Fatalf("应 queued=3: %s", rec.Body.String())
	}
}

// TestDifyAnalyzerHotConfig Dify 解析客户端热配置：Cfg 每次解析读运行时配置、
// 入参/出参口径、未配置文案对齐 Node（httptest，不实连）。
func TestDifyAnalyzerHotConfig(t *testing.T) {
	type hit struct {
		auth   string
		path   string
		mode   string
		user   string
		inputs map[string]string
	}
	var got hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs map[string]string `json:"inputs"`
			Mode   string            `json:"response_mode"`
			User   string            `json:"user"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = hit{auth: r.Header.Get("Authorization"), path: r.URL.Path, mode: body.Mode, user: body.User, inputs: body.Inputs}
		_, _ = w.Write([]byte(`{"data":{"outputs":{"analysis":"热配置解析"}}}`))
	}))
	defer srv.Close()

	cfg := fakeConfig{"DIFY_API_URL": srv.URL, "DIFY_QUIZ_API_KEY": "qk-1"}
	d := &DifyAnalyzer{Cfg: cfg, HTTP: srv.Client()}
	if !d.Configured() {
		t.Fatalf("热配置应视为已配置")
	}
	text, err := d.Analyze(context.Background(), QuestionForAI{
		ID: 7, Type: "judge", Content: "题干", Options: []string{"正确", "错误"}, Answer: "B",
	})
	if err != nil || text != "热配置解析" {
		t.Fatalf("解析产物不符: %q err=%v", text, err)
	}
	if got.auth != "Bearer qk-1" || got.path != "/v1/workflows/run" || got.mode != "blocking" || got.user != "quiz-q7" {
		t.Fatalf("请求口径不符: %+v", got)
	}
	if got.inputs["type"] != "判断题" || got.inputs["stem"] != "题干" ||
		got.inputs["options"] != "A. 正确\nB. 错误" || got.inputs["answer"] != "B（错误）" {
		t.Fatalf("工作流入参不符: %+v", got.inputs)
	}

	// 热配置：换密钥即时生效
	cfg["DIFY_QUIZ_API_KEY"] = "qk-2"
	if _, err := d.Analyze(context.Background(), QuestionForAI{ID: 1, Type: "single", Content: "c", Options: []string{"甲", "乙"}, Answer: "A"}); err != nil {
		t.Fatalf("二次解析应成功: %v", err)
	}
	if got.auth != "Bearer qk-2" {
		t.Fatalf("热配置应即时生效: %+v", got)
	}

	// 未配置文案对齐 Node createEnsureConfigured
	_, err = (&DifyAnalyzer{Cfg: fakeConfig{}}).Analyze(context.Background(), QuestionForAI{})
	if err == nil || err.Error() != "Dify 未配置（DIFY_API_URL / DIFY_QUIZ_API_KEY）" {
		t.Fatalf("未配置文案不符: %v", err)
	}
	if (&DifyAnalyzer{Cfg: fakeConfig{}}).Configured() {
		t.Fatalf("空配置应未配置")
	}
}

// TestQueueFailureLogged 解析失败应用日志回归（结构化日志桩）。
func TestQueueFailureLogged(t *testing.T) {
	db, fake := openFake(t)
	logger := &fakeLogger{}
	m := New(db)
	m.Log = logger
	m.SetAnalyzer(emptyAnalyzer{configured: true})
	fake.on("SELECT type, content, options, answer FROM quiz_question", []string{"type", "content", "options", "answer"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A"}})
	m.Queue().Enqueue([]int64{901})
	waitFor(t, func() bool {
		logger.mu.Lock()
		defer logger.mu.Unlock()
		for _, e := range logger.events {
			if strings.HasPrefix(e, "quiz.analysis.failed") {
				return true
			}
		}
		return false
	})
}
