// HTTP CRUD 契约测试：/wechat-answer 路由（items 形状、keys_configured 脱敏、
// 空密钥保持已存值、status 不泄密、/status/reconnect 热重连）。
package wechatanswer

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

type fakeAuthenticator struct{ ident auth.Identity }

func (a fakeAuthenticator) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}

func (a fakeAuthenticator) Authenticate(context.Context, string) (auth.Identity, error) {
	return a.ident, nil
}

func (a fakeAuthenticator) Logout(context.Context, string, time.Time) error { return nil }

var testDT = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

// httpEnv HTTP 测试装配。
type httpEnv struct {
	m   *Module
	fdb *fakeDB
	db  interface{ Close() error }
	h   http.Handler
	// moduleH 模块级测试入口：生产 isAdminPath 已把 /api/v1/wechat-answer/* 限定为超管，
	// 班管在到达模块前即被拒；本入口以 /api/v1/test-wechat 前缀绕开该门控，仅用于验证
	// 模块自身的班组收敛（AuthMiddleware 仍真实注入身份，不改变生产权限）。
	moduleH http.Handler
}

func newHTTPEnv(t *testing.T) *httpEnv {
	return newHTTPEnvWith(t, auth.Identity{ID: 1, Role: "admin"})
}

// newHTTPEnvWith 指定身份的 HTTP 测试装配（班组越权用例注入班管/普通用户身份）。
func newHTTPEnvWith(t *testing.T, ident auth.Identity) *httpEnv {
	t.Helper()
	db, fdb := openFake(t)
	// 群列表/详情
	fdb.on("FROM wa_group ORDER BY", []string{"id", "group_id", "group_name", "enabled", "team_id", "last_message_at"},
		[][]driver.Value{{int64(1), "123@chatroom", "吊车群", int64(1), int64(7), int64(1761520000)}})
	fdb.on("FROM wa_group WHERE id", []string{"id", "group_id", "group_name", "enabled", "team_id", "last_message_at"},
		[][]driver.Value{{int64(1), "123@chatroom", "吊车群", int64(1), int64(7), int64(1761520000)}})
	// 规则（treeWatch 全键 + location 各一）
	fdb.on("FROM wa_rule r JOIN", []string{
		"id", "group_id", "group_name", "trigger_type", "name", "enabled", "ask_enabled",
		"extract_key", "correct_key", "crossing_key", "crossing_correct_key",
		"ask_name_key", "chat_digest_key", "location_key", "team_id", "team_name", "created_at", "updated_at",
	}, [][]driver.Value{
		{int64(10), "123@chatroom", "吊车群", "treeWatch", "输电班组运维", int64(1), int64(1),
			"secret-extract", "secret-correct", "", "", "secret-ask", "", "", int64(7), "检修一班", testDT, testDT},
		{int64(11), "123@chatroom", "吊车群", "location", "位置消息", int64(0), int64(0),
			"", "", "", "", "", "", "secret-loc", int64(7), "检修一班", testDT, testDT},
	})
	// 班组名
	fdb.on("FROM sys_team", []string{"name"}, [][]driver.Value{{"检修一班"}})
	m := New(db, fakeCfg{values: map[string]string{
		"WECHATPADPRO_TOKEN": "secret-token-xyz",
		"DIFY_API_URL":       "https://dify.internal.example",
	}})
	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		m.RegisterRoutes(api)
	})
	h := httpapi.AuthMiddleware(r, fakeAuthenticator{ident: ident})

	moduleRouter := chi.NewRouter()
	m.RegisterRoutes(moduleRouter)
	mr := chi.NewRouter()
	mr.Route("/api/v1/test-wechat", func(sub chi.Router) {
		sub.Handle("/*", http.StripPrefix("/api/v1/test-wechat", moduleRouter))
	})
	moduleH := httpapi.AuthMiddleware(mr, fakeAuthenticator{ident: ident})
	return &httpEnv{m: m, fdb: fdb, db: db, h: h, moduleH: moduleH}
}

// doModule 走模块级测试入口（/api/v1/test-wechat 前缀），path 以 /wechat-answer 开头。
func (e *httpEnv) doModule(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/test-wechat"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.moduleH.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是合法 JSON（HTTP %d）: %s", rec.Code, rec.Body.String())
	}
	return rec.Code, parsed
}

func (e *httpEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是合法 JSON（HTTP %d）: %s", rec.Code, rec.Body.String())
	}
	return rec.Code, parsed
}

func dataOf(t *testing.T, parsed map[string]any) map[string]any {
	t.Helper()
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		t.Fatalf("缺少 data: %+v", parsed)
	}
	return data
}

func TestHTTPGroupListItemsContract(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	code, parsed := env.do(t, http.MethodGet, "/api/v1/wechat-answer/groups", "")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	data := dataOf(t, parsed)
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items 应有 1 项: %+v", data)
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"id", "group_id", "name", "enabled", "team_id", "rule_count", "enabled_rule_count"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("群项缺少契约键 %s: %+v", key, item)
		}
	}
	if item["name"] != "吊车群" || item["team_id"].(float64) != 7 ||
		item["rule_count"].(float64) != 2 || item["enabled_rule_count"].(float64) != 1 {
		t.Fatalf("群项取值不符: %+v", item)
	}
	// 兼容旧键
	if _, ok := data["groups"]; !ok {
		t.Fatal("应保留 groups 兼容键")
	}
	if item["team_name"] != "检修一班" || item["group_name"] != "吊车群" {
		t.Fatalf("兼容键不符: %+v", item)
	}
	// 规则内嵌视图脱敏
	raw, _ := json.Marshal(item)
	for _, secret := range []string{"secret-extract", "secret-correct", "secret-ask", "secret-loc", "secret-token-xyz"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("列表泄密 %s: %s", secret, raw)
		}
	}
}

func TestHTTPRuleListItemsAndKeysConfigured(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	code, parsed := env.do(t, http.MethodGet, "/api/v1/wechat-answer/groups/1/rules", "")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	data := dataOf(t, parsed)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items 应有 2 项: %+v", data)
	}
	if _, ok := data["rules"]; !ok {
		t.Fatal("应保留 rules 兼容键")
	}
	rule := items[0].(map[string]any)
	if rule["type"] != "treeWatch" || rule["ask_enabled"] != true {
		t.Fatalf("规则视图不符: %+v", rule)
	}
	kc := rule["keys_configured"].(map[string]any)
	if kc["tree_extract"] != true || kc["tree_correct"] != true || kc["ask_name"] != true ||
		kc["crossing_extract"] != false || kc["chat_digest"] != false {
		t.Fatalf("keys_configured 不符: %+v", kc)
	}
	if rule["complete"] != true {
		t.Fatalf("密钥齐全应 complete: %+v", rule)
	}
	raw, _ := json.Marshal(data)
	for _, secret := range []string{"secret-extract", "secret-correct", "secret-ask", "secret-loc"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("规则列表泄密 %s: %s", secret, raw)
		}
	}
}

func TestHTTPRuleUpsertKeepsExistingKeys(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	// 空密钥提交 → 保持已存值（不丢已有键）
	code, parsed := env.do(t, http.MethodPut, "/api/v1/wechat-answer/rules/10",
		`{"name":"输电班组运维","enabled":false,"ask_enabled":true,"keys":{"tree_correct":""}}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	exec, ok := env.fdb.lastExec("UPDATE wa_rule SET")
	if !ok {
		t.Fatal("规则更新 SQL 未执行")
	}
	if !execContainsArg(exec, "secret-extract") || !execContainsArg(exec, "secret-ask") {
		t.Fatalf("空密钥提交应保持已存值: %+v", exec.args)
	}
	if execContainsArg(exec, "secret-correct") {
		// 保持旧值仍然是 secret-correct（这里应存在——空值=不覆盖）
	} else {
		t.Fatalf("tree_correct 应保持 secret-correct: %+v", exec.args)
	}
	// 新密钥覆盖
	code, parsed = env.do(t, http.MethodPut, "/api/v1/wechat-answer/rules/10",
		`{"name":"输电班组运维","enabled":true,"ask_enabled":true,"keys":{"crossing_extract":"new-cross"}}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	exec, _ = env.fdb.lastExec("UPDATE wa_rule SET")
	if !execContainsArg(exec, "new-cross") {
		t.Fatalf("新密钥未写入: %+v", exec.args)
	}
}

func TestHTTPRuleCreateWithLegacyFlatKeys(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	code, parsed := env.do(t, http.MethodPost, "/api/v1/wechat-answer/groups/1/rules",
		`{"type":"location","name":"位置消息","enabled":true,"keys":{"location":"loc-key"}}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	rule := dataOf(t, parsed)["rule"].(map[string]any)
	kc := rule["keys_configured"].(map[string]any)
	if kc["location"] != true {
		t.Fatalf("location Key 未登记: %+v", kc)
	}
}

func TestHTTPStatusNoSecretsAndReconnect(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	code, parsed := env.do(t, http.MethodGet, "/api/v1/wechat-answer/status", "")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	status := dataOf(t, parsed)["status"].(map[string]any)
	if status["token_configured"] != true || status["configured"] != true {
		t.Fatalf("状态不符: %+v", status)
	}
	for _, key := range []string{"configured", "token_configured", "ws_connected", "auto_reconnect", "last_ready_at"} {
		if _, ok := status[key]; !ok {
			t.Fatalf("状态缺少键 %s: %+v", key, status)
		}
	}
	raw, _ := json.Marshal(parsed)
	if strings.Contains(string(raw), "secret-token-xyz") || strings.Contains(string(raw), "dify.internal.example") {
		t.Fatalf("状态泄密: %s", raw)
	}
	// 热重连：真实触发取消器 + 返回最新状态
	ctx, cancel := context.WithCancel(context.Background())
	env.m.mu.Lock()
	env.m.syncCancel = cancel
	env.m.mu.Unlock()
	code, parsed = env.do(t, http.MethodPost, "/api/v1/wechat-answer/status/reconnect", "")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %+v", code, parsed)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("reconnect 未触发热重连取消")
	}
	if _, ok := dataOf(t, parsed)["status"]; !ok {
		t.Fatalf("reconnect 应返回 status: %+v", parsed)
	}
	// 旧路径兼容
	code, _ = env.do(t, http.MethodPost, "/api/v1/wechatanswer/reconnect", "")
	if code != http.StatusOK {
		t.Fatalf("旧路径 reconnect HTTP %d", code)
	}
}

// execContainsArg 断言执行参数中含某值。
func execContainsArg(e fakeExec, want string) bool {
	for _, a := range e.args {
		if s, ok := a.(string); ok && s == want {
			return true
		}
	}
	return false
}
