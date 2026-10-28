package treehazard

// 内网 U 盘同步 / 差异比对 / 冲突裁决 / 导出预览 / 直推复用方法测试（假 UsbLink 注入，不连真实服务）。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// fakeUsb U 盘设备链路假实现。
type fakeUsb struct {
	files   map[string][]byte
	pushed  []string
	mounts  int
	deleted []string
}

func (f *fakeUsb) MountToHost(context.Context) error { f.mounts++; return nil }

func (f *fakeUsb) DownloadFile(_ context.Context, name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (f *fakeUsb) ListFiles(context.Context) ([]string, error) {
	out := make([]string, 0, len(f.files))
	for n := range f.files {
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeUsb) DeleteFiles(_ context.Context, names []string) error {
	for _, n := range names {
		delete(f.files, n)
		f.deleted = append(f.deleted, n)
	}
	return nil
}

func (f *fakeUsb) PushFile(_ context.Context, name string, data []byte) error {
	f.files[name] = data
	f.pushed = append(f.pushed, name)
	return nil
}

// syncEnv 构造同步测试环境（同步班组已建、records 预置、无墓碑）。
func syncEnv(t *testing.T, records [][]driver.Value) (*Module, *fakeDB, *fakeUsb) {
	t.Helper()
	db, fake := openFake(t)
	rule := teamRule()
	fake.on(rule.match, rule.cols, rule.rows)
	fake.on("FROM treehazard_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "position_note",
		"tree_species", "tree_count", "dist_small", "dist_big", "diameter", "horizontal", "vertical",
		"vertical_num", "finder_name", "found_at", "handler_name", "handle_note", "handled_at",
		"status", "source", "crane_session_id", "crane_record_id", "synced_at", "created_by",
		"created_at", "updated_at",
	}, records)
	fake.on("FROM treehazard_deleted_id", []string{"id"}, nil)
	m := New(db)
	m.Cfg = memConfig{
		"TREEHAZARD_SYNC_TEAM":           "检修一班",
		"TREEHAZARD_SYNC_DEVICE_DDNS":    "dev-1",
		"TREEHAZARD_SYNC_CMD_WINDOW_MIN": "0",
	}
	usb := &fakeUsb{files: map[string][]byte{}}
	m.Usb = usb
	return m, fake, usb
}

// intranetSheet 构造内网表 xlsx（两行表头 + 数据行）。
func intranetSheet(t *testing.T, row []string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetList()[0]
	headers := []string{"序号", "电压等级", "线路名称", "区段", "树种", "数量", "距小号侧距离（m）",
		"距大号侧距离（m）", "直径（cm）", "水平距离（m）", "垂直距离（m）", "发现人", "发现时间",
		"处理人", "处理情况", "处理时间", "记录ID"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	for i, v := range row {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(sheet, cell, v)
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("生成内网表失败: %v", err)
	}
	return buf.Bytes()
}

func TestRunIntranetCycleNotConfigured(t *testing.T) {
	db, _ := openFake(t)
	m := New(db)
	m.Cfg = memConfig{}
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); !errors.Is(err, ErrSyncNotConfigured) {
		t.Fatalf("链路未注入应返回 ErrSyncNotConfigured，got %v", err)
	}
	m.Usb = &fakeUsb{files: map[string][]byte{}}
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); !errors.Is(err, ErrSyncNotConfigured) {
		t.Fatalf("未配置设备应返回 ErrSyncNotConfigured，got %v", err)
	}
}

func TestRunIntranetCycleNoFilePushLatest(t *testing.T) {
	m, fake, usb := syncEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	res, err := m.RunIntranetCycle(context.Background(), "manual")
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || !res.Counts.NoFile || !res.Counts.Pushed {
		t.Fatalf("内网无表应直接下发最新表，got %+v", res)
	}
	if !containsStr(usb.pushed, "检修一班树木统计表.xlsx") || !containsStr(usb.pushed, syncCmdFile) {
		t.Fatalf("应推送统计表与 snapshot/replace 指令，got %v", usb.pushed)
	}
	if fake.execCount("INSERT INTO treehazard_sync_log") != 1 {
		t.Fatalf("应写同步日志")
	}
	if fake.execCount("UPDATE treehazard_record SET synced_at = NOW()") != 1 {
		t.Fatalf("下发后应落同步点")
	}
}

func TestRunIntranetCycleDiffConflictArchived(t *testing.T) {
	m, fake, usb := syncEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	// 内网行 record_id=1 与服务端不一致（树种改「柳树」），synced_at 空 = 从未同步 → 冲突留档。
	usb.files["检修一班树木统计表.xlsx"] = intranetSheet(t, []string{
		"1", "10kV", "甲线", "3#-4#", "柳树", "3", "5", "5", "20", "2", "4.5", "张三", "2026.10.01",
		"", "", "", "1",
	})
	res, err := m.RunIntranetCycle(context.Background(), "manual")
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || res.Counts.Conflicts != 1 {
		t.Fatalf("双边不一致应冲突留档，got %+v", res)
	}
	if fake.execCount("INSERT INTO treehazard_conflict") != 1 {
		t.Fatalf("应写入 treehazard_conflict")
	}
}

func TestDiffRecordsUpdateAndNews(t *testing.T) {
	server := []map[string]any{{
		"id": int64(1), "voltage_level": "10", "line_name": "甲线", "section": "3#-4#",
		"tree_species": "杨树", "tree_count": "3", "found_at": "2026-10-01",
		"handler_name": "", "handle_note": "", "handled_at": "",
		"status": "unhandled", "synced_at": "2026-10-01 08:00:00", "updated_at": "2026-10-01 07:00:00",
	}}
	rows := []map[string]any{
		{"record_id": int64(1), "line_name": "甲线", "section": "3#-4#", "tree_species": "柳树",
			"tree_count": "3", "found_at": "2026-10-01"}, // 仅内网改 → update
		{"line_name": "乙线", "section": "1#-2#", "tree_species": "杨树", "tree_count": "1", "found_at": ""}, // 新增
	}
	res := DiffRecords(server, rows, nil, nil)
	if len(res.Updates) != 1 || len(res.News) != 1 || len(res.Conflicts) != 0 {
		t.Fatalf("应 1 update + 1 news，got %+v", res)
	}
	if res.Updates[0].Fields["tree_species"] != "柳树" {
		t.Fatalf("update 应覆盖内网值，got %+v", res.Updates[0].Fields)
	}
	if res.Updates[0].Fields["status"] != "unhandled" {
		t.Fatalf("handled_at 空应回退未处理，got %+v", res.Updates[0].Fields)
	}
}

// TestSnapshotOfVerticalNum 快照 vertical_num 保留小数（DECIMAL 2.5 不得被吞成 nil）。
func TestSnapshotOfVerticalNum(t *testing.T) {
	v := 2.5
	snap := snapshotOf(map[string]any{"id": int64(7), "vertical_num": &v, "status": "unhandled"})
	f, ok := snap["vertical_num"].(float64)
	if !ok || f != 2.5 {
		t.Fatalf("vertical_num 应保留小数 2.5，got %#v", snap["vertical_num"])
	}
	empty := snapshotOf(map[string]any{"id": int64(7), "vertical_num": (*float64)(nil)})
	if empty["vertical_num"] != nil {
		t.Fatalf("空 vertical_num 应为 nil，got %#v", empty["vertical_num"])
	}
}

func TestDiffRecordsAdjudicatedSkip(t *testing.T) {
	server := []map[string]any{{
		"id": int64(1), "line_name": "甲线", "section": "3#-4#", "tree_species": "杨树",
		"tree_count": "3", "found_at": "2026-10-01",
		"synced_at": "2026-10-01 08:00:00", "updated_at": "2026-10-01 07:00:00",
	}}
	row := map[string]any{"record_id": int64(1), "line_name": "甲线", "section": "3#-4#",
		"tree_species": "柳树", "tree_count": "3", "found_at": "2026-10-01"}
	adj := []Adjudicated{{RecordID: 1, Intranet: map[string]any{
		"line_name": "甲线", "section": "3#-4#", "tree_species": "柳树",
		"tree_count": "3", "found_at": "2026-10-01",
	}}}
	res := DiffRecords(server, []map[string]any{row}, nil, adj)
	if res.Adjudicated != 1 || len(res.Updates) != 0 {
		t.Fatalf("已裁决驳回旧值应不回灌，got %+v", res)
	}
}

// 冲突/预览/直推复用方法：直接以带上下文的请求调用 handler（同包内可注入未导出上下文键）。

// ctxWith 组装 身份 + 班组 上下文。
func ctxWith(ident auth.Identity, teamID int64, teamName string) context.Context {
	ctx := context.WithValue(context.Background(), identityContextKey, ident)
	return context.WithValue(ctx, teamContextKey, httpapi.Team{ID: teamID, Name: teamName})
}

func TestConflictResolveKeepIntranet(t *testing.T) {
	db, fake := openFake(t)
	m := New(db)
	m.Cfg = memConfig{}
	fake.on("FROM treehazard_conflict WHERE id = ? AND team_id = ? AND status = 'pending'",
		[]string{"record_id", "intranet_snapshot"},
		[][]driver.Value{{int64(7), `{"tree_species":"柳树","handled_at":""}`}})
	body := strings.NewReader(`{"keep":"intranet"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/treehazard/conflicts/5/resolve", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "5")
	req = req.WithContext(context.WithValue(ctxWith(identAdmin, 1, "检修一班"), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	m.handleConflictResolve(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("裁决应成功，got %d %s", w.Code, w.Body.String())
	}
	if fake.execCount("UPDATE treehazard_record SET voltage_level") != 1 {
		t.Fatalf("keep=intranet 应以 intranet 快照覆盖记录")
	}
	if fake.execCount("UPDATE treehazard_conflict SET status = 'resolved'") != 1 {
		t.Fatalf("冲突行应置 resolved")
	}
	e, ok := fake.lastExec("UPDATE treehazard_record SET voltage_level")
	if !ok || len(e.args) == 0 {
		t.Fatalf("缺少覆盖语句")
	}
	if e.args[len(e.args)-1] != int64(7) {
		t.Fatalf("应更新 record_id=7，got %v", e.args[len(e.args)-1])
	}
}

func TestExportPreviewOOAndViewer(t *testing.T) {
	db, _ := openFake(t)
	m := New(db)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/treehazard/export/preview?line=%E7%94%B2%E7%BA%BF&split=1", nil)
	req = req.WithContext(ctxWith(identAdmin, 1, "检修一班"))
	req.Header.Set("Authorization", "Bearer jwt-1")

	// OO 启用 → office.html 承载页。
	m.Cfg = memConfig{"OO_PUBLIC_URL": "https://office.example.com"}
	w := httptest.NewRecorder()
	m.handleExportPreview(w, req)
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !strings.HasPrefix(env.Data.URL, "/office.html?src=treehazard&line=") ||
		!strings.Contains(env.Data.URL, "split=1") || !strings.Contains(env.Data.URL, "back=/treehazard.html") {
		t.Fatalf("OO 分支 URL 口径不符：%s", env.Data.URL)
	}

	// OO 停用 → 微软查看器 + ?token= 回源。
	m.Cfg = memConfig{}
	w = httptest.NewRecorder()
	m.handleExportPreview(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	viewerURL, err := url.Parse(env.Data.URL)
	if err != nil {
		t.Fatalf("查看器 URL 非法: %s", env.Data.URL)
	}
	if !strings.HasPrefix(env.Data.URL, "https://view.officeapps.live.com/op/view.aspx?src=") {
		t.Fatalf("查看器分支 URL 口径不符：%s", env.Data.URL)
	}
	inner, err := url.QueryUnescape(viewerURL.Query().Get("src"))
	if err != nil {
		t.Fatalf("src 解码失败: %v", err)
	}
	if !strings.Contains(inner, "token=jwt-1") ||
		!strings.Contains(inner, "/api/v1/treehazard/export/") ||
		!strings.Contains(inner, "甲线树木统计表.xlsx") ||
		!strings.Contains(inner, "line=甲线") || !strings.Contains(inner, "split=1") {
		t.Fatalf("回源地址口径不符：%s", inner)
	}
}

func TestIngestMethodTokenAndResult(t *testing.T) {
	db, fake := openFake(t)
	m := New(db)
	m.Cfg = memConfig{}
	ctx := context.Background()
	raw := []byte(`{"session_id":"s1","team_name":"检修一班","mode":"direct","records":[{"line_name":"甲线","crane_record_id":"c1"}]}`)

	if _, err := m.Ingest(ctx, "any", raw); !errors.Is(err, ErrIngestNotConfigured) {
		t.Fatalf("未配置密钥应 ErrIngestNotConfigured，got %v", err)
	}
	m.Cfg = memConfig{"TREEHAZARD_INGEST_TOKEN": "secret"}
	if _, err := m.Ingest(ctx, "wrong", raw); !errors.Is(err, ErrIngestToken) {
		t.Fatalf("密钥错误应 ErrIngestToken，got %v", err)
	}
	if _, err := m.Ingest(ctx, "secret", []byte(`{`)); !errors.Is(err, ErrIngestBadJSON) {
		t.Fatalf("坏 JSON 应 ErrIngestBadJSON，got %v", err)
	}
	if _, err := m.Ingest(ctx, "secret", []byte(`{"team_name":"检修一班","mode":"direct"}`)); err == nil {
		t.Fatalf("缺 session_id 应参数错误")
	} else if _, ok := err.(*IngestParamError); !ok {
		t.Fatalf("参数错误类型不符：%T", err)
	}
	if _, err := m.Ingest(ctx, "secret", []byte(`{"session_id":"s1","team_name":"无此班","mode":"direct"}`)); !errors.Is(err, ErrIngestTeamNotFound) {
		t.Fatalf("班组不存在应 ErrIngestTeamNotFound，got %v", err)
	}

	rule := teamRule()
	fake.on(rule.match, rule.cols, rule.rows)
	fake.on("SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?",
		[]string{"id"}, nil) // direct 幂等查：无旧行
	res, err := m.Ingest(ctx, "secret", raw)
	if err != nil {
		t.Fatalf("直推应成功: %v", err)
	}
	if res.Mode != "direct" || res.Inserted != 1 || res.Skipped != 0 {
		t.Fatalf("应插入 1 条，got %+v", res)
	}
	if fake.execCount("INSERT INTO treehazard_record") != 1 {
		t.Fatalf("应写入正式表")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
