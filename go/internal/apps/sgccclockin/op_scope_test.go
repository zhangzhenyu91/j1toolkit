// 出工卡片操作权限回归：/op/status 与 /photos/{id}/resync 按卡片可操作性（超管任意班组、
// 普通成员本班或成员含本班的跨班卡）判定，不得把真实任务当完成、不得泄露他班任务。
package sgccclockin

// 本文件仅用 fake 数据库与 fake 路由，不连接真实 MySQL/Redis/商旅。

import (
	"database/sql"
	"database/sql/driver"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"shade/internal/auth"
)

// opStatusUnknownMsg 任务不可用统一提示（契约固定文案）。
const opStatusUnknownMsg = "任务状态不可用，请刷新确认结果"

func opStatusEnv(t *testing.T, ident auth.Identity) (*Module, http.Handler, *fakeDB) {
	t.Helper()
	db, fdb := openFake(t)
	t.Cleanup(func() { _ = db.Close() })
	m := New(db)
	h := newTestRouter(m, ident)
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	return m, h, fdb
}

func opStatusData(t *testing.T, h http.Handler, opID int64) map[string]any {
	t.Helper()
	rec := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/op/status?op_id="+strconv.FormatInt(opID, 10), "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("op/status 应 200 业务成功: %v", env)
	}
	return env["data"].(map[string]any)
}

func assertUnknownOpStatus(t *testing.T, got map[string]any) {
	t.Helper()
	if got["status"] != "unknown" || got["step"] != "unknown" {
		t.Fatalf("不可用任务应为 unknown 而非伪装完成: %v", got)
	}
	if got["failed"] != false || got["progress"] != float64(0) || got["error"] != opStatusUnknownMsg {
		t.Fatalf("不可用任务字段不符: %v", got)
	}
}

// TestOpStatusAdminCrossTeam 超管轮询他班卡片任务：按 ID 校验可操作后返回真实任务视图。
func TestOpStatusAdminCrossTeam(t *testing.T) {
	m, h, fdb := opStatusEnv(t, auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1})
	fdb.onRowsAlways("FROM worklog_entry e WHERE e.id = ?", []string{"team_id"}, []driver.Value{int64(2)})
	op := m.Ops.New("resync", 2, 100, 9)
	if op == nil {
		t.Fatalf("任务登记失败")
	}
	m.Ops.SetCounts(op.ID, 1, 3)
	m.Ops.SetProgress(op.ID, 30, "running")

	got := opStatusData(t, h, op.ID)
	if got["status"] != "running" || got["done"] != float64(1) || got["total"] != float64(3) {
		t.Fatalf("超管应看到他班真实任务: %v", got)
	}
	calls := fdb.callsWith("FROM worklog_entry e WHERE e.id = ?")
	if len(calls) != 1 {
		t.Fatalf("异班任务应做一次卡片可操作性校验: %+v", calls)
	}
	q := calls[0]
	if !strings.Contains(q.query, "? = ?") || len(q.args) != 3 ||
		q.args[0] != int64(100) || q.args[1] != int64(1) || q.args[2] != int64(1) {
		t.Fatalf("超管校验 SQL 应放行任意班组且参数为 entryID/teamID/teamID: %+v", q)
	}
}

// TestOpStatusMemberVisibleCrossTeam 普通成员轮询本班可见的跨班卡任务：可操作则返回真实视图。
func TestOpStatusMemberVisibleCrossTeam(t *testing.T) {
	m, h, fdb := opStatusEnv(t, auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1})
	fdb.onRowsAlways("FROM worklog_entry e WHERE e.id = ?", []string{"team_id"}, []driver.Value{int64(2)})
	op := m.Ops.New("resync", 2, 100, 9)
	if op == nil {
		t.Fatalf("任务登记失败")
	}
	m.Ops.SetProgress(op.ID, 50, "running")

	got := opStatusData(t, h, op.ID)
	if got["status"] != "running" || got["progress"] != float64(50) {
		t.Fatalf("可见跨班卡应返回真实任务: %v", got)
	}
	calls := fdb.callsWith("FROM worklog_entry e WHERE e.id = ?")
	if len(calls) != 1 {
		t.Fatalf("异班任务应做一次卡片可操作性校验: %+v", calls)
	}
	q := calls[0]
	if !strings.Contains(q.query, "cross_team = 1") || len(q.args) != 3 ||
		q.args[0] != int64(100) || q.args[1] != int64(1) || q.args[2] != int64(1) {
		t.Fatalf("普通成员校验须沿用跨班可见性且参数为 entryID/teamID/teamID: %+v", q)
	}
}

// TestOpStatusForeignTeamUnknown 普通成员轮询不可见他班任务：统一 unknown，不泄露任务细节。
func TestOpStatusForeignTeamUnknown(t *testing.T) {
	m, h, fdb := opStatusEnv(t, auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1})
	fdb.onErr("FROM worklog_entry e WHERE e.id = ?", sql.ErrNoRows)
	op := m.Ops.New("resync", 2, 100, 9)
	if op == nil {
		t.Fatalf("任务登记失败")
	}
	m.Ops.SetFailedName(op.ID, "张三")
	assertUnknownOpStatus(t, opStatusData(t, h, op.ID))
}

// TestOpStatusMissingTaskUnknown 任务不存在/已过期：200 unknown，不得假报 done。
func TestOpStatusMissingTaskUnknown(t *testing.T) {
	_, h, _ := opStatusEnv(t, auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1})
	assertUnknownOpStatus(t, opStatusData(t, h, 999999))
}

// TestOpStatusSameTeamNoScopeQuery 本班任务直接返回真实状态，不再做额外的卡片校验查询。
func TestOpStatusSameTeamNoScopeQuery(t *testing.T) {
	m, h, fdb := opStatusEnv(t, testIdentity())
	op := m.Ops.New("resync", 1, 100, 9)
	if op == nil {
		t.Fatalf("任务登记失败")
	}
	got := opStatusData(t, h, op.ID)
	if got["status"] != "running" {
		t.Fatalf("本班任务应返回真实状态: %v", got)
	}
	if calls := fdb.callsWith("FROM worklog_entry e WHERE e.id = ?"); len(calls) != 0 {
		t.Fatalf("本班任务不应再做卡片校验查询: %+v", calls)
	}
}

// TestPhotoResyncAdminCrossTeam 超管可对他班卡片照片发起重同步。
func TestPhotoResyncAdminCrossTeam(t *testing.T) {
	m, h, fdb := opStatusEnv(t, auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1})
	fdb.onRowsAlways("p.entry_id, e.team_id", []string{"entry_id", "team_id"},
		[]driver.Value{int64(100), int64(2)})
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/photos/9/resync", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("超管应可重同步他班照片: %v", env)
	}
	opID := int64(env["data"].(map[string]any)["opId"].(float64))
	if opID == 0 {
		t.Fatalf("应返回 opId")
	}
	if st, _ := m.Ops.Status(opID); st.TeamID != 2 || st.EntryID != 100 {
		t.Fatalf("任务应挂他班卡片: %+v", st)
	}
	calls := fdb.callsWith("p.entry_id, e.team_id")
	if len(calls) != 1 {
		t.Fatalf("重同步应做一次照片所属卡片校验: %+v", calls)
	}
	q := calls[0]
	if !strings.Contains(q.query, "? = ?") || len(q.args) != 3 ||
		q.args[0] != int64(9) || q.args[1] != int64(1) || q.args[2] != int64(1) {
		t.Fatalf("超管校验 SQL 应放行任意班组且参数为 photoID/teamID/teamID: %+v", q)
	}
	waitOp(t, m, opID, "failed") // 后台照片查询无预置行 → 失败收尾（不污染后续用例）
}

// TestPhotoResyncMemberVisibleCrossTeam 普通成员可对可见跨班卡照片发起重同步。
func TestPhotoResyncMemberVisibleCrossTeam(t *testing.T) {
	m, h, fdb := opStatusEnv(t, auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1})
	fdb.onRowsAlways("p.entry_id, e.team_id", []string{"entry_id", "team_id"},
		[]driver.Value{int64(100), int64(2)})
	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/photos/9/resync", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("可见跨班卡应可重同步照片: %v", env)
	}
	opID := int64(env["data"].(map[string]any)["opId"].(float64))
	calls := fdb.callsWith("p.entry_id, e.team_id")
	if len(calls) != 1 {
		t.Fatalf("重同步应做一次照片所属卡片校验: %+v", calls)
	}
	q := calls[0]
	if !strings.Contains(q.query, "cross_team = 1") || len(q.args) != 3 ||
		q.args[0] != int64(9) || q.args[1] != int64(1) || q.args[2] != int64(1) {
		t.Fatalf("普通成员校验须沿用跨班可见性且参数为 photoID/teamID/teamID: %+v", q)
	}
	waitOp(t, m, opID, "failed")
}
