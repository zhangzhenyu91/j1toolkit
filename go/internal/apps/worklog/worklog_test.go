package worklog

// 出工日志行为测试：fake DB + httptest，覆盖权限/班组隔离、卡片 CRUD 校验、
// 七条核验规则、day-status 着色、字典管理、照片链路与跨班可见性；不访问外部服务。

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ---- 测试装配 ----

type fakeAuth struct{ ident auth.Identity }

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: f.ident}, nil
}
func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) { return f.ident, nil }
func (f fakeAuth) Logout(context.Context, string, time.Time) error             { return nil }

// memObjects 内存对象存储。
type memObjects struct {
	items map[string][]byte
}

func (m *memObjects) PutBuffer(_ context.Context, key string, buf []byte, _ string) error {
	if m.items == nil {
		m.items = map[string][]byte{}
	}
	m.items[key] = buf
	return nil
}
func (m *memObjects) DeleteObject(_ context.Context, key string) error {
	delete(m.items, key)
	return nil
}
func (m *memObjects) PublicURL(key string) string { return "https://cos.test/" + key }
func (m *memObjects) GetBuffer(_ context.Context, key string) ([]byte, error) {
	return m.items[key], nil
}

// memDify 可编排的 Dify fake。
type memDify struct {
	results []VerifyResult
	calls   int
}

func (d *memDify) VerifyPhoto(context.Context, string, string, string, string) VerifyResult {
	d.calls++
	if len(d.results) == 0 {
		return VerifyResult{}
	}
	r := d.results[0]
	if len(d.results) > 1 {
		d.results = d.results[1:]
	}
	return r
}

// memGeo 地理服务 fake。
type memGeo struct{ lw LocationWeather }

func (g memGeo) FetchLocationWeather(context.Context, string, string) LocationWeather { return g.lw }

// memConfig 运行时配置桩。
type memConfig map[string]string

func (c memConfig) Get(_ context.Context, key string) (string, error) { return c[key], nil }

type testEnv struct {
	handler http.Handler
	fake    *fakeDB
	module  *Module
	objects *memObjects
	dify    *memDify
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	objects := &memObjects{items: map[string][]byte{}}
	dify := &memDify{results: []VerifyResult{{
		OK: true, WorkContent: "绝缘子清扫", Time: "2026.08.18 09:30",
		Weather: "多云 32°C", Location: "孝义市·示范变电站", Lng: "111.7", Lat: "37.1",
	}}}
	m := New(db)
	m.Permit = AllowAllPermit{}
	m.Store = objects
	m.Dify = dify
	m.Geo = memGeo{lw: LocationWeather{Location: "尧都区·示例地标", Weather: "晴 30°C"}}
	m.DisableAsync = true
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler: httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:    fake,
		module:  m,
		objects: objects,
		dify:    dify,
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

func decodeEnv(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	return env
}

var (
	identAdmin = auth.Identity{ID: 1, Username: "admin", Nickname: "超管", Role: "admin", TeamID: 1}
	identTM    = auth.Identity{ID: 2, Username: "tm", Nickname: "班管", Role: "team_admin", TeamID: 1}
	identUser  = auth.Identity{ID: 4, Username: "u", Nickname: "赵登", Role: "user", TeamID: 1}
)

func teamRule() fakeRule {
	return fakeRule{match: "FROM sys_team", cols: []string{"id", "name"},
		rows: [][]driver.Value{{int64(1), "检修一班"}}}
}

func (e *testEnv) withTeam() *testEnv {
	e.fake.on(teamRule().match, teamRule().cols, teamRule().rows)
	return e
}

// ---- 核验规则单测 ----

func intPtr(v int) *int { return &v }

// baseEntry 构造一张已出车卡片（目的地已选、单号已填、一人一图）。
func baseEntry() *Entry {
	return &Entry{
		VehicleID: 1, PlateNo: "晋A12345", DestinationID: 2, DestinationName: "孝义市",
		DispatchOrderNo: "PC-001",
		Members:         []MemberRow{{ID: 1, MemberID: 10, Name: "赵登"}},
		Photos: []PhotoItem{{
			ID: 1, Members: []string{"赵登"}, VerifyStatus: "passed",
			WorkContent: "绝缘子清扫", IsWatermark: 1, DateOK: intPtr(1), DestOK: intPtr(1),
		}},
		ClockinMap: map[string]map[int]ClockinEntry{
			"10": {1: {Time: "08:00", Position: "孝义市·现场"}, 2: {Time: "17:00", Position: "孝义市·现场"}},
		},
		FeeMap: map[string]FeeEntry{"10": {FoodFee: 60, TransitFee: 0}},
		FeeStd: FeeStd{FoodFee: 60, TransitFee: 0, Scope: 0},
	}
}

func TestVerifyPassed(t *testing.T) {
	e := baseEntry()
	if got := ComputeVerifyPassed(e); got != "passed" {
		t.Fatalf("基础卡应 passed，实际 %s reasons=%v", got, ComputeFailReasons(e))
	}

	// 未出车 → exempt
	noVehicle := baseEntry()
	noVehicle.VehicleID = 0
	if got := ComputeVerifyPassed(noVehicle); got != "exempt" {
		t.Fatalf("未出车应 exempt，实际 %s", got)
	}

	// 缺目的地 → failed
	noDest := baseEntry()
	noDest.DestinationID = 0
	if got := ComputeVerifyPassed(noDest); got != "failed" {
		t.Fatalf("缺目的地应 failed，实际 %s", got)
	}

	// 缺派车单号 → failed
	noOrder := baseEntry()
	noOrder.DispatchOrderNo = ""
	if got := ComputeVerifyPassed(noOrder); got != "failed" {
		t.Fatalf("缺派车单号应 failed，实际 %s", got)
	}

	// 规则 a：绑定成员缺结束打卡 → failed
	noClock := baseEntry()
	noClock.Members[0].SgccBound = true
	noClock.ClockinMap = map[string]map[int]ClockinEntry{"10": {1: {Time: "08:00", Position: "孝义市·现场"}}}
	if got := ComputeVerifyPassed(noClock); got != "failed" {
		t.Fatalf("缺结束打卡应 failed，实际 %s", got)
	}

	// 规则 a 通过 + 规则 g：打卡地点不含目的地 → failed
	badLoc := baseEntry()
	badLoc.Members[0].SgccBound = true
	badLoc.ClockinMap = map[string]map[int]ClockinEntry{
		"10": {1: {Time: "08:00", Position: "太原市·公司"}, 2: {Time: "17:00", Position: "太原市·公司"}},
	}
	if got := ComputeVerifyPassed(badLoc); got != "failed" {
		t.Fatalf("打卡地点不含目的地应 failed，实际 %s", got)
	}

	// 规则 f：费用不达标 → failed
	badFee := baseEntry()
	badFee.FeeMap = map[string]FeeEntry{"10": {FoodFee: 30, TransitFee: 0}}
	if got := ComputeVerifyPassed(badFee); got != "failed" {
		t.Fatalf("费用不达标应 failed，实际 %s", got)
	}

	// 规则 b：人无照片 → failed
	noPhoto := baseEntry()
	noPhoto.Members = append(noPhoto.Members, MemberRow{ID: 2, MemberID: 11, Name: "郑海楠"})
	if got := ComputeVerifyPassed(noPhoto); got != "failed" {
		t.Fatalf("成员缺照片应 failed，实际 %s", got)
	}

	// 规则 d/e：照片 mismatch → failed
	badPhoto := baseEntry()
	badPhoto.Photos[0].VerifyStatus = "mismatch"
	if got := ComputeVerifyPassed(badPhoto); got != "failed" {
		t.Fatalf("照片未通过应 failed，实际 %s", got)
	}

	// 规则 c：施工内容为空 → failed
	emptyContent := baseEntry()
	emptyContent.Photos[0].WorkContent = "  "
	if got := ComputeVerifyPassed(emptyContent); got != "failed" {
		t.Fatalf("施工内容为空应 failed，实际 %s", got)
	}

	// 规则 c：多张内容不一致 → failed
	multi := baseEntry()
	multi.Photos = append(multi.Photos, PhotoItem{
		ID: 2, Members: []string{"赵登"}, VerifyStatus: "passed",
		WorkContent: "通道清理", IsWatermark: 1,
	})
	if got := ComputeVerifyPassed(multi); got != "failed" {
		t.Fatalf("施工内容不一致应 failed，实际 %s", got)
	}

	// 非水印照片不参与：skipped 照片不影响
	plain := baseEntry()
	plain.Photos = append(plain.Photos, PhotoItem{
		ID: 3, VerifyStatus: "skipped", IsWatermark: 0,
	})
	if got := ComputeVerifyPassed(plain); got != "passed" {
		t.Fatalf("非水印照片不应影响核验，实际 %s", got)
	}
}

func TestCheckWatermark(t *testing.T) {
	chk := CheckWatermark("2026.08.18 09:30", "孝义市·现场", "2026.08.18", "孝义市")
	if chk.Status != "passed" || !chk.DateOK || !chk.DestOK {
		t.Fatalf("应通过，实际 %+v", chk)
	}
	chk = CheckWatermark("2026.08.19 09:30", "孝义市·现场", "2026.08.18", "孝义市")
	if chk.Status != "mismatch" || chk.DateOK {
		t.Fatalf("日期不符应 mismatch，实际 %+v", chk)
	}
	chk = CheckWatermark("2026.08.18 09:30", "太原市·公司", "2026.08.18", "孝义市")
	if chk.Status != "mismatch" || chk.DestOK {
		t.Fatalf("地点不符应 mismatch，实际 %+v", chk)
	}
	// 目的地为空不约束
	chk = CheckWatermark("2026.08.18 09:30", "任意", "2026.08.18", "")
	if !chk.DestOK {
		t.Fatalf("目的地为空应不约束，实际 %+v", chk)
	}
}

func TestResolveFeeStd(t *testing.T) {
	rows := []FeeStdRow{
		{Scope: 0, FoodFee: 60, TransitFee: 0, Eff: "2000-01-01"},
		{Scope: 1, FoodFee: 60, TransitFee: 0, Eff: "2000-01-01"},
		{Scope: 1, FoodFee: 100, TransitFee: 0, Eff: "2026-08-14"},
		{Scope: 2, FoodFee: 40, TransitFee: 0, Eff: "2000-01-01"},
	}
	// 市外新规生效后
	if s := ResolveFeeStd(rows, 1, "2026-08-20"); s.FoodFee != 100 {
		t.Fatalf("市外新规应 100，实际 %+v", s)
	}
	// 新规生效前不翻旧账
	if s := ResolveFeeStd(rows, 1, "2026-08-01"); s.FoodFee != 60 {
		t.Fatalf("新规前应 60，实际 %+v", s)
	}
	if s := ResolveFeeStd(rows, 2, "2026-08-20"); s.FoodFee != 40 {
		t.Fatalf("驻地应 40，实际 %+v", s)
	}
	// 无配置兜底
	if s := ResolveFeeStd(nil, 0, "2026-08-20"); s.FoodFee != 60 {
		t.Fatalf("兜底应 60，实际 %+v", s)
	}
}

func TestMyReportReasons(t *testing.T) {
	e := baseEntry()
	e.Members[0].SgccBound = true
	me := MemberRow{MemberID: 10, Name: "赵登"}
	e.Photos = nil // 我没有照片
	reasons := MyReportReasons(e, me)
	found := false
	for _, r := range reasons {
		if r == "我未上传水印照片" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺照片应报「我未上传水印照片」，实际 %v", reasons)
	}
}

// ---- 路由行为 ----

func TestMetaRequiresTeam(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	rec := env.do(t, "GET", "/api/v1/worklog/meta", nil)
	env2 := decodeEnv(t, rec)
	if env2.Code != codeOK {
		t.Fatalf("meta 应成功，实际 %d %s", env2.Code, env2.Message)
	}
}

// ---- 字典行查询参数回归 ----

// TestQueryDictRowsArgCount 回归：queryDictRows 按语句实际占位符传参——
// 无 ? 的查询（跨班表单数据源）不能带 teamID，否则真 MySQL 报
// 「expected 0 arguments got 1」导致整块返回空；fake 驱动同口径校验参数个数。
func TestQueryDictRowsArgCount(t *testing.T) {
	db, fake := openFake(t)
	defer db.Close()
	ctx := context.Background()

	// 无占位符查询（cross/meta teams 口径）：0 参数出行
	fake.on("FROM sys_team WHERE status = 1", []string{"id", "name"},
		[][]driver.Value{{int64(1), "检修一班"}})
	rows := queryDictRows(ctx, db,
		`SELECT id, name FROM sys_team WHERE status = 1 ORDER BY sort, id`, 0)
	if len(rows) != 1 {
		t.Fatalf("无占位符查询应返回 1 行，实际 %d", len(rows))
	}
	if q, ok := fake.lastQuery("FROM sys_team WHERE status = 1"); !ok || len(q.args) != 0 {
		t.Fatalf("无占位符查询不应携带参数，args=%v", q.args)
	}

	// 有占位符查询（meta 口径）：占位符个数 = 参数个数，teamID 到位
	fake.on("FROM worklog_vehicle WHERE status = 1", []string{"id", "plate_no"},
		[][]driver.Value{{int64(5), "晋A12345"}})
	rows = queryDictRows(ctx, db,
		`SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ? ORDER BY sort, id`, 7)
	if len(rows) != 1 {
		t.Fatalf("带占位符查询应返回 1 行，实际 %d", len(rows))
	}
	if q, ok := fake.lastQuery("FROM worklog_vehicle WHERE status = 1"); !ok || len(q.args) != 1 || q.args[0] != int64(7) {
		t.Fatalf("带占位符查询应传 1 个 teamID=7，args=%v", q.args)
	}

	// 字符串字面量里的 ? 不是占位符
	fake.on("FROM worklog_destination", []string{"id", "name"},
		[][]driver.Value{{int64(2), "孝义市"}})
	rows = queryDictRows(ctx, db,
		`SELECT id, name FROM worklog_destination WHERE name = '?' AND team_id = ?`, 7)
	if len(rows) != 1 {
		t.Fatalf("字面量 ? 查询应返回 1 行，实际 %d", len(rows))
	}
	if q, ok := fake.lastQuery("FROM worklog_destination"); !ok || len(q.args) != 1 {
		t.Fatalf("字面量 ? 不应计为占位符，args=%v", q.args)
	}
}

// TestCrossMetaTeamsNotEmpty 回归：跨班表单数据源（teams/vehicles 等）查询无 ? 占位符，
// 多带 teamID 参数真 MySQL 会报 expected 0 arguments got 1，导致 teams 恒空。
func TestCrossMetaTeamsNotEmpty(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("FROM worklog_vehicle v", []string{"name", "team_name"},
		[][]driver.Value{{"晋A12345", "检修一班"}})
	rec := env.do(t, "GET", "/api/v1/worklog/logs/cross/meta", nil)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("cross/meta 应成功，实际 %d %s", envr.Code, envr.Message)
	}
	data, ok := envr.Data.(map[string]any)
	if !ok {
		t.Fatalf("cross/meta 应返回对象，实际 %T", envr.Data)
	}
	if teams, _ := data["teams"].([]any); len(teams) == 0 {
		t.Fatalf("跨班 teams 不应为空（无占位符查询被误带参数会整块报空），data=%#v", data)
	}
	if vehicles, _ := data["vehicles"].([]any); len(vehicles) == 0 {
		t.Fatalf("跨班 vehicles 不应为空，data=%#v", data)
	}
}

func TestLogCreateValidation(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()

	// 日期格式错误
	rec := env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{"log_date": "2026/08/18"})
	if env := decodeEnv(t, rec); env.Code != codeParam {
		t.Fatalf("日期格式错误应 40000，实际 %d", env.Code)
	}

	// 未出车填目的地
	rec = env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "destination_id": 3,
	})
	if env := decodeEnv(t, rec); env.Code != codeInput {
		t.Fatalf("未出车填目的地应 40001，实际 %d", env.Code)
	}

	// 车牌无效/停用（查询无行）
	rec = env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "vehicle_id": 99,
	})
	if env := decodeEnv(t, rec); env.Code != codeVehicle {
		t.Fatalf("车牌无效应 40002，实际 %d", env.Code)
	}

	// 正常建卡
	env.fake.on("FROM worklog_vehicle", []string{"status"}, [][]driver.Value{{int64(1)}})
	rec = env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "vehicle_id": 5, "patrol_content": "巡视",
	})
	if env := decodeEnv(t, rec); env.Code != codeOK {
		t.Fatalf("建卡应成功，实际 %d %s", env.Code, env.Message)
	}
	if env.fake.execCount("INSERT INTO worklog_entry ") != 1 {
		t.Fatalf("应写入 worklog_entry")
	}
}

func TestLogCreateDateMemberDup(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_vehicle", []string{"status"}, [][]driver.Value{{int64(1)}})
	env.fake.on("FROM worklog_member WHERE id IN", []string{"id", "sort"},
		[][]driver.Value{{int64(10), int64(1)}})
	// 同日同人重复：查到占用行
	env.fake.on("JOIN worklog_entry e ON e.id = em.entry_id", []string{"name"},
		[][]driver.Value{{"赵登"}})
	rec := env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "vehicle_id": 5, "member_ids": []int64{10},
	})
	if env := decodeEnv(t, rec); env.Code != codeFileCount {
		t.Fatalf("同日同人重复应 40020，实际 %d %s", env.Code, env.Message)
	}
}

func TestDispatchOrderNoWrites(t *testing.T) {
	// 派车单号写入口径：建卡/改卡/跨班建卡按提交值落库，未出车强制置空；跨班建卡归属班组取 owner team。
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_vehicle", []string{"status"}, [][]driver.Value{{int64(1)}})
	env.fake.on("FROM worklog_destination", []string{"status"}, [][]driver.Value{{int64(1)}})

	// 建卡带派车单号 → INSERT 落库
	rec := env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "vehicle_id": 5, "dispatch_order_no": "PC-77",
	})
	if envd := decodeEnv(t, rec); envd.Code != codeOK {
		t.Fatalf("建卡应成功，实际 %d %s", envd.Code, envd.Message)
	}
	ex, ok := env.fake.lastExec("INSERT INTO worklog_entry ")
	if !ok || len(ex.args) < 6 || ex.args[5] != "PC-77" {
		t.Fatalf("建卡应写入派车单号 PC-77，args=%v", ex.args)
	}

	// 未出车提交单号 → 强制置空
	rec = env.do(t, "POST", "/api/v1/worklog/logs", map[string]any{
		"log_date": "2026-08-18", "dispatch_order_no": "PC-77",
	})
	if envd := decodeEnv(t, rec); envd.Code != codeOK {
		t.Fatalf("未出车建卡应成功，实际 %d %s", envd.Code, envd.Message)
	}
	if ex, ok = env.fake.lastExec("INSERT INTO worklog_entry "); !ok || len(ex.args) < 6 || ex.args[5] != nil {
		t.Fatalf("未出车建卡派车单号应置空，args=%v", ex.args)
	}

	// 改卡提交派车单号 → UPDATE 落库；未出车 → 置空
	env.fake.on("FROM worklog_entry e WHERE e.id",
		[]string{"team_id", "vehicle_id", "destination_id", "cross_team", "dispatch_order_no", "log_date"},
		[][]driver.Value{{int64(1), int64(5), int64(2), int64(0), "PC-1", "2026-08-18"}})
	rec = env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 5, "destination_id": 2, "dispatch_order_no": "PC-99",
	})
	if envd := decodeEnv(t, rec); envd.Code != codeOK {
		t.Fatalf("改卡应成功，实际 %d %s", envd.Code, envd.Message)
	}
	exu, ok := env.fake.lastExec("UPDATE worklog_entry SET patrol_content = ?, vehicle_id")
	if !ok || len(exu.args) < 4 || exu.args[3] != "PC-99" {
		t.Fatalf("改卡应写入派车单号 PC-99，args=%v", exu.args)
	}
	rec = env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 0, "dispatch_order_no": "PC-99",
	})
	if envd := decodeEnv(t, rec); envd.Code != codeOK {
		t.Fatalf("改未出车应成功，实际 %d %s", envd.Code, envd.Message)
	}
	if exu, ok = env.fake.lastExec("UPDATE worklog_entry SET patrol_content = ?, vehicle_id"); !ok || len(exu.args) < 4 || exu.args[3] != nil {
		t.Fatalf("未出车改卡派车单号应置空，args=%v", exu.args)
	}

	// 跨班建卡：单号按提交值落库，归属班组取 owner team（规则先于 withTeam 注册避免误命中）
	env2 := newTestServer(t, identAdmin)
	env2.fake.on("name, status FROM sys_team", []string{"name", "status"},
		[][]driver.Value{{"检修一班", int64(1)}})
	env2.withTeam()
	env2.fake.on("FROM worklog_vehicle WHERE team_id", []string{"id", "status"},
		[][]driver.Value{{int64(5), int64(1)}})
	rec = env2.do(t, "POST", "/api/v1/worklog/logs/cross", map[string]any{
		"log_date": "2026-08-18", "team_id": 1, "plate_no": "晋A12345",
		"dispatch_order_no": "PC-88",
	})
	if envd := decodeEnv(t, rec); envd.Code != codeOK {
		t.Fatalf("跨班建卡应成功，实际 %d %s", envd.Code, envd.Message)
	}
	exc, ok := env2.fake.lastExec("INSERT INTO worklog_entry ")
	if !ok || len(exc.args) < 7 {
		t.Fatalf("跨班应写入 worklog_entry，args=%v", exc.args)
	}
	if exc.args[6] != "PC-88" {
		t.Fatalf("跨班建卡应写入派车单号 PC-88，args=%v", exc.args)
	}
	if exc.args[0] != int64(1) {
		t.Fatalf("跨班建卡归属班组应为 1，args=%v", exc.args)
	}
}

func TestDayStatusAndStats(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	// 一张未通过卡（缺目的地）
	env.fake.on("FROM worklog_entry e", []string{
		"id", "team_id", "log_date", "patrol_content", "remark", "remark_files", "cross_team",
		"vehicle_id", "plate_no", "destination_id", "destination_name", "out_of_city",
		"stationed", "dispatch_order_no", "created_by", "created_at",
	}, [][]driver.Value{{
		int64(1), int64(1), "2026-08-18", "", "", "", int64(0),
		int64(5), "晋A12345", int64(0), "", int64(0),
		int64(0), "PC-1", int64(4), "2026-08-18 09:00:00",
	}})
	env.fake.on("FROM worklog_entry_member", []string{"id", "entry_id", "member_id", "name", "checked", "sort"},
		[][]driver.Value{{int64(1), int64(1), int64(10), "赵登", int64(0), int64(1)}})
	env.fake.on("FROM worklog_photo", []string{
		"id", "entry_id", "cos_key", "url", "members", "verify_status", "work_content", "shot_time",
		"weather", "location", "lng", "lat", "date_ok", "dest_ok", "is_watermark", "source", "sgcc_synced",
	}, nil)
	env.fake.on("FROM worklog_sgcc_account", []string{"id", "token_status"}, nil)
	env.fake.on("FROM worklog_clockin", []string{
		"member_id", "clock_date", "seq", "detail_id", "clock_time", "position",
		"longitude", "latitude", "city_code", "city_name", "work_hours",
	}, nil)
	env.fake.on("FROM worklog_fee ", []string{"member_id", "fee_date", "food_fee", "transit_fee"}, nil)
	env.fake.on("FROM worklog_fee_std", []string{"scope", "food_fee", "transit_fee", "eff"}, nil)

	rec := env.do(t, "GET", "/api/v1/worklog/day-status?month=2026-08", nil)
	envd := decodeEnv(t, rec)
	if envd.Code != codeOK {
		t.Fatalf("day-status 应成功，实际 %d %s", envd.Code, envd.Message)
	}
	data, _ := envd.Data.(map[string]any)
	m, _ := data["map"].(map[string]any)
	if m == nil {
		t.Fatalf("day-status 应返回 map，实际 %#v", envd.Data)
	}
	if m["2026-08-18"] != "failed" {
		t.Fatalf("缺目的地应标红 failed，实际 %v", m["2026-08-18"])
	}

	rec = env.do(t, "GET", "/api/v1/worklog/stats?month=2026-08", nil)
	envs := decodeEnv(t, rec)
	sd, _ := envs.Data.(map[string]any)
	if sd["total"].(float64) != 1 {
		t.Fatalf("total 应 1，实际 %#v", sd)
	}
	if sd["passed"].(float64) != 0 {
		t.Fatalf("passed 应 0，实际 %#v", sd)
	}
}

func TestDictAdmin(t *testing.T) {
	// 班组管理员可管本班
	env := newTestServer(t, identTM).withTeam()
	rec := env.do(t, "POST", "/api/v1/worklog/admin/vehicles", map[string]any{"name": "晋A12345"})
	if env := decodeEnv(t, rec); env.Code != codeOK {
		t.Fatalf("新增车牌应成功，实际 %d %s", env.Code, env.Message)
	}

	// 名称为空
	rec = env.do(t, "POST", "/api/v1/worklog/admin/vehicles", map[string]any{"name": "  "})
	if env := decodeEnv(t, rec); env.Code != codeDictName {
		t.Fatalf("名称为空应 40012，实际 %d", env.Code)
	}

	// 重名 → 40900
	env.fake.onErr("INSERT INTO worklog_vehicle", errDup())
	rec = env.do(t, "POST", "/api/v1/worklog/admin/vehicles", map[string]any{"name": "晋A12345"})
	if env := decodeEnv(t, rec); env.Code != codeDupName {
		t.Fatalf("重名应 40900，实际 %d", env.Code)
	}

	// 删除被引用 → 40901
	env.fake.on("SELECT COUNT(*) FROM worklog_entry WHERE vehicle_id", []string{"cnt"},
		[][]driver.Value{{int64(3)}})
	rec = env.do(t, "DELETE", "/api/v1/worklog/admin/vehicles/7", nil)
	if env := decodeEnv(t, rec); env.Code != codeDictInUse {
		t.Fatalf("被引用删除应 40901，实际 %d", env.Code)
	}
}

func errDup() error { return &dupErr{} }

type dupErr struct{}

func (*dupErr) Error() string { return "Error 1062: Duplicate entry" }

func TestPhotoUploadAndVerify(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	// 卡片存在且已出车
	env.fake.on("FROM worklog_entry e", []string{"team_id", "vehicle_id", "log_date", "destination_name", "team_name"},
		[][]driver.Value{{int64(1), int64(5), "2026-08-18", "孝义市", "检修一班"}})
	env.fake.on("FROM worklog_entry_member", []string{"name"}, [][]driver.Value{{"赵登"}})
	env.fake.on("FROM worklog_photo", []string{"id", "members"}, nil)

	rec := uploadPhotoReq(t, env, "/api/v1/worklog/logs/1/photos", tinyPNG(t), map[string]string{
		"members": `["赵登"]`, "plain": "true",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("照片上传应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if env.fake.execCount("INSERT INTO worklog_photo") != 1 {
		t.Fatalf("应写入 worklog_photo")
	}
	// plain 照片免验证
	if env.fake.execCount("is_watermark = 0") != 1 {
		t.Fatalf("非水印照片应标记 skipped")
	}

	// 格式错误
	rec = uploadPhotoReq(t, env, "/api/v1/worklog/logs/1/photos", []byte("not-image"), map[string]string{
		"members": `["赵登"]`, "plain": "true",
	})
	if envr := decodeEnv(t, rec); envr.Code != codePhotoFormat {
		t.Fatalf("格式错误应 40010，实际 %d", envr.Code)
	}

	// 人名不在本卡
	rec = uploadPhotoReq(t, env, "/api/v1/worklog/logs/1/photos", tinyPNG(t), map[string]string{
		"members": `["外人"]`, "plain": "true",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeNotCardMember {
		t.Fatalf("人名越界应 40007，实际 %d", envr.Code)
	}
}

func TestPhotoVerifyPipeline(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	// 重验入口：照片处于 failed
	env.fake.on("FROM worklog_photo p", []string{"verify_status", "log_date", "destination_name", "cos_key", "url"},
		[][]driver.Value{{"failed", "2026-08-18", "孝义市", "worklog/k/1.jpg", ""}})
	rec := env.do(t, "POST", "/api/v1/worklog/photos/9/verify", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("重新验证应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if env.dify.calls == 0 {
		t.Fatalf("应触发 Dify 识别")
	}
	// 回写 verify_status（CheckWatermark 口径：time 含记录日期 → passed）
	if env.fake.execCount("UPDATE worklog_photo SET verify_status") == 0 {
		t.Fatalf("应回写识别结果")
	}

	// 非 failed 不可重验
	env2 := newTestServer(t, identUser).withTeam()
	env2.fake.on("FROM worklog_photo p", []string{"verify_status", "log_date", "destination_name", "cos_key", "url"},
		[][]driver.Value{{"passed", "2026-08-18", "孝义市", "k", ""}})
	rec = env2.do(t, "POST", "/api/v1/worklog/photos/9/verify", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeVerifyBusy {
		t.Fatalf("非 failed 重验应 40014，实际 %d", envr.Code)
	}
}

func TestCrossTeamVisibility(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	// 跨班卡可见性 SQL 含 cross_team 条件
	env.fake.on("FROM worklog_entry e", []string{
		"id", "team_id", "log_date", "patrol_content", "remark", "remark_files", "cross_team",
		"vehicle_id", "plate_no", "destination_id", "destination_name", "out_of_city",
		"stationed", "dispatch_order_no", "created_by", "created_at",
	}, [][]driver.Value{{
		int64(7), int64(2), "2026-08-18", "", "", "", int64(1),
		int64(5), "晋B", int64(2), "交城县", int64(0),
		int64(0), "", int64(1), "2026-08-18 09:00:00",
	}})
	env.fake.on("FROM worklog_entry_member", []string{"id", "entry_id", "member_id", "name", "checked", "sort"},
		[][]driver.Value{{int64(1), int64(7), int64(10), "赵登", int64(0), int64(1)}})
	env.fake.on("FROM worklog_photo", []string{
		"id", "entry_id", "cos_key", "url", "members", "verify_status", "work_content", "shot_time",
		"weather", "location", "lng", "lat", "date_ok", "dest_ok", "is_watermark", "source", "sgcc_synced",
	}, nil)
	env.fake.on("FROM worklog_sgcc_account", []string{"id", "token_status"}, nil)
	env.fake.on("FROM worklog_clockin", []string{
		"member_id", "clock_date", "seq", "detail_id", "clock_time", "position",
		"longitude", "latitude", "city_code", "city_name", "work_hours",
	}, nil)
	env.fake.on("FROM worklog_fee ", []string{"member_id", "fee_date", "food_fee", "transit_fee"}, nil)
	env.fake.on("FROM worklog_fee_std", []string{"scope", "food_fee", "transit_fee", "eff"}, nil)

	rec := env.do(t, "GET", "/api/v1/worklog/logs?date=2026-08-18", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("logs 应成功，实际 %d %s", envr.Code, envr.Message)
	}
	// 可见性 SQL 必须包含跨班片段
	found := false
	for _, q := range env.fake.allQueries() {
		if strings.Contains(q, "cross_team = 1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("查询应含跨班可见性条件")
	}
}

// ---- 工具 ----

func base64Of(b []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		sb.WriteByte(table[(n>>18)&0x3f])
		sb.WriteByte(table[(n>>12)&0x3f])
		if rem > 1 {
			sb.WriteByte(table[(n>>6)&0x3f])
		} else {
			sb.WriteByte('=')
		}
		if rem > 2 {
			sb.WriteByte(table[n&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

var _ = bytes.MinRead

// ---- 识别包装导出 / sync-now 无文件兜底（sgccclockin 联动与派车手动触发口径）----

// memNotifier 人工审核通知 fake。
type memNotifier struct{ photoIDs []int64 }

func (n *memNotifier) NotifyManualReview(_ context.Context, photoID int64) error {
	n.photoIDs = append(n.photoIDs, photoID)
	return nil
}

// TestVerifyAndWriteBackExported 导出包装（sgccclockin.PhotoVerifier 契约）：识别含空内容
// 重试后回写；全部不可用时置 failed 并通知人工审核——复用现有 verifyAndWriteBack 流水线。
func TestVerifyAndWriteBackExported(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	notifier := &memNotifier{}
	env.module.Notifier = notifier
	// 识别重试：首调 OK 但施工内容为空 → 重试；次调可用 → 回写核验结果
	env.dify.results = []VerifyResult{
		{OK: true, WorkContent: ""},
		{OK: true, WorkContent: "绝缘子清扫", Time: "2026.08.18 09:30", Location: "孝义市·现场"},
	}
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"log_date", "destination_name"}, [][]driver.Value{{"2026-08-18", "孝义市"}})
	// date 传 'YYYY.MM.DD'（sgccclockin 拉下入库口径）；Dots 归一幂等
	env.module.VerifyAndWriteBack(context.Background(), 55, "u", "2026.08.18", "孝义市", "https://cos.test/x.jpg")
	if env.dify.calls != 2 {
		t.Fatalf("识别应重试至可用: calls=%d", env.dify.calls)
	}
	if env.fake.execCount("UPDATE worklog_photo SET verify_status = ?") == 0 {
		t.Fatalf("应回写识别结果")
	}
	if len(notifier.photoIDs) != 0 {
		t.Fatalf("可用结果不应通知人工审核: %v", notifier.photoIDs)
	}

	// 识别成功但始终无施工内容 → 重试耗尽置 failed + 通知人工审核
	env2 := newTestServer(t, identUser).withTeam()
	notifier2 := &memNotifier{}
	env2.module.Notifier = notifier2
	env2.dify.results = []VerifyResult{{OK: true, WorkContent: ""}}
	env2.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"log_date", "destination_name"}, [][]driver.Value{{"2026-08-18", "孝义市"}})
	env2.module.VerifyAndWriteBack(context.Background(), 56, "u", "2026.08.18", "孝义市", "https://cos.test/y.jpg")
	if env2.dify.calls != 1+maxVerifyRetry {
		t.Fatalf("识别应共调 %d 次: calls=%d", 1+maxVerifyRetry, env2.dify.calls)
	}
	if env2.fake.execCount("verify_status = 'failed'") == 0 {
		t.Fatalf("应置 failed")
	}
	if len(notifier2.photoIDs) != 1 || notifier2.photoIDs[0] != 56 {
		t.Fatalf("应通知人工审核: %v", notifier2.photoIDs)
	}
}

// fakeDispatcher DispatchSyncer fake（sync-now 兜底口径断言用）。
type fakeDispatcher struct {
	n   int
	err error
}

func (d fakeDispatcher) PrepareNow(context.Context) error { return nil }

func (d fakeDispatcher) SyncNow(context.Context, int64) (int, error) { return d.n, d.err }

// TestSyncNowNoFileOK 当日无文件按旧版口径 200 正常返回（执行器已发「今日无文件」通知，不报 500）；
// 真失败仍按 codeSyncFail 下发。
func TestSyncNowNoFileOK(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.module.Dispatcher = fakeDispatcher{} // 无文件口径：(0, nil)
	rec := env.do(t, "POST", "/api/v1/worklog/dispatch/sync-now", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP = %d", rec.Code)
	}
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("无文件应正常返回，实际 %d %s", envr.Code, envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	if data != nil {
		if synced, ok := data["synced"].(float64); !ok || synced != 0 {
			t.Fatalf("synced = %v", data["synced"])
		}
	}

	env2 := newTestServer(t, identAdmin).withTeam()
	env2.module.Dispatcher = fakeDispatcher{err: errors.New("取件失败：设备不在线")}
	rec2 := env2.do(t, "POST", "/api/v1/worklog/dispatch/sync-now", nil)
	if envr := decodeEnv(t, rec2); envr.Code != codeSyncFail {
		t.Fatalf("真失败应 %d，实际 %d %s", codeSyncFail, envr.Code, envr.Message)
	}
}
