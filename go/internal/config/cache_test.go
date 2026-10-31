package config

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- 有状态假库：Seed/Update 后 Get 能读到新值，用于验证缓存失效顺序 ----

type kvRow struct {
	value     string
	sensitive bool
}

type kvDB struct {
	mu   sync.Mutex
	rows map[string]kvRow
}

func newKVDB() *kvDB { return &kvDB{rows: map[string]kvRow{}} }

func (d *kvDB) set(key, value string, sensitive bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows[key] = kvRow{value: value, sensitive: sensitive}
}

func (d *kvDB) install(fdb *fakeDB) {
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		key, _ := args[0].(string)
		switch {
		case strings.Contains(q, "SELECT cfg_key"):
			if _, ok := d.rows[key]; !ok {
				return &fakeRows{cols: []string{"cfg_key"}}, nil
			}
			return rowsOf([]string{"cfg_key"}, []driver.Value{key}), nil
		case strings.Contains(q, "SELECT cfg_value"):
			row, ok := d.rows[key]
			if !ok {
				return &fakeRows{cols: []string{"cfg_value", "is_sensitive"}}, nil
			}
			sens := int64(0)
			if row.sensitive {
				sens = 1
			}
			return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{row.value, sens}), nil
		}
		return &fakeRows{cols: []string{"v"}}, nil
	}
	fdb.onExec = func(q string, args []driver.Value) (driver.Result, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case strings.Contains(q, "INSERT INTO sys_runtime_config"):
			key, _ := args[0].(string)
			value, _ := args[1].(string)
			sens, _ := args[2].(int64)
			d.rows[key] = kvRow{value: value, sensitive: sens != 0}
		case strings.Contains(q, "UPDATE sys_runtime_config"):
			value, _ := args[0].(string)
			key, _ := args[1].(string)
			row := d.rows[key]
			row.value = value
			d.rows[key] = row
		}
		return fakeResult(1), nil
	}
}

// cfgQueryCount 统计读取配置行（cfg_value）的 SQL 次数。
func cfgQueryCount(fdb *fakeDB) int {
	fdb.mu.Lock()
	defer fdb.mu.Unlock()
	n := 0
	for _, c := range fdb.queries {
		if strings.Contains(c.query, "SELECT cfg_value") {
			n++
		}
	}
	return n
}

// cacheSize 读取缓存条目数（测试断言上限用）。
func cacheSize(st *Store) int {
	st.cacheMu.RLock()
	defer st.cacheMu.RUnlock()
	return len(st.cache)
}

func TestStoreCacheDefaults(t *testing.T) {
	st := NewStore(nil, nil)
	if st.cacheTTL != 60*time.Second {
		t.Fatalf("正值缓存 TTL = %v，期望 60s", st.cacheTTL)
	}
	if st.negTTL != 10*time.Second {
		t.Fatalf("负缓存 TTL = %v，期望 10s", st.negTTL)
	}
	if st.maxKeys != 512 {
		t.Fatalf("缓存键上限 = %d，期望 512", st.maxKeys)
	}
	if st.loadTO != 5*time.Second {
		t.Fatalf("共享查库预算 = %v，期望 5s", st.loadTO)
	}
}

func TestStoreCacheSameKeyQueriedOnce(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("k", "v1", false)
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := st.Get(ctx, "k"); err != nil || v != "v1" {
			t.Fatalf("第 %d 次 Get = %q %v", i+1, v, err)
		}
	}
	// PublicValue 复用同一份缓存，不再查库
	if v, err := st.PublicValue(ctx, "k"); err != nil || v != "v1" {
		t.Fatalf("PublicValue = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("同键查询次数 = %d，期望 1", got)
	}
	if got := cacheSize(st); got != 1 {
		t.Fatalf("缓存条目 = %d，期望 1", got)
	}
	// 不同键各自查一次
	state.set("k2", "v2", false)
	if v, err := st.Get(ctx, "k2"); err != nil || v != "v2" {
		t.Fatalf("Get(k2) = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 2 {
		t.Fatalf("不同键查询次数 = %d，期望 2", got)
	}
}

func TestStoreCacheEmptyValueHit(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("empty", "", false)
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	if v, err := st.Get(ctx, "empty"); err != nil || v != "" {
		t.Fatalf("Get(empty) = %q %v", v, err)
	}
	if v, err := st.Get(ctx, "empty"); err != nil || v != "" {
		t.Fatalf("二次 Get(empty) = %q %v", v, err)
	}
	if v, err := st.PublicValue(ctx, "empty"); err != nil || v != "" {
		t.Fatalf("PublicValue(empty) = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("空值命中查询次数 = %d，期望 1", got)
	}
}

func TestStoreCacheNegativeHit(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := st.Get(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("第 %d 次 Get(missing) = %v，期望 sql.ErrNoRows", i+1, err)
		}
	}
	if _, err := st.PublicValue(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("PublicValue(missing) = %v，期望 sql.ErrNoRows", err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("缺失键查询次数 = %d，期望 1（负缓存）", got)
	}
}

func TestStoreCacheDBErrorNotCached(t *testing.T) {
	sdb, fdb := openFake(t)
	fail := true
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if fail {
			return nil, errors.New("boom")
		}
		return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{"v", int64(0)}), nil
	}
	st := NewStore(sdb, nil)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := st.Get(ctx, "k"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("第 %d 次 Get 错误 = %v，期望 boom", i+1, err)
		}
	}
	if got := cfgQueryCount(fdb); got != 2 {
		t.Fatalf("数据库错误查询次数 = %d，期望 2（错误不缓存）", got)
	}
	fail = false
	if v, err := st.Get(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("恢复后 Get = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 3 {
		t.Fatalf("恢复后查询次数 = %d，期望 3", got)
	}
}

func TestStoreUpdateInvalidatesBeforeNotify(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("k", "old", false)
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	if v, err := st.Get(ctx, "k"); err != nil || v != "old" {
		t.Fatalf("Get(old) = %q %v", v, err)
	}
	var seen string
	var seenErr error
	st.OnChange(func(ChangeEvent) {
		seen, seenErr = st.Get(context.Background(), "k")
	})
	if err := st.Update(ctx, "k", "new"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if seenErr != nil || seen != "new" {
		t.Fatalf("监听回调内 Get = %q %v，期望 new（先失效再 notify）", seen, seenErr)
	}
	// 初始 Get(1) + Update 读旧值(1) + 回调 Get 重新读(1)
	if got := cfgQueryCount(fdb); got != 3 {
		t.Fatalf("查询次数 = %d，期望 3", got)
	}
	if v, err := st.Get(ctx, "k"); err != nil || v != "new" {
		t.Fatalf("Update 后 Get = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 3 {
		t.Fatalf("Update 后 Get 查询次数 = %d，期望仍为 3（命中缓存）", got)
	}
}

func TestStoreSeedInvalidatesBeforeNotify(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	// 先负缓存该键
	if _, err := st.Get(ctx, "seed.key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get(seed.key) = %v，期望 sql.ErrNoRows", err)
	}
	var seen string
	var seenErr error
	st.OnChange(func(ChangeEvent) {
		seen, seenErr = st.Get(context.Background(), "seed.key")
	})
	n, err := st.SeedFromEnv(ctx, []Spec{{Key: "seed.key", Env: "SEED_KEY"}}, func(string) string {
		return "env-value"
	})
	if err != nil || n != 1 {
		t.Fatalf("SeedFromEnv = %d %v", n, err)
	}
	if seenErr != nil || seen != "env-value" {
		t.Fatalf("监听回调内 Get = %q %v，期望 env-value（先失效再 notify）", seen, seenErr)
	}
	if got := cfgQueryCount(fdb); got != 2 {
		t.Fatalf("查询次数 = %d，期望 2（负缓存 1 + 回调重读 1）", got)
	}
}

func TestStoreCacheTTL(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("k", "v", false)
	state.install(fdb)
	st := NewStore(sdb, nil)
	now := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return now }
	ctx := context.Background()

	if v, err := st.Get(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("Get = %q %v", v, err)
	}
	now = now.Add(59 * time.Second)
	if v, err := st.Get(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("59s 内 Get = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("59s 内查询次数 = %d，期望 1", got)
	}
	// 跨过 60s：重新查库
	state.set("k", "v2", false)
	now = now.Add(2 * time.Second)
	if v, err := st.Get(ctx, "k"); err != nil || v != "v2" {
		t.Fatalf("60s 后 Get = %q %v，期望 v2", v, err)
	}
	if got := cfgQueryCount(fdb); got != 2 {
		t.Fatalf("60s 后查询次数 = %d，期望 2", got)
	}
}

func TestStoreCacheNegativeTTL(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.install(fdb)
	st := NewStore(sdb, nil)
	now := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := st.Get(ctx, "k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get = %v，期望 sql.ErrNoRows", err)
	}
	now = now.Add(9 * time.Second)
	if _, err := st.Get(ctx, "k"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("9s 内 Get = %v，期望 sql.ErrNoRows", err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("9s 内查询次数 = %d，期望 1", got)
	}
	// 负缓存 10s 后过期：重新查库并命中新行
	state.set("k", "created", false)
	now = now.Add(2 * time.Second)
	if v, err := st.Get(ctx, "k"); err != nil || v != "created" {
		t.Fatalf("10s 后 Get = %q %v，期望 created", v, err)
	}
	if got := cfgQueryCount(fdb); got != 2 {
		t.Fatalf("10s 后查询次数 = %d，期望 2", got)
	}
}

func TestStoreCacheMaxKeys(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.install(fdb)
	st := NewStore(sdb, nil)
	ctx := context.Background()

	const total = 600
	for i := 0; i < total; i++ {
		state.set(fmt.Sprintf("k%03d", i), "v", false)
	}
	for i := 0; i < total; i++ {
		if _, err := st.Get(ctx, fmt.Sprintf("k%03d", i)); err != nil {
			t.Fatalf("Get(k%03d): %v", i, err)
		}
	}
	if got := cacheSize(st); got > 512 {
		t.Fatalf("缓存条目 = %d，超过上限 512", got)
	}
	// 最近写入的键仍在缓存
	before := cfgQueryCount(fdb)
	if _, err := st.Get(ctx, fmt.Sprintf("k%03d", total-1)); err != nil {
		t.Fatalf("Get(最近键): %v", err)
	}
	if got := cfgQueryCount(fdb); got != before {
		t.Fatalf("最近键重复查询（%d → %d）", before, got)
	}
}

func TestStorePublicValueMasksSensitive(t *testing.T) {
	sdb, fdb := openFake(t)
	cipher, err := NewConfigCipher("", "unit-jwt")
	if err != nil {
		t.Fatalf("NewConfigCipher: %v", err)
	}
	enc, err := cipher.Encrypt("top-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	state := newKVDB()
	state.set("secret", enc, true)
	state.install(fdb)
	st := NewStore(sdb, cipher)
	ctx := context.Background()

	if v, err := st.Get(ctx, "secret"); err != nil || v != "top-secret" {
		t.Fatalf("Get(secret) = %q %v", v, err)
	}
	if v, err := st.PublicValue(ctx, "secret"); err != nil || v != MaskSecret("top-secret") {
		t.Fatalf("PublicValue(secret) = %q %v", v, err)
	}
	// 脱敏结果不入缓存：Get 仍应返回明文
	if v, err := st.Get(ctx, "secret"); err != nil || v != "top-secret" {
		t.Fatalf("再 Get(secret) = %q %v", v, err)
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("敏感键查询次数 = %d，期望 1", got)
	}
}

func TestStoreCacheConcurrentSameKey(t *testing.T) {
	sdb, fdb := openFake(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		once.Do(func() { close(entered) })
		<-release
		return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{"v", int64(0)}), nil
	}
	st := NewStore(sdb, nil)

	const n = 64
	vals := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vals[i], errs[i] = st.Get(context.Background(), "k")
		}(i)
	}
	<-entered
	// 让其余 goroutine 尽量都进入等待（即便有个别晚到，也应命中缓存）
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil || vals[i] != "v" {
			t.Fatalf("goroutine %d = %q %v", i, vals[i], errs[i])
		}
	}
	if got := cfgQueryCount(fdb); got != 1 {
		t.Fatalf("64 goroutine 同键查询次数 = %d，期望 1（single-flight）", got)
	}
}

func TestStoreCacheConcurrentMixed(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("a", "va", false)
	state.set("b", "vb", true)
	state.install(fdb)
	cipher, err := NewConfigCipher("", "unit-jwt")
	if err != nil {
		t.Fatalf("NewConfigCipher: %v", err)
	}
	st := NewStore(sdb, cipher)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				key := "a"
				if j%2 == 1 {
					key = "b"
				}
				switch i % 3 {
				case 0:
					_, _ = st.Get(ctx, key)
				case 1:
					_, _ = st.PublicValue(ctx, key)
				default:
					_ = st.Update(ctx, key, fmt.Sprintf("v-%d-%d", i, j))
				}
			}
		}(i)
	}
	wg.Wait()
	if _, err := st.Get(ctx, "a"); err != nil {
		t.Fatalf("并发后 Get(a): %v", err)
	}
}

// ---- 并发边界：失效代数（旧 flight 不得回填 / follower 重试）与 leader 取消隔离 ----

// TestStoreStaleFlightNotPublishedAfterUpdate 旧 flight 在 Update 失效后完成时不得回填缓存；
// notify 内的 Get 必须读到新值（而不是加入旧 flight 读旧值）。
func TestStoreStaleFlightNotPublishedAfterUpdate(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("k", "old", false)
	state.install(fdb)
	origQuery := fdb.onQuery

	var calls int32
	entered := make(chan struct{})
	release := make(chan struct{})
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(q, "SELECT cfg_value") && atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
			return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{"old", int64(0)}), nil
		}
		return origQuery(q, args)
	}
	st := NewStore(sdb, nil)
	ctx := context.Background()

	var leaderV string
	var leaderErr error
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		leaderV, leaderErr = st.Get(ctx, "k")
	}()
	<-entered

	var seenV string
	var seenErr error
	listenerEntered := make(chan struct{})
	st.OnChange(func(ChangeEvent) {
		close(listenerEntered)
		seenV, seenErr = st.Get(context.Background(), "k")
	})

	updateDone := make(chan error, 1)
	go func() { updateDone <- st.Update(ctx, "k", "new") }()
	<-listenerEntered
	// 让监听方进入旧 flight 等待（修复后它会因代数变化立即重读新值）
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-updateDone; err != nil {
		t.Fatalf("Update: %v", err)
	}
	<-leaderDone

	if seenErr != nil || seenV != "new" {
		t.Fatalf("notify 内 Get = %q %v，期望 new（旧 flight 不得回填）", seenV, seenErr)
	}
	if leaderErr != nil || leaderV != "old" {
		t.Fatalf("旧 leader 结果 = %q %v，期望 old", leaderV, leaderErr)
	}
	if v, err := st.Get(ctx, "k"); err != nil || v != "new" {
		t.Fatalf("Update 后 Get = %q %v，期望 new", v, err)
	}
	if got := cfgQueryCount(fdb); got != 3 {
		t.Fatalf("查询次数 = %d，期望 3（旧 flight 1 + Update 读旧值 1 + 监听重读 1）", got)
	}
}

// TestStoreWaitingFollowerRetriesAfterInvalidate 已在等待旧 flight 的 follower，
// 在失效后醒来必须重试并读到新值，而不是复用旧 flight 的结果。
func TestStoreWaitingFollowerRetriesAfterInvalidate(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newKVDB()
	state.set("k", "old", false)
	state.install(fdb)
	origQuery := fdb.onQuery

	var calls int32
	entered := make(chan struct{})
	release := make(chan struct{})
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if strings.Contains(q, "SELECT cfg_value") && atomic.AddInt32(&calls, 1) == 1 {
			close(entered)
			<-release
			return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{"old", int64(0)}), nil
		}
		return origQuery(q, args)
	}
	st := NewStore(sdb, nil)
	ctx := context.Background()

	var leaderV string
	var leaderErr error
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		leaderV, leaderErr = st.Get(ctx, "k")
	}()
	<-entered

	var followerV string
	var followerErr error
	followerDone := make(chan struct{})
	go func() {
		defer close(followerDone)
		followerV, followerErr = st.Get(ctx, "k")
	}()
	// 让 follower 加入旧 flight 等待
	time.Sleep(50 * time.Millisecond)

	if err := st.Update(ctx, "k", "new"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	close(release)
	<-leaderDone
	<-followerDone

	if followerErr != nil || followerV != "new" {
		t.Fatalf("follower = %q %v，期望 new（旧 flight 结果不得复用）", followerV, followerErr)
	}
	if leaderErr != nil || leaderV != "old" {
		t.Fatalf("旧 leader 结果 = %q %v，期望 old", leaderV, leaderErr)
	}
	if got := cfgQueryCount(fdb); got != 3 {
		t.Fatalf("查询次数 = %d，期望 3（旧 flight 1 + Update 读旧值 1 + follower 重试 1）", got)
	}
}

// ---- 上下文感知假驱动：让 QueryContext 真正响应 ctx 取消（旧 fake 驱动是 legacy Queryer）----

type blockingQueryState struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	value   string
	calls   int32
}

type blockingQueryDriver struct{ state *blockingQueryState }

func (d *blockingQueryDriver) Open(string) (driver.Conn, error) {
	return &blockingQueryConn{state: d.state}, nil
}

type blockingQueryConn struct{ state *blockingQueryState }

func (c *blockingQueryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: 不支持 Prepare")
}
func (c *blockingQueryConn) Close() error { return nil }
func (c *blockingQueryConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake: 事务未实现")
}

func (c *blockingQueryConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	atomic.AddInt32(&c.state.calls, 1)
	c.state.once.Do(func() { close(c.state.entered) })
	select {
	case <-c.state.release:
		return rowsOf([]string{"cfg_value", "is_sensitive"}, []driver.Value{c.state.value, int64(0)}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var blockingQuerySeq int64

func openBlockingQueryDB(t *testing.T, state *blockingQueryState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("blocking-query-%d", atomic.AddInt64(&blockingQuerySeq, 1))
	sql.Register(name, &blockingQueryDriver{state: state})
	sdb, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开假库失败: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	return sdb
}

// TestStoreLeaderCancelDoesNotFailFollower leader 的 ctx 取消只应影响其自身返回值，
// 同键等待方仍应拿到共享查库结果（共享查询使用 WithoutCancel + 短预算）。
func TestStoreLeaderCancelDoesNotFailFollower(t *testing.T) {
	state := &blockingQueryState{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		value:   "v",
	}
	sdb := openBlockingQueryDB(t, state)
	st := NewStore(sdb, nil)

	leaderCtx, leaderCancel := context.WithCancel(context.Background())
	var leaderErr error
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, leaderErr = st.Get(leaderCtx, "k")
	}()
	<-state.entered

	var followerV string
	var followerErr error
	followerDone := make(chan struct{})
	go func() {
		defer close(followerDone)
		followerV, followerErr = st.Get(context.Background(), "k")
	}()
	// 让 follower 加入同一 flight 等待
	time.Sleep(50 * time.Millisecond)

	leaderCancel()
	// 确保取消先于放行生效：旧实现下 leader 的 QueryContext 会以 ctx 错误结束
	time.Sleep(20 * time.Millisecond)
	close(state.release)
	<-leaderDone
	<-followerDone

	if followerErr != nil || followerV != "v" {
		t.Fatalf("follower = %q %v，期望 v（leader 取消不得扩散）", followerV, followerErr)
	}
	if !errors.Is(leaderErr, context.Canceled) {
		t.Fatalf("leader 错误 = %v，期望 context.Canceled（仅影响自身）", leaderErr)
	}
	if got := atomic.LoadInt32(&state.calls); got != 1 {
		t.Fatalf("查询次数 = %d，期望 1（leader 与 follower 共享一次查库）", got)
	}
}
