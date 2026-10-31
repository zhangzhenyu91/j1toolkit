package sgccclockin

// 同步并发回归：全局并发上限（默认 4，1–8）、不同成员并行、同成员同日串行、
// 旧 SGCC_SYNC_INTERVAL_MS 间隔不再读取/不再等待、费用照片关联与同步单元共用 member+date 键锁。

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// probeAPI 并发观测 fake：记录 DayNew 在途数与同键重叠，可挂起等待放行。
type probeAPI struct {
	*fakeAPI
	mu       sync.Mutex
	inFlight int
	peak     int
	overlap  bool
	delay    time.Duration
	hold     chan struct{} // 非 nil：DayNew 进入后等待该 channel 关闭（并发编排用）
	holdTok  string        // 非空：仅挂起该 token 的 DayNew（区分同步单元与打卡/费用链路）
	entered  chan struct{} // 非 nil：每次进入 DayNew 通知一次（容量需足够）
}

func (a *probeAPI) DayNew(ctx context.Context, token, date string, opts CallOpts) (map[string]any, error) {
	a.mu.Lock()
	a.inFlight++
	if a.inFlight > a.peak {
		a.peak = a.inFlight
	}
	if a.inFlight > 1 {
		a.overlap = true
	}
	a.mu.Unlock()
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
	}
	if a.hold != nil && (a.holdTok == "" || a.holdTok == token) {
		<-a.hold
	}
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	d, err := a.fakeAPI.DayNew(ctx, token, date, opts)
	a.mu.Lock()
	a.inFlight--
	a.mu.Unlock()
	return d, err
}

func (a *probeAPI) stats() (peak int, overlap bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peak, a.overlap
}

// recordingCfg 记录被读取的配置键（断言旧间隔键不再读取）。
type recordingCfg struct {
	inner ConfigStore
	mu    sync.Mutex
	keys  map[string]int
}

func (c *recordingCfg) Get(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	if c.keys == nil {
		c.keys = map[string]int{}
	}
	c.keys[key]++
	c.mu.Unlock()
	return c.inner.Get(ctx, key)
}

func (c *recordingCfg) reads(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys[key]
}

// syncProbeModule 构造可观测并发的模块（fake DB + fake 商旅 + 记录配置键）。
func syncProbeModule(t *testing.T) (*Module, *probeAPI, *fakeDB) {
	t.Helper()
	db, fdb := openFake(t)
	m := New(db)
	probe := &probeAPI{fakeAPI: &fakeAPI{dayNew: map[string]any{
		"statusCode": float64(200),
		"data":       map[string]any{"body": map[string]any{}},
	}}}
	m.API = probe
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	return m, probe, fdb
}

// TestSyncOneGlobalConcurrencyLimit 同步单元全局并发：不同成员并行且峰值不超过默认 4。
func TestSyncOneGlobalConcurrencyLimit(t *testing.T) {
	m, probe, _ := syncProbeModule(t)
	probe.delay = 40 * time.Millisecond

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			acc := &Account{ID: int64(100 + i), MemberID: int64(200 + i), TokenStatus: 1, Token: "tk"}
			if err := m.SyncOne(context.Background(), acc, "2026-10-28", "daily", 1); err != nil {
				t.Errorf("同步失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	peak, _ := probe.stats()
	if peak > 4 {
		t.Fatalf("同步并发峰值应 ≤4（默认上限），实际 %d", peak)
	}
	if peak < 2 {
		t.Fatalf("不同成员应并行，实际峰值 %d", peak)
	}
}

// TestSyncOneSameMemberDateSerialized 同成员同日并发同步必须串行（member+date 键控锁）。
func TestSyncOneSameMemberDateSerialized(t *testing.T) {
	m, probe, _ := syncProbeModule(t)
	probe.delay = 40 * time.Millisecond

	acc := func() *Account {
		return &Account{ID: 3, TeamID: 1, MemberID: 11, TokenStatus: 1, Token: "tk"}
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.SyncOne(context.Background(), acc(), "2026-10-28", "daily", 1); err != nil {
				t.Errorf("同步失败: %v", err)
			}
		}()
	}
	wg.Wait()

	if _, overlap := probe.stats(); overlap {
		t.Fatalf("同成员同日必须串行，实际出现并发重叠")
	}
}

// TestSyncTeamDayIgnoresIntervalConfig 成员间不再按 SGCC_SYNC_INTERVAL_MS 等待，也不再读取该键。
func TestSyncTeamDayIgnoresIntervalConfig(t *testing.T) {
	m, _, fdb := syncProbeModule(t)
	cfg := newMapCfg()
	cfg.set(CfgSyncIntervalMs, "3000")
	rec := &recordingCfg{inner: cfg}
	m.Cfg = rec
	fdb.onRowsAlways("JOIN worklog_sgcc_account a ON a.member_id = m.id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token",
			"device_type", "system_version", "token_status", "card_id"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1), int64(11)},
		[]driver.Value{int64(2), int64(1), int64(8), int64(12), "13800138001", "tk", "dev", "sys", int64(1), int64(12)},
		[]driver.Value{int64(3), int64(1), int64(9), int64(13), "13800138002", "tk", "dev", "sys", int64(1), int64(13)},
	)

	done := make(chan struct{})
	go func() {
		m.syncTeamDay(context.Background(), 1, "2026-10-28", "daily", nil, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("成员间不得再有固定间隔等待（旧键 3000ms × 3 名成员会超过 2s）")
	}
	if n := rec.reads(CfgSyncIntervalMs); n != 0 {
		t.Fatalf("旧间隔键不应再被读取，实际读取 %d 次", n)
	}
}

// TestFeeLinkWaitsForSameMemberDateSync 费用照片远端关联与同步单元共用 member+date 键锁：
// 同成员同日同步进行中，linkFeePhoto 必须等待（防费用模板 read-modify-write 互踩）。
func TestFeeLinkWaitsForSameMemberDateSync(t *testing.T) {
	m, probe, _ := syncProbeModule(t)
	probe.hold = make(chan struct{})
	probe.entered = make(chan struct{}, 4)
	m.API.(*probeAPI).feeTpl = feeTplWithImages("")
	m.API.(*probeAPI).uploadImg = UploadedImage{FileInfoID: "F1", HTTPURL: "https://sgcc.test/f1.jpg"}
	m.API.(*probeAPI).feeSave = map[string]any{"statusCode": 200}

	date := "2026-10-28"
	acc := &Account{ID: 3, TeamID: 1, MemberID: 11, TokenStatus: 1, Token: "tk"}
	syncDone := make(chan struct{})
	go func() {
		_ = m.SyncOne(context.Background(), acc, date, "daily", 1)
		close(syncDone)
	}()
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("同步单元未进入 DayNew")
	}

	linkDone := make(chan error, 1)
	go func() {
		_, err := m.linkFeePhoto(context.Background(), acc, 11, date, []byte("jpeg"))
		linkDone <- err
	}()
	select {
	case err := <-linkDone:
		t.Fatalf("同步单元持锁期间费用照片关联不得抢跑（err=%v）", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(probe.hold)
	select {
	case err := <-linkDone:
		if err != nil {
			t.Fatalf("同步结束后关联应成功: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("同步结束后费用照片关联仍未完成")
	}
	<-syncDone
}

// TestClockinPostWaitsForSameMemberDateSync 打卡（含已存在校验与本地刷新）与同步单元共用键锁。
func TestClockinPostWaitsForSameMemberDateSync(t *testing.T) {
	m, probe, fdb := syncProbeModule(t)
	probe.hold = make(chan struct{})
	probe.holdTok = "sync-tk" // 只挂起同步单元，打卡链路的 DayNew 不挂起
	probe.entered = make(chan struct{}, 4)
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token",
			"device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk", "dev", "sys", int64(1)})

	date := TodayCN()
	acc := &Account{ID: 3, TeamID: 1, MemberID: 11, TokenStatus: 1, Token: "sync-tk"}
	syncDone := make(chan struct{})
	go func() {
		_ = m.SyncOne(context.Background(), acc, date, "daily", 1)
		close(syncDone)
	}()
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("同步单元未进入 DayNew")
	}

	body := fmt.Sprintf(`{"member_id":11,"date":"%s","seq":1,"position":"中国山西省临汾市尧都区","longitude":"111.5","latitude":"36.1","city_code":"141002","city_name":"临汾市"}`, date)
	rec := make(chan *httptest.ResponseRecorder, 1)
	go func() { rec <- doJSON(t, h, http.MethodPost, "/api/v1/sgcc/clockin", body) }()
	select {
	case r := <-rec:
		t.Fatalf("同步单元持锁期间打卡不得抢跑（响应 %d）", r.Code)
	case <-time.After(150 * time.Millisecond):
	}
	close(probe.hold)
	select {
	case r := <-rec:
		if r.Code != http.StatusOK {
			t.Fatalf("同步结束后打卡应完成，实际 HTTP %d：%s", r.Code, r.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("同步结束后打卡请求仍未返回")
	}
	<-syncDone
}
