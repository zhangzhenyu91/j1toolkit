package sgccclockin

// 路由测试：登录绑定（归并/接管/拒绝）、限流、账号管理、打卡/费用、同步任务。
// fake SQL + fake Client/Notifier，不实连外部服务。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	markResp   map[string]any // 非空覆盖 MarkClockin 应答（默认 statusCode=200）
	markLast   map[string]any // 最近一次 MarkClockin 请求体
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
func (f *fakeAPI) MarkClockin(_ context.Context, _ string, payload map[string]any, _ CallOpts) (map[string]any, error) {
	f.markCalls++
	f.markLast = payload
	if f.markResp != nil {
		return f.markResp, nil
	}
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
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status", "last_check_at"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1), nil})
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

// TestParseDayClockinsBodyShape dayNew 应答解析（data.body.clockInDetailList + body.workHours 同 Node 口径）。
func TestParseDayClockinsBodyShape(t *testing.T) {
	decoded := map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{
			"clockInDetailList": []any{
				map[string]any{
					"detailId": "D1", "clockInTime": "2026-10-27 08:00:00",
					"position": "P1", "longitude": "111.5", "latitude": "36.1",
				},
				map[string]any{
					"id": "D2", "createTime": float64(1761532800000), // 毫秒时间戳
					"position": "P2",
				},
			},
			"workHours": "8",
		}},
	}
	recs := ParseDayClockins(decoded)
	if len(recs) != 2 {
		t.Fatalf("应解析 2 条: %d", len(recs))
	}
	if recs[0].DetailID != "D1" || recs[0].ClockTime != "2026-10-27 08:00:00" {
		t.Fatalf("首条解析不符: %+v", recs[0])
	}
	if recs[1].DetailID != "D2" || recs[1].ClockTime == "" {
		t.Fatalf("次条（id/createTime 兜底）解析不符: %+v", recs[1])
	}
	if recs[0].WorkHours != "8" || recs[1].WorkHours != "8" {
		t.Fatalf("工时应取 body.workHours 逐行同值: %+v", recs)
	}
	// 旧形状（data.list）不再解析
	if got := ParseDayClockins(map[string]any{
		"data": map[string]any{"list": []any{map[string]any{"detailId": "X"}}},
	}); got != nil {
		t.Fatalf("data.list 形状不应解析出记录: %+v", got)
	}
}

// clockinAPIReady 打卡路由测试共用：班组/成员/绑定行就绪（登录态有效）。
func clockinAPIReady(t *testing.T, db *sql.DB, fdb *fakeDB) {
	t.Helper()
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})
}

// TestClockinPostMarkContract 打卡请求体口径：remarks 默认值、seq=1 不带 NextClockIn、成功回「打卡成功」并补写城市。
func TestClockinPostMarkContract(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.API = api
	h := newTestRouter(m, testIdentity())
	clockinAPIReady(t, db, fdb)

	body := fmt.Sprintf(`{"member_id":11,"date":"%s","seq":1,"position":"中国山西省临汾市尧都区","longitude":"111.5","latitude":"36.1","city_code":"141002","city_name":"临汾市"}`, TodayCN())
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/clockin", body)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("打卡应成功: %v", env)
	}
	if env["message"] != "打卡成功" {
		t.Fatalf("message 应为「打卡成功」: %v", env["message"])
	}
	if api.markCalls != 1 || api.markLast == nil {
		t.Fatalf("应调用 markNew 一次: %+v", api.markLast)
	}
	if got := api.markLast["remarks"]; got != "110kV及220kV输电线路巡视" {
		t.Fatalf("remarks 应默认「110kV及220kV输电线路巡视」: %v", got)
	}
	if _, has := api.markLast["NextClockIn"]; has {
		t.Fatalf("seq=1 不应带 NextClockIn: %+v", api.markLast)
	}
	if api.markLast["clockInDate"] != TodayCN() {
		t.Fatalf("clockInDate 不符: %v", api.markLast["clockInDate"])
	}
	if len(fdb.callsWith("UPDATE worklog_clockin SET city_code")) != 1 {
		t.Fatalf("打卡成功后应补写城市信息")
	}
}

// TestClockinPostDuplicate40037 已有该 seq 记录再打卡 → 40037「已开始打卡」。
func TestClockinPostDuplicate40037(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.API = api
	h := newTestRouter(m, testIdentity())
	clockinAPIReady(t, db, fdb)
	fdb.onRowsAlways("FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq",
		[]string{"cnt"}, []driver.Value{int64(1)})

	body := fmt.Sprintf(`{"member_id":11,"date":"%s","seq":1,"position":"P","longitude":"111.5","latitude":"36.1","city_code":"141002","city_name":"临汾市"}`, TodayCN())
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/clockin", body)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codePlatform) || env["message"] != "已开始打卡" {
		t.Fatalf("应 40037 已开始打卡: %v", env)
	}
	if api.markCalls != 0 {
		t.Fatalf("不应再调商旅打卡")
	}
}

// TestClockinPostFailMapping 平台失败 → 探测登录态：仍有效 40036 带平台原文；已过期 40021。
func TestClockinPostFailMapping(t *testing.T) {
	run := func(dayNew map[string]any, wantCode float64, wantMsg string) {
		db, fdb := openFake(t)
		defer db.Close()
		m := New(db)
		api := &fakeAPI{dayNew: dayNew, markResp: map[string]any{"statusCode": float64(500), "msg": "服务丢了"}}
		m.API = api
		h := newTestRouter(m, testIdentity())
		clockinAPIReady(t, db, fdb)
		body := fmt.Sprintf(`{"member_id":11,"date":"%s","seq":1,"position":"P","longitude":"111.5","latitude":"36.1","city_code":"141002","city_name":"临汾市"}`, TodayCN())
		rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/clockin", body)
		env := decodeEnvelope(t, rec)
		if env["code"] != wantCode || env["message"] != wantMsg {
			t.Fatalf("映射不符: code=%v message=%v（期望 %v %q）", env["code"], env["message"], wantCode, wantMsg)
		}
	}
	run(map[string]any{"statusCode": float64(200)}, 40036, "服务丢了")
	run(map[string]any{"statusCode": float64(401)}, 40021, "该成员商旅登录已过期，请其本人重新登录")
}

// TestSyncOneRemoteOverwrite 对账以商旅为准：商旅有 1 条 → 本地多出的 detailId 行删除并记 ok/diff 明细。
func TestSyncOneRemoteOverwrite(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{
			"clockInDetailList": []any{
				map[string]any{"detailId": "D1", "clockInTime": "2026-10-27 08:00:00", "position": "P"},
			},
			"workHours": "8",
		}},
	}}
	fdb.onRowsAlways("SELECT COUNT(*) FROM worklog_clockin", []string{"cnt"}, []driver.Value{int64(3)})
	fdb.onRowsAlways("SELECT seq, detail_id FROM worklog_clockin", []string{"seq", "detail_id"})
	fdb.onRowsAlways("SELECT id, detail_id FROM worklog_clockin", []string{"id", "detail_id"},
		[]driver.Value{int64(9), "OLD"})
	fdb.onExec("INSERT INTO worklog_clockin", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
	if err := m.SyncOne(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
		t.Fatalf("对账应成功: %v", err)
	}
	deletes := fdb.callsWith("DELETE FROM worklog_clockin WHERE id = ?")
	if len(deletes) != 1 || fmt.Sprintf("%v", deletes[0].args) != "[9]" {
		t.Fatalf("应删除商旅已无的本地行 id=9: %+v", deletes)
	}
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	found := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprintf("%v", c.args), "打卡：商旅 1 条覆盖本地（本地原 3 条）") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应记对账明细「打卡：商旅 1 条覆盖本地（本地原 3 条）」: %+v", logs)
	}
}

// TestSyncClockinKeepLocalOnBadDayNew dayNew 应答无效（非 200 / 无 data.body）时不得把本地打卡按 0 条删除。
func TestSyncClockinKeepLocalOnBadDayNew(t *testing.T) {
	for name, dayNew := range map[string]map[string]any{
		"statusCode 非 200": {"statusCode": float64(500)},
		"无 data.body":      {"statusCode": float64(200), "data": map[string]any{}},
		"应答为 nil":          nil,
	} {
		db, fdb := openFake(t)
		m := New(db)
		m.API = &fakeAPI{dayNew: dayNew}
		fdb.onExec("INSERT INTO worklog_sync_log", 0)

		acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
		// 直接进打卡对账段（probeAuth 会先因非 200 翻转登录态，与本守卫无关）
		if err := m.syncClockinPart(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
			t.Fatalf("%s：应答无效应跳过而非报错: %v", name, err)
		}
		if n := len(fdb.callsWith("DELETE FROM worklog_clockin")); n != 0 {
			t.Fatalf("%s：不得删除本地打卡流水，实际 DELETE %d 次", name, n)
		}
		db.Close()
	}
}

// TestAddSyncLogWriteFailLogged 写库失败必须落服务端日志，且日志不含 token/密钥/原始报文。
func TestAddSyncLogWriteFailLogged(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	fdb.onErr("INSERT INTO worklog_sync_log", fmt.Errorf("Error 1406: Data too long"))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	m.AddSyncLog(context.Background(), 1, 11, "2026-10-27", "daily", "clockin", "fail", "detail-文本")
	out := buf.String()
	if !strings.Contains(out, "核查流水写库失败") || !strings.Contains(out, "Data too long") {
		t.Fatalf("写库失败应记服务端日志: %q", out)
	}
	if strings.Contains(out, "tk-SECRET") || strings.Contains(out, "detail-文本") {
		t.Fatalf("日志不得输出 detail/密钥原文: %q", out)
	}
}

// ---- 路由审计补差回归（超管 team_id / sync/pull 三模式 / 进度任务 / op/status / sync/logs / accounts / fee / day）----
// adminIdentity 超管身份（team_id 指定用）。
func adminIdentity() auth.Identity {
	return auth.Identity{ID: 7, Username: "zhang", Nickname: "张振宇", Role: "admin", TeamID: 1}
}

// userIdentity 普通用户身份。
func userIdentity() auth.Identity {
	return auth.Identity{ID: 8, Username: "li", Nickname: "李四", Role: "user", TeamID: 1}
}

// TestWantedTeamIDResolve 超管 ?team_id= / body.team_id 生效班组解析（Node resolveReqTeam 语义）。
func TestWantedTeamIDResolve(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, adminIdentity())
	// 超管指定班组 2：生效班组应为 2（查询参数进 SQL）
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(2), "检修二班"})
	fdb.onRowsAlways("FROM worklog_sync_log", []string{"id", "team_id", "member_id", "member_name", "sync_date", "scope", "type", "result", "detail", "created_at"})
	_ = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/logs?team_id=2", "")
	for _, c := range fdb.callsWith("FROM worklog_sync_log") {
		if len(c.args) > 0 && c.args[0] == int64(2) {
			return // 命中：team_id=2 已生效
		}
	}
	t.Fatalf("超管 ?team_id= 未生效: %+v", fdb.callsWith("FROM worklog_sync_log"))
}

// TestWantedTeamIDBodyAndNonAdmin body.team_id 生效；非超管传 team_id 被收敛回本班。
func TestWantedTeamIDBodyAndNonAdmin(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.IntervalMs = 0
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(2), "检修二班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(2), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	// 超管 body.team_id=2：requireMemberAccount 校验 SQL 应带班组 2
	h := newTestRouter(m, adminIdentity())
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"team_id":2,"member_id":11,"date":"2026-10-28"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("body.team_id 拉取应成功: %v", env)
	}
	found := false
	for _, c := range fdb.callsWith("FROM worklog_member m") {
		for _, a := range c.args {
			if a == int64(2) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("body.team_id= 未生效: %+v", fdb.callsWith("FROM worklog_member m"))
	}

	// 非超管传 team_id=2：收敛回本班 1（生效班组解析只按本人 team_id 查）
	db2, fdb2 := openFake(t)
	defer db2.Close()
	m2 := New(db2)
	m2.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m2.IntervalMs = 0
	fdb2.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb2.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb2.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})
	fdb2.onExec("INSERT INTO worklog_sync_log", 0)
	h2 := newTestRouter(m2, userIdentity())
	_ = doJSON(t, h2, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"team_id":2,"member_id":11,"date":"2026-10-28"}`)
	for _, c := range fdb2.callsWith("FROM sys_team") {
		for _, a := range c.args {
			if a == int64(2) {
				t.Fatalf("非超管 team_id 不应生效（不应按 2 查班组）: %+v", c.args)
			}
		}
	}
	for _, c := range fdb2.callsWith("FROM worklog_member m") {
		for _, a := range c.args {
			if a == int64(2) {
				t.Fatalf("非超管 team_id 不应进校验 SQL: %+v", c.args)
			}
		}
	}
}

// TestSyncPullEntryAndBatch /sync/pull 三模式：entry_id 卡片级、区段批量门禁、进度与 active 形状。
func TestSyncPullEntryAndBatch(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.IntervalMs = 0
	h := newTestRouter(m, adminIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_entry WHERE id", []string{"log_date"}, []driver.Value{"2026-10-28"})
	fdb.onRowsAlways("FROM worklog_entry_member WHERE entry_id", []string{"member_id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsAlways("COUNT(DISTINCT m.id) FROM worklog_member", []string{"cnt"}, []driver.Value{int64(2)})
	fdb.onRowsAlways("COUNT(DISTINCT m.id, e.log_date)", []string{"cnt"}, []driver.Value{int64(3)})
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	// 卡片级：{entry_id} → jobId+total，文案「仅本卡用车人」
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"entry_id":100}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("entry_id 拉取应成功: %v", env)
	}
	data := env["data"].(map[string]any)
	if data["total"] != float64(2) {
		t.Fatalf("卡片级 total 应为 2: %v", data)
	}
	if !strings.Contains(env["message"].(string), "仅本卡用车人") {
		t.Fatalf("文案不符: %v", env["message"])
	}

	// 缺 job → 200 空完成态（Node 口径）
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/progress?job_id=999999", "")
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("缺 job 应 200: %v", env)
	}
	p := env["data"].(map[string]any)
	if p["finished"] != true || p["total"] != float64(0) {
		t.Fatalf("缺 job 空完成态不符: %v", p)
	}

	// 进度字段：failed/failMsg/errors 齐备（web errors 旧键并存）
	jobID := jobs.New(1, 3, "card", "2026-10-28")
	jobs.Step(jobID, "connect ECONNREFUSED")
	rec = doJSON(t, h, http.MethodGet, fmt.Sprintf("/api/v1/sgcc/sync/progress?job_id=%d", jobID), "")
	env = decodeEnvelope(t, rec)
	p = env["data"].(map[string]any)
	if p["total"] != float64(3) || p["done"] != float64(1) || p["failed"] != float64(1) {
		t.Fatalf("进度计数不符: %v", p)
	}
	if p["failMsg"] != "网络/代理异常，无法连接商旅平台" {
		t.Fatalf("failMsg 友好口径不符: %v", p)
	}
	if p["errors"] != float64(1) || p["finished"] != false {
		t.Fatalf("errors/finished 不符: %v", p)
	}

	// /sync/active 形状：running/total/done/failed/batchDone/daily/ops/jobs
	batchID := jobs.New(1, 5, "batch", "2026-10-28")
	jobs.Step(batchID, "")
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/active", "")
	env = decodeEnvelope(t, rec)
	a := env["data"].(map[string]any)
	for _, key := range []string{"running", "total", "done", "failed", "batchDone", "daily", "ops", "jobs"} {
		if _, ok := a[key]; !ok {
			t.Fatalf("sync/active 缺 %s: %v", key, a)
		}
	}
	if a["running"] != true || a["total"] != float64(5) || a["done"] != float64(1) {
		t.Fatalf("active 计数不符: %v", a)
	}
	if d, _ := a["daily"].(map[string]any); d["running"] != false {
		t.Fatalf("daily 空态不符: %v", a["daily"])
	}
	jobs.Finish(batchID)
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/active", "")
	env = decodeEnvelope(t, rec)
	a = env["data"].(map[string]any)
	if bd, _ := a["batchDone"].(map[string]any); bd == nil || bd["total"] != float64(5) {
		t.Fatalf("batchDone 终态不符: %v", a["batchDone"])
	}

	// 批量门禁：普通用户 40304；进行中批量 40909
	hu := newTestRouter(m, userIdentity())
	rec = doJSON(t, hu, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"from":"2026-10-01","to":"2026-10-28"}`)
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(40304) {
		t.Fatalf("批量应仅管理员: %v", env)
	}
	jobs.New(1, 1, "batch", "2026-10-28")
	rec = doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"from":"2026-10-01","to":"2026-10-28"}`)
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(40909) {
		t.Fatalf("批量互斥应 40909: %v", env)
	}

	// 登录过期门禁 40910
	fdb.onRowsAlways("a.token_status = 0", []string{"name"}, []driver.Value{"王五"})
	rec = doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"date":"2026-10-27"}`)
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(40910) || !strings.Contains(env["message"].(string), "王五") {
		t.Fatalf("过期门禁不符: %v", env)
	}

	// 区段超 62 天 40000
	rec = doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"from":"2026-01-01","to":"2026-03-15"}`)
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(40000) {
		t.Fatalf("区段超限应 40000: %v", env)
	}
}

// TestSyncPullBatchConcurrentSingleRegister 两个并发批量请求（同班组）至多一个批量登记成功：
// 检查与登记原子化后，后到者回 40909；修复前两次检查都先于登记通过，会同班双开。
func TestSyncPullBatchConcurrentSingleRegister(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	m.IntervalMs = 0
	h := newTestRouter(m, adminIdentity())
	// 班组 2：避开其他用例在全局注册表留下的班组 1 批量任务
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(2), "检修二班"})
	gateTotal := make(chan struct{}) // syncTotal 查询闸门：两请求同时停在「检查→登记」窗口
	gateDay := make(chan struct{})   // 单日同步闸门：拴住胜者后台任务，使其批量任务保持进行中
	var onceTotal, onceDay sync.Once
	releaseTotal := func() { onceTotal.Do(func() { close(gateTotal) }) }
	releaseDay := func() { onceDay.Do(func() { close(gateDay) }) }
	defer releaseTotal()
	defer releaseDay()
	fdb.onRowsGate("COUNT(DISTINCT m.id, e.log_date)", []string{"cnt"}, gateTotal, []driver.Value{int64(2)})
	fdb.onRowsGate("ORDER BY (a.member_id = m.id) DESC", []string{"id"}, gateDay)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	recs := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sgcc/sync/pull",
				strings.NewReader(`{"from":"2026-10-28","to":"2026-10-28"}`))
			req.Header.Set("Authorization", "Bearer t")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			recs <- rec
		}()
	}
	// 等两请求都进入 syncTotal（同时被闸门挡住）再放行，保证处于同一重入窗口
	deadline := time.Now().Add(2 * time.Second)
	for len(fdb.callsWith("COUNT(DISTINCT m.id, e.log_date)")) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("两个请求未并发进入 syncTotal: %+v", fdb.callsWith("COUNT(DISTINCT m.id, e.log_date)"))
		}
		time.Sleep(2 * time.Millisecond)
	}
	releaseTotal()

	// 恰好一个登记成功、一个 40909
	okJobID := int64(0)
	okCount, conflictCount := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case rec := <-recs:
			env := decodeEnvelope(t, rec)
			switch env["code"] {
			case float64(0):
				okCount++
				okJobID = int64(env["data"].(map[string]any)["jobId"].(float64))
			case float64(40909):
				conflictCount++
			default:
				t.Fatalf("并发批量应答异常: %v", env)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("并发批量请求未返回")
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("两个并发批量请求应恰好一个登记成功、一个 40909: 成功 %d / 40909 %d", okCount, conflictCount)
	}
	// 放行胜者后台任务并等其收尾（勿留进行中任务影响后续用例）
	releaseDay()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := jobs.Get(okJobID); ok && job.Finished {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("登记成功的批量任务未收尾: job=%d", okJobID)
}

// TestOpStatusEmptyAndShape /op/status：未命中 200 空完成态；命中返回双端超集字段。
func TestOpStatusEmptyAndShape(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})

	// 未命中：200 空完成态（status=ok、step=done 兼容网页端收尾）
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/op/status?op_id=999999", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("缺任务应 200: %v", env)
	}
	empty := env["data"].(map[string]any)
	if empty["status"] != "ok" || empty["done"] != float64(0) || empty["total"] != float64(0) {
		t.Fatalf("空完成态不符: %v", empty)
	}
	if empty["step"] != "done" {
		t.Fatalf("空态应带 step=done（网页端收尾）: %v", empty)
	}

	// 命中：Node 字段 + 网页端字段并存
	op := m.Ops.New("resync", 1, 100, 9)
	if op == nil {
		t.Fatalf("任务登记失败")
	}
	m.Ops.SetCounts(op.ID, 2, 5)
	m.Ops.SetProgress(op.ID, 40, "running")
	rec = doJSON(t, h, http.MethodGet, fmt.Sprintf("/api/v1/sgcc/op/status?op_id=%d", op.ID), "")
	env = decodeEnvelope(t, rec)
	got := env["data"].(map[string]any)
	if got["status"] != "running" || got["done"] != float64(2) || got["total"] != float64(5) {
		t.Fatalf("Node 字段不符: %v", got)
	}
	if got["step"] != "running" || got["progress"] != float64(40) {
		t.Fatalf("网页端字段不符: %v", got)
	}
	if got["entryId"] != float64(100) || got["entry_id"] != float64(100) {
		t.Fatalf("entry 键不符: %v", got)
	}

	// 终态：fail → status=fail + failedName/error
	m.Ops.SetFailedName(op.ID, "张三")
	m.Ops.Run(op.ID, func(*PhotoOp) error { return fmt.Errorf("商旅同步失败") })
	rec = doJSON(t, h, http.MethodGet, fmt.Sprintf("/api/v1/sgcc/op/status?op_id=%d", op.ID), "")
	env = decodeEnvelope(t, rec)
	got = env["data"].(map[string]any)
	if got["status"] != "fail" || got["failed"] != true || got["failedName"] != "张三" {
		t.Fatalf("失败终态不符: %v", got)
	}
	if got["error"] != "商旅同步失败" {
		t.Fatalf("error 不符: %v", got)
	}
}

// TestSyncLogsMonthRange /sync/logs：month/from-to 与既有过滤并存。
func TestSyncLogsMonthRange(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	cols := []string{"id", "team_id", "member_id", "member_name", "sync_date", "scope", "type", "result", "detail", "created_at"}
	fdb.onRowsAlways("FROM worklog_sync_log", cols,
		[]driver.Value{int64(1), int64(1), int64(11), "张三", "2026-10-28", "daily", "clockin", "ok", "", "2026-10-28 23:00"})

	// month：按月 + LIMIT 100
	_ = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/logs?month=2026-10", "")
	qs := fdb.callsWith("FROM worklog_sync_log")
	if len(qs) == 0 || !strings.Contains(qs[0].query, "DATE_FORMAT(l.sync_date, '%Y-%m') = ?") ||
		!strings.Contains(qs[0].query, "LIMIT 100") {
		t.Fatalf("month 查询不符: %+v", qs)
	}

	// from/to：区段 + LIMIT 200
	_ = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/logs?from=2026-10-01&to=2026-10-28", "")
	qs = fdb.callsWith("FROM worklog_sync_log")
	last := qs[len(qs)-1]
	if !strings.Contains(last.query, "l.sync_date BETWEEN ? AND ?") || !strings.Contains(last.query, "LIMIT 200") {
		t.Fatalf("区段查询不符: %+v", last)
	}

	// 既有过滤（网页端）：type/result 保留
	_ = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/logs?type=clockin&result=fail", "")
	qs = fdb.callsWith("FROM worklog_sync_log")
	last = qs[len(qs)-1]
	if !strings.Contains(last.query, "l.type = ?") || !strings.Contains(last.query, "l.result = ?") {
		t.Fatalf("既有过滤丢失: %+v", last)
	}
}

// TestAccountsCamelCase /accounts 双端键名（camel + snake 并存）。
func TestAccountsCamelCase(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, adminIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_sgcc_account a",
		[]string{"id", "name", "member_name", "mobile", "device_type", "system_version", "token_status", "last_check_at"},
		[]driver.Value{int64(1), "张三", "张三", "13800138000", "Pixel 7", "Android 13", int64(1),
			time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)})
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/accounts", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("accounts 查询失败: %v", env)
	}
	list := env["data"].(map[string]any)["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("列表条数不符: %v", list)
	}
	it := list[0].(map[string]any)
	if it["memberName"] != "张三" || it["mobile"] != "138****8000" {
		t.Fatalf("camel 字段不符: %v", it)
	}
	if it["deviceType"] != "Pixel 7" || it["systemVersion"] != "Android 13" ||
		it["tokenStatus"] != float64(1) || it["lastCheckAt"] != "2026-10-28 23:00" {
		t.Fatalf("camel 字段缺失: %v", it)
	}
	if it["name"] != "张三" || it["mobile_masked"] != "138****8000" ||
		it["device_type"] != "Pixel 7" || it["token_status"] != float64(1) {
		t.Fatalf("snake 字段丢失: %v", it)
	}
}

// TestFeeGetTemplateError40038 /fee 模板取不到 → 40038。
func TestFeeGetTemplateError40038(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{feeTpl: nil}
	h := newTestRouter(m, testIdentity())
	clockinAPIReady(t, db, fdb)
	fdb.onRowsAlways("FROM worklog_fee", []string{"food_fee", "transit_fee", "cost_center_code", "cost_center_name", "extra"})
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/fee?member_id=11&date=2026-10-28", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(codeFeeTpl) {
		t.Fatalf("模板缺失应 40038: %v", env)
	}
	// 有模板 → 正常返回 clockTemplate
	m.API = &fakeAPI{feeTpl: map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{"clockTemplate": map[string]any{
			"id": 7, "name": "T", "dtComponentList": []any{},
		}}},
	}}
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/fee?member_id=11&date=2026-10-28", "")
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("模板正常应成功: %v", env)
	}
	if _, ok := env["data"].(map[string]any)["clockTemplate"]; !ok {
		t.Fatalf("应返回 clockTemplate: %v", env["data"])
	}
}

// TestDayMembersShape /day 全班 members 形状（Node）+ member_id 单成员摘要旧键并存。
func TestDayMembersShape(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("UNION", []string{"id"}, []driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsAlways("JOIN worklog_sgcc_account a ON a.member_id = m.id",
		[]string{"id", "token_status"}, []driver.Value{int64(11), int64(1)})
	fdb.onRowsAlways("FROM worklog_clockin WHERE member_id IN",
		[]string{"member_id", "seq", "detail_id", "clock_time", "position", "work_hours"},
		[]driver.Value{int64(11), int64(1), "D1", "2026-10-28 08:00:00", "变电站", "8"})
	fdb.onRowsAlways("FROM worklog_fee WHERE member_id IN",
		[]string{"member_id", "food_fee", "transit_fee", "cost_center_code", "cost_center_name"},
		[]driver.Value{int64(11), float64(60), float64(0), "C1", "成本中心"})

	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/day?date=2026-10-28", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("day 查询失败: %v", env)
	}
	members := env["data"].(map[string]any)["members"].(map[string]any)
	mm := members["11"].(map[string]any)
	if mm["bound"] != true || mm["tokenStatus"] != float64(1) {
		t.Fatalf("绑定态不符: %v", mm)
	}
	seq1 := mm["seq1"].(map[string]any)
	if seq1["detailId"] != "D1" || seq1["time"] != "2026-10-28 08:00:00" {
		t.Fatalf("打卡行不符: %v", seq1)
	}
	if mm["workHours"] != "8" {
		t.Fatalf("工时不符: %v", mm)
	}
	fee := mm["fee"].(map[string]any)
	if fee["foodFee"] != float64(60) || fee["costCenterCode"] != "C1" {
		t.Fatalf("费用行不符: %v", fee)
	}

	// member_id 摘要旧键并存（网页端）
	fdb.onRowsAlways("FROM worklog_clockin WHERE member_id = ? AND clock_date = ?",
		[]string{"seq", "clock_time", "position", "work_hours"})
	fdb.onRowsAlways("FROM worklog_fee WHERE member_id = ? AND fee_date = ?",
		[]string{"food_fee", "transit_fee"}, []driver.Value{float64(60), float64(0)})
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/day?date=2026-10-28&member_id=11", "")
	env = decodeEnvelope(t, rec)
	data := env["data"].(map[string]any)
	if data["date"] != "2026-10-28" {
		t.Fatalf("缺 date 摘要键: %v", data)
	}
	if _, ok := data["clockin"]; !ok {
		t.Fatalf("缺 clockin 摘要键: %v", data)
	}
	if _, ok := data["members"]; !ok {
		t.Fatalf("缺 members 键: %v", data)
	}

	// 日期格式非法 40000
	rec = doJSON(t, h, http.MethodGet, "/api/v1/sgcc/day?date=2026/10/28", "")
	env = decodeEnvelope(t, rec)
	if env["code"] != float64(40000) {
		t.Fatalf("日期非法应 40000: %v", env)
	}
}

// 保证 driver.Value 在测试中可见（编译期）。
var _ = sql.ErrNoRows
