package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
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

// Store sys_runtime_config 访问器
type Store struct {
	db        *sql.DB
	cipher    *ConfigCipher
	mu        sync.RWMutex
	listeners []Listener
}

// NewStore 构造访问器（cipher 仅敏感项需要；nil 时敏感项读写报 ErrNoMasterKey）
func NewStore(db *sql.DB, cipher *ConfigCipher) *Store {
	return &Store{db: db, cipher: cipher}
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

func (s *Store) notify(ev ChangeEvent) {
	s.mu.RLock()
	ls := append([]Listener(nil), s.listeners...)
	s.mu.RUnlock()
	for _, l := range ls {
		l(ev)
	}
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
		s.notify(ChangeEvent{Key: spec.Key, OldValue: "", NewValue: val, Sensitive: spec.Sensitive, Source: SourceEnv})
	}
	return seeded, nil
}

// rowOf 读单行（值 + 敏感标记）
func (s *Store) rowOf(ctx context.Context, key string) (stored string, sensitive bool, err error) {
	var sens int64
	err = s.db.QueryRowContext(ctx,
		`SELECT cfg_value, is_sensitive FROM sys_runtime_config WHERE cfg_key = ?`, key).
		Scan(&stored, &sens)
	if err != nil {
		return "", false, err
	}
	return stored, sens != 0, nil
}

// Get 读配置值（敏感项返回解密明文）；键不存在返回 sql.ErrNoRows
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	stored, sensitive, err := s.rowOf(ctx, key)
	if err != nil {
		return "", err
	}
	return s.loadValue(stored, sensitive)
}

// PublicValue 读脱敏公开值：敏感项 MaskSecret 后返回，普通项原值（管理端列表口径）
func (s *Store) PublicValue(ctx context.Context, key string) (string, error) {
	stored, sensitive, err := s.rowOf(ctx, key)
	if err != nil {
		return "", err
	}
	if !sensitive {
		return stored, nil
	}
	plain, err := s.loadValue(stored, sensitive)
	if err != nil {
		return "", err
	}
	return MaskSecret(plain), nil
}

// Update 更新配置（按库内 is_sensitive 决定是否加密）；键不存在返回 sql.ErrNoRows。
// 成功后按明文新旧值触发变更监听。
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
