package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"shade/internal/apps/wechatanswer"
)

type permitRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *permitRows) Columns() []string { return r.cols }
func (r *permitRows) Close() error      { return nil }
func (r *permitRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type permitConn struct{ role string }

func (permitConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("unsupported") }
func (permitConn) Close() error                        { return nil }
func (permitConn) Begin() (driver.Tx, error)           { return nil, fmt.Errorf("unsupported") }
func (c permitConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case containsAll(query, "SELECT role FROM sys_user"):
		return &permitRows{cols: []string{"role"}, rows: [][]driver.Value{{c.role}}}, nil
	case containsAll(query, "COUNT(*) FROM sys_user_app"):
		return &permitRows{cols: []string{"cnt"}, rows: [][]driver.Value{{int64(1)}}}, nil
	default:
		return &permitRows{}, nil
	}
}
func (permitConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type permitDriver struct{ role string }

func (d permitDriver) Open(string) (driver.Conn, error) { return permitConn{role: d.role}, nil }

var permitSeq atomic.Int64

func openPermitFake(t *testing.T, role string) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("main-permit-fake-%d", permitSeq.Add(1))
	sql.Register(name, permitDriver{role: role})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func containsAll(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 有 wechat-answer 应用授权的班组管理员也必须被模块拒绝（仅超管）。
func TestWechatPermitRejectsAuthorizedTeamAdmin(t *testing.T) {
	permit := wechatPermit{db: openPermitFake(t, "team_admin")}
	allow, err := permit.Allow(context.Background(), 9, wechatanswer.AppKey)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if allow {
		t.Fatal("有应用授权的班组管理员不得通过微信智能应答模块权限")
	}
}

// 超管无需应用授权即可通过。
func TestWechatPermitAllowsAdmin(t *testing.T) {
	permit := wechatPermit{db: openPermitFake(t, "admin")}
	allow, err := permit.Allow(context.Background(), 1, wechatanswer.AppKey)
	if err != nil || !allow {
		t.Fatalf("超管应通过，实际 %v/%v", allow, err)
	}
}
