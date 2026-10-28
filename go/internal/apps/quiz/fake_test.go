package quiz

// 本文件是测试专用轻量 database/sql 驱动（fake）与外部依赖桩：按 SQL 子串（可含参数）匹配
// 返回预置行/错误，并记录执行语句；AI 解析可注入桩；不依赖外部数据库、不调用真实外部服务。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

type fakeRule struct {
	match string
	args  []driver.Value // 非 nil 时要求参数逐位匹配
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

func (f *fakeDB) onArgs(match string, args []driver.Value, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, args: args, cols: cols, rows: rows})
	return f
}

func (f *fakeDB) onErr(match string, err error) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, err: err})
	return f
}

func (f *fakeDB) find(query string, args []driver.Value) *fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.rules) - 1; i >= 0; i-- {
		rule := &f.rules[i]
		if !strings.Contains(query, rule.match) {
			continue
		}
		if rule.args != nil {
			if len(args) != len(rule.args) {
				continue
			}
			matched := true
			for j := range args {
				if args[j] != rule.args[j] {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		return rule
	}
	return nil
}

func (f *fakeDB) execCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.execs {
		if strings.Contains(e.query, substr) {
			n++
		}
	}
	return n
}

func (f *fakeDB) lastExec(substr string) (fakeExec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.execs) - 1; i >= 0; i-- {
		if strings.Contains(f.execs[i].query, substr) {
			return f.execs[i], true
		}
	}
	return fakeExec{}, false
}

var fakeSeq atomic.Int64

// openFake 打开一个 fake *sql.DB。
func openFake(t interface{ Fatalf(string, ...any) }) (*sql.DB, *fakeDB) {
	f := &fakeDB{}
	name := fmt.Sprintf("quiz-fake-%d", fakeSeq.Add(1))
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
	if rule := s.db.find(s.query, args); rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return driver.RowsAffected(1), nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.db.mu.Lock()
	s.db.queries = append(s.db.queries, fakeExec{query: s.query, args: append([]driver.Value{}, args...)})
	s.db.mu.Unlock()
	rule := s.db.find(s.query, args)
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

// lastQuery 取最后一条匹配子串的查询语句（分页 SQL 断言用）。
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

// fakePermit 应用权限桩。
type fakePermit struct{ allow bool }

func (p fakePermit) Allow(context.Context, int64, string) (bool, error) { return p.allow, nil }

// fakeAnalyzer AI 解析桩（可切换配置态与产物）。
type fakeAnalyzer struct {
	configured bool
	analysis   string
	err        error
	calls      int
	mu         sync.Mutex
}

func (a *fakeAnalyzer) Configured() bool { return a.configured }

func (a *fakeAnalyzer) Analyze(_ context.Context, _ QuestionForAI) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return "", a.err
	}
	if a.analysis == "" {
		return "默认解析", nil
	}
	return a.analysis, nil
}

// fakeConfig 运行时配置桩（Dify 热配置读取用）。
type fakeConfig map[string]string

func (c fakeConfig) Get(_ context.Context, key string) (string, error) {
	v, ok := c[key]
	if !ok {
		return "", errors.New("配置键不存在")
	}
	return v, nil
}

// fakeLogger 应用日志桩：记录事件供断言。
type fakeLogger struct {
	mu     sync.Mutex
	events []string
}

func (l *fakeLogger) Info(_ context.Context, _, event, messageCN, _ string, _ map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event+":"+messageCN)
	return nil
}

func (l *fakeLogger) Error(_ context.Context, _, event, messageCN, _ string, _ map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event+":"+messageCN)
	return nil
}
