package treehazard

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// fakeAuth 把固定身份注入 httpapi.AuthMiddleware 的上下文（与生产同一条装配路径）。
type fakeAuth struct {
	ident auth.Identity
	fail  bool
}

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: f.ident}, nil
}

func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) {
	if f.fail {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return f.ident, nil
}

func (f fakeAuth) Logout(context.Context, string, time.Time) error { return nil }

type testEnv struct {
	handler http.Handler
	fake    *fakeDB
	module  *Module
	objects *memObjects
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	objects := &memObjects{}
	m := New(db)
	m.Permit = AllowAllPermit{}
	m.Objects = objects
	m.Cfg = memConfig{"TREEHAZARD_INGEST_TOKEN": "secret-token", "COS_TREEHAZARD_PREFIX": "treehazard/"}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler: httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:    fake,
		module:  m,
		objects: objects,
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	return env
}

func dataMap(t *testing.T, env Envelope) map[string]any {
	t.Helper()
	m, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data 应为对象，实际 %#v", env.Data)
	}
	return m
}

var (
	identAdmin     = auth.Identity{ID: 1, Username: "admin", Nickname: "超管", Role: "admin", TeamID: 1}
	identTeamAdmin = auth.Identity{ID: 2, Username: "tm", Nickname: "班管", Role: "team_admin", TeamID: 1}
	identOtherTM   = auth.Identity{ID: 3, Username: "tm2", Nickname: "他班班管", Role: "team_admin", TeamID: 2}
	identUser      = auth.Identity{ID: 4, Username: "u", Nickname: "小张", Role: "user", TeamID: 1}
)

func teamRule() fakeRule {
	return fakeRule{match: "FROM sys_team", cols: []string{"id", "name"}, rows: [][]driver.Value{{int64(1), "检修一班"}}}
}

// TestIngestToken 直推密钥：未配置 50301 / 错误 40104 / 正确放行。
func TestIngestToken(t *testing.T) {
	db, fake := openFake(t)
	m := New(db)
	m.Cfg = memConfig{} // 未配置
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/treehazard/ingest", strings.NewReader("{}"))
	req.Header.Set("X-Ingest-Token", "any")
	r.ServeHTTP(rec, req)
	if env := decodeEnvelope(t, rec); env.Code != codeNotConfigured {
		t.Fatalf("未配置应 50301，实际 %d", env.Code)
	}

	env := newTestServer(t, identAdmin)
	env.module.Cfg = memConfig{"TREEHAZARD_INGEST_TOKEN": "secret-token"}
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	rec = env.do(t, "POST", "/api/v1/treehazard/ingest", map[string]any{})
	// 未带密钥 → 40104
	if env := decodeEnvelope(t, rec); env.Code != codeIngestToken {
		t.Fatalf("缺密钥应 40104，实际 %d", env.Code)
	}

	req = httptest.NewRequest("POST", "/api/v1/treehazard/ingest", strings.NewReader(`{"session_id":"s1","team_name":"检修一班","mode":"direct","records":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Token", "secret-token")
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确密钥应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	_ = fake
}

// TestIngestDirectUpsertAndSkipped direct 幂等 upsert + 跳过计数 + deletes 撤回。
func TestIngestDirectUpsertAndSkipped(t *testing.T) {
	env := newTestServer(t, identAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	// 已存在（幂等命中）：SELECT ... crane_session_id = ? AND crane_record_id = ?
	env.fake.on("crane_session_id = ? AND crane_record_id = ?", []string{"id"}, [][]driver.Value{{int64(55)}})

	body := map[string]any{
		"session_id": "sess-1",
		"team_name":  "检修一班",
		"mode":       "direct",
		"records": []any{
			map[string]any{ // 命中幂等 → update
				"crane_record_id": "r1", "line_name": "甲线", "section": "3#-4#",
				"vertical": "超高2", "tree_species": "杨树", "found_at": "2026.10.20",
			},
			map[string]any{ // 缺线路名 → skipped
				"crane_record_id": "r2", "section": "1#-2#",
			},
		},
		"deletes": []any{
			map[string]any{"crane_record_id": "gone"}, // 未命中 → skipped
			map[string]any{"msg_id": "m"},             // 缺 id → skipped
		},
	}
	req := httptest.NewRequest("POST", "/api/v1/treehazard/ingest", nil)
	raw, _ := json.Marshal(body)
	req = httptest.NewRequest("POST", "/api/v1/treehazard/ingest", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Token", "secret-token")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("直推应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	envj := decodeEnvelope(t, rec)
	data := dataMap(t, envj)
	if fmt.Sprint(data["inserted"]) != "0" || fmt.Sprint(data["updated"]) != "1" {
		t.Fatalf("应 updated=1 inserted=0，实际 %v", data)
	}
	if fmt.Sprint(data["skipped"]) != "3" { // 缺线路名 + deletes 未命中 + 缺 id
		t.Fatalf("应 skipped=3，实际 %v", data)
	}
	if env.fake.execCount("UPDATE treehazard_record SET voltage_level") != 1 {
		t.Fatalf("幂等命中应更新发现侧列，实际 %v", env.fake.allQueries())
	}
	if env.fake.execCount("INSERT INTO treehazard_record") != 0 {
		t.Fatalf("幂等命中不应插入，实际 %v", env.fake.allQueries())
	}
}

// TestIngestDirectDeleteTombstone deletes 命中 → 整条删除 + 写墓碑。
func TestIngestDirectDeleteTombstone(t *testing.T) {
	env := newTestServer(t, identAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT id FROM treehazard_record WHERE team_id = ? AND crane_record_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(77)}})
	env.fake.on("SELECT cos_key FROM treehazard_photo WHERE record_id = ?", []string{"cos_key"},
		[][]driver.Value{{"treehazard/检修一班/77/before-1.jpg"}})

	body := map[string]any{
		"session_id": "sess-2", "team_name": "检修一班", "mode": "direct",
		"deletes": []any{map[string]any{"crane_record_id": "r9"}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/treehazard/ingest", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Token", "secret-token")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("直推应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["deleted"]) != "1" {
		t.Fatalf("应 deleted=1，实际 %v", data)
	}
	if env.fake.execCount("INSERT IGNORE INTO treehazard_deleted_id") != 1 {
		t.Fatalf("应写删除墓碑，实际 %v", env.fake.allQueries())
	}
	if len(env.objects.deletes) != 1 {
		t.Fatalf("应删除照片 COS 对象，实际 %v", env.objects.deletes)
	}
}

// TestIngestStaging 整批替换暂存区：复用批次 → 清旧行、复位 pending。
func TestIngestStaging(t *testing.T) {
	env := newTestServer(t, identAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT id FROM treehazard_staging_batch WHERE team_id = ? AND crane_session_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(9)}})
	body := map[string]any{
		"session_id": "sess-3", "team_name": "检修一班", "mode": "staging",
		"group_name": "检修一班群", "pusher": map[string]any{"name": "王五"},
		"records": []any{map[string]any{"crane_record_id": "c1", "line_name": "乙线", "section": "5#-6#"}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/treehazard/ingest", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ingest-Token", "secret-token")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("暂存直推应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["batch_id"]) != "9" || fmt.Sprint(data["count"]) != "1" {
		t.Fatalf("应复用批次 9 且 count=1，实际 %v", data)
	}
	if env.fake.execCount("DELETE FROM treehazard_staging_record WHERE batch_id = ?") != 1 {
		t.Fatalf("整批替换应清旧暂存行，实际 %v", env.fake.allQueries())
	}
	if env.fake.execCount("status = 'pending'") != 1 {
		t.Fatalf("应复位批次为 pending，实际 %v", env.fake.allQueries())
	}
	if env.fake.execCount("INSERT INTO treehazard_staging_record") != 1 {
		t.Fatalf("应写入暂存行，实际 %v", env.fake.allQueries())
	}
}

// TestSettingsThreshold 阈值设置：非负数字校验 + upsert；班管门控。
func TestSettingsThreshold(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	rec := env.do(t, "PUT", "/api/v1/treehazard/settings", map[string]any{"vertical_threshold": -1})
	if env := decodeEnvelope(t, rec); env.Code != codeParam {
		t.Fatalf("负阈值应 40001，实际 %d", env.Code)
	}
	rec = env.do(t, "PUT", "/api/v1/treehazard/settings", map[string]any{"vertical_threshold": 2.5})
	if rec.Code != http.StatusOK {
		t.Fatalf("设置应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if env.fake.execCount("INSERT INTO treehazard_team_setting") != 1 {
		t.Fatalf("应 upsert 阈值，实际 %v", env.fake.allQueries())
	}

	// 普通用户不可改设置。
	env3 := newTestServer(t, identUser)
	env3.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	rec = env3.do(t, "PUT", "/api/v1/treehazard/settings", map[string]any{"vertical_threshold": 3})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户改设置应 403，实际 %d", rec.Code)
	}

	// 他班班管不可改本班设置。
	env4 := newTestServer(t, identOtherTM)
	env4.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	rec = env4.do(t, "PUT", "/api/v1/treehazard/settings", map[string]any{"vertical_threshold": 3})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("他班班管应 403，实际 %d", rec.Code)
	}
}

// TestRecordDeleteTombstone 删记录：墓碑 SQL + 照片对象清理。
func TestRecordDeleteTombstone(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(5)}})
	env.fake.on("SELECT cos_key FROM treehazard_photo WHERE record_id = ?", []string{"cos_key"},
		[][]driver.Value{{"treehazard/k/5/before-1.jpg"}, {"treehazard/k/5/after-1.jpg"}})
	rec := env.do(t, "DELETE", "/api/v1/treehazard/records/5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if env.fake.execCount("DELETE FROM treehazard_record WHERE id = ?") != 1 {
		t.Fatalf("应删记录行，实际 %v", env.fake.allQueries())
	}
	if env.fake.execCount("INSERT IGNORE INTO treehazard_deleted_id") != 1 {
		t.Fatalf("应写墓碑，实际 %v", env.fake.allQueries())
	}
	if len(env.objects.deletes) != 2 {
		t.Fatalf("应清理 2 个照片对象，实际 %v", env.objects.deletes)
	}
}

// TestDashboard 看板计数（含阈值比对）。
func TestDashboard(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT vertical_threshold FROM treehazard_team_setting",
		[]string{"vertical_threshold"}, [][]driver.Value{{float64(3)}})
	env.fake.on("status = 'unhandled' AND vertical_num IS NOT NULL", []string{"n"}, [][]driver.Value{{int64(2)}})
	env.fake.on("COUNT(*) FROM treehazard_record WHERE team_id = ? AND status = 'unhandled'", []string{"n"}, [][]driver.Value{{int64(5)}})
	env.fake.on("created_at >=", []string{"n"}, [][]driver.Value{{int64(1)}})
	env.fake.on("status = 'handled'", []string{"n"}, [][]driver.Value{{int64(4)}})
	env.fake.on("treehazard_conflict", []string{"n"}, [][]driver.Value{{int64(0)}})
	env.fake.on("treehazard_staging_batch", []string{"n"}, [][]driver.Value{{int64(1)}})
	rec := env.do(t, "GET", "/api/v1/treehazard/dashboard", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("看板应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["unhandled"]) != "5" || fmt.Sprint(data["over_threshold"]) != "2" {
		t.Fatalf("看板计数不对：%v", data)
	}
	if fmt.Sprint(data["threshold"]) != "3" {
		t.Fatalf("阈值应为 3，实际 %v", data["threshold"])
	}
}

// TestStagingImport 暂存导入：撞幂等键走更新、pole 由区段解析、批次清空后置 imported。
func TestStagingImport(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT crane_session_id, status FROM treehazard_staging_batch",
		[]string{"crane_session_id", "status"}, [][]driver.Value{{"sess-1", "pending"}})
	env.fake.on("FROM treehazard_staging_record WHERE batch_id", []string{
		"id", "batch_id", "crane_record_id", "voltage_level", "line_name", "section", "tree_species",
		"tree_count", "dist_small", "dist_big", "diameter", "horizontal", "vertical", "vertical_num",
		"finder_name", "found_at", "handler_name", "handle_note", "handled_at",
	}, [][]driver.Value{{
		int64(11), int64(3), "c1", "110kV", "甲线", "3#-4#", "杨树",
		"2", "5", "6", "20", "3", "超高2", float64(2),
		"张三", "2026-10-20", "", "", "",
	}})
	env.fake.on("SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ? AND (crane_record_id",
		[]string{"cnt"}, [][]driver.Value{{int64(1)}})
	env.fake.on("crane_session_id = ? AND crane_record_id = ?", []string{"id"}, [][]driver.Value{{int64(55)}})
	env.fake.on("SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ?",
		[]string{"cnt"}, [][]driver.Value{{int64(0)}})

	rec := env.do(t, "POST", "/api/v1/treehazard/staging/batches/3/import", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["imported"]) != "1" {
		t.Fatalf("应 imported=1，实际 %v", data)
	}
	if env.fake.execCount("UPDATE treehazard_record SET voltage_level") != 1 {
		t.Fatalf("撞幂等键应更新发现侧列，实际 %v", env.fake.allQueries())
	}
	if env.fake.execCount("status = 'imported'") != 1 {
		t.Fatalf("批次清空应置 imported，实际 %v", env.fake.allQueries())
	}
	// pole 区间由区段解析写入。
	if ex, ok := env.fake.lastExec("UPDATE treehazard_record SET voltage_level"); ok {
		if len(ex.args) < 5 {
			t.Fatalf("更新参数不足：%v", ex.args)
		}
		if fmt.Sprint(ex.args[3]) != "3" || fmt.Sprint(ex.args[4]) != "4" {
			t.Fatalf("pole_from/to 应为 3/4，实际 %v %v", ex.args[3], ex.args[4])
		}
	} else {
		t.Fatalf("未找到更新语句")
	}
}

// TestPhotoUpload 照片上传：格式/大小校验 + COS key 前缀 + 表行写入。
func TestPhotoUpload(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(5)}})

	rec := env.do(t, "POST", "/api/v1/treehazard/records/5/photos?kind=before",
		map[string]any{"image": "data:image/gif;base64,AAAA"})
	if env := decodeEnvelope(t, rec); env.Code != codePhotoFormat {
		t.Fatalf("非 jpeg/png 应 40010，实际 %d", env.Code)
	}

	rec = env.do(t, "POST", "/api/v1/treehazard/records/5/photos?kind=before",
		map[string]any{"image": "data:image/jpeg;base64," + strings.Repeat("A", 24)})
	if rec.Code != http.StatusOK {
		t.Fatalf("上传应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if len(env.objects.puts) != 1 || !strings.HasPrefix(env.objects.puts[0], "treehazard/检修一班/5/before-") {
		t.Fatalf("COS key 前缀不对：%v", env.objects.puts)
	}
	if env.fake.execCount("INSERT INTO treehazard_photo") != 1 {
		t.Fatalf("应写照片表行，实际 %v", env.fake.allQueries())
	}
}
