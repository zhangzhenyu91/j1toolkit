// Task 5 回归：批量处理必须一次解码包含 ids 与处理字段的完整结构。
package treehazard

import (
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleRecordHandleBatchSingleDecode 同一次请求体解码出 ids + handle_note/handled_at，
// 不再先读处理字段再二次解码（第二次读到 EOF 导致所有真实批量请求 400）。
func TestHandleRecordHandleBatchSingleDecode(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})

	body := `{"ids":[5,6],"handle_note":"削枝","handled_at":"2026-10-31"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/treehazard/records/handle-batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量处理应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	envelope := decodeEnvelope(t, rec)
	if envelope.Code != codeOK {
		t.Fatalf("批量处理业务码应 0，实际 %d %s", envelope.Code, envelope.Message)
	}
	data, _ := envelope.Data.(map[string]any)
	if got := data["updated"]; got != float64(2) {
		t.Fatalf("updated=%v，期望 2", got)
	}
	if env.fake.execCount("UPDATE treehazard_record SET status = 'handled'") != 2 {
		t.Fatalf("应逐条更新 2 行，实际 %d", env.fake.execCount("UPDATE treehazard_record SET status = 'handled'"))
	}
	upd, ok := env.fake.lastExec("UPDATE treehazard_record SET status = 'handled'")
	if !ok || len(upd.args) < 3 || upd.args[0] != driver.Value("班管") ||
		upd.args[1] != driver.Value("削枝") || upd.args[2] != driver.Value("2026-10-31") {
		t.Fatalf("处理字段未进入 SQL: %v", upd.args)
	}
}
