package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 缓存口径：命中值与缺失（sql.ErrNoRows）分别缓存 60s / 10s，最多 512 键；
// 数据库错误不缓存，Update/SeedFromEnv 先失效缓存再通知监听方。
// 共享查库与调用方 ctx 解耦，用短预算兜底（leader 取消不得扩散给同键等待方）。
const (
	defaultCacheTTL        = 60 * time.Second
	defaultNegativeTTL     = 10 * time.Second
	defaultCacheMaxKeys    = 512
	defaultCacheLoadBudget = 5 * time.Second
)

// RuntimeConfigSchemaSQL sys_runtime_config 建表 SQL（敏感值以 enc:v1: AES-256-GCM 密文落库）
const RuntimeConfigSchemaSQL = `
CREATE TABLE IF NOT EXISTS sys_runtime_config (
  cfg_key VARCHAR(191) NOT NULL COMMENT '配置键',
  cfg_value TEXT NOT NULL COMMENT '配置值（敏感项为 enc:v1: AES-256-GCM 密文）',
  is_sensitive TINYINT NOT NULL DEFAULT 0 COMMENT '是否敏感（1=脱敏展示/加密存储）',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (cfg_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='运行时配置（库内可覆盖 env）'
`

// Spec 单个运行时配置键规格
type Spec struct {
	// Key 库内配置键（cfg_key）
	Key string
	// Env 对应环境变量名（首次导入取值来源）
	Env string
	// Sensitive 敏感项：加密存储、公开值脱敏
	Sensitive bool
	// Default env 未配置时的缺省值（原样存，不做加密以外的转换）
	Default string
}

// ChangeSource 变更来源
type ChangeSource string

const (
	// SourceEnv 首次 env 补缺值导入
	SourceEnv ChangeSource = "env"
	// SourceUpdate 接口/管理端更新
	SourceUpdate ChangeSource = "update"
)

// ChangeEvent 配置变更事件（值均为解密后的明文，供监听方使用；对外展示仍走脱敏口径）
type ChangeEvent struct {
	Key       string
	OldValue  string
	NewValue  string
	Sensitive bool
	Source    ChangeSource
}

// Listener 变更监听回调
type Listener func(ChangeEvent)

// BatchListener 批量变更监听：一次 UpdateBatch 成功后只回调一次（含全部变更项）。
type BatchListener func([]ChangeEvent)

// Update 批量更新的单项（键 + 明文新值）。
type Update struct {
	Key   string
	Value string
}

// Store sys_runtime_config 访问器（带进程内缓存：同键并发读取合并为一次查库）
type Store struct {
	db             *sql.DB
	cipher         *ConfigCipher
	mu             sync.RWMutex
	listeners      []Listener
	batchListeners []BatchListener

	cacheMu     sync.RWMutex
	cache       map[string]cacheEntry
	flights     map[string]*cacheFlight
	generations map[string]uint64 // 单键失效代数：旧 flight 不得回填、等待方须重试
	now         func() time.Time
	cacheTTL    time.Duration
	negTTL      time.Duration
	maxKeys     int
	loadTO      time.Duration
}

// cacheEntry 单键缓存项：value 为解密后的明文，sensitive 决定 PublicValue 读出时是否脱敏
type cacheEntry struct {
	value     string
	sensitive bool
	err       error
	expires   time.Time
}

// cacheFlight 同键进行中的查库调用；followers 等 done 关闭后复用 entry。
// gen 为创建时的单键失效代数，finish 时据此决定是否回填缓存。
type cacheFlight struct {
	done  chan struct{}
	gen   uint64
	entry cacheEntry
}

// NewStore 构造访问器（cipher 仅敏感项需要；nil 时敏感项读写报 ErrNoMasterKey）
func NewStore(db *sql.DB, cipher *ConfigCipher) *Store {
	return &Store{
		db:          db,
		cipher:      cipher,
		cache:       map[string]cacheEntry{},
		flights:     map[string]*cacheFlight{},
		generations: map[string]uint64{},
		cacheTTL:    defaultCacheTTL,
		negTTL:      defaultNegativeTTL,
		maxKeys:     defaultCacheMaxKeys,
		loadTO:      defaultCacheLoadBudget,
	}
}

// OnChange 注册变更监听（SeedFromEnv / Update 成功后异步前同步回调；回调内勿再写库）
func (s *Store) OnChange(l Listener) {
	if l == nil {
		return
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, l)
	s.mu.Unlock()
}

// OnChangeBatch 注册批量变更监听（UpdateBatch 成功后一次回调；单键 Update/Seed 不触发）。
func (s *Store) OnChangeBatch(l BatchListener) {
	if l == nil {
		return
	}
	s.mu.Lock()
	s.batchListeners = append(s.batchListeners, l)
	s.mu.Unlock()
}

func (s *Store) notify(ev ChangeEvent) {
	s.mu.RLock()
	ls := append([]Listener(nil), s.listeners...)
	s.mu.RUnlock()
	for _, l := range ls {
		l(ev)
	}
}

// notifyBatch 一次批量通知：批量监听只回调一次；兼容的单键监听仍逐项收到事件，
// 保证既有热更新（微信智能应答重连等）不被批量写入绕过。
func (s *Store) notifyBatch(events []ChangeEvent) {
	if len(events) == 0 {
		return
	}
	s.mu.RLock()
	bl := append([]BatchListener(nil), s.batchListeners...)
	ls := append([]Listener(nil), s.listeners...)
	s.mu.RUnlock()
	for _, l := range bl {
		l(append([]ChangeEvent(nil), events...))
	}
	for _, ev := range events {
		for _, l := range ls {
			l(ev)
		}
	}
}

// clock 当前时间（测试注入 now 以验证 TTL）。
func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Store) positiveTTL() time.Duration {
	if s.cacheTTL > 0 {
		return s.cacheTTL
	}
	return defaultCacheTTL
}

func (s *Store) negativeTTL() time.Duration {
	if s.negTTL > 0 {
		return s.negTTL
	}
	return defaultNegativeTTL
}

func (s *Store) cacheLimit() int {
	if s.maxKeys > 0 {
		return s.maxKeys
	}
	return defaultCacheMaxKeys
}

// cachedEntry 取单键（明文 + 敏感标记）：先查缓存，未命中走 per-key single-flight 合并并发查库。
// 仅成功读取（含 sql.ErrNoRows 负缓存）入缓存；其它数据库错误原样返回且不缓存。
//
// 并发边界：
//   - per-key 失效代数：invalidate（Update/Seed）后旧 flight 的结果不得回填；正在等待旧 flight
//     的 follower 发现代数变化必须重试，保证 notify 内的 Get 读到新值。
//   - leader 的 ctx 取消只影响其自身返回值：共享查库使用 WithoutCancel + 短预算，不向等待方扩散。
func (s *Store) cachedEntry(ctx context.Context, key string) (cacheEntry, error) {
	for {
		if err := ctx.Err(); err != nil {
			// 调用方已取消：不再新建/加入 flight（重试循环同样受此约束）
			return cacheEntry{}, err
		}
		now := s.clock()
		s.cacheMu.RLock()
		if e, ok := s.cache[key]; ok && now.Before(e.expires) {
			s.cacheMu.RUnlock()
			return e, e.err
		}
		s.cacheMu.RUnlock()

		s.cacheMu.Lock()
		// 复查：等锁期间可能已被其它调用填充
		if e, ok := s.cache[key]; ok && s.clock().Before(e.expires) {
			s.cacheMu.Unlock()
			return e, e.err
		}
		gen := s.generations[key]
		if f, ok := s.flights[key]; ok && f.gen == gen {
			s.cacheMu.Unlock()
			select {
			case <-f.done:
				// 等待期间发生失效：该 flight 结果已过时，重试获取新值
				if s.generationOf(key) != f.gen {
					continue
				}
				return f.entry, f.entry.err
			case <-ctx.Done():
				return cacheEntry{}, ctx.Err()
			}
		}
		if s.flights == nil {
			s.flights = map[string]*cacheFlight{}
		}
		// 旧代 flight 仍占用该键时直接替换：它完成时按身份与代数检查放弃回填，
		// 其等待方醒来后因代数变化重试。
		f := &cacheFlight{done: make(chan struct{}), gen: gen}
		s.flights[key] = f
		s.cacheMu.Unlock()

		e := s.loadFlight(ctx, key, f)
		// 调用方自身取消：只把错误返回给该调用方，不影响已发布给等待方的结果
		if err := ctx.Err(); err != nil && e.err == nil {
			return cacheEntry{}, err
		}
		return e, e.err
	}
}

// generationOf 读单键失效代数。
func (s *Store) generationOf(key string) uint64 {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	return s.generations[key]
}

func (s *Store) loadTimeout() time.Duration {
	if s.loadTO > 0 {
		return s.loadTO
	}
	return defaultCacheLoadBudget
}

// loadFlight 执行共享查库并发布结果；defer 保证 flight 一定被清理并唤醒等待方。
// 查库与调用方 ctx 解耦（WithoutCancel + 短预算）：leader 取消不拖垮 follower，也不会挂死。
func (s *Store) loadFlight(ctx context.Context, key string, f *cacheFlight) (e cacheEntry) {
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.loadTimeout())
	defer cancel()
	defer func() { s.finishFlight(key, f, e) }()
	return s.loadEntry(loadCtx, key)
}

// finishFlight 清理 flight，并仅在「仍是当前 flight 且失效代数未变」时回填缓存。
func (s *Store) finishFlight(key string, f *cacheFlight, e cacheEntry) {
	s.cacheMu.Lock()
	if s.flights[key] == f {
		delete(s.flights, key)
		if (e.err == nil || errors.Is(e.err, sql.ErrNoRows)) && s.generations[key] == f.gen {
			ttl := s.positiveTTL()
			if errors.Is(e.err, sql.ErrNoRows) {
				ttl = s.negativeTTL()
			}
			e.expires = s.clock().Add(ttl)
			s.putLocked(key, e)
		}
	}
	f.entry = e
	s.cacheMu.Unlock()
	close(f.done)
}

// loadEntry 直查数据库并解密（缓存未命中路径）。
func (s *Store) loadEntry(ctx context.Context, key string) cacheEntry {
	stored, sensitive, err := s.rowOf(ctx, key)
	if err != nil {
		return cacheEntry{err: err}
	}
	value, err := s.loadValue(stored, sensitive)
	if err != nil {
		return cacheEntry{err: err}
	}
	return cacheEntry{value: value, sensitive: sensitive}
}

// putLocked 写入缓存并维持键上限（调用方持写锁）：先淘汰已过期项，仍超限时淘汰任意旧键
// （保留本次写入键），避免缓存无界增长。
func (s *Store) putLocked(key string, e cacheEntry) {
	if s.cache == nil {
		s.cache = map[string]cacheEntry{}
	}
	s.cache[key] = e
	limit := s.cacheLimit()
	if len(s.cache) <= limit {
		return
	}
	now := s.clock()
	for k, v := range s.cache {
		if len(s.cache) <= limit {
			return
		}
		if !now.Before(v.expires) {
			delete(s.cache, k)
		}
	}
	for k := range s.cache {
		if len(s.cache) <= limit {
			return
		}
		if k != key {
			delete(s.cache, k)
		}
	}
}

// invalidate 失效单键缓存并递增失效代数（Update/Seed 成功落库后、notify 前调用）。
// 代数变化使旧 flight 不再回填，等待中的 follower 重试读取新值。
func (s *Store) invalidate(key string) {
	s.cacheMu.Lock()
	delete(s.cache, key)
	if s.generations == nil {
		s.generations = map[string]uint64{}
	}
	s.generations[key]++
	s.cacheMu.Unlock()
}

// EnsureSchema 建表（幂等）
func (s *Store) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, RuntimeConfigSchemaSQL)
	if err != nil {
		return fmt.Errorf("config: 建表失败: %w", err)
	}
	return nil
}

// storeValue 按敏感标记落库（敏感加密；加密器缺失时报错）
func (s *Store) storeValue(value string, sensitive bool) (string, error) {
	if !sensitive {
		return value, nil
	}
	if s.cipher == nil {
		return "", ErrNoMasterKey
	}
	return s.cipher.Encrypt(value)
}

// loadValue 读库值（敏感解密；旧库遗留明文按原文返回并兼容——不强制迁移）
func (s *Store) loadValue(stored string, sensitive bool) (string, error) {
	if !sensitive || !IsEncrypted(stored) {
		return stored, nil
	}
	if s.cipher == nil {
		return "", ErrNoMasterKey
	}
	return s.cipher.Decrypt(stored)
}

// SeedFromEnv 首次 env 补缺值导入：库内不存在的键按 env → Default 顺序取值写入（敏感项加密）；
// 已存在的键不动。getenv 由调用方注入（生产传 os.Getenv，测试传桩）；返回本次新导入条数。
func (s *Store) SeedFromEnv(ctx context.Context, specs []Spec, getenv func(string) string) (int, error) {
	if getenv == nil {
		return 0, errors.New("config: getenv 未注入")
	}
	seeded := 0
	for _, spec := range specs {
		if spec.Key == "" {
			return seeded, errors.New("config: Spec.Key 为空")
		}
		var exists string
		err := s.db.QueryRowContext(ctx,
			`SELECT cfg_key FROM sys_runtime_config WHERE cfg_key = ?`, spec.Key).Scan(&exists)
		switch {
		case err == nil:
			continue // 已有值：不覆盖（env 只做首次补缺）
		case errors.Is(err, sql.ErrNoRows):
			// 继续导入
		default:
			return seeded, fmt.Errorf("config: 查询 %s 失败: %w", spec.Key, err)
		}
		val := getenv(spec.Env)
		if val == "" {
			val = spec.Default
		}
		stored, err := s.storeValue(val, spec.Sensitive)
		if err != nil {
			return seeded, fmt.Errorf("config: 导入 %s 失败: %w", spec.Key, err)
		}
		sens := 0
		if spec.Sensitive {
			sens = 1
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO sys_runtime_config (cfg_key, cfg_value, is_sensitive) VALUES (?, ?, ?)`,
			spec.Key, stored, sens); err != nil {
			return seeded, fmt.Errorf("config: 导入 %s 失败: %w", spec.Key, err)
		}
		seeded++
		s.invalidate(spec.Key)
		s.notify(ChangeEvent{Key: spec.Key, OldValue: "", NewValue: val, Sensitive: spec.Sensitive, Source: SourceEnv})
	}
	return seeded, nil
}

// rowOf 读单行（值 + 敏感标记）
func (s *Store) rowOf(ctx context.Context, key string) (stored string, sensitive bool, err error) {
	return rowOfQ(ctx, s.db, key, false)
}

// rowOfQ 读单行（事务内可加 FOR UPDATE）；queries 接口由 *sql.DB / *sql.Tx 共同满足。
func rowOfQ(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string, forUpdate bool) (stored string, sensitive bool, err error) {
	query := `SELECT cfg_value, is_sensitive FROM sys_runtime_config WHERE cfg_key = ?`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var sens int64
	err = q.QueryRowContext(ctx, query, key).Scan(&stored, &sens)
	if err != nil {
		return "", false, err
	}
	return stored, sens != 0, nil
}

// Get 读配置值（敏感项返回解密明文）；键不存在返回 sql.ErrNoRows。
// 进程内缓存：命中值 60s、缺失 10s，同键并发读取合并为一次查库。
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	e, err := s.cachedEntry(ctx, key)
	if err != nil {
		return "", err
	}
	return e.value, nil
}

// PublicValue 读脱敏公开值：与 Get 共用缓存与 single-flight，敏感项读出后 MaskSecret，
// 普通项原值（管理端列表口径）；缓存内只存明文，避免 Get 读到脱敏值。
func (s *Store) PublicValue(ctx context.Context, key string) (string, error) {
	e, err := s.cachedEntry(ctx, key)
	if err != nil {
		return "", err
	}
	if !e.sensitive {
		return e.value, nil
	}
	return MaskSecret(e.value), nil
}

// UpdateBatch 事务型批量更新：先事务内逐键 SELECT ... FOR UPDATE 校验全部存在并完成加密，
// 再逐键 UPDATE，一次 Commit；任一步失败整体回滚，不产生部分写入、不触发通知。
// 成功后统一失效缓存，批量监听只回调一次，兼容单键监听逐项收到变更（监听方读到新值）。
func (s *Store) UpdateBatch(ctx context.Context, updates []Update) error {
	if len(updates) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(updates))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("config: 批量更新开启事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	type pendingUpdate struct {
		key       string
		value     string
		newStored string
		oldPlain  string
		sensitive bool
	}
	pending := make([]pendingUpdate, 0, len(updates))
	for _, u := range updates {
		if strings.TrimSpace(u.Key) == "" {
			return errors.New("config: 批量更新键不能为空")
		}
		if seen[u.Key] {
			return fmt.Errorf("config: 批量更新键重复: %s", u.Key)
		}
		seen[u.Key] = true
		stored, sensitive, err := rowOfQ(ctx, tx, u.Key, true)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("config: 校验 %s 失败: %w", u.Key, err)
		}
		oldPlain, err := s.loadValue(stored, sensitive)
		if err != nil {
			return fmt.Errorf("config: 读取 %s 失败: %w", u.Key, err)
		}
		newStored, err := s.storeValue(u.Value, sensitive)
		if err != nil {
			return fmt.Errorf("config: 加密 %s 失败: %w", u.Key, err)
		}
		pending = append(pending, pendingUpdate{key: u.Key, value: u.Value, newStored: newStored, oldPlain: oldPlain, sensitive: sensitive})
	}
	for _, p := range pending {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sys_runtime_config SET cfg_value = ? WHERE cfg_key = ?`, p.newStored, p.key); err != nil {
			return fmt.Errorf("config: 更新 %s 失败: %w", p.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("config: 批量更新提交失败: %w", err)
	}
	committed = true
	events := make([]ChangeEvent, 0, len(pending))
	for _, p := range pending {
		s.invalidate(p.key)
		events = append(events, ChangeEvent{Key: p.key, OldValue: p.oldPlain, NewValue: p.value, Sensitive: p.sensitive, Source: SourceUpdate})
	}
	s.notifyBatch(events)
	return nil
}

// Update 更新配置（按库内 is_sensitive 决定是否加密）；键不存在返回 sql.ErrNoRows。
// 成功后先失效缓存再按明文新旧值触发变更监听（监听方读到的一定是新值）。
func (s *Store) Update(ctx context.Context, key, value string) error {
	stored, sensitive, err := s.rowOf(ctx, key)
	if err != nil {
		return err
	}
	oldPlain, err := s.loadValue(stored, sensitive)
	if err != nil {
		return err
	}
	newStored, err := s.storeValue(value, sensitive)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sys_runtime_config SET cfg_value = ? WHERE cfg_key = ?`,
		newStored, key); err != nil {
		return fmt.Errorf("config: 更新 %s 失败: %w", key, err)
	}
	s.invalidate(key)
	s.notify(ChangeEvent{
		Key:       key,
		OldValue:  oldPlain,
		NewValue:  value,
		Sensitive: sensitive,
		Source:    SourceUpdate,
	})
	return nil
}

// ListPublic 全量脱敏公开值（管理端列表；按 cfg_key 排序由调用方 SQL 决定）
func (s *Store) ListPublic(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cfg_key, cfg_value, is_sensitive FROM sys_runtime_config ORDER BY cfg_key`)
	if err != nil {
		return nil, fmt.Errorf("config: 列表查询失败: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		var sens int64
		if err := rows.Scan(&k, &v, &sens); err != nil {
			return nil, err
		}
		if sens != 0 {
			plain, err := s.loadValue(v, true)
			if err != nil {
				return nil, err
			}
			v = MaskSecret(plain)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// UpdatedAt 读取最近更新时间（监控用；库内无行返回 sql.ErrNoRows）
func (s *Store) UpdatedAt(ctx context.Context, key string) (time.Time, error) {
	var ts time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT updated_at FROM sys_runtime_config WHERE cfg_key = ?`, key).Scan(&ts)
	return ts, err
}

// Value 是供配置中心读取的单项明文值；敏感项只在此处解密，HTTP 层再统一脱敏。
type Value struct {
	Key       string
	Value     string
	Sensitive bool
	UpdatedAt time.Time
}

// List 返回全部配置项，供 HTTP 适配器使用；不调用 MaskSecret，避免把明文误当密文。
func (s *Store) List(ctx context.Context) ([]Value, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cfg_key, cfg_value, is_sensitive, updated_at FROM sys_runtime_config ORDER BY cfg_key`)
	if err != nil {
		return nil, fmt.Errorf("config: 配置列表查询失败: %w", err)
	}
	defer rows.Close()
	out := make([]Value, 0)
	for rows.Next() {
		var key, stored string
		var sensitive int64
		var updated time.Time
		if err := rows.Scan(&key, &stored, &sensitive, &updated); err != nil {
			return nil, err
		}
		plain, err := s.loadValue(stored, sensitive != 0)
		if err != nil {
			return nil, err
		}
		out = append(out, Value{Key: key, Value: plain, Sensitive: sensitive != 0, UpdatedAt: updated})
	}
	return out, rows.Err()
}
