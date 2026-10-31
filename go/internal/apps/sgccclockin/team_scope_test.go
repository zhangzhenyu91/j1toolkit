package sgccclockin

// 商旅代绑班组边界：管理员代绑必须落在生效班组内——他班 member_id 一律 403（不写绑定行），
// 超管可先指定 team_id 再代绑该班成员（既有能力保留）；代绑由服务端代登录换 token（前端不再传 token）。

import (
	"database/sql/driver"
	"net/http"
	"testing"
)

// onTeams 按 id 应答 sys_team 查询（TeamByID 口径）。
func onTeams(fdb *fakeDB, teams map[int64]string) {
	fdb.onRowsFunc("FROM sys_team", func(args []driver.Value) ([]string, [][]driver.Value) {
		id, _ := args[0].(int64)
		if name, ok := teams[id]; ok {
			return []string{"id", "name"}, [][]driver.Value{{id, name}}
		}
		return []string{"id", "name"}, nil
	})
}

// TestAccountsBindRejectsOtherTeamMember 班管代绑他班成员必须 403 且不落库。
func TestAccountsBindRejectsOtherTeamMember(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := &bindAPI{fakeAPI: &fakeAPI{token: "srv-token"}}
	m.API = api
	h := newTestRouter(m, testIdentity()) // team_admin，team_id=1
	onTeams(fdb, map[int64]string{1: "检修一班", 2: "检修二班"})
	fdb.onRowsAlways("FROM worklog_member WHERE id", []string{"team_id"}, []driver.Value{int64(2)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"member_id":99,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if rec.Code != http.StatusForbidden || env["code"] != float64(codeForbidden) {
		t.Fatalf("他班成员代绑应 403 %d: HTTP %d %v", codeForbidden, rec.Code, env)
	}
	if n := len(fdb.callsWith("INSERT INTO worklog_sgcc_account")); n != 0 {
		t.Fatalf("越权代绑不应写库（%d 次）", n)
	}
	if sms, _, _ := api.calls(); sms != 0 {
		t.Fatalf("他班成员不应触发平台登录（%d）", sms)
	}
}

// TestAccountsBindOwnTeamMember 本班成员代绑保持可用（服务端换 token 落库）。
func TestAccountsBindOwnTeamMember(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{token: "srv-token"}
	h := newTestRouter(m, testIdentity())
	onTeams(fdb, map[int64]string{1: "检修一班"})
	fdb.onRowsAlways("FROM worklog_member WHERE id", []string{"team_id"}, []driver.Value{int64(1)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"member_id":99,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if rec.Code != http.StatusOK || env["code"] != float64(0) {
		t.Fatalf("本班成员代绑应成功: HTTP %d %v", rec.Code, env)
	}
	execs := fdb.callsWith("INSERT INTO worklog_sgcc_account")
	if len(execs) != 1 {
		t.Fatalf("本班代绑应写库一次（%d）", len(execs))
	}
	if args := execs[0].args; len(args) < 4 || args[3] != "srv-token" {
		t.Fatalf("代绑行应写服务端 token: %#v", args)
	}
}

// TestAccountsBindAdminSelectTeam 超管指定班组后仍可代绑该班成员（能力保留）。
func TestAccountsBindAdminSelectTeam(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{token: "srv-token"}
	h := newTestRouter(m, adminIdentity()) // admin，team_id=1
	onTeams(fdb, map[int64]string{1: "检修一班", 2: "检修二班"})
	fdb.onRowsAlways("FROM worklog_member WHERE id", []string{"team_id"}, []driver.Value{int64(2)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"team_id":2,"member_id":99,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if rec.Code != http.StatusOK || env["code"] != float64(0) {
		t.Fatalf("超管指定班组代绑应成功: HTTP %d %v", rec.Code, env)
	}
	execs := fdb.callsWith("INSERT INTO worklog_sgcc_account")
	if len(execs) != 1 {
		t.Fatalf("超管代绑应写库一次（%d）", len(execs))
	}
	found := false
	for _, arg := range execs[0].args {
		if v, ok := arg.(int64); ok && v == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("代绑行应写入班组 2: %+v", execs[0].args)
	}
}

// TestAccountsBindAdminWrongTeamDenied 超管不指定班组时同样不能代绑他班成员（须先选定班组）。
func TestAccountsBindAdminWrongTeamDenied(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{token: "srv-token"}
	h := newTestRouter(m, adminIdentity())
	onTeams(fdb, map[int64]string{1: "检修一班", 2: "检修二班"})
	fdb.onRowsAlways("FROM worklog_member WHERE id", []string{"team_id"}, []driver.Value{int64(2)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"member_id":99,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if rec.Code != http.StatusForbidden || env["code"] != float64(codeForbidden) {
		t.Fatalf("超管未选班组代绑他班成员应 403: HTTP %d %v", rec.Code, env)
	}
	if n := len(fdb.callsWith("INSERT INTO worklog_sgcc_account")); n != 0 {
		t.Fatalf("越权代绑不应写库（%d 次）", n)
	}
}

// TestDayGetRejectsOtherTeamMember 当日简报（/day）指定他班 member_id 必须 403，
// 不得返回他班打卡/费用摘要（本班与当日卡上用车人仍可见，见 requireMemberAccount 同口径）。
func TestDayGetRejectsOtherTeamMember(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity()) // team_admin，team_id=1
	onTeams(fdb, map[int64]string{1: "检修一班"})
	fdb.onRowsAlways("FROM worklog_member WHERE team_id", []string{"id"}) // 本班成员为空
	fdb.onRowsAlways("COUNT(*) FROM worklog_member", []string{"v"}, []driver.Value{int64(0)})

	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/day?date=2026-10-28&member_id=99", "")
	env := decodeEnvelope(t, rec)
	if rec.Code != http.StatusForbidden || env["code"] != float64(codeForbidden) {
		t.Fatalf("他班成员当日简报应 403 %d: HTTP %d %v", codeForbidden, rec.Code, env)
	}
	if _, has := env["data"]; has {
		t.Fatalf("越权请求不应返回 data: %v", env)
	}
}
