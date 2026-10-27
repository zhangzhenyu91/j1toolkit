package crossing

// 直推复用方法 / 导出预览 / WeKnora 同步 / 内网 U 盘同步测试（假实现注入，不连真实服务）。

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
)

// crossingSyncFakeUsb U 盘设备链路假实现。
type crossingSyncFakeUsb struct {
	files  map[string][]byte
	pushed []string
}

func (f *crossingSyncFakeUsb) MountToHost(context.Context) error { return nil }

func (f *crossingSyncFakeUsb) DownloadFile(_ context.Context, name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (f *crossingSyncFakeUsb) ListFiles(context.Context) ([]string, error) {
	out := make([]string, 0, len(f.files))
	for n := range f.files {
		out = append(out, n)
	}
	return out, nil
}

func (f *crossingSyncFakeUsb) DeleteFiles(_ context.Context, names []string) error {
	for _, n := range names {
		delete(f.files, n)
	}
	return nil
}

func (f *crossingSyncFakeUsb) PushFile(_ context.Context, name string, data []byte) error {
	f.files[name] = data
	f.pushed = append(f.pushed, name)
	return nil
}

// crossingFakeWeKnora WeKnora API 假实现。
type crossingFakeWeKnora struct {
	uploaded []string
	deleted  []string
	list     []map[string]any
}

func (f *crossingFakeWeKnora) ListKnowledge(context.Context, string, map[string]string) ([]map[string]any, error) {
	return f.list, nil
}

func (f *crossingFakeWeKnora) UploadKnowledgeFile(_ context.Context, _ string, p WeKnoraUpload) (string, error) {
	f.uploaded = append(f.uploaded, p.FileName)
	return "k1", nil
}

func (f *crossingFakeWeKnora) DeleteKnowledge(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *crossingFakeWeKnora) BatchDeleteKnowledge(context.Context, string, []string) error {
	return nil
}

// crossingFakeWkState 内存状态存储。
type crossingFakeWkState struct{ st WeKnoraState }

func (f *crossingFakeWkState) Load(context.Context) (WeKnoraState, error) {
	if f.st.Lines == nil {
		return WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}, nil
	}
	return f.st, nil
}

func (f *crossingFakeWkState) Save(_ context.Context, st WeKnoraState) error {
	f.st = st
	return nil
}

// crossingRecordRow 一条 crossing_record 行（24 列，对齐 recordCols）。
func crossingRecordRow(id int64, line string) []driver.Value {
	now := driver.Value(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	return []driver.Value{
		id, int64(1), "10", line, "15#-16#", nil, nil, "跨越高速", "50", "50",
		"12", "8", "45", "无", "李四", "2026-10-01", "", "crane", nil, nil, nil, nil, now, now,
	}
}

func TestIngestMethodReusable(t *testing.T) {
	db, fdb := openFake(t)
	m := New(db, fakeCfg{})
	raw := []byte(`{"session_id":"s1","team_name":"检修一班","mode":"direct","records":[{"line_name":"甲线","crane_record_id":"c1"}]}`)
	ctx := context.Background()

	if _, err := m.Ingest(ctx, "any", raw); !errors.Is(err, ErrIngestNotConfigured) {
		t.Fatalf("未配置密钥应 ErrIngestNotConfigured，got %v", err)
	}
	m.Cfg = fakeCfg{kv: map[string]string{"CROSSING_INGEST_TOKEN": "s3cr3t"}}
	if _, err := m.Ingest(ctx, "wrong", raw); !errors.Is(err, ErrIngestToken) {
		t.Fatalf("密钥错误应 ErrIngestToken，got %v", err)
	}
	if _, err := m.Ingest(ctx, "s3cr3t", []byte(`{`)); !errors.Is(err, ErrIngestBadJSON) {
		t.Fatalf("坏 JSON 应 ErrIngestBadJSON，got %v", err)
	}
	if _, err := m.Ingest(ctx, "s3cr3t", []byte(`{"session_id":"s1","team_name":"检修一班","mode":"bad"}`)); err == nil {
		t.Fatalf("非法 mode 应参数错误")
	} else if _, ok := err.(*IngestParamError); !ok {
		t.Fatalf("参数错误类型不符：%T", err)
	}
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(1), "检修一班"})
	res, err := m.Ingest(ctx, "s3cr3t", raw)
	if err != nil {
		t.Fatalf("直推应成功: %v", err)
	}
	if res.Mode != "direct" || res.Inserted != 1 {
		t.Fatalf("应插入 1 条，got %+v", res)
	}
	if len(fdb.callsWith("INSERT INTO crossing_record")) != 1 {
		t.Fatalf("应写入正式表")
	}
}

func TestExportPreviewBranches(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 1, "检修一班")
	m := New(db, fakeCfg{kv: map[string]string{"OO_PUBLIC_URL": "https://office.example.com"}})
	h := newTestRouter(m, auth.Identity{ID: 1, Role: "admin", TeamID: 1})
	rec := doJSON(t, h, "GET", "/crossing/export/preview?line=%E7%94%B2%E7%BA%BF&split=1", "t", "")
	env := decodeEnvelope(t, rec)
	data, _ := env["data"].(map[string]any)
	urlStr, _ := data["url"].(string)
	if !strings.HasPrefix(urlStr, "/office.html?src=crossing&line=") ||
		!strings.Contains(urlStr, "split=1") || !strings.Contains(urlStr, "back=/crossing.html") {
		t.Fatalf("OO 分支 URL 口径不符：%s", urlStr)
	}

	m2 := New(db, fakeCfg{})
	h2 := newTestRouter(m2, auth.Identity{ID: 1, Role: "admin", TeamID: 1})
	rec2 := doJSON(t, h2, "GET", "/crossing/export/preview", "t", "")
	env2 := decodeEnvelope(t, rec2)
	data2, _ := env2["data"].(map[string]any)
	urlStr2, _ := data2["url"].(string)
	if !strings.HasPrefix(urlStr2, "https://view.officeapps.live.com/op/view.aspx?src=") {
		t.Fatalf("查看器分支 URL 口径不符：%s body=%s", urlStr2, rec2.Body.String())
	}
	parsed, err := url.Parse(urlStr2)
	if err != nil {
		t.Fatalf("查看器 URL 非法: %v", err)
	}
	inner, err := url.QueryUnescape(parsed.Query().Get("src"))
	if err != nil {
		t.Fatalf("src 解码失败: %v", err)
	}
	if !strings.Contains(inner, "token=t") || !strings.Contains(inner, "/api/v1/crossing/export/") {
		t.Fatalf("回源地址口径不符：%s", inner)
	}
}

func TestWeKnoraSyncNotConfiguredAndFlow(t *testing.T) {
	db, fdb := openFake(t)
	m := New(db, fakeCfg{})
	if _, err := m.RunWeKnoraSync(context.Background()); !errors.Is(err, ErrWeKnoraNotConfigured) {
		t.Fatalf("未配置应 ErrWeKnoraNotConfigured，got %v", err)
	}
	m.Cfg = fakeCfg{kv: map[string]string{
		"WEKNORA_CROSSING_KB_ID": "kb-1",
		"WEKNORA_API_KEY":        "key-1",
		"CROSSING_SYNC_TEAM":     "检修一班",
	}}
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "crossing_type",
		"dist_small", "dist_big", "cross_horizontal", "cross_vertical", "cross_angle", "has_hazard",
		"finder_name", "inspect_at", "remark", "source", "crane_session_id", "crane_record_id",
		"synced_at", "created_by", "created_at", "updated_at",
	}, crossingRecordRow(1, "甲线"))
	client := &crossingFakeWeKnora{}
	m.WeKnora = client
	m.WeKnoraState = &crossingFakeWkState{st: WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}}
	res, err := m.RunWeKnoraSync(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || res.Created != 1 || len(client.uploaded) != 1 {
		t.Fatalf("应上传 1 张线路表，got %+v uploads=%v", res, client.uploaded)
	}
	if client.uploaded[0] != "交跨台账/甲线.xlsx" {
		t.Fatalf("上传文件名口径不符：%s", client.uploaded[0])
	}
}

func TestIntranetSyncNotConfiguredAndNoFile(t *testing.T) {
	db, fdb := openFake(t)
	m := New(db, fakeCfg{})
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); !errors.Is(err, ErrSyncNotConfigured) {
		t.Fatalf("链路未注入应 ErrSyncNotConfigured，got %v", err)
	}
	m.Usb = &crossingSyncFakeUsb{files: map[string][]byte{}}
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); !errors.Is(err, ErrSyncNotConfigured) {
		t.Fatalf("未配置设备应 ErrSyncNotConfigured，got %v", err)
	}
	m.Cfg = fakeCfg{kv: map[string]string{
		"CROSSING_SYNC_TEAM":           "检修一班",
		"CROSSING_SYNC_DEVICE_DDNS":    "dev-1",
		"CROSSING_SYNC_CMD_WINDOW_MIN": "0",
	}}
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "crossing_type",
		"dist_small", "dist_big", "cross_horizontal", "cross_vertical", "cross_angle", "has_hazard",
		"finder_name", "inspect_at", "remark", "source", "crane_session_id", "crane_record_id",
		"synced_at", "created_by", "created_at", "updated_at",
	}, crossingRecordRow(1, "甲线"))
	fdb.onRowsAlways("FROM crossing_deleted_id", []string{"id"})
	usb := &crossingSyncFakeUsb{files: map[string][]byte{}}
	m.Usb = usb
	res, err := m.RunIntranetCycle(context.Background(), "manual")
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || !res.Counts.NoFile || !res.Counts.Pushed {
		t.Fatalf("内网无表应直接下发最新表，got %+v", res)
	}
	found := false
	for _, n := range usb.pushed {
		if n == "检修一班交跨统计表.xlsx" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应推送交跨统计表，got %v", usb.pushed)
	}
	if len(fdb.callsWith("INSERT INTO crossing_sync_log")) != 1 {
		t.Fatalf("应写同步日志")
	}
}
