package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

// ---------------------------------------------------------------- 测试 fake

// fakeAPI OrderAPI 的可配置 fake（单测不连真实接口）。
type fakeAPI struct {
	mu           sync.Mutex
	orders       map[string]uvmpapi.Dict // runCode → 单据
	queryDelay   time.Duration
	failQuery    map[string]error
	queryConc    int
	maxQueryConc int

	points      []uvmpapi.Dict
	seg         uvmpapi.Dict
	segs        map[string]uvmpapi.Dict // vehicleId → 行程段（按车区分）
	failDetails error
	failPoints  error
	failSeg     error
}

func (f *fakeAPI) QueryPcdByRunCode(ctx context.Context, code, dateHint string, lookbackDays int, log uvmpapi.LogFunc) (uvmpapi.Dict, bool, error) {
	f.mu.Lock()
	f.queryConc++
	if f.queryConc > f.maxQueryConc {
		f.maxQueryConc = f.queryConc
	}
	err := f.failQuery[code]
	f.mu.Unlock()
	if f.queryDelay > 0 {
		select {
		case <-time.After(f.queryDelay):
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	f.mu.Lock()
	f.queryConc--
	f.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	o, ok := f.orders[code]
	return o, ok, nil
}

func (f *fakeAPI) GetPrintDetails(ctx context.Context, ids []string, chunk int) ([]uvmpapi.Dict, error) {
	if f.failDetails != nil {
		return nil, f.failDetails
	}
	var out []uvmpapi.Dict
	for _, id := range ids {
		for _, o := range f.orders {
			if strAny(o["id"]) == id {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

func (f *fakeAPI) QueryTrackPoints(ctx context.Context, vehicleID, begin, end string) ([]uvmpapi.Dict, error) {
	if f.failPoints != nil {
		return nil, f.failPoints
	}
	if f.points != nil {
		return f.points, nil
	}
	return []uvmpapi.Dict{{"locatetime": "2026-10-28 08:00:00", "dx": 112.5, "dy": 37.8, "speed": 30, "direction": 90}}, nil
}

func (f *fakeAPI) QueryTrackSegment(ctx context.Context, vehicleID, begin, end string) (uvmpapi.Dict, error) {
	if f.failSeg != nil {
		return nil, f.failSeg
	}
	if f.segs != nil {
		return f.segs[vehicleID], nil
	}
	return f.seg, nil
}

// fakeRenderer 渲染 fake：产合法 stub PDF，可注入失败/阻塞，记录调用与关闭。
type fakeRenderer struct {
	mu         sync.Mutex
	failPDF    func(outPDF string) bool
	failConv   func(outPDF string) bool // PNGToPDF 失败注入（轨迹渲染失败路径）
	block      chan struct{}            // 非 nil 时渲染阻塞至关闭或 ctx 取消
	closed     bool
	pdfCalls   int
	pngCalls   int
	routeCalls int
	conv       int
}

func (r *fakeRenderer) RenderPCDPDF(ctx context.Context, item map[string]any, outPDF, tag, diagDir string) error {
	r.mu.Lock()
	r.pdfCalls++
	r.mu.Unlock()
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.failPDF != nil && r.failPDF(outPDF) {
		return fmt.Errorf("渲染失败(测试)")
	}
	return writeStubPDF(outPDF, "pcd stub")
}

func (r *fakeRenderer) RenderRoutePNG(ctx context.Context, routeID, outPNG, preShotJS string) (render.RewriteResult, error) {
	r.mu.Lock()
	r.pngCalls++
	r.routeCalls++
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return render.RewriteResult{}, err
	}
	_ = os.WriteFile(outPNG, []byte("png"), 0o644)
	return render.RewriteResult{}, nil
}

func (r *fakeRenderer) RenderPCDPNG(ctx context.Context, order map[string]any, outPNG, preShotJS string) (render.RewriteResult, error) {
	r.mu.Lock()
	r.pngCalls++
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return render.RewriteResult{}, err
	}
	_ = os.WriteFile(outPNG, []byte("png"), 0o644)
	return render.RewriteResult{}, nil
}

func (r *fakeRenderer) PNGToPDF(ctx context.Context, pngPath, outPDF string) error {
	r.mu.Lock()
	r.conv++
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.failConv != nil && r.failConv(outPDF) {
		return fmt.Errorf("轨迹渲染失败(测试)")
	}
	return writeStubPDF(outPDF, "track stub")
}

func (r *fakeRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.block != nil {
		select {
		case <-r.block:
		default:
			close(r.block)
		}
	}
	return nil
}

func (r *fakeRenderer) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// fakeFactory 渲染工厂 fake。
type fakeFactory struct {
	mu        sync.Mutex
	created   []*fakeRenderer
	failPDF   func(outPDF string) bool
	failConv  func(outPDF string) bool
	block     bool
	failStart bool
}

func (f *fakeFactory) factory(ctx context.Context, idx int, creds render.Credentials) (Renderer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart {
		return nil, fmt.Errorf("启动失败(测试)")
	}
	r := &fakeRenderer{failPDF: f.failPDF, failConv: f.failConv}
	if f.block {
		r.block = make(chan struct{})
	}
	f.created = append(f.created, r)
	return r, nil
}

func (f *fakeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeFactory) all() []*fakeRenderer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeRenderer(nil), f.created...)
}

// fakeCreds 凭证快照 fake（Generation 可变）。
type fakeCreds struct {
	mu sync.Mutex
	c  render.Credentials
}

func (f *fakeCreds) Snapshot() render.Credentials {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}

func (f *fakeCreds) setGen(g int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c.Generation = g
}

// ---------------------------------------------------------------- 测试工具

// runReq 跑一次 Run，返回解析后的 NDJSON 事件与错误。
func runReq(t *testing.T, ctx context.Context, req Request, build func(ctx context.Context, req Request, cfg *RunConfig) (Deps, error)) ([]map[string]any, error) {
	t.Helper()
	buf := &bytes.Buffer{}
	em := NewEmitter(buf)
	err := Run(ctx, req, em, build)
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if jerr := json.Unmarshal([]byte(line), &ev); jerr != nil {
			t.Fatalf("stdout 非法 NDJSON 行 %q: %v", line, jerr)
		}
		events = append(events, ev)
	}
	return events, err
}

func evTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprint(e["event"]))
	}
	return out
}

func lastEvent(events []map[string]any) map[string]any {
	if len(events) == 0 {
		return nil
	}
	return events[len(events)-1]
}

func progressLabels(events []map[string]any) []string {
	var out []string
	for _, e := range events {
		if fmt.Sprint(e["event"]) == "progress" {
			out = append(out, fmt.Sprint(e["label"]))
		}
	}
	return out
}

func logLines(events []map[string]any) []string {
	var out []string
	for _, e := range events {
		if fmt.Sprint(e["event"]) == "log" {
			out = append(out, fmt.Sprint(e["line"]))
		}
	}
	return out
}

func hasLog(events []map[string]any, sub string) bool {
	for _, l := range logLines(events) {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func doneResult(t *testing.T, events []map[string]any) Result {
	t.Helper()
	ev := lastEvent(events)
	if fmt.Sprint(ev["event"]) != "done" {
		t.Fatalf("末事件应为 done，实际 %v", ev)
	}
	raw, _ := json.Marshal(ev["result"])
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("解析 result 失败: %v", err)
	}
	return res
}

// mkTestOrder 测试单据。
func mkTestOrder(code string) uvmpapi.Dict {
	return uvmpapi.Dict{
		"id":              "ID-" + code,
		"runCode":         code,
		"vehicleNumber":   "晋AD10001",
		"vehicleId":       "V1",
		"planSendTimeStr": "2026-10-28 08:00:00",
		"realSendTime":    "2026-10-28 08:00:00.0",
		"realBackTime":    "2026-10-28 10:00:00.0",
	}
}

// ---------------------------------------------------------------- mock 全流程

// TestMockRunFullFlow dev.mock 全流程：查询/命名/跳过/合并/清单/未找到/12点报表/
// manifest 与 Python 编排结果一致，全程无浏览器。
func TestMockRunFullFlow(t *testing.T) {
	today := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	api := NewFakeAPIAt(today)
	api.Delay = 0
	out := t.TempDir()
	ymd, ymdY := today.Format("20060102"), today.AddDate(0, 0, -1).Format("20060102")
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": out, "batch_subdir": false},
		Codes: []CodeItem{
			{Seq: 1, Code: "PCD" + ymd + "01"},  // 当日已完结：派车单+轨迹
			{Seq: 2, Code: "PCD" + ymd + "02"},  // 当日已完结
			{Seq: 3, Code: "PCD" + ymd + "06"},  // 已派车未完结：轨迹跳过
			{Seq: 4, Code: "PCD" + ymdY + "11"}, // 昨日已完结
			{Seq: 5, Code: "PCDNOTEXIST"},       // 未找到
		},
	}
	build := func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api}, nil
	}
	events, err := runReq(t, context.Background(), req, build)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	res := doneResult(t, events)

	// ---- 结果形状（backend.run 同口径）----
	if res.Total != 5 || res.Found != 4 || len(res.NotFound) != 1 || res.NotFound[0] != "PCDNOTEXIST" {
		t.Fatalf("结果计数不符: %+v", res)
	}
	if res.PcdOk != 4 || res.PcdFail != 0 || res.TrackOk != 3 || res.TrackFail != 0 {
		t.Fatalf("成功/失败计数不符: pcd %d/%d track %d/%d", res.PcdOk, res.PcdFail, res.TrackOk, res.TrackFail)
	}
	if len(res.Rows) != 4 {
		t.Fatalf("rows 应为找到的 4 单，实际 %d", len(res.Rows))
	}
	bySeq := map[int]row{}
	for _, r := range res.Rows {
		bySeq[r.Seq] = r
	}
	if r := bySeq[1]; r.PcdStatus != "成功" || r.TrackStatus != "成功" || r.Note != "mock 占位；mock 占位" {
		t.Fatalf("行1 不符（Python 口径派车单/轨迹各追加一次 mock 占位）: %+v", r)
	}
	if r := bySeq[3]; r.TrackStatus != "跳过（未完结或缺实际时间）" {
		t.Fatalf("行3 应跳过轨迹: %+v", r)
	}

	// ---- 逐单文件（seq 补零命名）----
	files, _ := os.ReadDir(filepath.Join(out, "逐单"))
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name()] = true
	}
	for _, n := range []string{
		"1_派车单_PCD" + ymd + "01_晋AD10001_2026-10-28.pdf",
		"1_轨迹_PCD" + ymd + "01_晋AD10001_2026-10-28.pdf",
		"1_轨迹点_PCD" + ymd + "01.csv",
		"4_派车单_PCD" + ymdY + "11_晋AD10011_2026-10-27.pdf",
	} {
		if !names[n] {
			t.Fatalf("缺少逐单文件 %s（实际 %v）", n, names)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "_合并_派车单.pdf")); err != nil {
		t.Fatalf("缺少合并派车单: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "_合并_轨迹.pdf")); err != nil {
		t.Fatalf("缺少合并轨迹: %v", err)
	}

	// ---- _清单.csv（BOM + 表头 + 4 行）----
	raw, err := os.ReadFile(filepath.Join(out, "_清单.csv"))
	if err != nil {
		t.Fatal(err)
	}
	csv := string(raw)
	if !strings.HasPrefix(csv, csvBOM) {
		t.Fatalf("_清单.csv 缺 UTF-8 BOM")
	}
	if !strings.Contains(csv, "序号,派车单号,车牌,用车日期,派车单PDF,派车单状态,轨迹PDF,轨迹状态,备注") {
		t.Fatalf("_清单.csv 表头不符: %q", csv)
	}
	if strings.Contains(csv, "PCDNOTEXIST") {
		t.Fatalf("_清单.csv 仅含找到的单（未找到走 _未找到.txt）: %q", csv)
	}
	if n := strings.Count(csv, "\n"); n != 5 { // 表头 + 4 行
		t.Fatalf("_清单.csv 行数不符（%d 行）: %q", n, csv)
	}

	// ---- _未找到.txt ----
	nf, err := os.ReadFile(filepath.Join(out, "_未找到.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nf), "以下 1 个派车单号未查询到：\nPCDNOTEXIST") {
		t.Fatalf("_未找到.txt 内容不符: %q", nf)
	}

	// ---- _12点前结束行程.xlsx（mock 无行程段 → 仅表头）----
	noonPath := filepath.Join(out, "_12点前结束行程.xlsx")
	if _, err := os.Stat(noonPath); err != nil {
		t.Fatalf("缺少 12 点报表: %v", err)
	}
	checkNoonSheet(t, noonPath, []string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间"}, 0)

	// ---- manifest.json（4 pcd + 3 track）----
	raw, err = os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mf map[string]map[string]string
	if err := json.Unmarshal(raw, &mf); err != nil {
		t.Fatalf("manifest 非法 JSON: %v", err)
	}
	if len(mf["pcd"]) != 4 || len(mf["track"]) != 3 {
		t.Fatalf("manifest 条目数不符: %s", raw)
	}

	// ---- 进度/日志与 Python 编排一致 ----
	wantLabels := []string{"按单号查询",
		"查询 " + req.Codes[0].Code, "查询 " + req.Codes[1].Code, "查询 " + req.Codes[2].Code,
		"查询 " + req.Codes[3].Code, "查询 " + req.Codes[4].Code,
		"查询完成",
	}
	got := progressLabels(events)
	for i, w := range wantLabels {
		if i >= len(got) || got[i] != w {
			t.Fatalf("进度序列不符\nwant %v\ngot  %v", wantLabels, got)
		}
	}
	if !hasLog(events, "共 5 个派车单号") || !hasLog(events, "[dev] dev.mock=true") ||
		!hasLog(events, "查询完成：找到 4 单，未找到 1 单") ||
		!hasLog(events, "派车单：共 4 单，已导出 0 单，本次待导出 4 单") ||
		!hasLog(events, "轨迹：待导出 3 条") ||
		!hasLog(events, "[3/3]") ||
		!hasLog(events, "未找到清单已写入 _未找到.txt（1 个）") ||
		!hasLog(events, "全部完成：派车单 成功 4/失败 0；轨迹 成功 3/失败 0；未找到 1") {
		t.Fatalf("关键日志缺失: %v", logLines(events))
	}
}

// TestEmptyCodesErrorEvent CI 冒烟：config 为空、codes 为空时先给 error 事件，
// 不触发依赖构建。
func TestEmptyCodesErrorEvent(t *testing.T) {
	called := false
	build := func(context.Context, Request, *RunConfig) (Deps, error) {
		called = true
		return Deps{}, nil
	}
	events, err := runReq(t, context.Background(), Request{Config: map[string]any{}}, build)
	if err == nil {
		t.Fatal("空清单应报错")
	}
	if called {
		t.Fatal("校验失败不应构建依赖（登录等）")
	}
	if len(events) != 1 || fmt.Sprint(events[0]["event"]) != "error" ||
		!strings.Contains(fmt.Sprint(events[0]["error"]), "未解析到任何派车单号") {
		t.Fatalf("应仅有一个 error 事件，实际 %v", events)
	}
}

// TestNoExportSelected 勾选校验：派车单/轨迹至少勾选一项。
func TestNoExportSelected(t *testing.T) {
	req := Request{
		Config: map[string]any{},
		Params: map[string]any{"export_pcd": false, "export_track": false},
		Codes:  []CodeItem{{Seq: 1, Code: "X"}},
	}
	events, err := runReq(t, context.Background(), req, func(context.Context, Request, *RunConfig) (Deps, error) {
		t.Fatal("不应构建依赖")
		return Deps{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "至少勾选一项") {
		t.Fatalf("应报勾选错误，实际 %v", err)
	}
	if fmt.Sprint(lastEvent(events)["event"]) != "error" {
		t.Fatalf("应发 error 事件，实际 %v", lastEvent(events))
	}
}

// TestSeqZeroPadding NNN 按清单行数最大位数补零（12 单 → 两位）。
func TestSeqZeroPadding(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{}}
	var codes []CodeItem
	for i := 1; i <= 12; i++ {
		c := fmt.Sprintf("T%02d", i)
		api.orders[c] = mkTestOrder(c)
		codes = append(codes, CodeItem{Seq: i, Code: c})
	}
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_track": false},
		Codes:  codes,
	}
	events, err := runReq(t, context.Background(), req, func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(out, "逐单"))
	found01, found12 := false, false
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "01_派车单_T01_") {
			found01 = true
		}
		if strings.HasPrefix(f.Name(), "12_派车单_T12_") {
			found12 = true
		}
	}
	if !found01 || !found12 {
		t.Fatalf("补零命名不符: %v", files)
	}
	if res := doneResult(t, events); res.PcdOk != 12 {
		t.Fatalf("应成功 12 单，实际 %+v", res)
	}
}

// TestManifestResume 断点续传：重跑跳过已导出（此前已导出备注），manifest 删一条则只补导该条。
func TestManifestResume(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{}}
	api.orders["R1"] = mkTestOrder("R1")
	api.orders["R2"] = mkTestOrder("R2")
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_track": false},
		Codes:  []CodeItem{{Seq: 1, Code: "R1"}, {Seq: 2, Code: "R2"}},
	}
	build := func(context.Context, Request, *RunConfig) (Deps, error) { return Deps{API: api}, nil }
	if _, err := runReq(t, context.Background(), req, build); err != nil {
		t.Fatal(err)
	}

	// 第二跑：全部续传
	events, err := runReq(t, context.Background(), req, build)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLog(events, "已导出 2 单，本次待导出 0 单") {
		t.Fatalf("应全部续传: %v", logLines(events))
	}
	res := doneResult(t, events)
	for _, r := range res.Rows {
		if r.PcdStatus != "成功" || !strings.Contains(r.Note, "派车单此前已导出") {
			t.Fatalf("续传行不符: %+v", r)
		}
	}

	// 删掉 R2 的 manifest 条目 → 只补导 R2
	raw, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	var mf map[string]map[string]string
	_ = json.Unmarshal(raw, &mf)
	delete(mf["pcd"], "ID-R2")
	raw, _ = json.MarshalIndent(mf, "", " ")
	_ = os.WriteFile(filepath.Join(out, "manifest.json"), raw, 0o644)

	events, err = runReq(t, context.Background(), req, build)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLog(events, "已导出 1 单，本次待导出 1 单") || !hasLog(events, "[1/1] 2_派车单_R2_") {
		t.Fatalf("补导不符: %v", logLines(events))
	}
}

// TestSingleFailureNotFatal 单条渲染失败不毁批：其余成功，done 照出。
func TestSingleFailureNotFatal(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{}}
	for _, c := range []string{"A1", "A2", "A3"} {
		api.orders[c] = mkTestOrder(c)
	}
	f := &fakeFactory{failPDF: func(outPDF string) bool { return strings.Contains(outPDF, "A2") }}
	browser := filepath.Join(t.TempDir(), "fake-chrome")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"render": map[string]any{"chrome": browser}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_track": false},
		Codes:  []CodeItem{{Seq: 1, Code: "A1"}, {Seq: 2, Code: "A2"}, {Seq: 3, Code: "A3"}},
	}
	build := func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api, NewRenderer: f.factory, Creds: &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}}, nil
	}
	events, err := runReq(t, context.Background(), req, build)
	if err != nil {
		t.Fatalf("单条失败不应毁批: %v", err)
	}
	res := doneResult(t, events)
	if res.PcdOk != 2 || res.PcdFail != 1 {
		t.Fatalf("计数不符: %+v", res)
	}
	if !hasLog(events, "  失败 A2: 渲染失败(测试)") {
		t.Fatalf("缺失败日志: %v", logLines(events))
	}
	if res.Rows[1].PcdStatus != "失败" || res.Rows[0].PcdStatus != "成功" {
		t.Fatalf("行状态不符: %+v", res.Rows)
	}
}

// TestCancelClosesAllWorkers 取消即关全部渲染 worker，error 事件为「已取消」。
func TestCancelClosesAllWorkers(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{"B1": mkTestOrder("B1"), "B2": mkTestOrder("B2")}}
	f := &fakeFactory{block: true}
	browser := filepath.Join(t.TempDir(), "fake-chrome")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"render": map[string]any{"chrome": browser}, "order_export": map[string]any{"render_workers": 2}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_track": false},
		Codes:  []CodeItem{{Seq: 1, Code: "B1"}, {Seq: 2, Code: "B2"}},
	}
	build := func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api, NewRenderer: f.factory, Creds: &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	events, err := runReq(t, ctx, req, build)
	if err == nil {
		t.Fatal("取消应返回错误")
	}
	if fmt.Sprint(lastEvent(events)["event"]) != "error" ||
		!strings.Contains(fmt.Sprint(lastEvent(events)["error"]), "已取消") {
		t.Fatalf("应发「已取消」error 事件: %v", lastEvent(events))
	}
	for _, r := range f.all() {
		if !r.isClosed() {
			t.Fatal("取消后所有渲染 worker 应关闭")
		}
	}
}

// TestQueryPoolConcurrency 查询池并发受 query_workers 钳制且确有并行。
func TestQueryPoolConcurrency(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{}, queryDelay: 80 * time.Millisecond}
	var codes []CodeItem
	for i := 1; i <= 8; i++ {
		c := fmt.Sprintf("Q%d", i)
		api.orders[c] = mkTestOrder(c)
		codes = append(codes, CodeItem{Seq: i, Code: c})
	}
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}, "order_export": map[string]any{"query_workers": 4}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_track": false},
		Codes:  codes,
	}
	events, err := runReq(t, context.Background(), req, func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := doneResult(t, events); res.PcdOk != 8 {
		t.Fatalf("应成功 8 单: %+v", res)
	}
	api.mu.Lock()
	maxc := api.maxQueryConc
	api.mu.Unlock()
	if maxc < 2 {
		t.Fatalf("查询未并行（最大并发 %d）", maxc)
	}
	if maxc > 4 {
		t.Fatalf("查询并发超出钳制（最大并发 %d）", maxc)
	}
}

// TestBatchSubdirNaming 批次子目录命名 <批次名>_导出_<YYYYMMDD_HHMMSS>。
func TestBatchSubdirNaming(t *testing.T) {
	api := &fakeAPI{orders: map[string]uvmpapi.Dict{"N1": mkTestOrder("N1")}}
	base := t.TempDir()
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": base, "batch_name": "夜班/测试"},
		Codes:  []CodeItem{{Seq: 1, Code: "N1"}},
	}
	events, err := runReq(t, context.Background(), req, func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res := doneResult(t, events)
	name := filepath.Base(res.Dir)
	if !strings.HasPrefix(name, "夜班_测试_导出_") {
		t.Fatalf("批次目录名不符: %s", name)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "manifest.json")); err != nil {
		t.Fatalf("批次目录未落产物: %v", err)
	}
}

// TestTrackNoonReportAndCalibrate 真实模式轨迹：12 点前结束行程统计 + 自动校准
// （校准日志、xlsx 校准列、合并页码、PNG 清理、行程详情弹窗路径）。
func TestTrackNoonReportAndCalibrate(t *testing.T) {
	api := &fakeAPI{
		orders: map[string]uvmpapi.Dict{},
		segs:   map[string]uvmpapi.Dict{},
	}
	o1 := mkTestOrder("S1") // 10:30 结束 → 校准至 12:30
	o1["vehicleId"] = "VA"
	o2 := mkTestOrder("S2") // 15:00 结束 → 不进 12 点表
	o2["vehicleId"] = "VB"
	o2["realBackTime"] = "2026-10-28 15:00:00.0"
	api.orders["S1"] = o1
	api.orders["S2"] = o2
	api.segs["VA"] = uvmpapi.Dict{
		"id": "SEG-1", "starttime": "2026-10-28 08:00:00", "endtime": "2026-10-28 10:30:00",
	}
	api.segs["VB"] = uvmpapi.Dict{
		"id": "SEG-2", "starttime": "2026-10-28 08:00:00", "endtime": "2026-10-28 15:00:00",
	}
	f := &fakeFactory{}
	browser := filepath.Join(t.TempDir(), "fake-chrome")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"render": map[string]any{"chrome": browser}},
		Params: map[string]any{
			"outdir": out, "batch_subdir": false,
			"export_pcd": false, "calibrate_noon": true,
		},
		Codes: []CodeItem{{Seq: 1, Code: "S1"}, {Seq: 2, Code: "S2"}},
	}
	build := func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api, NewRenderer: f.factory, Creds: &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}}, nil
	}
	events, err := runReq(t, context.Background(), req, build)
	if err != nil {
		t.Fatal(err)
	}
	res := doneResult(t, events)
	if res.TrackOk != 2 || res.PcdOk != 0 {
		t.Fatalf("计数不符: %+v", res)
	}
	if !hasLog(events, "  [校准] 晋AD10001 S1 行程结束 2026-10-28 10:30:00 → 2026-10-28 12:30:00，行驶时间 → 4小时30分钟") {
		t.Fatalf("缺校准日志: %v", logLines(events))
	}
	// 12 点表：仅 S1，带校准列，页码 = 轨迹成功序列位次
	noonPath := filepath.Join(out, "_12点前结束行程.xlsx")
	checkNoonSheet(t, noonPath,
		[]string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间", "校准后结束时间", "校准后行驶时长"}, 1)
	fx, _ := excelize.OpenFile(noonPath)
	defer fx.Close()
	if v, _ := fx.GetCellValue("12点前结束行程", "B2"); v != "S1" {
		t.Fatalf("12 点表应仅含 S1: %q", v)
	}
	if v, _ := fx.GetCellValue("12点前结束行程", "A2"); v != "1" {
		t.Fatalf("合并页码应为 1: %q", v)
	}
	if v, _ := fx.GetCellValue("12点前结束行程", "F2"); v != "2026-10-28 12:30:00" {
		t.Fatalf("校准后结束时间不符: %q", v)
	}
	if v, _ := fx.GetCellValue("12点前结束行程", "G2"); v != "4小时30分钟" {
		t.Fatalf("校准后行驶时长不符: %q", v)
	}
	// PNG 中间件清理
	files, _ := os.ReadDir(filepath.Join(out, "逐单"))
	for _, fi := range files {
		if filepath.Ext(fi.Name()) == ".png" {
			t.Fatalf("PNG 中间件未清理: %s", fi.Name())
		}
	}
	// 行程段存在 → 走行程详情弹窗（RenderRoutePNG）
	totalPNG := 0
	for _, r := range f.all() {
		r.mu.Lock()
		totalPNG += r.routeCalls
		r.mu.Unlock()
	}
	if totalPNG != 2 {
		t.Fatalf("应 2 次行程详情弹窗截图，实际 %d", totalPNG)
	}
}

// TestTrackNoonKeptOnRenderFailure 轨迹渲染/CSV 失败仍保留 12 点前结束记录
// （与 Python 一致：noon 在渲染前已判定），页码留空（未进合并轨迹 PDF）。
func TestTrackNoonKeptOnRenderFailure(t *testing.T) {
	api := &fakeAPI{
		orders: map[string]uvmpapi.Dict{},
		segs:   map[string]uvmpapi.Dict{},
	}
	o1 := mkTestOrder("S1") // 10:30 结束 → noon 记录，渲染将失败
	o1["vehicleId"] = "VA"
	o2 := mkTestOrder("S2") // 15:00 结束 → 不进 12 点表，渲染成功
	o2["vehicleId"] = "VB"
	o2["realBackTime"] = "2026-10-28 15:00:00.0"
	api.orders["S1"] = o1
	api.orders["S2"] = o2
	api.segs["VA"] = uvmpapi.Dict{
		"id": "SEG-1", "starttime": "2026-10-28 08:00:00", "endtime": "2026-10-28 10:30:00",
	}
	api.segs["VB"] = uvmpapi.Dict{
		"id": "SEG-2", "starttime": "2026-10-28 08:00:00", "endtime": "2026-10-28 15:00:00",
	}
	f := &fakeFactory{failConv: func(outPDF string) bool { return strings.Contains(outPDF, "S1") }}
	browser := filepath.Join(t.TempDir(), "fake-chrome")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	out := t.TempDir()
	req := Request{
		Config: map[string]any{"render": map[string]any{"chrome": browser}},
		Params: map[string]any{"outdir": out, "batch_subdir": false, "export_pcd": false},
		Codes:  []CodeItem{{Seq: 1, Code: "S1"}, {Seq: 2, Code: "S2"}},
	}
	events, err := runReq(t, context.Background(), req, func(context.Context, Request, *RunConfig) (Deps, error) {
		return Deps{API: api, NewRenderer: f.factory, Creds: &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res := doneResult(t, events)
	if res.TrackOk != 1 || res.TrackFail != 1 {
		t.Fatalf("计数不符: %+v", res)
	}
	// noon 记录保留（S1），页码留空；S2 非 12 点前不进表
	noonPath := filepath.Join(out, "_12点前结束行程.xlsx")
	checkNoonSheet(t, noonPath, []string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间"}, 1)
	fx, _ := excelize.OpenFile(noonPath)
	defer fx.Close()
	if v, _ := fx.GetCellValue("12点前结束行程", "B2"); v != "S1" {
		t.Fatalf("12 点表应保留失败单 S1: %q", v)
	}
	if v, _ := fx.GetCellValue("12点前结束行程", "A2"); v != "" {
		t.Fatalf("失败单页码应留空: %q", v)
	}
}

// TestRunBuildReceivesJobCtx build 回调收到 job ctx（真实路径 SSO 登录同链取消）。
func TestRunBuildReceivesJobCtx(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "job-ctx")
	req := Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": t.TempDir(), "batch_subdir": false},
		Codes:  []CodeItem{{Seq: 1, Code: "X"}},
	}
	var got any
	_, err := runReq(t, ctx, req, func(c context.Context, _ Request, _ *RunConfig) (Deps, error) {
		got = c.Value(ctxKey{})
		return Deps{API: &fakeAPI{orders: map[string]uvmpapi.Dict{"X": mkTestOrder("X")}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "job-ctx" {
		t.Fatalf("build 未收到 job ctx: %v", got)
	}
}
