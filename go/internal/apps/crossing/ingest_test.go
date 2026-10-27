package crossing

import (
	"database/sql/driver"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shade/internal/auth"
)

// doIngest 以指定 X-Ingest-Token 发直推请求（直推自鉴权，不走 JWT）。
func doIngest(t *testing.T, m *Module, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/crossing/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Token", token)
	rec := httptest.NewRecorder()
	newRawRouter(m).ServeHTTP(rec, req)
	return rec
}

func TestIngestTokenGate(t *testing.T) {
	db, _ := openFake(t)

	// 未配置密钥 → 503 50301
	m := New(db, fakeCfg{})
	rec := doIngest(t, m, "", `{"session_id":"s","team_name":"检修一班","mode":"direct"}`)
	if rec.Code != 503 || decodeEnvelope(t, rec)["code"] != float64(50301) {
		t.Fatalf("未配置密钥应 503/50301，实际 %d %s", rec.Code, rec.Body.String())
	}

	m2 := New(db, fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}})

	// 密钥错误 → 401 40104
	rec2 := doIngest(t, m2, "wrong", `{"session_id":"s","team_name":"检修一班","mode":"direct"}`)
	if rec2.Code != 401 || decodeEnvelope(t, rec2)["code"] != float64(40104) {
		t.Fatalf("密钥错误应 401/40104，实际 %d %s", rec2.Code, rec2.Body.String())
	}

	// 缺 session_id → 40001
	rec3 := doIngest(t, m2, "s3cr3t", `{"team_name":"检修一班","mode":"direct"}`)
	if rec3.Code != 400 || decodeEnvelope(t, rec3)["code"] != float64(40001) {
		t.Fatalf("缺 session_id 应 400/40001，实际 %d %s", rec3.Code, rec3.Body.String())
	}

	// mode 非法 → 40001
	rec4 := doIngest(t, m2, "s3cr3t", `{"session_id":"s","team_name":"检修一班","mode":"other"}`)
	if rec4.Code != 400 || decodeEnvelope(t, rec4)["code"] != float64(40001) {
		t.Fatalf("mode 非法应 400/40001，实际 %d %s", rec4.Code, rec4.Body.String())
	}
}

func TestIngestDirectUpsertAndSkip(t *testing.T) {
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(5), "检修一班"})
	// rec-1 已存在（同 session 幂等命中）→ 更新；rec-2 未命中 → 插入
	fdb.onRows("crane_session_id = ? AND crane_record_id = ?", []string{"id"}, []driver.Value{int64(101)})
	fdb.onRows("crane_session_id = ? AND crane_record_id = ?", []string{"id"}) // 空 → 未命中
	m := New(db, fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}})
	body := `{
      "session_id": "s1", "team_name": "检修一班", "mode": "direct",
      "records": [
        {"crane_record_id": "rec-1", "line_name": "甲线", "section": "15#-16#", "crossing_type": "10kV线路", "inspect_at": "2026.03.04"},
        {"crane_record_id": "rec-2", "line_name_raw": "乙线", "pole_from": "12x", "remark": "备注"},
        {"crane_record_id": "rec-3", "section": "无线路名"}
      ]
    }`
	rec := doIngest(t, m, "s3cr3t", body)
	if rec.Code != 200 {
		t.Fatalf("直推应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec)["data"].(map[string]any)
	if data["inserted"] != float64(1) || data["updated"] != float64(1) || data["skipped"] != float64(1) {
		t.Fatalf("应 inserted=1 updated=1 skipped=1，实际 %v", data)
	}
	if data["deleted"] != float64(0) {
		t.Fatalf("deleted 应 0，实际 %v", data["deleted"])
	}
	updates := fdb.callsWith("UPDATE crossing_record SET ")
	if len(updates) != 1 {
		t.Fatalf("应更新 1 次，实际 %d", len(updates))
	}
	// 日期归一：2026.03.04 → 2026-03-04
	found := false
	for _, a := range updates[0].args {
		if a == driver.Value("2026-03-04") {
			found = true
		}
	}
	if !found {
		t.Fatalf("inspect_at 应归一为 2026-03-04，args=%v", updates[0].args)
	}
	inserts := fdb.callsWith("INSERT INTO crossing_record")
	if len(inserts) != 1 {
		t.Fatalf("应插入 1 次，实际 %d", len(inserts))
	}
	// line_name_raw 回退 + parseInt("12x") → 12
	hasFallback, hasPole := false, false
	for _, a := range inserts[0].args {
		if a == driver.Value("乙线") {
			hasFallback = true
		}
		if a == driver.Value(int64(12)) {
			hasPole = true
		}
	}
	if !hasFallback || !hasPole {
		t.Fatalf("应回退 line_name_raw=乙线 且 pole_from=12，args=%v", inserts[0].args)
	}
}

func TestIngestDeletesAndSkipped(t *testing.T) {
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(5), "检修一班"})
	fdb.onRows("FROM crossing_record WHERE team_id = ? AND crane_record_id = ?", []string{"id"},
		[]driver.Value{int64(102)}, []driver.Value{int64(103)}) // 同 id 跨会话桶多行
	fdb.onRows("FROM crossing_record WHERE team_id = ? AND crane_record_id = ?", []string{"id"}) // d2 未命中
	m := New(db, fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}})
	body := `{
      "session_id": "s1", "team_name": "检修一班", "mode": "direct",
      "records": [],
      "deletes": [{"crane_record_id": "d1"}, {"crane_record_id": "d2"}, {"msg_id": "x"}]
    }`
	rec := doIngest(t, m, "s3cr3t", body)
	if rec.Code != 200 {
		t.Fatalf("直推应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec)["data"].(map[string]any)
	if data["deleted"] != float64(1) {
		t.Fatalf("deleted 应 1（命中一次记一），实际 %v", data["deleted"])
	}
	if data["skipped"] != float64(2) {
		t.Fatalf("skipped 应 2（d2 未命中 + 缺 id），实际 %v", data["skipped"])
	}
	dels := fdb.callsWith("DELETE FROM crossing_record WHERE id = ?")
	if len(dels) != 2 {
		t.Fatalf("命中两行应删 2 次，实际 %d", len(dels))
	}
	tombs := fdb.callsWith("INSERT IGNORE INTO crossing_deleted_id")
	if len(tombs) != 2 {
		t.Fatalf("应写 2 个墓碑，实际 %d", len(tombs))
	}
}

func TestIngestStagingBatchReplace(t *testing.T) {
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(5), "检修一班"})
	fdb.onRowsAlways("FROM crossing_staging_batch WHERE team_id = ? AND crane_session_id = ?",
		[]string{"id"}, []driver.Value{int64(7)})
	notifier := &fakeNotifier{}
	m := New(db, fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}})
	m.Notifier = notifier
	m.ServerDir = t.TempDir()
	xlsx := base64.StdEncoding.EncodeToString([]byte("fake-xlsx"))
	body := `{
      "session_id": "s2", "team_name": "检修一班", "mode": "staging",
      "group_name": "排查群", "pusher": {"name": "张三"}, "review_note": "请补充区段",
      "xlsx_base64": "` + xlsx + `",
      "records": [{"crane_record_id": "r1", "line_name": "甲线", "section": "15#-16#"}]
    }`
	rec := doIngest(t, m, "s3cr3t", body)
	if rec.Code != 200 {
		t.Fatalf("staging 直推应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec)["data"].(map[string]any)
	if data["batch_id"] != float64(7) || data["count"] != float64(1) {
		t.Fatalf("应 batch_id=7 count=1，实际 %v", data)
	}
	if len(fdb.callsWith("DELETE FROM crossing_staging_record WHERE batch_id = ?")) == 0 {
		t.Fatal("整批替换应先清旧暂存行")
	}
	if len(fdb.callsWith("UPDATE crossing_staging_batch SET ")) != 1 {
		t.Fatal("应复位批次 1 次")
	}
	if len(fdb.callsWith("INSERT INTO crossing_staging_record")) != 1 {
		t.Fatal("应写入 1 条暂存行")
	}
	// 随批 xlsx 落盘
	saved := filepath.Join(m.ServerDir, "data", "crossing", "inbox", "5", "s2.xlsx")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("随批 xlsx 应落盘于 %s: %v", saved, err)
	}
	// 站内通知（超管 + 班管）
	if len(notifier.pushes) != 1 || !strings.Contains(notifier.pushes[0], "交跨记录进暂存区") ||
		!strings.Contains(notifier.pushes[0], "张三") || !strings.Contains(notifier.pushes[0], "请补充区段") {
		t.Fatalf("暂存通知内容不符: %v", notifier.pushes)
	}
}

func TestIngestTeamNotFound(t *testing.T) {
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"}) // 空 → 不存在
	m := New(db, fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}})
	rec := doIngest(t, m, "s3cr3t", `{"session_id":"s","team_name":"不存在班","mode":"direct"}`)
	if rec.Code != 404 || decodeEnvelope(t, rec)["code"] != float64(40400) {
		t.Fatalf("班组不存在应 404/40400，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestStagingImportParsesSectionPoles(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'",
		[]string{"id", "crane_session_id"}, []driver.Value{int64(7), "s1"})
	fdb.onRowsAlways("FROM crossing_staging_record WHERE batch_id = ? ORDER BY id", stagingColsOf(),
		stagingRow(9, "r1", "甲线", "15#-16#"))
	fdb.onRows("crane_session_id = ? AND crane_record_id = ?", []string{"id"}, []driver.Value{int64(55)})
	fdb.onRowsAlways("COUNT(*) AS cnt FROM crossing_staging_record", []string{"cnt"}, []driver.Value{int64(0)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/staging/batches/7/import", "t", `{"record_ids":[9]}`)
	if rec.Code != 200 {
		t.Fatalf("导入应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if decodeEnvelope(t, rec)["data"].(map[string]any)["imported"] != float64(1) {
		t.Fatalf("imported 应 1，实际 %s", rec.Body.String())
	}
	upd := fdb.callsWith("UPDATE crossing_record SET ")
	if len(upd) != 1 {
		t.Fatalf("r1 撞幂等键应更新既有记录，实际 %d 次更新", len(upd))
	}
	args := upd[0].args
	if args[len(args)-1] != driver.Value(int64(55)) {
		t.Fatalf("更新应命中记录 55，args=%v", args)
	}
	hasFrom, hasTo := false, false
	for _, a := range args {
		if a == driver.Value(int64(15)) {
			hasFrom = true
		}
		if a == driver.Value(int64(16)) {
			hasTo = true
		}
	}
	if !hasFrom || !hasTo {
		t.Fatalf("区段 15#-16# 应解析为 pole_from=15 pole_to=16，args=%v", args)
	}
	if len(fdb.callsWith("SET status = 'imported'")) != 1 {
		t.Fatal("批次行空后应置 imported")
	}
}

func TestStagingImportInsertsNew(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'",
		[]string{"id", "crane_session_id"}, []driver.Value{int64(7), "s1"})
	// 空 crane_record_id + 解析不出杆号的区段
	fdb.onRowsAlways("FROM crossing_staging_record WHERE batch_id = ? ORDER BY id", stagingColsOf(),
		stagingRow(9, "", "甲线", "无杆号区段"))
	fdb.onRows("crane_session_id = ? AND crane_record_id = ?", []string{"id"}) // 未命中 → 插入
	fdb.onRowsAlways("COUNT(*) AS cnt FROM crossing_staging_record", []string{"cnt"}, []driver.Value{int64(0)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "POST", "/crossing/staging/batches/7/import", "t", "")
	if rec.Code != 200 {
		t.Fatalf("导入应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	inserts := fdb.callsWith("INSERT INTO crossing_record")
	if len(inserts) != 1 {
		t.Fatalf("未命中幂等键应插入，实际 %d", len(inserts))
	}
	args := inserts[0].args
	// pole 列应为 NULL（区段解析不出）
	poleSeen := 0
	for _, a := range args {
		if a == driver.Value(nil) {
			poleSeen++
		}
	}
	if poleSeen < 3 { // pole_from / pole_to / crane_record_id 均为 NULL
		t.Fatalf("pole_from/pole_to/crane_record_id 空值应存 NULL，args=%v", args)
	}
}

func TestStagingRecordUpdateWhitelist(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM crossing_staging_record s JOIN crossing_staging_batch b",
		[]string{"id"}, []driver.Value{int64(9)})
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "PUT", "/crossing/staging/records/9", "t",
		`{"voltage_level":"220","inspect_at":"2026.03.04","bogus":"x"}`)
	if rec.Code != 200 {
		t.Fatalf("暂存行更新应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	upd := fdb.callsWith("UPDATE crossing_staging_record SET ")
	if len(upd) != 1 {
		t.Fatalf("应更新 1 次，实际 %d", len(upd))
	}
	if !strings.Contains(upd[0].query, "voltage_level = ?") || !strings.Contains(upd[0].query, "inspect_at = ?") ||
		strings.Contains(upd[0].query, "bogus") {
		t.Fatalf("应仅更新白名单字段，SQL=%s", upd[0].query)
	}
	args := upd[0].args
	if args[0] != driver.Value("220") || args[1] != driver.Value("2026-03-04") {
		t.Fatalf("字段值/日期归一不符，args=%v", args)
	}
}

func TestStagingBatchDeleteCleansXlsx(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	m := New(db, fakeCfg{})
	m.ServerDir = t.TempDir()
	rel := filepath.Join("data", "crossing", "inbox", "5", "s2.xlsx")
	abs := filepath.Join(m.ServerDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fdb.onRowsAlways("FROM crossing_staging_batch WHERE id = ? AND team_id = ?",
		[]string{"xlsx_path"}, []driver.Value{rel})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "DELETE", "/crossing/staging/batches/7", "t", "")
	if rec.Code != 200 {
		t.Fatalf("批次删除应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	if len(fdb.callsWith("DELETE FROM crossing_staging_record WHERE batch_id = ?")) == 0 {
		t.Fatal("应删暂存行")
	}
	if len(fdb.callsWith("DELETE FROM crossing_staging_batch")) == 0 {
		t.Fatal("应删批次")
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("随批 xlsx 应一并清理: %v", err)
	}
}

// stagingColsOf 暂存行列名（与 stagingImport 扫描顺序一致）。
func stagingColsOf() []string {
	return []string{
		"id", "crane_record_id", "voltage_level", "line_name", "section", "crossing_type",
		"dist_small", "dist_big", "cross_horizontal", "cross_vertical", "cross_angle",
		"has_hazard", "finder_name", "inspect_at", "remark",
	}
}

// stagingRow 构造一行暂存记录（craneID 空串按空值存 NULL 口径）。
func stagingRow(id int64, craneID, line, section string) []driver.Value {
	var crane any
	if craneID != "" {
		crane = craneID
	}
	return []driver.Value{
		id, crane, "110", line, section, "10kV线路",
		"5", "6", "", "7", "30", "", "张三", "2026-02-01", "",
	}
}
