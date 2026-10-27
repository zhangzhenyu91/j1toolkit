package kvm

// 本文件是测试专用轻量 database/sql 驱动（fake）与外部依赖桩：按 SQL 子串匹配返回预置行/错误，
// 平台/网盘/锁均可注入桩；不依赖外部数据库、不调用真实外部服务。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"shade/internal/auth"
)

type fakeRule struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	rules   []fakeRule
	queries []fakeExec
	execs   []fakeExec
}

func (f *fakeDB) on(match string, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, cols: cols, rows: rows})
	return f
}

func (f *fakeDB) onErr(match string, err error) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, err: err})
	return f
}

func (f *fakeDB) find(query string) *fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.rules) - 1; i >= 0; i-- {
		if strings.Contains(query, f.rules[i].match) {
			return &f.rules[i]
		}
	}
	return nil
}

func (f *fakeDB) lastQuery(substr string) (fakeExec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.queries) - 1; i >= 0; i-- {
		if strings.Contains(f.queries[i].query, substr) {
			return f.queries[i], true
		}
	}
	return fakeExec{}, false
}

var fakeSeq atomic.Int64

// openFake 打开一个 fake *sql.DB。
func openFake(t interface{ Fatalf(string, ...any) }) (*sql.DB, *fakeDB) {
	f := &fakeDB{}
	name := fmt.Sprintf("kvm-fake-%d", fakeSeq.Add(1))
	sql.Register(name, &fakeDriver{db: f})
	db, err := sql.Open(name, "fake")
	if err != nil {
		t.Fatalf("打开 fake db 失败: %v", err)
	}
	return db, f
}

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, query: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.db.mu.Lock()
	s.db.execs = append(s.db.execs, fakeExec{query: s.query, args: append([]driver.Value{}, args...)})
	s.db.mu.Unlock()
	if rule := s.db.find(s.query); rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return driver.RowsAffected(1), nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.mu.Lock()
	s.db.queries = append(s.db.queries, fakeExec{query: s.query, args: append([]driver.Value{}, args...)})
	s.db.mu.Unlock()
	rule := s.db.find(s.query)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, rows: rule.rows}, nil
}

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// fakePlatform 平台桩：预置设备集与转发会话。
type fakePlatform struct {
	devices []Device
	session string
	proxy   ProxySession
	err     error
	baseURL string
}

func (p *fakePlatform) BaseURL() string {
	if p.baseURL == "" {
		return "https://kvm.example.invalid"
	}
	return p.baseURL
}

func (p *fakePlatform) ListDevices(context.Context, url.Values, string) ([]Device, error) {
	return p.devices, p.err
}

func (p *fakePlatform) SessionToken(context.Context, string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	if p.session == "" {
		return "sid-token", nil
	}
	return p.session, nil
}

func (p *fakePlatform) ProxySession(context.Context, string, string) (ProxySession, error) {
	if p.err != nil {
		return ProxySession{}, p.err
	}
	return p.proxy, nil
}

// fakePermit 应用权限桩：按 appKey 集合放行。
type fakePermit struct{ allow map[string]bool }

func (p fakePermit) Allow(_ context.Context, _ int64, appKey string) (bool, error) {
	return p.allow[appKey], nil
}

// fakeConfig 运行时配置桩（GLKVM_URL / GLKVM_PASSWORD 动态取值，可变值模拟配置中心改值）。
type fakeConfig struct {
	mu   sync.Mutex
	vals map[string]string
}

func (c *fakeConfig) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vals[key]
	if !ok {
		return "", errors.New("配置不存在")
	}
	return v, nil
}

// set 改配置值（模拟配置中心 Update，配合 OnChange 清缓存口径）。
func (c *fakeConfig) set(key, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = map[string]string{}
	}
	c.vals[key] = v
}

// fakeNetdisk 网盘桥接桩。
type fakeNetdisk struct {
	files     map[string][]byte // rel → 内容
	saved     []string
	savedLen  int   // 最近一次保存的字节数（流式读出后统计）
	savedSize int64 // 最近一次保存传入的 size
	failSave  bool
}

func (n *fakeNetdisk) AssertAccess(context.Context, int64) error { return nil }

func (n *fakeNetdisk) Download(_ context.Context, _ auth.Identity, _, rel string) ([]byte, error) {
	data, ok := n.files[rel]
	if !ok {
		return nil, errors.New("文件不存在")
	}
	return data, nil
}

func (n *fakeNetdisk) Save(_ context.Context, _ auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error) {
	if n.failSave {
		return "", bizErr("保存失败", 500, codeInternal)
	}
	data, err := io.ReadAll(body) // 桩内整读仅用于断言，生产实现应流式转发
	if err != nil {
		return "", err
	}
	n.savedLen = len(data)
	n.savedSize = size
	n.saved = append(n.saved, space+"/"+dir+"/"+name)
	return space + "/" + dir + "/" + name, nil
}
