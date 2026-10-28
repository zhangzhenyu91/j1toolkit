package crossing

import (
	"database/sql/driver"
	"strings"
	"testing"

	"shade/internal/auth"
)

// ---- POST /records 手动新增 ----

// TestRecordCreateManual 手动新增：业务列清洗 + 区段解析数值锚点 + source='manual' + created_by。
func TestRecordCreateManual(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "team_admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/records", "t", `{
      "voltage_level": "110", "line_name": "甲线", "section": "15#-16#",
      "crossing_type": "10kV线路", "dist_small": "10", "dist_big": "20",
      "cross_horizontal": "", "cross_vertical": "8", "cross_angle": "45",
      "has_hazard": "", "finder_name": "张三", "inspect_at": "2026.02.01", "remark": "无"
    }`)
	if rec.Code != 200 {
		t.Fatalf("手动新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	inserts := fdb.callsWith("INSERT INTO crossing_record")
	if len(inserts) != 1 {
		t.Fatalf("应插入 1 次，实际 %d", len(inserts))
	}
	q := inserts[0].query
	if !strings.Contains(q, "'manual'") || !strings.Contains(q, "created_by") {
		t.Fatalf("插入应落 source='manual' 与 created_by，SQL=%s", q)
	}
	if strings.Contains(q, "crane_session_id") || strings.Contains(q, "crane_record_id") {
		t.Fatalf("手动新增不得写 crane 幂等键（应保持 NULL），SQL=%s", q)
	}
	args := inserts[0].args
	// 顺序：team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
	// dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
	// finder_name, inspect_at, remark, created_by
	if args[0] != driver.Value(int64(5)) {
		t.Fatalf("team_id 应 5，实际 %v", args[0])
	}
	if args[3] != driver.Value("15#-16#") {
		t.Fatalf("区段应原文入库，实际 %v", args[3])
	}
	if args[4] != driver.Value(int64(15)) || args[5] != driver.Value(int64(16)) {
		t.Fatalf("pole 数值锚点应 15/16，实际 %v/%v", args[4], args[5])
	}
	if args[14] != driver.Value("2026-02-01") {
		t.Fatalf("inspect_at 应归一 2026-02-01，实际 %v", args[14])
	}
	if args[16] != driver.Value(int64(9)) {
		t.Fatalf("created_by 应 9，实际 %v", args[16])
	}
}

// TestRecordCreateLetteredSection 带字母杆号：区段原文不剔除字母，pole 数值锚点解析不出即空。
func TestRecordCreateLetteredSection(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "team_admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/records", "t",
		`{"line_name": "甲线", "section": "A15#-A16#", "inspect_at": ""}`)
	if rec.Code != 200 {
		t.Fatalf("手动新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	inserts := fdb.callsWith("INSERT INTO crossing_record")
	if len(inserts) != 1 {
		t.Fatalf("应插入 1 次，实际 %d", len(inserts))
	}
	args := inserts[0].args
	if args[3] != driver.Value("A15#-A16#") {
		t.Fatalf("带字母杆号区段应原文保留，实际 %v", args[3])
	}
	if args[4] != nil || args[5] != nil {
		t.Fatalf("带字母杆号 pole 锚点应为空，实际 %v/%v", args[4], args[5])
	}
	if args[14] != nil {
		t.Fatalf("空/无法识别的 inspect_at 应存 NULL，实际 %v", args[14])
	}
}

// TestRecordCreateRequiresLineName 缺线路名称 → 40001。
func TestRecordCreateRequiresLineName(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/records", "t", `{"section": "15#-16#"}`)
	if rec.Code != 400 {
		t.Fatalf("缺线路名称应 400，实际 %d", rec.Code)
	}
	if env := decodeEnvelope(t, rec); env["code"] != float64(40001) {
		t.Fatalf("业务码应 40001，实际 %v", env["code"])
	}
}

// TestRecordCreateNoTeam 无可用班组 → 40310（同记录详情/删除口径；enterAdmin 管理门控下仅超管可到此步）。
func TestRecordCreateNoTeam(t *testing.T) {
	db, _ := openFake(t) // 不打团队桩：班组解析空
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/records", "t", `{"line_name": "甲线"}`)
	if rec.Code != 403 {
		t.Fatalf("无班组应 403，实际 %d", rec.Code)
	}
	if env := decodeEnvelope(t, rec); env["code"] != float64(40310) {
		t.Fatalf("业务码应 40310，实际 %v", env["code"])
	}
}

// ---- PUT /records/:id 编辑 ----

// TestRecordUpdateKeepsIdentity 编辑：原地更新业务列 + 区段重解析锚点，ID/source/kcrane 键/synced_at 不动。
func TestRecordUpdateKeepsIdentity(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_record WHERE id = ? AND team_id = ?", []string{"id"},
		[]driver.Value{int64(42)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "team_admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/records/42", "t",
		`{"section": "A15#-A16#", "remark": "新备注", "inspect_at": "2026/2/1"}`)
	if rec.Code != 200 {
		t.Fatalf("编辑应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	updates := fdb.callsWith("UPDATE crossing_record SET")
	if len(updates) != 1 {
		t.Fatalf("应更新 1 次，实际 %d", len(updates))
	}
	q := updates[0].query
	if !strings.Contains(q, "WHERE id = ? AND team_id = ?") {
		t.Fatalf("更新应带 id + team_id 防越班，SQL=%s", q)
	}
	for _, banned := range []string{"source", "crane_session_id", "crane_record_id", "synced_at", "created_by", "created_at"} {
		if strings.Contains(q, banned) {
			t.Fatalf("编辑不得改动 %s，SQL=%s", banned, q)
		}
	}
	args := updates[0].args
	// 顺序：section, remark, inspect_at, pole_from, pole_to, id, team_id
	if args[0] != driver.Value("A15#-A16#") {
		t.Fatalf("区段应原文保留，实际 %v", args[0])
	}
	if args[1] != driver.Value("新备注") {
		t.Fatalf("备注应更新，实际 %v", args[1])
	}
	if args[2] != driver.Value("2026-02-01") {
		t.Fatalf("inspect_at 应归一 2026-02-01，实际 %v", args[2])
	}
	if args[3] != nil || args[4] != nil {
		t.Fatalf("带字母杆号区段锚点应为空，实际 %v/%v", args[3], args[4])
	}
	if args[5] != driver.Value(int64(42)) || args[6] != driver.Value(int64(5)) {
		t.Fatalf("WHERE 参数应 (42,5)，实际 %v/%v", args[5], args[6])
	}
}

// TestRecordUpdatePlainSectionPoles 普通区段：编辑时数值锚点随区段重解析。
func TestRecordUpdatePlainSectionPoles(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_record WHERE id = ? AND team_id = ?", []string{"id"},
		[]driver.Value{int64(7)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/records/7", "t", `{"section": "3#-4#"}`)
	if rec.Code != 200 {
		t.Fatalf("编辑应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	updates := fdb.callsWith("UPDATE crossing_record SET")
	if len(updates) != 1 {
		t.Fatalf("应更新 1 次，实际 %d", len(updates))
	}
	args := updates[0].args
	if args[0] != driver.Value("3#-4#") || args[1] != driver.Value(int64(3)) || args[2] != driver.Value(int64(4)) {
		t.Fatalf("区段与锚点应 3#-4# / 3 / 4，实际 %v / %v / %v", args[0], args[1], args[2])
	}
}

// TestRecordUpdatePartialNoPoles 不带区段的局部编辑：锚点列不进 SET。
func TestRecordUpdatePartialNoPoles(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_record WHERE id = ? AND team_id = ?", []string{"id"},
		[]driver.Value{int64(7)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/records/7", "t", `{"remark": "仅备注"}`)
	if rec.Code != 200 {
		t.Fatalf("编辑应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	updates := fdb.callsWith("UPDATE crossing_record SET")
	if len(updates) != 1 {
		t.Fatalf("应更新 1 次，实际 %d", len(updates))
	}
	q := updates[0].query
	if strings.Contains(q, "pole_from") {
		t.Fatalf("未改区段不应重写 pole 锚点，SQL=%s", q)
	}
	if updates[0].args[0] != driver.Value("仅备注") {
		t.Fatalf("仅应更新备注，实际 %v", updates[0].args)
	}
}

// TestRecordUpdateNotFound 跨班/不存在 → 40400（班组隔离）。
func TestRecordUpdateNotFound(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班") // 不登记记录存在性查询：默认空行 → 判不存在
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/records/404", "t", `{"remark": "x"}`)
	if rec.Code != 404 {
		t.Fatalf("不存在应 404，实际 %d", rec.Code)
	}
	if env := decodeEnvelope(t, rec); env["code"] != float64(40400) {
		t.Fatalf("业务码应 40400，实际 %v", env["code"])
	}
}

// TestRecordUpdateNoFields 空 body → 40001 没有可更新的字段。
func TestRecordUpdateNoFields(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_record WHERE id = ? AND team_id = ?", []string{"id"},
		[]driver.Value{int64(7)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/records/7", "t", `{}`)
	if rec.Code != 400 {
		t.Fatalf("空字段应 400，实际 %d", rec.Code)
	}
	if env := decodeEnvelope(t, rec); env["code"] != float64(40001) {
		t.Fatalf("业务码应 40001，实际 %v", env["code"])
	}
}

// TestRecordWriteAdminGate 普通用户手动增改 → 40304（管理口径与删除一致）。
func TestRecordWriteAdminGate(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 2, Role: "user", TeamID: 5})
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/crossing/records", `{"line_name": "甲线"}`},
		{"PUT", "/crossing/records/7", `{"remark": "x"}`},
	} {
		rec := doJSON(t, h, tc.method, tc.path, "t", tc.body)
		if rec.Code != 403 {
			t.Fatalf("%s %s 普通用户应 403，实际 %d", tc.method, tc.path, rec.Code)
		}
		if env := decodeEnvelope(t, rec); env["code"] != float64(40304) {
			t.Fatalf("%s %s 业务码应 40304，实际 %v", tc.method, tc.path, env["code"])
		}
	}
}
