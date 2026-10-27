package authwx

// 测试假件（纯标准库，绝不连微信/真实库）：database/sql 假驱动按 SQL 子串脚本化响应、
// 假 code2session 适配与假 JWT 签发核心。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/auth"
)

// ---- database/sql 假驱动 ----

type fakeResult struct{ affected, lastID int64 }

func (r fakeResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

type fakeRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// fakeCall 一次执行/查询记录。
type fakeCall struct {
	query string
	args  []driver.Value
}

// fakeRule 按 SQL 子串或参数子串匹配的响应脚本（同文本查询按参数值区分场景）。
type fakeRule struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

type fakeDB struct {
	mu      sync.Mutex
	rules   []fakeRule
	queries []fakeCall
	execs   []fakeCall
}

// onRows 注册查询响应（cols 为列名，rows 为行值）；同 match 已有脚本则覆盖。
func (d *fakeDB) onRows(match string, cols []string, rows ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.rules {
		if d.rules[i].match == match {
			d.rules[i] = fakeRule{match: match, cols: cols, rows: rows}
			return
		}
	}
	d.rules = append(d.rules, fakeRule{match: match, cols: cols, rows: rows})
}

// onErr 注册报错响应；同 match 已有脚本则覆盖。
func (d *fakeDB) onErr(match string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.rules {
		if d.rules[i].match == match {
			d.rules[i] = fakeRule{match: match, err: err}
			return
		}
	}
	d.rules = append(d.rules, fakeRule{match: match, err: err})
}

func (d *fakeDB) find(q string, args []driver.Value) *fakeRule {
	for i := range d.rules {
		if strings.Contains(q, d.rules[i].match) {
			return &d.rules[i]
		}
		for _, a := range args {
			if s, ok := a.(string); ok && strings.Contains(s, d.rules[i].match) {
				return &d.rules[i]
			}
		}
	}
	return nil
}

// execQueries 返回全部 Exec 语句（断言绑定 SQL）。
func (d *fakeDB) execQueries() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.execs))
	for _, e := range d.execs {
		out = append(out, e.query)
	}
	return out
}

// execArgs 返回全部 Exec 参数（断言 openid 转移参数）。
func (d *fakeDB) execArgs() []driver.Value {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []driver.Value
	for _, e := range d.execs {
		out = append(out, e.args...)
	}
	return out
}

var fakeSeq atomic.Int64

func openFake(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	f := &fakeDB{}
	name := fmt.Sprintf("authwx-fake-%d", fakeSeq.Add(1))
	sql.Register(name, &fakeDriver{db: f})
	db, err := sql.Open(name, "fake")
	if err != nil {
		t.Fatalf("打开 fake db 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, query: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	vals := namedValues(args)
	c.db.mu.Lock()
	c.db.queries = append(c.db.queries, fakeCall{query: q, args: vals})
	c.db.mu.Unlock()
	rule := c.db.find(q, vals)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, data: rule.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals := namedValues(args)
	c.db.mu.Lock()
	c.db.execs = append(c.db.execs, fakeCall{query: q, args: vals})
	c.db.mu.Unlock()
	if rule := c.db.find(q, vals); rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return fakeResult{affected: 1, lastID: 1}, nil
}

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.db.conn().ExecContext(context.Background(), s.query, toNamed(args))
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.db.conn().QueryContext(context.Background(), s.query, toNamed(args))
}

func (d *fakeDB) conn() *fakeConn { return &fakeConn{db: d} }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

func namedValues(in []driver.NamedValue) []driver.Value {
	out := make([]driver.Value, 0, len(in))
	for _, v := range in {
		out = append(out, v.Value)
	}
	return out
}

func toNamed(in []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, 0, len(in))
	for i, v := range in {
		out = append(out, driver.NamedValue{Ordinal: i + 1, Value: v})
	}
	return out
}

// ---- 假微信 code2session ----

type fakeWX struct {
	mu    sync.Mutex
	sess  WXSession
	err   error
	calls int
	codes []string
}

func (f *fakeWX) Code2Session(_ context.Context, appID, secret, code string) (WXSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.codes = append(f.codes, code)
	if f.err != nil {
		return WXSession{}, f.err
	}
	return f.sess, nil
}

// ---- 假运行时配置 ----

type fakeCfg map[string]string

func (c fakeCfg) Get(_ context.Context, key string) (string, error) {
	v, ok := c[key]
	if !ok {
		return "", sql.ErrNoRows
	}
	return v, nil
}

// ---- 假 JWT 签发核心（*auth.Service 的最小替身） ----

type fakeCore struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeCore) IssueToken(_ context.Context, user auth.Identity, client string) (auth.LoginResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	ttl := int64(3600)
	if client == "web" {
		ttl = 1800
	}
	return auth.LoginResult{Token: "jwt-" + user.Username, ExpiresIn: ttl, User: user}, nil
}

// ---- 纯 httpapi.Authenticator 桩（不实现 WXLoginService，走核心回退登录） ----

type plainAuth struct{ ident auth.Identity }

func (p plainAuth) Login(_ context.Context, username, password, _ string) (auth.LoginResult, error) {
	if username == "" || password == "" {
		return auth.LoginResult{}, auth.ErrInvalidCredentials
	}
	if password != "ok" {
		return auth.LoginResult{}, auth.ErrInvalidCredentials
	}
	return auth.LoginResult{Token: "plain-token", ExpiresIn: 60, User: p.ident}, nil
}

func (p plainAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if token != "good-token" {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return p.ident, nil
}

func (plainAuth) Logout(context.Context, string, time.Time) error { return nil }
