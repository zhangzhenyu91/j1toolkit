package sgccclockin

// Task 9 代绑契约测试：管理员代绑改为「服务端代登录换 token」。
// 两个 handler 复用同一登录校验/换 token 逻辑（短信、密码×图形码/滑块）；
// 认领成员规则不同：loginBind 按昵称认领，accountsBind 按显式 memberId（兼容 member_id）。

import (
	"context"
	"database/sql/driver"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// bindAPI 记录登录通道调用（嵌入 fakeAPI 复用其余 Client 方法）。
type bindAPI struct {
	*fakeAPI
	mu         sync.Mutex
	smsCalls   int
	pwdV3Calls int
	pwdCalls   int
	lastMobile string
	lastCode   string
	lastPwd    string
	lastImg    string
	lastToken  string
	lastConst  string
}

func (b *bindAPI) record(mobile, code, pwd, img, token, constID string) {
	b.mu.Lock()
	b.lastMobile, b.lastCode, b.lastPwd = mobile, code, pwd
	b.lastImg, b.lastToken, b.lastConst = img, token, constID
	b.mu.Unlock()
}

func (b *bindAPI) calls() (sms, pwdV3, pwd int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.smsCalls, b.pwdV3Calls, b.pwdCalls
}

func (b *bindAPI) LoginBySmsSlapp(_ context.Context, mobile, checkCode, captchaToken, constID string, _ CallOpts) (string, error) {
	b.mu.Lock()
	b.smsCalls++
	b.mu.Unlock()
	b.record(mobile, checkCode, "", "", captchaToken, constID)
	return b.fakeAPI.token, b.fakeAPI.loginErr
}

func (b *bindAPI) LoginByPasswordV3(_ context.Context, mobile, password, checkImgCode string, _ CallOpts) (string, error) {
	b.mu.Lock()
	b.pwdV3Calls++
	b.mu.Unlock()
	b.record(mobile, "", password, checkImgCode, "", "")
	return b.fakeAPI.token, b.fakeAPI.loginErr
}

func (b *bindAPI) LoginByPassword(_ context.Context, mobile, password, captchaToken, constID string, _ CallOpts) (string, error) {
	b.mu.Lock()
	b.pwdCalls++
	b.mu.Unlock()
	b.record(mobile, "", password, "", captchaToken, constID)
	return b.fakeAPI.token, b.fakeAPI.loginErr
}

// newBindRig 构造代绑测试脚手架：班组固定 1（检修一班），登录通道记录调用。
func newBindRig(t *testing.T, role string) (*Module, *fakeDB, *bindAPI, http.Handler) {
	t.Helper()
	db, fdb := openFake(t)
	t.Cleanup(func() { _ = db.Close() })
	m := New(db)
	api := &bindAPI{fakeAPI: &fakeAPI{token: "srv-token"}}
	m.API = api
	ident := testIdentity()
	ident.Role = role
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	return m, fdb, api, newTestRouter(m, ident)
}

// memberTeamRows 成员归属行（team_id 单列）。
func memberTeamRows(teamID int64) ([]string, [][]driver.Value) {
	return []string{"team_id"}, [][]driver.Value{{teamID}}
}

// bindInsertArgs 取代绑 INSERT 的参数（无则 nil）。
func bindInsertArgs(fdb *fakeDB) []driver.Value {
	calls := fdb.callsWith("INSERT INTO worklog_sgcc_account")
	if len(calls) == 0 {
		return nil
	}
	return calls[len(calls)-1].args
}

// TestAccountsBindSmsServerToken 短信码代绑：服务端换 token，前端不再传 token。
func TestAccountsBindSmsServerToken(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","checkCode":"123456","captchaToken":"cap","constId":"cid","deviceType":"Xiaomi","systemVersion":"Android 16"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("短信代绑应成功（不要求前端 token）: %v body=%s", env, rec.Body.String())
	}
	if sms, _, _ := api.calls(); sms != 1 {
		t.Fatalf("应走短信换 token 通道: %d", sms)
	}
	if api.lastCode != "123456" || api.lastToken != "cap" || api.lastConst != "cid" {
		t.Fatalf("滑块/短信字段应透传: %q %q %q", api.lastCode, api.lastToken, api.lastConst)
	}
	args := bindInsertArgs(fdb)
	if len(args) < 4 || args[1] != int64(12) || args[2] != "13800138000" || args[3] != "srv-token" {
		t.Fatalf("代绑行应写成员/手机号/服务端 token: %#v", args)
	}
	if !strings.Contains(fdb.callsWith("INSERT INTO worklog_sgcc_account")[0].query, "NULL") {
		t.Fatalf("代绑行 user_id 应为 NULL")
	}
}

// TestAccountsBindPasswordGraphCode 密码 + 图形码通道。
func TestAccountsBindPasswordGraphCode(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","password":"pwd","checkImgCode":"ab12"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("密码图形码代绑应成功: %v", env)
	}
	if _, v3, _ := api.calls(); v3 != 1 {
		t.Fatalf("应走密码图形码通道: %d", v3)
	}
	if api.lastImg != "ab12" || api.lastPwd != "pwd" || api.lastMobile != "13800138000" {
		t.Fatalf("图形码/密码字段应透传: %q %q", api.lastImg, api.lastPwd)
	}
	if args := bindInsertArgs(fdb); len(args) < 4 || args[3] != "srv-token" {
		t.Fatalf("应写服务端 token: %#v", args)
	}
}

// TestAccountsBindPasswordSlider 密码 + 滑块通道。
func TestAccountsBindPasswordSlider(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","password":"pwd","captchaToken":"cap","constId":"cid"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("密码滑块代绑应成功: %v", env)
	}
	if _, _, pwd := api.calls(); pwd != 1 {
		t.Fatalf("应走密码滑块通道: %d", pwd)
	}
	if api.lastToken != "cap" || api.lastConst != "cid" || api.lastPwd != "pwd" {
		t.Fatalf("滑块字段应透传: %q %q", api.lastToken, api.lastConst)
	}
}

// TestAccountsBindSnakeMemberID 兼容 member_id，且忽略前端自带 token。
func TestAccountsBindSnakeMemberID(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"member_id":13,"mobile":"13800138000","checkCode":"123456","token":"client-token"}`)
	if env := decodeEnvelope(t, rec); env["code"] != float64(0) {
		t.Fatalf("member_id 兼容应成功: %v", env)
	}
	if sms, _, _ := api.calls(); sms != 1 {
		t.Fatalf("应走服务端登录换 token: %d", sms)
	}
	if args := bindInsertArgs(fdb); len(args) < 4 || args[1] != int64(13) || args[3] != "srv-token" {
		t.Fatalf("应写入服务端 token 而非前端 token: %#v", args)
	}
}

// TestAccountsBindOtherTeamForbidden 他班 memberId 一律 403，且不触达平台登录。
func TestAccountsBindOtherTeamForbidden(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(2)
	})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":99,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeForbidden) || rec.Code != http.StatusForbidden {
		t.Fatalf("他班成员应 403: %v status=%d", env, rec.Code)
	}
	if sms, v3, pwd := api.calls(); sms+v3+pwd != 0 {
		t.Fatalf("归属校验未过不得登录换 token: sms=%d v3=%d pwd=%d", sms, v3, pwd)
	}
	if bindInsertArgs(fdb) != nil {
		t.Fatalf("归属校验未过不得写代绑行")
	}
}

// TestAccountsBindRequiresAdmin 非管理员不得代绑。
func TestAccountsBindRequiresAdmin(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "user")
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeNotAdmin) {
		t.Fatalf("非管理员应拒绝: %v", env)
	}
	if sms, _, _ := api.calls(); sms != 0 {
		t.Fatalf("非管理员不得换 token")
	}
	if bindInsertArgs(fdb) != nil {
		t.Fatalf("非管理员不得写代绑行")
	}
}

// TestAccountsBindUpsertSqlKeepsOwnerAndDevice 代绑 upsert 的 SQL 形状：
// 不写 user_id / 不覆盖 device_type/system_version（fake DB 只校验 SQL 与参数，不模拟既有行）。
func TestAccountsBindUpsertSqlKeepsOwnerAndDevice(t *testing.T) {
	_, fdb, api, h := newBindRig(t, "team_admin")
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("已自绑成员代绑应只换 token（不拒绝）: %v", env)
	}
	if sms, _, _ := api.calls(); sms != 1 {
		t.Fatalf("已自绑成员代绑应走登录换 token: %d", sms)
	}
	calls := fdb.callsWith("INSERT INTO worklog_sgcc_account")
	if len(calls) != 1 {
		t.Fatalf("应执行一次代绑 upsert（%d）", len(calls))
	}
	if !strings.Contains(calls[0].query, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("代绑应走 upsert 以保留既有 user_id")
	}
	if strings.Contains(calls[0].query, "device_type = VALUES") {
		t.Fatalf("代绑已自绑成员不得覆盖既有设备口径")
	}
	if args := calls[0].args; len(args) < 4 || args[3] != "srv-token" {
		t.Fatalf("应写入服务端 token: %#v", args)
	}
}

// TestBindSharedCredentialRules 两个 handler 复用同一凭据校验（密码须带图形/滑块；须有验证码或密码；手机号格式）。
func TestBindSharedCredentialRules(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
		code int
	}{
		{"登录密码缺风控", "/api/v1/sgcc/login/bind", `{"mobile":"13800138000","password":"pwd"}`, codeNeedRisk},
		{"代绑密码缺风控", "/api/v1/sgcc/accounts/bind", `{"memberId":12,"mobile":"13800138000","password":"pwd"}`, codeNeedRisk},
		{"登录缺凭据", "/api/v1/sgcc/login/bind", `{"mobile":"13800138000"}`, codeNeedCred},
		{"代绑缺凭据", "/api/v1/sgcc/accounts/bind", `{"memberId":12,"mobile":"13800138000"}`, codeNeedCred},
		{"登录手机号非法", "/api/v1/sgcc/login/bind", `{"mobile":"123","checkCode":"1"}`, codeBadMobile},
		{"代绑手机号非法", "/api/v1/sgcc/accounts/bind", `{"memberId":12,"mobile":"123","checkCode":"1"}`, codeBadMobile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fdb, api, h := newBindRig(t, "team_admin")
			if strings.Contains(tc.path, "accounts/bind") {
				fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
					return memberTeamRows(1)
				})
			}
			rec := doJSON(t, h, http.MethodPost, tc.path, tc.body)
			env := decodeEnvelope(t, rec)
			if env["code"] != float64(tc.code) {
				t.Fatalf("业务码应为 %d，实际 %v", tc.code, env)
			}
			if sms, v3, pwd := api.calls(); sms+v3+pwd != 0 {
				t.Fatalf("凭据校验未过不得登录")
			}
		})
	}
}

// TestBindClaimRulesDiffer 两个 handler 认领成员规则不同：
// loginBind 按当前员工昵称认领，accountsBind 只认显式 memberId（不按昵称改绑他人）。
func TestBindClaimRulesDiffer(t *testing.T) {
	// loginBind：昵称「张振宇」命中成员 11
	_, fdb, _, h := newBindRig(t, "team_admin")
	fdb.onRowsAlways("FROM worklog_member WHERE team_id", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE user_id", []string{"id"})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id", []string{"id", "user_id"})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/bind",
		`{"mobile":"13800138000","checkCode":"123456"}`)
	if env := decodeEnvelope(t, rec); env["code"] != float64(0) {
		t.Fatalf("登录绑定应成功: %v", env)
	}
	args := bindInsertArgs(fdb)
	if len(args) < 3 || args[2] != int64(11) {
		t.Fatalf("loginBind 应按昵称认领成员 11: %#v", args)
	}
	if len(fdb.callsWith("AND name =")) == 0 {
		t.Fatalf("loginBind 应按昵称匹配成员")
	}
}

// TestAccountsBindIgnoresNickname 代绑不按昵称认领：显式 memberId 优先。
func TestAccountsBindIgnoresNickname(t *testing.T) {
	_, fdb, _, h := newBindRig(t, "team_admin")
	fdb.onRowsAlways("FROM worklog_member WHERE team_id", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","checkCode":"123456"}`)
	if env := decodeEnvelope(t, rec); env["code"] != float64(0) {
		t.Fatalf("代绑应成功: %v", env)
	}
	if args := bindInsertArgs(fdb); len(args) < 2 || args[1] != int64(12) {
		t.Fatalf("代绑应按显式 memberId 落成员 12: %#v", args)
	}
	if len(fdb.callsWith("AND name =")) != 0 {
		t.Fatalf("代绑不应按昵称认领成员")
	}
}

// TestAccountsBindAdminAuditLog 管理员代绑写审计日志：只记操作人/目标成员/结果，不记 token/密码/验证码。
func TestAccountsBindAdminAuditLog(t *testing.T) {
	m, fdb, _, h := newBindRig(t, "team_admin")
	log := &fakeAppLogger{}
	m.Log = log
	fdb.onRowsFunc("FROM worklog_member WHERE id = ?", func([]driver.Value) ([]string, [][]driver.Value) {
		return memberTeamRows(1)
	})
	// 既有绑定行 user_id=99：本次为已自绑成员的换 token 代绑
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id", []string{"user_id"}, []driver.Value{int64(99)})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/accounts/bind",
		`{"memberId":12,"mobile":"13800138000","checkCode":"123456"}`)
	if env := decodeEnvelope(t, rec); env["code"] != float64(0) {
		t.Fatalf("代绑应成功: %v", env)
	}

	var entry fakeAppLogEntry
	found := false
	for _, e := range log.snapshot() {
		if e.event == "sgcc.bind.admin" {
			entry, found = e, true
		}
	}
	if !found {
		t.Fatalf("应写管理员代绑审计事件 sgcc.bind.admin")
	}
	if entry.fields["operator_id"] != int64(7) || entry.fields["member_id"] != int64(12) {
		t.Fatalf("审计应记操作人与目标成员: %#v", entry.fields)
	}
	if entry.fields["operator_name"] != "zhang" {
		t.Fatalf("审计应记操作人账号: %#v", entry.fields)
	}
	if entry.fields["result"] != "token_refreshed" {
		t.Fatalf("已自绑成员换 token 的结果应为 token_refreshed: %#v", entry.fields)
	}
	if len(entry.fields) != 4 {
		t.Fatalf("审计字段应恰为操作人/目标成员/结果: %#v", entry.fields)
	}
	for k, v := range entry.fields {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "password") ||
			strings.Contains(lk, "checkcode") || strings.Contains(lk, "captcha") || strings.Contains(lk, "sms") {
			t.Fatalf("审计字段不得包含凭据键 %q", k)
		}
		if s, ok := v.(string); ok && (strings.Contains(s, "srv-token") || strings.Contains(s, "123456")) {
			t.Fatalf("审计字段不得包含凭据值: %q", s)
		}
	}
}
