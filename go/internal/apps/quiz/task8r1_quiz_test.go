package quiz

// Task 8 round 1 契约：wrong/fav 指定 ids 时必须与 rand 共用稳定 ids 过滤，
// 只返回目标集合且顺序按 ids，不再依赖 OFFSET（小程序端 alignBatch 仅作防御）。

import (
	"database/sql/driver"
	"net/http"
	"strings"
	"testing"
)

func mustLastQuery(t *testing.T, env *testEnv, substr string) fakeExec {
	t.Helper()
	q, ok := env.fake.lastQuery(substr)
	if !ok {
		t.Fatalf("未执行查询: %s", substr)
	}
	return q
}

func assertSQL(t *testing.T, q fakeExec, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(q.query, w) {
			t.Fatalf("SQL 缺少 %q:\n%s", w, q.query)
		}
	}
}

func assertNoSQL(t *testing.T, q fakeExec, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(q.query, b) {
			t.Fatalf("SQL 不应包含 %q:\n%s", b, q.query)
		}
	}
}

func listIDs(t *testing.T, data map[string]any) []int64 {
	t.Helper()
	list, ok := data["list"].([]any)
	if !ok {
		t.Fatalf("list 类型不符: %#v", data["list"])
	}
	ids := make([]int64, 0, len(list))
	for _, it := range list {
		row, _ := it.(map[string]any)
		id, _ := row["id"].(float64)
		ids = append(ids, int64(id))
	}
	return ids
}

// TestPracticeFavStableIDs：收藏专项指定 ids 时按 FIELD 顺序返回目标集合，COUNT/SELECT 共用 ids 过滤。
func TestPracticeFavStableIDs(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_favorite", []string{"total"},
		[][]driver.Value{{int64(2)}})
	env.fake.on("SELECT q.id, q.type, q.content, q.options FROM quiz_favorite",
		[]string{"id", "type", "content", "options"},
		[][]driver.Value{
			{int64(31), "single", "题干31", `["甲","乙"]`},
			{int64(33), "judge", "题干33", `["对","错"]`},
		})

	rec := env.do(t, http.MethodGet, "/api/v1/quiz/practice/questions?mode=fav&ids=31,33", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("收藏取题应成功: %s", rec.Body.String())
	}
	ids := listIDs(t, data)
	if len(ids) != 2 || ids[0] != 31 || ids[1] != 33 {
		t.Fatalf("应按 ids 顺序返回 [31 33]，实际 %v", ids)
	}

	countQ := mustLastQuery(t, env, "SELECT COUNT(*) AS total FROM quiz_favorite")
	assertSQL(t, countQ, "q.id IN (?, ?)", "f.user_id = ?", "b.scope = 'all'")
	assertNoSQL(t, countQ, "OFFSET", "LIMIT")

	selectQ := mustLastQuery(t, env, "SELECT q.id, q.type, q.content, q.options FROM quiz_favorite")
	assertSQL(t, selectQ, "q.id IN (?, ?)", "ORDER BY FIELD(q.id, 31,33)",
		"f.user_id = ?", "b.scope = 'all'")
	assertNoSQL(t, selectQ, "OFFSET", "LIMIT")
	if len(selectQ.args) != 4 || selectQ.args[2] != int64(31) || selectQ.args[3] != int64(33) {
		t.Fatalf("ids 参数应按用户/班组后追加: %#v", selectQ.args)
	}
}

// TestPracticeWrongStableIDs：错题本指定 ids 时同样按 FIELD 顺序，COUNT/SELECT 都过滤。
func TestPracticeWrongStableIDs(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_wrong", []string{"total"},
		[][]driver.Value{{int64(2)}})
	env.fake.on("SELECT q.id, q.type, q.content, q.options FROM quiz_wrong",
		[]string{"id", "type", "content", "options"},
		[][]driver.Value{
			{int64(41), "single", "错题41", `["甲","乙"]`},
			{int64(42), "single", "错题42", `["甲","乙"]`},
		})

	rec := env.do(t, http.MethodGet, "/api/v1/quiz/practice/questions?mode=wrong&ids=41,42", nil)
	code, _, data := decodeEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("错题本取题应成功: %s", rec.Body.String())
	}
	ids := listIDs(t, data)
	if len(ids) != 2 || ids[0] != 41 || ids[1] != 42 {
		t.Fatalf("应按 ids 顺序返回 [41 42]，实际 %v", ids)
	}

	countQ := mustLastQuery(t, env, "SELECT COUNT(*) AS total FROM quiz_wrong")
	assertSQL(t, countQ, "q.id IN (?, ?)", "w.user_id = ?", "b.scope = 'all'")
	assertNoSQL(t, countQ, "OFFSET", "LIMIT")

	selectQ := mustLastQuery(t, env, "SELECT q.id, q.type, q.content, q.options FROM quiz_wrong")
	assertSQL(t, selectQ, "q.id IN (?, ?)", "ORDER BY FIELD(q.id, 41,42)",
		"w.user_id = ?", "b.scope = 'all'")
	assertNoSQL(t, selectQ, "OFFSET", "LIMIT")
}

// TestPracticeFavStableIDsKeepBankFilter：指定 ids 时仍需保留题库归属过滤（跨班不可通过 ids 越权）。
func TestPracticeFavStableIDsKeepBankFilter(t *testing.T) {
	env := newTestServer(t, identUser)
	teamRules(env.fake)
	env.fake.on("FROM quiz_bank WHERE id = ? AND status = 1", []string{"id", "team_id", "scope"},
		[][]driver.Value{{int64(3), int64(1), "team"}})
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_favorite", []string{"total"},
		[][]driver.Value{{int64(1)}})
	env.fake.on("SELECT q.id, q.type, q.content, q.options FROM quiz_favorite",
		[]string{"id", "type", "content", "options"},
		[][]driver.Value{{int64(31), "single", "题干31", `["甲","乙"]`}})

	rec := env.do(t, http.MethodGet,
		"/api/v1/quiz/practice/questions?mode=fav&bankId=3&ids=31", nil)
	if code, _, _ := decodeEnvelope(t, rec); code != 0 {
		t.Fatalf("带 bankId 的收藏取题应成功: %s", rec.Body.String())
	}
	selectQ := mustLastQuery(t, env, "SELECT q.id, q.type, q.content, q.options FROM quiz_favorite")
	assertSQL(t, selectQ, "f.bank_id = ?", "q.id IN (?)", "ORDER BY FIELD(q.id, 31)",
		"b.scope = 'all'")
	assertNoSQL(t, selectQ, "OFFSET", "LIMIT")
}

// TestPracticeQuestionsWithoutIDsKeepsPaging：旧调用（大纲/无 ids）保持原 OFFSET 分页口径。
func TestPracticeQuestionsWithoutIDsKeepsPaging(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("SELECT COUNT(*) AS total FROM quiz_wrong", []string{"total"},
		[][]driver.Value{{int64(5)}})
	env.fake.on("SELECT q.id, q.type, q.content, q.options FROM quiz_wrong",
		[]string{"id", "type", "content", "options"},
		[][]driver.Value{{int64(41), "single", "错题41", `["甲","乙"]`}})

	rec := env.do(t, http.MethodGet, "/api/v1/quiz/practice/questions?mode=wrong&offset=20&limit=20", nil)
	if code, _, _ := decodeEnvelope(t, rec); code != 0 {
		t.Fatalf("无 ids 取题应成功: %s", rec.Body.String())
	}
	selectQ := mustLastQuery(t, env, "SELECT q.id, q.type, q.content, q.options FROM quiz_wrong")
	assertSQL(t, selectQ, "ORDER BY w.last_wrong_at DESC, w.id DESC LIMIT 20 OFFSET 20")
	assertNoSQL(t, selectQ, "FIELD(")
}
