package sgccclockin

// 路由测试：登录绑定（归并/接管/拒绝）、限流、账号管理、打卡/费用、同步任务。
// fake SQL + fake Client/Notifier，不实连外部服务。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
)

// 保证 driver.Value 在测试中可见（编译期）。
var _ = driver.Value(0)

// fakeAPI 测试用商旅客户端。
type fakeAPI struct {
	captchaErr error
	captchaImg string
	smsV2      map[string]any
	smsSlapp   map[string]any
	token      string
	loginErr   error
	dayNew     map[string]any
	dayNewErr  error
	markCalls  int
	saveCalls  int
	feeTpl     map[string]any
	feeSave    map[string]any
	uploadImg  UploadedImage
	uploadErr  error
	probeOK    bool
	probeSkip  bool
}

func (f *fakeAPI) LoginCaptcha(context.Context, string) (string, error) {
	return f.captchaImg, f.captchaErr
}
func (f *fakeAPI) LoginSendSmsV2(context.Context, string, string) (map[string]any, error) {
	return f.smsV2, nil
}
func (f *fakeAPI) LoginSendSmsSlapp(context.Context, string, string, string, CallOpts) (map[string]any, error) {
	return f.smsSlapp, nil
}
func (f *fakeAPI) LoginBySmsSlapp(context.Context, string, string, string, string, CallOpts) (string, error) {
	return f.token, f.loginErr
}
func (f *fakeAPI) LoginByPasswordV3(context.Context, string, string, string, CallOpts) (string, error) {
	return f.token, f.loginErr
}
func (f *fakeAPI) LoginByPassword(context.Context, string, string, string, string, CallOpts) (string, error) {
	return f.token, f.loginErr
}
func (f *fakeAPI) DayNew(context.Context, string, string, CallOpts) (map[string]any, error) {
	return f.dayNew, f.dayNewErr
}
func (f *fakeAPI) MarkClockin(context.Context, string, map[string]any, CallOpts) (map[string]any, error) {
	f.markCalls++
	return map[string]any{"statusCode": 200}, nil
}
func (f *fakeAPI) SaveFee(context.Context, string, map[string]any, CallOpts) (map[string]any, error) {
	f.saveCalls++
	return map[string]any{"statusCode": 200}, nil
}
func (f *fakeAPI) GetFeeInfoNew(context.Context, string, FeeTplParams, CallOpts) (map[string]any, error) {
	return f.feeTpl, nil
}
func (f *fakeAPI) SaveFeeInfoNew(context.Context, string, string, map[string]any, map[string]string, CallOpts) (map[string]any, error) {
	f.saveCalls++
	return f.feeSave, nil
}
func (f *fakeAPI) UploadImageV2(context.Context, string, []byte, CallOpts) (UploadedImage, error) {
	return f.uploadImg, f.uploadErr
}
func (f *fakeAPI) ProbeProxy() (bool, bool, string) {
	return f.probeOK, f.probeSkip, ""
}
func (f *fakeAPI) SelfCheck(context.Context) error { return nil }

// fakeNotifier 记录通知调用。
type fakeNotifier struct {
	tokenExpired int
	reminds      map[string]int
	manualChecks int
}

func (n *fakeNotifier) NotifyTokenExpired(context.Context, Account) error {
	n.tokenExpired++
	return nil
}
func (n *fakeNotifier) NotifyMissingEndClockin(context.Context, int64, string, []MissingClockinRow) error {
	return nil
}
func (n *fakeNotifier) NotifyDailySyncResult(context.Context, int64, string, SyncSummary) error {
	return nil
}
func (n *fakeNotifier) NotifyRemind(_ context.Context, kind string, _ []MissingClockinRow) error {
	if n.reminds == nil {
		n.reminds = map[string]int{}
	}
	n.reminds[kind]++
	return nil
}
func (n *fakeNotifier) NotifyProxyState(context.Context, string, bool) error { return nil }
func (n *fakeNotifier) NotifyManualCheck(context.Context, string, []MissingClockinRow, string) error {
	n.manualChecks++
	return nil
}

func testIdentity() auth.Identity {
	return auth.Identity{ID: 7, Username: "zhang", Nickname: "张振宇", Role: "team_admin", TeamID: 1}
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应: %v body=%s", err, rec.Body.String())
	}
	return env
}

// TestLoginBindUpsert 新绑定插入 + 本人重绑更新 + 接管代绑行。
func TestLoginBindUpsert(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := &fakeAPI{token: "tok-1"}
	m.API = api
	h := newTestRouter(m, testIdentity())

	// 班组解析 + 成员认领 + 查本人行/代绑行（均无）→ INSERT
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member WHERE team_id", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE user_id", []string{"id"})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id", []string{"id", "user_id"})
	fdb.onExec("INSERT INTO worklog_sgcc_account", 1)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/bind",
		`{"mobile":"13800138000","checkCode":"123456","deviceType":"Xiaomi","systemVersion":"Android 16"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("绑定应成功: %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data["bound"] != true {
		t.Fatalf("bound 应为 true: %v", data)
	}
	if len(fdb.callsWith("INSERT INTO worklog_sgcc_account")) != 1 {
		t.Fatalf("应插入一行绑定")
	}
}

// TestLoginBindRejectTakeover 成员已被其他账号自绑 → 40910 拒绝。
func TestLoginBindRejectTakeover(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{token: "tok"}
	h := newTestRouter(m, testIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member WHERE team_id", []string{"id"}, []driver.Value{int64(11)})
	// 该成员绑定行 user_id=99（他人）——查询仅取 user_id 一列
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id", []string{"user_id"},
		[]driver.Value{int64(99)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/bind",
		`{"mobile":"13800138000","checkCode":"123456"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeBoundOther) {
		t.Fatalf("应拒绝接管他人绑定: %v", env)
	}
}

// TestLoginRateLimit 短信接口限流（>5 次/分钟 → 42901）。
func TestLoginRateLimit(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{smsV2: map[string]any{"statusCode": 200}}
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})

	for i := 0; i < 5; i++ {
		rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/sms",
			`{"mobile":"13800138000","checkImgCode":"abcd"}`)
		env := decodeEnvelope(t, rec)
		if env["code"] != float64(0) {
			t.Fatalf("第 %d 次发短信应成功: %v", i+1, env)
		}
	}
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/sms",
		`{"mobile":"13800138000","checkImgCode":"abcd"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeRateLimit) {
		t.Fatalf("第 6 次应限流: %v", env)
	}
}

// TestLoginSmsNeedRisk 无图形码/滑块凭据 → 40034。
func TestLoginSmsNeedRisk(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{smsSlapp: map[string]any{"statusCode": 200}}
	h := newTestRouter(m, testIdentity())
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/login/sms", `{"mobile":"13800138000"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeNeedRisk) {
		t.Fatalf("应要求先完成验证: %v", env)
	}
}

// TestExpiredList 登录过期名单。
func TestExpiredList(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("token_status = 0", []string{"name"}, []driver.Value{"王五"})
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/expired", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("查询失败: %v", env)
	}
	data := env["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 1 || list[0] != "王五" {
		t.Fatalf("过期名单不符: %v", list)
	}
}

// TestAccountsListAdminGate 非管理员访问 /accounts → 40012。
func TestAccountsListAdminGate(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity()) // role=team_admin，但班组不匹配 → 拒绝
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(2), "检修二班"})
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/accounts", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeNotAdmin) {
		t.Fatalf("班组管理员仅限本班: %v", env)
	}
}

// TestProbeAuthFlip 登录态 1→0 翻转落库 + 通知；应答不可解不翻转。
func TestProbeAuthFlip(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	notifier := &fakeNotifier{}
	m.Notifier = notifier
	api := &fakeAPI{dayNew: map[string]any{"statusCode": 401}}
	m.API = api

	acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
	if m.probeAuth(context.Background(), acc) {
		t.Fatalf("statusCode=401 应判失效")
	}
	if notifier.tokenExpired != 1 {
		t.Fatalf("应通知一次登录过期: %d", notifier.tokenExpired)
	}
	if len(fdb.callsWith("UPDATE worklog_sgcc_account SET token_status")) != 1 {
		t.Fatalf("应更新 token_status")
	}

	// 应答不可解：保持原值不翻转不通知
	api2 := &fakeAPI{dayNew: nil}
	m.API = api2
	acc2 := &Account{ID: 4, MemberID: 12, TokenStatus: 1, Token: "tk"}
	if !m.probeAuth(context.Background(), acc2) {
		t.Fatalf("应答不可解应保持原值（有效）")
	}
	if notifier.tokenExpired != 1 {
		t.Fatalf("不应重复通知: %d", notifier.tokenExpired)
	}
}

// TestRefreshClockinSeqKeep 对账时按 detailId 保留原 seq，新记录补空位。
func TestRefreshClockinSeqKeep(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	// 本地已有 detail_id=D1 → seq=2
	fdb.onRowsAlways("SELECT seq, detail_id FROM worklog_clockin", []string{"seq", "detail_id"},
		[]driver.Value{int64(2), "D1"})
	fdb.onExec("INSERT INTO worklog_clockin", 0)

	err := m.RefreshClockins(context.Background(), 1, 11, "2026-10-27", []ClockinRecordLocal{
		{DetailID: "D1", ClockTime: "2026-10-27 17:00:00", Source: 1}, // 保留 seq 2
		{DetailID: "D2", ClockTime: "2026-10-27 08:00:00", Source: 1}, // 新记录 → seq 1
	})
	if err != nil {
		t.Fatalf("对账回写: %v", err)
	}
	inserts := fdb.callsWith("INSERT INTO worklog_clockin")
	if len(inserts) != 2 {
		t.Fatalf("应写入两条: %d", len(inserts))
	}
	// 第一条 D1（列表序）seq=2；第二条 D2 seq=1
	if !strings.Contains(fmt.Sprintf("%v", inserts[0].args), "2") {
		t.Fatalf("D1 应保留 seq=2: %v", inserts[0].args)
	}
}

// TestPhotoOpLock 同卡片互斥 + 状态流转。
func TestPhotoOpLock(t *testing.T) {
	ops := NewPhotoOpManager()
	op1 := ops.New("upload", 1, 100, 0)
	if op1 == nil {
		t.Fatalf("首个任务应创建")
	}
	if op2 := ops.New("upload", 1, 100, 0); op2 != nil {
		t.Fatalf("同卡片第二任务应拒绝")
	}
	if !ops.ActivePhotoOp(1, 100) {
		t.Fatalf("应有进行中任务")
	}
	done := make(chan struct{})
	go func() {
		ops.Run(op1.ID, func(op *PhotoOp) error {
			ops.SetProgress(op.ID, 50, "uploading")
			return nil
		})
		close(done)
	}()
	<-done
	if ops.ActivePhotoOp(1, 100) {
		t.Fatalf("完成后应解锁")
	}
	st, ok := ops.Status(op1.ID)
	if !ok || st.Progress != 100 || st.Step != "done" {
		t.Fatalf("状态不符: %+v", st)
	}
	// 失败任务
	op3 := ops.New("delete", 1, 100, 9)
	ops.Run(op3.ID, func(op *PhotoOp) error { return fmt.Errorf("商旅同步失败") })
	st3, _ := ops.Status(op3.ID)
	if !st3.Failed || st3.Error == "" {
		t.Fatalf("失败态不符: %+v", st3)
	}
}

// TestSyncPullJob 手动拉取任务进度闭环。
func TestSyncPullJob(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.IntervalMs = 0
	h := newTestRouter(m, testIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})
	fdb.onRowsAlways("SELECT seq, detail_id FROM worklog_clockin", []string{"seq", "detail_id"})
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	fdb.onExec("INSERT INTO worklog_clockin", 0)

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"member_id":11,"date":"2026-10-27"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("发起同步失败: %v", env)
	}
	data := env["data"].(map[string]any)
	jobID := int64(data["jobId"].(float64))
	// 等后台完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := jobs.Get(jobID); ok && job.Finished {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, ok := jobs.Get(jobID)
	if !ok || !job.Finished {
		t.Fatalf("任务未完成: %+v", job)
	}
	if job.Errors != 0 {
		t.Fatalf("不应有失败: %+v", job)
	}
}

// TestAccountGetMasked 手机号脱敏输出。
func TestAccountGetMasked(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE user_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})
	fdb.onRowsAlways("FROM worklog_clockin", []string{"seq", "clock_time", "position", "work_hours"})
	fdb.onRowsAlways("FROM worklog_fee", []string{"food_fee", "transit_fee"})

	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/account", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("查询失败: %v", env)
	}
	data := env["data"].(map[string]any)
	if data["mobile_masked"] != "138****8000" {
		t.Fatalf("脱敏不符: %v", data["mobile_masked"])
	}
}

// 保证 driver.Value 在测试中可见（编译期）。
var _ = sql.ErrNoRows
