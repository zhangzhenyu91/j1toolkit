package wechatanswer

// 监听群/规则/密钥的跨班身份边界：
// 班管只能读写本班（team_id 与身份一致）的群与规则；他班读/写一律 403 40304；
// 建群/改群不能指定他班；超管保留任意班组能力。
//
// 说明：生产 httpapi.isAdminPath 已把 /api/v1/wechat-answer/* 限定为超管，班管在到达模块前
// 即被中间件 403（见 httpapi/auth_test.go）。本文件经 doModule 的测试专用前缀绕开该外层门控，
// 验证模块自身也具备班组收敛（纵深防御）；生产门控未改变。

import (
	"net/http"
	"testing"

	"shade/internal/auth"
)

// otherTeamEnv 班管身份（team_id=8），假库中的群/规则归属 team_id=7 → 全部跨班。
func otherTeamEnv(t *testing.T) *httpEnv {
	t.Helper()
	return newHTTPEnvWith(t, auth.Identity{ID: 2, Username: "li", Role: "team_admin", TeamID: 8})
}

// ownTeamEnv 班管身份（team_id=7），与假库群/规则归属一致。
func ownTeamEnv(t *testing.T) *httpEnv {
	t.Helper()
	return newHTTPEnvWith(t, auth.Identity{ID: 2, Username: "li", Role: "team_admin", TeamID: 7})
}

// assertCrossTeamCode 断言 403 + 业务码 40304。
func assertCrossTeamCode(t *testing.T, code int, parsed map[string]any) {
	t.Helper()
	if code != http.StatusForbidden || parsed["code"] != float64(codeNotAdmin) {
		t.Fatalf("跨班应 403 %d: HTTP %d %+v", codeNotAdmin, code, parsed)
	}
}

// TestTeamAdminOtherTeamDenied 班管读写他班群/规则/密钥与建群指定他班均 403，且不落任何写库。
func TestTeamAdminOtherTeamDenied(t *testing.T) {
	env := otherTeamEnv(t)
	defer env.db.Close()

	// 列表只含本班：假库群归属 7，班管 8 → 空列表。
	code, parsed := env.doModule(t, http.MethodGet, "/wechat-answer/groups", "")
	if code != http.StatusOK {
		t.Fatalf("列表 HTTP %d: %+v", code, parsed)
	}
	if items, _ := dataOf(t, parsed)["items"].([]any); len(items) != 0 {
		t.Fatalf("班管列表不应含他班群: %+v", items)
	}

	// 读他班：群规则列表 / 规则详情。
	for _, path := range []string{
		"/wechat-answer/groups/1/rules",
		"/wechat-answer/rules/10",
	} {
		code, parsed = env.doModule(t, http.MethodGet, path, "")
		assertCrossTeamCode(t, code, parsed)
	}

	// 写他班群：改名 / 删除 / 规则 upsert。
	code, parsed = env.doModule(t, http.MethodPut, "/wechat-answer/groups/1", `{"name":"改名"}`)
	assertCrossTeamCode(t, code, parsed)
	code, parsed = env.doModule(t, http.MethodDelete, "/wechat-answer/groups/1", "")
	assertCrossTeamCode(t, code, parsed)
	code, parsed = env.doModule(t, http.MethodPost, "/wechat-answer/groups/1/rules",
		`{"type":"location","name":"位置消息","keys":{"location":"loc-key"}}`)
	assertCrossTeamCode(t, code, parsed)

	// 写他班规则：更新（含密钥）/ 删除。
	code, parsed = env.doModule(t, http.MethodPut, "/wechat-answer/rules/10",
		`{"name":"改名","keys":{"tree_extract":"new-key"}}`)
	assertCrossTeamCode(t, code, parsed)
	code, parsed = env.doModule(t, http.MethodDelete, "/wechat-answer/rules/10", "")
	assertCrossTeamCode(t, code, parsed)

	// 建群指定他班。
	code, parsed = env.doModule(t, http.MethodPost, "/wechat-answer/groups",
		`{"name":"新群","group_id":"new@chatroom","team_id":7}`)
	assertCrossTeamCode(t, code, parsed)

	// 越权请求不得落库。
	for _, substr := range []string{"UPDATE wa_group SET", "DELETE FROM wa_group", "INSERT INTO wa_group",
		"UPDATE wa_rule SET", "DELETE FROM wa_rule", "INSERT INTO wa_rule"} {
		if n := env.fdb.execCount(substr); n != 0 {
			t.Fatalf("越权请求不应执行 %q（%d 次）", substr, n)
		}
	}
}

// TestTeamAdminCreateGroupDefaultsOwnTeam 班管建群未指定 team_id 时归属本班。
func TestTeamAdminCreateGroupDefaultsOwnTeam(t *testing.T) {
	env := otherTeamEnv(t)
	defer env.db.Close()

	code, parsed := env.doModule(t, http.MethodPost, "/wechat-answer/groups",
		`{"name":"新群","group_id":"new@chatroom"}`)
	if code != http.StatusOK {
		t.Fatalf("班管建群 HTTP %d: %+v", code, parsed)
	}
	exec, ok := env.fdb.lastExec("INSERT INTO wa_group")
	if !ok {
		t.Fatal("建群 SQL 未执行")
	}
	found := false
	for _, arg := range exec.args {
		if v, ok := arg.(int64); ok && v == 8 {
			found = true
		}
	}
	if !found {
		t.Fatalf("建群应归属本班 8: %+v", exec.args)
	}
}

// TestTeamAdminOwnTeamAllowed 班管本班群/规则读写（含密钥更新）保持可用。
func TestTeamAdminOwnTeamAllowed(t *testing.T) {
	env := ownTeamEnv(t)
	defer env.db.Close()

	code, parsed := env.doModule(t, http.MethodGet, "/wechat-answer/groups", "")
	if code != http.StatusOK {
		t.Fatalf("列表 HTTP %d: %+v", code, parsed)
	}
	if items, _ := dataOf(t, parsed)["items"].([]any); len(items) != 1 {
		t.Fatalf("班管应看到本班群: %+v", items)
	}
	code, parsed = env.doModule(t, http.MethodGet, "/wechat-answer/groups/1/rules", "")
	if code != http.StatusOK {
		t.Fatalf("群规则 HTTP %d: %+v", code, parsed)
	}
	code, parsed = env.doModule(t, http.MethodGet, "/wechat-answer/rules/10", "")
	if code != http.StatusOK {
		t.Fatalf("规则详情 HTTP %d: %+v", code, parsed)
	}
	code, parsed = env.doModule(t, http.MethodPut, "/wechat-answer/groups/1", `{"name":"吊车群2"}`)
	if code != http.StatusOK {
		t.Fatalf("改群 HTTP %d: %+v", code, parsed)
	}
	code, parsed = env.doModule(t, http.MethodPut, "/wechat-answer/rules/10",
		`{"name":"输电班组运维","keys":{"tree_extract":"own-new-key"}}`)
	if code != http.StatusOK {
		t.Fatalf("改规则 HTTP %d: %+v", code, parsed)
	}
	code, parsed = env.doModule(t, http.MethodDelete, "/wechat-answer/rules/11", "")
	if code != http.StatusOK {
		t.Fatalf("删规则 HTTP %d: %+v", code, parsed)
	}
	code, parsed = env.doModule(t, http.MethodDelete, "/wechat-answer/groups/1", "")
	if code != http.StatusOK {
		t.Fatalf("删群 HTTP %d: %+v", code, parsed)
	}
}

// TestAdminCrossTeamGroupAllowed 超管保留跨班管理能力。
func TestAdminCrossTeamGroupAllowed(t *testing.T) {
	env := newHTTPEnv(t)
	defer env.db.Close()
	code, parsed := env.doModule(t, http.MethodPut, "/wechat-answer/groups/1", `{"name":"超管改名"}`)
	if code != http.StatusOK {
		t.Fatalf("超管跨班改群 HTTP %d: %+v", code, parsed)
	}
	exec, ok := env.fdb.lastExec("UPDATE wa_group SET")
	if !ok || !execContainsArg(exec, "超管改名") {
		t.Fatalf("超管改群未落库: %+v", exec)
	}
}
