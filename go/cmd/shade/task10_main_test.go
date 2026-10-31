package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/config"
)

// JWT_EXPIRES / JWT_WEB_EXPIRES 必须真实接入装配：默认 7d/12h，支持 d/h 写法，非法值回退默认。
func TestJWTTTLsUseEnvAndDefaults(t *testing.T) {
	t.Setenv("JWT_EXPIRES", "")
	t.Setenv("JWT_WEB_EXPIRES", "")
	got, web := jwtTokenTTLs(os.Getenv)
	if got != 7*24*time.Hour || web != 12*time.Hour {
		t.Fatalf("默认应为 7d/12h，实际 %s/%s", got, web)
	}
	t.Setenv("JWT_EXPIRES", "2d")
	t.Setenv("JWT_WEB_EXPIRES", "30m")
	got, web = jwtTokenTTLs(os.Getenv)
	if got != 48*time.Hour || web != 30*time.Minute {
		t.Fatalf("env 覆盖后应为 2d/30m，实际 %s/%s", got, web)
	}
	t.Setenv("JWT_EXPIRES", "bogus")
	t.Setenv("JWT_WEB_EXPIRES", "0h")
	got, web = jwtTokenTTLs(os.Getenv)
	if got != 7*24*time.Hour || web != 12*time.Hour {
		t.Fatalf("非法值应回退 7d/12h，实际 %s/%s", got, web)
	}
}

// ---- 配置批量通知装配测试用轻量假驱动（不连真实 MySQL） ----

type cfgFakeRows struct{ i int }

func (r *cfgFakeRows) Columns() []string { return []string{"cfg_value", "is_sensitive"} }
func (r *cfgFakeRows) Close() error      { return nil }
func (r *cfgFakeRows) Next(dest []driver.Value) error {
	if r.i > 0 {
		return io.EOF
	}
	dest[0], dest[1] = "old", int64(0)
	r.i++
	return nil
}

type cfgFakeResult int64

func (r cfgFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r cfgFakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type cfgFakeConn struct{}

func (cfgFakeConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("unsupported") }
func (cfgFakeConn) Close() error                        { return nil }
func (cfgFakeConn) Begin() (driver.Tx, error)           { return cfgFakeTx{}, nil }
func (cfgFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return cfgFakeTx{}, nil
}
func (cfgFakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &cfgFakeRows{}, nil
}
func (cfgFakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return cfgFakeResult(1), nil
}

type cfgFakeTx struct{}

func (cfgFakeTx) Commit() error   { return nil }
func (cfgFakeTx) Rollback() error { return nil }

type cfgFakeDriver struct{}

func (cfgFakeDriver) Open(string) (driver.Conn, error) { return cfgFakeConn{}, nil }

var cfgFakeSeq atomic.Int64

func openCfgFake(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("main-cfg-fake-%d", cfgFakeSeq.Add(1))
	sql.Register(name, cfgFakeDriver{})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 一次 UpdateBatch 命中多个微信键只触发一次 Reconnect；非微信键不触发。
func TestWechatReconnectRegisteredOnChangeBatchOnce(t *testing.T) {
	store := config.NewStore(openCfgFake(t), nil)
	calls := 0
	registerWechatReconnect(store, func() { calls++ })
	err := store.UpdateBatch(context.Background(), []config.Update{
		{Key: "WECHATPADPRO_TOKEN", Value: "t1"},
		{Key: "WECHATPADPRO_API_BASE", Value: "https://example.invalid"},
		{Key: "WECHATPADPRO_WS_URL", Value: "wss://example.invalid"},
	})
	if err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("多键批量变更应只触发一次重连，实际 %d", calls)
	}
	if err := store.UpdateBatch(context.Background(), []config.Update{
		{Key: "DIFY_API_URL", Value: "https://dify.invalid"},
	}); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("非微信键不应触发重连，实际 %d", calls)
	}
}
