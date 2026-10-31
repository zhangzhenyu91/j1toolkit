package safeday

// 本文件是测试专用轻量 database/sql 驱动（fake）与外部依赖桩：按 SQL 子串匹配返回预置行/错误，
// Dify/转换/对象存储/网盘均可注入桩；不依赖外部数据库、不调用真实外部服务。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shade/internal/auth"
	"shade/internal/compat/imagepdf"
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

// onArgs 注册查询规则（SQL 子串 + 参数逐位匹配）。
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
	name := fmt.Sprintf("safeday-fake-%d", fakeSeq.Add(1))
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

// fakeConfig 运行时配置桩。
type fakeConfig map[string]string

func (c fakeConfig) Get(_ context.Context, key string) (string, error) {
	v, ok := c[key]
	if !ok {
		return "", errors.New("配置键不存在")
	}
	return v, nil
}

// fakeTokenAuth 严格令牌鉴权桩：仅认预置 token（验证 ?token= 自验证链路与拒绝匿名）。
type fakeTokenAuth struct {
	valid map[string]auth.Identity
}

func (f fakeTokenAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if ident, ok := f.valid[token]; ok {
		return ident, nil
	}
	return auth.Identity{}, errors.New("无效令牌")
}

func (f fakeTokenAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, errors.New("不支持")
}

func (f fakeTokenAuth) Logout(context.Context, string, time.Time) error { return nil }

// fakeCOS 对象存储桩。
type fakeCOS struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
	failPut bool
}

func newFakeCOS() *fakeCOS { return &fakeCOS{objects: map[string][]byte{}} }

func (c *fakeCOS) Put(_ context.Context, key string, data []byte, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failPut {
		return "", errors.New("COS 上传失败")
	}
	c.objects[key] = append([]byte{}, data...)
	return "https://cos.example.invalid/" + key, nil
}

func (c *fakeCOS) PublicURL(key string) string { return "https://cos.example.invalid/" + key }

func (c *fakeCOS) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, key)
	c.deleted = append(c.deleted, key)
	return nil
}

// fakeDify 工作流桩：记录触发入参；可注入失败回调触发。
type fakeDify struct {
	mu       sync.Mutex
	inputs   []WorkflowInput
	failRun  bool
	autoFail bool // 触发后立即模拟工作流失败
}

func (d *fakeDify) Run(_ context.Context, in WorkflowInput) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failRun {
		return errors.New("Dify 触发失败")
	}
	d.inputs = append(d.inputs, in)
	if d.autoFail && in.OnFailed != nil {
		in.OnFailed(errors.New("Dify 工作流执行失败"))
	}
	return nil
}

// fakeConverter PDF 转换桩（产出真实一页 PDF，供 imagepdf.MergePDFs 合并）。
type fakeConverter struct{ fail bool }

// tinyJPEG 生成 8x8 JPEG 样图。
func tinyJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 90, G: 120, B: 150, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

// realPDF 用 compat/imagepdf 产一页真实 PDF。
func realPDF() ([]byte, error) {
	return imagepdf.BuildPhotosPDF([]imagepdf.PhotoCard{{Photos: [][]byte{tinyJPEG()}}})
}

func (c fakeConverter) ToPDF(_ context.Context, data []byte, _ string) ([]byte, error) {
	if c.fail {
		return nil, errors.New("格式转换失败：桩")
	}
	pdf, err := realPDF()
	if err != nil {
		return nil, err
	}
	_ = data
	return pdf, nil
}

// fakeNetdisk 网盘保存桩。
type fakeNetdisk struct {
	saved []NetdiskSaveRequest
	err   error
}

func (n *fakeNetdisk) SaveToNetdisk(_ context.Context, _ auth.Identity, req NetdiskSaveRequest) (string, error) {
	n.saved = append(n.saved, req)
	if n.err != nil {
		return "", n.err
	}
	return req.Space + "/" + req.Dir + "/" + req.Name, nil
}

// fakeLogger 应用日志桩：记录事件供断言。
type fakeLogger struct {
	mu     sync.Mutex
	events []string
	fields map[string]any
}

func (l *fakeLogger) Info(_ context.Context, _, event, messageCN, _ string, fields map[string]any) error {
	l.record(event, messageCN, fields)
	return nil
}

func (l *fakeLogger) Error(_ context.Context, _, event, messageCN, _ string, fields map[string]any) error {
	l.record(event, messageCN, fields)
	return nil
}

func (l *fakeLogger) record(event, messageCN string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event+":"+messageCN)
	l.fields = fields
}

func (l *fakeLogger) lastFields() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fields
}

func (l *fakeLogger) eventList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.events...)
}

func (l *fakeLogger) has(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
