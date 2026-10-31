package office

// 跨班身份边界测试：/config 签发前按身份收敛生效班组——普通用户/班管显式传他班 team_id
// 一律 403（不静默改写成自家班组）；超管可指定班组并经 httpapi.ResolveTeam 校验存在/启用。
// 假 database/sql 驱动只回答 sys_team 查询，不连真实库。

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// teamRows sys_team 查询结果行。
type teamRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *teamRows) Columns() []string { return r.cols }
func (r *teamRows) Close() error      { return nil }

func (r *teamRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type teamResult int64

func (r teamResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r teamResult) RowsAffected() (int64, error) { return 1, nil }

// teamDB 只回答 sys_team 查询的假库（id → 名称）。
type teamDB struct{ teams map[int64]string }

func (d *teamDB) dispatch(query string, args []driver.Value, isExec bool) (driver.Rows, driver.Result, error) {
	if strings.Contains(query, "FROM sys_team") {
		if strings.Contains(query, "ORDER BY") {
			var minID int64
			var name string
			first := true
			for id, n := range d.teams {
				if first || id < minID {
					minID, name, first = id, n, false
				}
			}
			if first {
				return &teamRows{cols: []string{"id", "name"}}, nil, nil
			}
			return &teamRows{cols: []string{"id", "name"}, rows: [][]driver.Value{{minID, name}}}, nil, nil
		}
		id, _ := args[0].(int64)
		if name, ok := d.teams[id]; ok {
			return &teamRows{cols: []string{"id", "name"}, rows: [][]driver.Value{{id, name}}}, nil, nil
		}
		return &teamRows{cols: []string{"id", "name"}}, nil, nil
	}
	if isExec {
		return nil, teamResult(1), nil
	}
	return &teamRows{cols: []string{"v"}}, nil, nil
}

type teamStmt struct {
	db    *teamDB
	query string
}

func (s *teamStmt) Close() error  { return nil }
func (s *teamStmt) NumInput() int { return -1 }

func (s *teamStmt) Exec(args []driver.Value) (driver.Result, error) {
	_, res, err := s.db.dispatch(s.query, args, true)
	return res, err
}

func (s *teamStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, _, err := s.db.dispatch(s.query, args, false)
	return rows, err
}

type teamTx struct{}

func (teamTx) Commit() error   { return nil }
func (teamTx) Rollback() error { return nil }

type teamConn struct{ db *teamDB }

func (c *teamConn) Prepare(q string) (driver.Stmt, error) { return &teamStmt{db: c.db, query: q}, nil }
func (c *teamConn) Close() error                          { return nil }
func (c *teamConn) Begin() (driver.Tx, error)             { return teamTx{}, nil }

type teamDriver struct{ db *teamDB }

func (d *teamDriver) Open(string) (driver.Conn, error) { return &teamConn{db: d.db}, nil }

var teamSeq int64

// openTeamFakeDB 打开只含 sys_team 的假库（id → 班组名）。
func openTeamFakeDB(t *testing.T, teams map[int64]string) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("office-team-%d", atomic.AddInt64(&teamSeq, 1))
	sql.Register(name, &teamDriver{db: &teamDB{teams: teams}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开假库失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newRouterIdent 指定身份的测试路由（与 newRouter 同一条 httpapi.AuthMiddleware 装配）。
func newRouterIdent(m *Module, ident auth.Identity) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return httpapi.AuthMiddleware(r, fakeAuth{ident: ident})
}

// adminIdentity 超管身份（可指定班组）。
func adminIdentity() auth.Identity {
	return auth.Identity{ID: 9, Username: "admin", Nickname: "管理员", Role: "admin", TeamID: 1}
}

// crossTeamEnvelope 断言 403 + 40304 + 统一文案。
func crossTeamEnvelope(t *testing.T, body []byte) {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析信封: %v body=%s", err, body)
	}
	if env.Code != codeForbidden || env.Message != "无权访问其他班组的数据" {
		t.Fatalf("跨班拒绝口径错误: code=%d message=%q", env.Code, env.Message)
	}
}

// TestConfigRejectsCrossTeamForOrdinaryUser 普通用户/班管显式传他班 team_id 读写 Office 必须 403
// （含网盘公共区与只读统计表源），不得静默改写成自家班组继续签发。
func TestConfigRejectsCrossTeamForOrdinaryUser(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"ndp-public", map[string]any{"src": "ndp", "space": "public", "path": "/a.docx", "team_id": 5}},
		{"ndp-my", map[string]any{"src": "ndp", "space": "my", "path": "/a.docx", "team_id": 5}},
		{"wls", map[string]any{"src": "wls", "sheet": "task", "from": "2026-10-01", "to": "2026-10-02", "team_id": 5}},
		{"thd", map[string]any{"src": "thd", "line": "", "team_id": 5}},
	}
	identities := map[string]auth.Identity{
		"user":       {ID: 1, Username: "zhang", Nickname: "小张", Role: "user", TeamID: 1},
		"team_admin": {ID: 2, Username: "li", Nickname: "小李", Role: "team_admin", TeamID: 1},
	}
	for identName, ident := range identities {
		m, _ := newTestModule(t)
		h := newRouterIdent(m, ident)
		for _, tc := range cases {
			rec := doJSON(t, h, "/api/v1/office/config", tc.body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s/%s 应 403: %d %s", identName, tc.name, rec.Code, rec.Body)
			}
			crossTeamEnvelope(t, rec.Body.Bytes())
		}
	}
}

// TestConfigAdminSelectsTeam 超管保留指定班组能力：经 ResolveTeam 校验存在/启用，
// 缺省回落本人班组；不存在的班组按 400 40010 拒绝（生产 TeamByID 同时过滤 status=1，停用与不存在同口径）。
func TestConfigAdminSelectsTeam(t *testing.T) {
	m, _ := newTestModule(t)
	m.DB = openTeamFakeDB(t, map[int64]string{1: "检修一班", 5: "检修二班"})
	bridge := &fakeBridge{data: []byte("doc"), docKey: "k1"}
	caught := &captureBridge{NetdiskDoc: NewNetdiskDoc(bridge), docKey: "k1"}
	m.Netdisk = caught
	h := newRouterIdent(m, adminIdentity())

	// 超管显式班组 5。
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "public", "path": "/a.docx", "team_id": 5,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("超管指定班组应 200: %d %s", rec.Code, rec.Body)
	}
	if len(caught.ndps) != 1 || caught.ndps[0].Team != 5 {
		t.Fatalf("票据应携带指定班组 5: %+v", caught.ndps)
	}

	// 超管缺省：回落到本人班组 1。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "public", "path": "/a.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("超管缺省班组应 200: %d %s", rec.Code, rec.Body)
	}
	if len(caught.ndps) != 2 || caught.ndps[1].Team != 1 {
		t.Fatalf("缺省班组应回落本人班组 1: %+v", caught.ndps)
	}

	// 不存在的班组（生产 TeamByID 同时过滤 status=1，停用同口径）：ResolveTeam 查无 → 400 40010。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "public", "path": "/a.docx", "team_id": 6,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在班组应 400: %d %s", rec.Code, rec.Body)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析信封: %v", err)
	}
	if env.Code != codeNoTeam || env.Message != "无可用班组" {
		t.Fatalf("不存在班组口径错误: %d %q", env.Code, env.Message)
	}
}

// TestConfigTeamAdminCrossTeamStillDenied 班管同样不能借 team_id 越权（超管能力不外溢）。
func TestConfigTeamAdminCrossTeamStillDenied(t *testing.T) {
	m, _ := newTestModule(t)
	m.DB = openTeamFakeDB(t, map[int64]string{1: "检修一班", 5: "检修二班"})
	h := newRouterIdent(m, auth.Identity{ID: 2, Username: "li", Role: "team_admin", TeamID: 1})
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "public", "path": "/a.docx", "team_id": 5,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("班管他班应 403: %d %s", rec.Code, rec.Body)
	}
	crossTeamEnvelope(t, rec.Body.Bytes())
}
