package sgccclockin

// 排程/提醒测试：时间口径、下次触发计算、剔除名单、提醒前失败转人工核查。
// 定时器可注入，不真睡、不联网。

import (
	"context"
	"database/sql/driver"
	"sync"
	"testing"
	"time"

	"shade/internal/config"
)

// TestFmtCnDateTime 商旅时间口径归一（毫秒/秒时间戳、'YYYY-MM-DD HH:mm:ss'、ISO）。
func TestFmtCnDateTime(t *testing.T) {
	if got := FmtCnDateTime("1761542400000"); got == "" {
		t.Fatalf("毫秒时间戳应可解析")
	}
	if got := FmtCnDateTime("1761542400"); got == "" {
		t.Fatalf("秒时间戳应可解析")
	}
	if got := FmtCnDateTime("2026-10-27 08:30:00"); got != "2026-10-27 08:30:00" {
		t.Fatalf("DATETIME 应原样: %q", got)
	}
	if got := FmtCnDateTime(""); got != "" {
		t.Fatalf("空值应为空串: %q", got)
	}
	if got := FmtCnDateTime("not-a-date"); got != "" {
		t.Fatalf("不可解析应为空串: %q", got)
	}
}

// TestNextDailyRun 下次触发计算（UTC+8 口径）。
func TestNextDailyRun(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	// 北京时间 2026-10-27 10:00 → 当天 11:00（UTC 03:00）
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, cst)
	next := nextDailyRun(now, 11, 0)
	want := time.Date(2026, 10, 27, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%v want=%v", next, want)
	}
	// 北京时间 2026-10-27 12:00 → 次日 11:00
	now2 := time.Date(2026, 10, 27, 12, 0, 0, 0, cst)
	next2 := nextDailyRun(now2, 11, 0)
	if next2.Equal(want) {
		t.Fatalf("过点应排次日: %v", next2)
	}
}

// TestParseHHMM 非法时间回退默认。
func TestParseHHMM(t *testing.T) {
	if h, m := parseHHMM("18:30", 11, 0); h != 18 || m != 30 {
		t.Fatalf("解析不符: %d:%d", h, m)
	}
	if h, m := parseHHMM("bad", 11, 0); h != 11 || m != 0 {
		t.Fatalf("应退回默认: %d:%d", h, m)
	}
	if h, m := parseHHMM("0:15", 11, 0); h != 0 || m != 15 {
		t.Fatalf("0 点应生效（不能用 || 判缺省）: %d:%d", h, m)
	}
}

// TestMissingEndRows 缺结束卡查询解析 + 剔除名单。
func TestMissingEndRows(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	s := NewScheduler(m)

	// 一行：王五（已打开始卡 08:30、未打结束卡）
	fdb.onRowsAlways("HAVING MAX", []string{"team_id", "member_id", "name", "user_id", "start_hm"},
		[]driver.Value{int64(1), int64(11), "王五", int64(7), "08:30"})
	rows := s.MissingEndRows(context.Background(), "2026-10-27", nil)
	if len(rows) != 1 || rows[0].Name != "王五" || rows[0].StartHM != "08:30" {
		t.Fatalf("缺结束卡行不符: %+v", rows)
	}
	// 带剔除名单时 SQL 含 NOT IN
	s.MissingEndRows(context.Background(), "2026-10-27", []int64{11, 12})
	found := false
	for _, c := range fdb.callsWith("NOT IN") {
		if len(c.args) >= 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("剔除名单应进 SQL 参数")
	}
}

// TestBeforeRemindFailureExclude 提醒前同步失败成员剔除 + 转人工核查。
func TestBeforeRemindFailureExclude(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	notifier := &fakeNotifier{}
	m.Notifier = notifier
	// 成员 11（tk1）登录态失效（dayNew 401）；成员 12 正常。并发后按 token 区分，
	// 不依赖调用先后（旧实现串行时用调用序号，并发下顺序不确定）。
	api := &recordingAPI{onDay: func(token string) map[string]any {
		if token == "tk1" {
			return map[string]any{"statusCode": 401}
		}
		return map[string]any{"statusCode": 200}
	}}
	m.API = api

	fdb.onRowsAlways("JOIN worklog_sgcc_account a ON a.member_id = m.id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status", "card_id"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "138", "tk1", "d", "s", int64(1), int64(11)},
		[]driver.Value{int64(2), int64(1), int64(8), int64(12), "139", "tk2", "d", "s", int64(1), int64(12)},
	)
	fdb.onRowsAlways("UPDATE worklog_sgcc_account", []string{"v"})
	fdb.onRowsAlways("INSERT INTO worklog_sync_log", []string{"v"})
	fdb.onRowsAlways("SELECT id, seq, detail_id FROM worklog_clockin", []string{"id", "seq", "detail_id"})

	s := NewScheduler(m)
	exclude := s.BeforeClockinRemind(context.Background(), "2026-10-27", "开始打卡")
	if len(exclude) != 1 || exclude[0] != 11 {
		t.Fatalf("应剔除同步失败成员 11: %v", exclude)
	}
	if notifier.manualChecks != 1 {
		t.Fatalf("应发一次人工核查通知: %d", notifier.manualChecks)
	}
}

// recordingAPI 按 token 返回 dayNew 结果的 fake（并发下顺序不确定，不能用调用序号）。
type recordingAPI struct {
	fakeAPI
	onDay func(token string) map[string]any
}

func (r *recordingAPI) DayNew(_ context.Context, token, _ string, _ CallOpts) (map[string]any, error) {
	return r.onDay(token), nil
}

// ---- 排程生命周期测试 ----

// fakeTimer 手动可控定时器：记录排程与取消，测试手动触发（不真睡）。
type fakeTimer struct {
	mu      sync.Mutex
	seq     int
	entries map[int]*fakeTimerEntry
}

type fakeTimerEntry struct {
	fn       func()
	canceled bool
}

func newFakeTimer() *fakeTimer {
	return &fakeTimer{entries: map[int]*fakeTimerEntry{}}
}

func (t *fakeTimer) AfterFunc(_ time.Duration, fn func()) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	id := t.seq
	t.entries[id] = &fakeTimerEntry{fn: fn}
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if e, ok := t.entries[id]; ok {
			e.canceled = true
		}
	}
}

// liveCount 未取消的排程数。
func (t *fakeTimer) liveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, e := range t.entries {
		if !e.canceled {
			n++
		}
	}
	return n
}

// fireAll 触发全部未取消排程的 fn（模拟一次性定时器到点消费）。
func (t *fakeTimer) fireAll() {
	t.mu.Lock()
	fns := make([]func(), 0)
	for _, e := range t.entries {
		if !e.canceled {
			e.canceled = true
			fns = append(fns, e.fn)
		}
	}
	t.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// fakeCfgStore 测试用运行时配置桩（支持变更订阅，模拟 config.Store）。
type fakeCfgStore struct {
	mapCfg
	listeners []config.Listener
}

func (c *fakeCfgStore) OnChange(l config.Listener) {
	c.listeners = append(c.listeners, l)
}

// fire 更新值并触发变更监听。
func (c *fakeCfgStore) fire(key, v string) {
	c.set(key, v)
	for _, l := range c.listeners {
		l(config.ChangeEvent{Key: key, NewValue: v, Source: config.SourceUpdate})
	}
}

// TestSchedulerLifecycle 排程生命周期：Start 三槽排程 → 配置变更热重排（旧定时器取消）→
// Stop 幂等取消全部，停止后到点不执行也不再自排。
func TestSchedulerLifecycle(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	cfg := &fakeCfgStore{mapCfg: mapCfg{m: map[string]string{
		CfgStartRemindTime: "11:00",
		CfgEndRemindTime:   "18:00",
		CfgSyncTime:        "23:00",
	}}}
	m.Cfg = cfg
	timer := newFakeTimer()
	s := NewScheduler(m)
	s.Timer = timer

	stopFn := s.Start()
	if got := timer.liveCount(); got != 3 {
		t.Fatalf("启动应排 3 项定时: %d", got)
	}
	// 配置变更 → 热重排：旧 3 项取消、新 3 项排上
	cfg.fire(CfgStartRemindTime, "12:00")
	if got := timer.liveCount(); got != 3 {
		t.Fatalf("热重排后应仍有 3 项进行中: %d", got)
	}
	canceled := 0
	timer.mu.Lock()
	for _, e := range timer.entries {
		if e.canceled {
			canceled++
		}
	}
	total := len(timer.entries)
	timer.mu.Unlock()
	if canceled != 3 || total != 6 {
		t.Fatalf("旧定时器应取消并重排: canceled=%d total=%d", canceled, total)
	}
	// 到点执行后自排次日
	fdb.onRowsAlways("JOIN worklog_sgcc_account a ON a.member_id = m.id", []string{"v"})
	timer.fireAll()
	if got := timer.liveCount(); got != 3 {
		t.Fatalf("执行完应自排 3 项: %d", got)
	}
	// Stop 幂等，全部取消
	stopFn()
	stopFn()
	if got := timer.liveCount(); got != 0 {
		t.Fatalf("停止后不应有进行中定时: %d", got)
	}
	// 已停止：残留 fn 到点也不执行、不再自排
	before := timer.seq
	timer.mu.Lock()
	for _, e := range timer.entries {
		e.canceled = false
	}
	fns := make([]func(), 0)
	for _, e := range timer.entries {
		fns = append(fns, e.fn)
	}
	timer.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
	if timer.seq != before {
		t.Fatalf("停止后不应再排新定时")
	}
}

// TestSchedulerStartIdempotent 重复 Start 不重复排程；Reconfigure 未启动为无操作。
func TestSchedulerStartIdempotent(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	timer := newFakeTimer()
	s := NewScheduler(m)
	s.Timer = timer
	s.Reconfigure() // 未启动：无操作
	stop1 := s.Start()
	stop2 := s.Start()
	if got := timer.liveCount(); got != 3 {
		t.Fatalf("重复 Start 不应重复排程: %d", got)
	}
	stop1()
	_ = stop2
	if got := timer.liveCount(); got != 0 {
		t.Fatalf("停止后残留定时: %d", got)
	}
}

// TestDailyCheckRestAccountsRowsClosed 每日核查「其余账号登录态保鲜」必须先关闭查询游标再逐号探测：
// db.SetMaxOpenConns(1) 下持 Rows 写库会永久等待，probeAuth 的 UPDATE 无法执行。
func TestDailyCheckRestAccountsRowsClosed(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	m := New(db)
	// m.API = nil：probeAuth 只落库 token_status（不触网络），写库即本用例唯一游标-写库竞争点
	fdb.onRowsAlways("SELECT DISTINCT team_id", []string{"team_id"}, []driver.Value{int64(1)})
	fdb.onRowsAlways("FROM worklog_sgcc_account a", []string{
		"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(5), int64(1), int64(9), int64(13), "138", "tk-rest", "d", "s", int64(1)})
	s := NewScheduler(m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.DailyCheckJob(ctx)
	if got := len(fdb.callsWith("UPDATE worklog_sgcc_account SET token_status")); got != 1 {
		t.Fatalf("其余账号登录态保鲜应落库一次（先关游标再探测）: %d", got)
	}
}
