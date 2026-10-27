package crossing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"shade/internal/auth"
)

// teamStub 让生效班组解析命中固定班组（sys_team 查询）。
func teamStub(fdb *fakeDB, id int64, name string) {
	fdb.onRowsAlways("FROM sys_team WHERE id = ?", []string{"id", "name"},
		[]driver.Value{id, name})
}

func TestRequiresLogin(t *testing.T) {
	db, _ := openFake(t)
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 1, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/dashboard", "", "")
	if rec.Code != 401 {
		t.Fatalf("未登录应 401，实际 %d", rec.Code)
	}
}

func TestAppPermitDenied(t *testing.T) {
	db, _ := openFake(t)
	m := New(db, fakeCfg{})
	m.Permit = denyPermit{}
	h := newTestRouter(m, auth.Identity{ID: 1, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/dashboard", "t", "")
	if rec.Code != 403 {
		t.Fatalf("无应用权限应 403，实际 %d", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(40301) {
		t.Fatalf("业务码应 40301，实际 %v", env["code"])
	}
}

type denyPermit struct{}

func (denyPermit) Allow(_ context.Context, _ int64, _ string) (bool, error) { return false, nil }

type allowPermit struct{}

func (allowPermit) Allow(_ context.Context, _ int64, _ string) (bool, error) { return true, nil }

type failPermit struct{ err error }

func (p failPermit) Allow(_ context.Context, _ int64, _ string) (bool, error) { return false, p.err }

func TestAdminGateBlocksPlainUser(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 1, "检修一班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 2, Role: "user", TeamID: 1})
	rec := doJSON(t, h, "DELETE", "/crossing/records/9", "t", "")
	if rec.Code != 403 {
		t.Fatalf("普通用户删记录应 403，实际 %d", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(40304) {
		t.Fatalf("业务码应 40304，实际 %v", env["code"])
	}
}

func TestTeamAdminCannotCrossTeam(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 1, "检修一班") // 班管固定本班（Node 口径：非超管忽略 ?team_id=）
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 3, Role: "team_admin", TeamID: 1})
	rec := doJSON(t, h, "GET", "/crossing/staging/batches?team_id=2", "t", "")
	if rec.Code != 200 {
		t.Fatalf("班管查本班暂存区应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	calls := fdb.callsWith("FROM sys_team")
	for _, c := range calls {
		if len(c.args) > 0 && c.args[0] != driver.Value(int64(1)) {
			t.Fatalf("班管生效班组应固定本班 1，实际 %v", c.args)
		}
	}
}

func TestAdminSelectsTeamByQuery(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 2, "检修二班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/staging/batches?team_id=2", "t", "")
	if rec.Code != 200 {
		t.Fatalf("超管指定班组应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	found := false
	for _, c := range fdb.callsWith("FROM sys_team WHERE id = ?") {
		if len(c.args) > 0 && c.args[0] == driver.Value(int64(2)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("超管 ?team_id=2 应查询班组 2，calls=%v", fdb.calls)
	}
}

func TestDashboardCounts(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	// 路由顺序敏感：更具体的子串先注册
	fdb.onRowsAlways("AND created_at >= ?", []string{"n"}, []driver.Value{int64(3)})
	fdb.onRowsAlways("has_hazard <> ''", []string{"n"}, []driver.Value{int64(2)})
	fdb.onRowsAlways("COUNT(DISTINCT", []string{"n"}, []driver.Value{int64(4)})
	fdb.onRowsAlways("FROM crossing_conflict", []string{"n"}, []driver.Value{int64(1)})
	fdb.onRowsAlways("FROM crossing_staging_batch", []string{"n"}, []driver.Value{int64(6)})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", []string{"n"}, []driver.Value{int64(7)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/dashboard", "t", "")
	if rec.Code != 200 {
		t.Fatalf("看板应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec)
	data := env["data"].(map[string]any)
	want := map[string]float64{
		"total": 7, "week_new": 3, "hazard": 2, "pending_lines": 4,
		"pending_conflicts": 1, "staging_batches": 6,
	}
	for k, v := range want {
		if data[k] != v {
			t.Fatalf("%s 应 %v，实际 %v", k, v, data[k])
		}
	}
}

func TestRecordListGrouping(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM team_line WHERE team_id", []string{"voltage_level", "line_name"},
		[]driver.Value{"110", "甲线"},
		[]driver.Value{"220", "乙线"},
	)
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", recordColsOf(),
		recordRow(1, 5, "110", "甲线", "2#-3#", 2, 3),
		recordRow(2, 5, "110", "甲线", "10#-20#", 10, 20),
		recordRow(3, 5, "35", "丙线", "1#-2#", 1, 2),
	)
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/records", "t", "")
	if rec.Code != 200 {
		t.Fatalf("记录列表应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec)["data"].(map[string]any)
	lines := data["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("运维线路应全量成组下发 2 组，实际 %d", len(lines))
	}
	first := lines[0].(map[string]any)
	if first["line_name"] != "甲线" {
		t.Fatalf("首组应甲线，实际 %v", first["line_name"])
	}
	recs := first["records"].([]any)
	if len(recs) != 2 {
		t.Fatalf("甲线应 2 条，实际 %d", len(recs))
	}
	// 组内排序：区段数字自然序（2#-3# 在 10#-20# 前）
	if recs[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("组内应按区段自然序，首条 %v", recs[0].(map[string]any)["id"])
	}
	unmatched := data["unmatched"].([]any)
	if len(unmatched) != 1 || unmatched[0].(map[string]any)["line_name"] != "丙线" {
		t.Fatalf("未匹配应为丙线，实际 %v", unmatched)
	}
}

func TestDeleteWritesTombstone(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_record WHERE id = ? AND team_id = ?", []string{"id"},
		[]driver.Value{int64(42)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "DELETE", "/crossing/records/42", "t", "")
	if rec.Code != 200 {
		t.Fatalf("删除应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if len(fdb.callsWith("DELETE FROM crossing_record WHERE id = ?")) == 0 {
		t.Fatal("应执行删除记录 SQL")
	}
	tombs := fdb.callsWith("INSERT IGNORE INTO crossing_deleted_id")
	if len(tombs) != 1 {
		t.Fatalf("应写墓碑 1 次，实际 %d", len(tombs))
	}
	if tombs[0].args[0] != driver.Value(int64(42)) || tombs[0].args[1] != driver.Value(int64(5)) {
		t.Fatalf("墓碑参数应为 (42,5)，实际 %v", tombs[0].args)
	}
}

// ---- 便捷行构造 ----

// recordColsOf 记录全列列名（与 scanRecord 顺序一致）。
func recordColsOf() []string {
	return []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to",
		"crossing_type", "dist_small", "dist_big", "cross_horizontal", "cross_vertical",
		"cross_angle", "has_hazard", "finder_name", "inspect_at", "remark", "source",
		"crane_session_id", "crane_record_id", "synced_at", "created_by", "created_at", "updated_at",
	}
}

// recordRow 构造一行记录（可空列给默认值）。
func recordRow(id, teamID int64, voltage, line, section string, poleFrom, poleTo int64) []driver.Value {
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	return []driver.Value{
		id, teamID, voltage, line, section, poleFrom, poleTo,
		"厂区", "10", "20", "", "8", "45", "",
		"张三", "2026-02-01", "无", "crane", "sess-1", "rec-1",
		nil, nil, now, now,
	}
}

// errBoom 通用失败错误。
var errBoom = errors.New("boom")

// 防止未使用导入告警。
var _ = sql.ErrNoRows
