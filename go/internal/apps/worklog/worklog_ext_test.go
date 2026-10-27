package worklog

// 补齐链路行为测试：照片 ZIP 打包、备注附件上传/预览/下载、派车对齐、汇总表预览与存网盘、
// 商旅照片任务化交互；fake DB + 内存对象存储 + 注入桩，不访问外部服务。
// 注意 fake 规则按注册顺序首个匹配命中，同 SQL 不同结果须用独立 env。

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"shade/internal/auth"
)

// memNetdisk 网盘保存桩。
type memNetdisk struct {
	calls []NetdiskSaveRequest
	err   error
}

func (n *memNetdisk) SaveToNetdisk(_ context.Context, _ auth.Identity, req NetdiskSaveRequest) (string, error) {
	n.calls = append(n.calls, req)
	if n.err != nil {
		return "", n.err
	}
	space := "我的空间"
	if req.Space == "public" {
		space = "公共区"
	}
	return space + "/" + req.Dir + "/" + req.Name, nil
}

// memRemote 商旅费用照片远端联动桩。
type memRemote struct {
	uploadErr  error
	unlinkErr  error
	uploadCnt  int
	unlinkCnt  int
	removeCnt  int
	syncCnt    int
	lastRemove []string
}

func (r *memRemote) UploadToMembers(_ context.Context, _ int64, _ string, _ []string, _ []byte) (string, error) {
	r.uploadCnt++
	if r.uploadErr != nil {
		return "", r.uploadErr
	}
	return `{"10":"img-1"}`, nil
}
func (r *memRemote) RemoveMembers(_ context.Context, _ int64, removed []string) error {
	r.removeCnt++
	r.lastRemove = removed
	return nil
}
func (r *memRemote) SyncMembers(_ context.Context, _ int64, _ []string) error {
	r.syncCnt++
	return nil
}
func (r *memRemote) Unlink(_ context.Context, _ int64) error {
	r.unlinkCnt++
	return r.unlinkErr
}

// memOps 商旅照片任务队列桩（同步执行 fn 保测试确定性）。
type memOps struct {
	started  int
	lastKind string
	runErr   error
}

type nopRec struct{}

func (nopRec) SetFailedName(string)   {}
func (nopRec) SetPhoto(int64, string) {}

func (o *memOps) StartPhotoOp(kind string, _, _, _ int64, fn func(ctx context.Context, rec PhotoOpRec) error) int64 {
	o.started++
	o.lastKind = kind
	o.runErr = fn(context.Background(), nopRec{})
	return 42
}

// errString 简单错误串。
type errString string

func (e errString) Error() string { return string(e) }

// multipartBody 构造 multipart 请求体（字段 file + 附加表单字段）。
func multipartBody(t *testing.T, fieldName, fileName string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("构造附件失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("写入附件失败: %v", err)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return body, mw.FormDataContentType()
}

// doMultipart 发送 multipart 请求。
func (e *testEnv) doMultipart(t *testing.T, method, path string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// tinyPNG 生成 2×2 PNG 字节（照片回源/组合 PDF 用）。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

// withRemarkEntry 注册卡片查询规则（备注附件上传入口：4 列）。
func (e *testEnv) withRemarkEntry(filesJSON string) *testEnv {
	e.fake.on("COALESCE(e.remark_files", []string{"team_id", "log_date", "name", "remark_files"},
		[][]driver.Value{{int64(1), "2026-08-18", "检修一班", filesJSON}})
	return e
}

// withRemarkFiles 注册备注附件定位查询规则（预览/下载入口：仅 remark_files 列）。
func (e *testEnv) withRemarkFiles(filesJSON string) *testEnv {
	e.fake.on("COALESCE(e.remark_files", []string{"remark_files"}, [][]driver.Value{{filesJSON}})
	return e
}

// ---- 照片 ZIP 打包 ----

func TestZipPackPhotos(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.objects.items["worklog/a/1-1.jpg"] = []byte("JPEGDATA-1")
	env.objects.items["worklog/a/1-2.jpg"] = []byte("JPEGDATA-2")

	rec := env.do(t, "POST", "/api/v1/worklog/zip", map[string]any{
		"photos": []map[string]any{
			{"url": "https://cos.test/worklog/a/1-1.jpg", "name": "水印 1.jpg"},
			{"url": "https://cos.test/worklog/a/1-2.jpg", "name": "../越权/水印2.jpg"},
			{"url": "https://cos.test/worklog/a/缺.jpg", "name": "缺.jpg"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("打包应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 应 application/zip，实际 %s", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip 解析失败: %v", err)
	}
	names := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip 条目打开失败: %v", err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(rc)
		_ = rc.Close()
		names[f.Name] = b.String()
	}
	if names["水印 1.jpg"] != "JPEGDATA-1" {
		t.Fatalf("缺水印1条目: %v", names)
	}
	// 路径穿越字符应被替换为 _
	if _, ok := names[".._越权_水印2.jpg"]; !ok {
		t.Fatalf("文件名未净化: %v", names)
	}
	if !strings.Contains(names[zipNameFile], "缺.jpg") {
		t.Fatalf("失败清单缺失: %v", names)
	}

	// SSRF 防护：非本仓 COS 地址
	rec = env.do(t, "POST", "/api/v1/worklog/zip", map[string]any{
		"photos": []map[string]any{{"url": "https://evil.test/x.jpg", "name": "x.jpg"}},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhoto {
		t.Fatalf("非法地址应 %d，实际 %d", codeNoPhoto, envr.Code)
	}

	// 全部下载失败 → 50001
	rec = env.do(t, "POST", "/api/v1/worklog/zip", map[string]any{
		"photos": []map[string]any{{"url": "https://cos.test/worklog/a/缺2.jpg", "name": "缺2.jpg"}},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeDownload {
		t.Fatalf("全失败应 %d，实际 %d", codeDownload, envr.Code)
	}

	// 超量
	many := make([]map[string]any, 0, zipMaxPhotos+1)
	for i := 0; i <= zipMaxPhotos; i++ {
		many = append(many, map[string]any{"url": "https://cos.test/worklog/a/1-1.jpg", "name": "x.jpg"})
	}
	rec = env.do(t, "POST", "/api/v1/worklog/zip", map[string]any{"photos": many})
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhoto {
		t.Fatalf("超量应 %d，实际 %d", codeNoPhoto, envr.Code)
	}
}

// ---- 备注附件 ----

func TestRemarkUpload(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkEntry("[]")
	body, ct := multipartBody(t, "file", "临时文件.tmp", []byte("DOC-DATA"), map[string]string{"name": "现场记录.docx"})
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/logs/7/remark-files", body, ct)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("上传应成功，实际 %d %s", envr.Code, envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	key, _ := data["cos_key"].(string)
	if !strings.HasPrefix(key, "worklog/remark/检修一班/2026.08.18/7-") || !strings.HasSuffix(key, ".docx") {
		t.Fatalf("cos_key 口径不符: %q", key)
	}
	if data["type"] != "doc" {
		t.Fatalf("type 应 doc，实际 %v", data["type"])
	}
	if _, ok := env.objects.items[key]; !ok {
		t.Fatalf("附件未落对象存储: %q", key)
	}

	// 白名单外扩展名
	body, ct = multipartBody(t, "file", "run.exe", []byte("X"), nil)
	rec = env.doMultipart(t, "POST", "/api/v1/worklog/logs/7/remark-files", body, ct)
	if envr := decodeEnv(t, rec); envr.Code != codeFileBad {
		t.Fatalf("非法扩展名应 %d，实际 %d", codeFileBad, envr.Code)
	}
}

func TestRemarkUploadCap(t *testing.T) {
	full := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		full = append(full, `{"name":"a","url":"https://cos.test/k","cos_key":"k","type":"doc","size":1}`)
	}
	env := newTestServer(t, identUser).withTeam().
		withRemarkEntry("[" + strings.Join(full, ",") + "]")
	body, ct := multipartBody(t, "file", "a.jpg", []byte("J"), map[string]string{"name": "a.jpg"})
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/logs/7/remark-files", body, ct)
	if envr := decodeEnv(t, rec); envr.Code != codeFileCount {
		t.Fatalf("超上限应 %d，实际 %d", codeFileCount, envr.Code)
	}
}

const remarkDocJSON = `[{"name":"报告.docx","url":"","cos_key":"worklog/remark/检修一班/2026.08.18/7-1.docx","type":"doc","size":8}]`

func TestRemarkPreviewOO(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkFiles(remarkDocJSON)
	env.module.Cfg = memConfig{"OO_PUBLIC_URL": "https://oo.test"}
	rec := env.do(t, "GET", "/api/v1/worklog/logs/7/remark-preview?key=worklog/remark/检修一班/2026.08.18/7-1.docx", nil)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("预览应成功，实际 %d %s", envr.Code, envr.Message)
	}
	url, _ := envr.Data.(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "/office.html?src=worklog-remark&id=7&key=") || !strings.Contains(url, "&back=/worklog.html") {
		t.Fatalf("OO 预览地址口径不符: %q", url)
	}
}

func TestRemarkPreviewViewer(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkFiles(remarkDocJSON)
	rec := env.do(t, "GET", "/api/v1/worklog/logs/7/remark-preview?key=worklog/remark/检修一班/2026.08.18/7-1.docx", nil)
	envr := decodeEnv(t, rec)
	url, _ := envr.Data.(map[string]any)["url"].(string)
	if envr.Code != codeOK || !strings.HasPrefix(url, "https://view.officeapps.live.com/op/view.aspx?src=") {
		t.Fatalf("查看器地址口径不符: %d %q", envr.Code, url)
	}
}

func TestRemarkPreviewOnlyDoc(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().
		withRemarkFiles(`[{"name":"a.jpg","url":"","cos_key":"worklog/a.jpg","type":"image","size":1}]`)
	rec := env.do(t, "GET", "/api/v1/worklog/logs/7/remark-preview?key=worklog/a.jpg", nil)
	if envr := decodeEnv(t, rec); envr.Code != codePreviewOnly {
		t.Fatalf("非 doc 预览应 %d，实际 %d", codePreviewOnly, envr.Code)
	}
}

func TestRemarkDownload(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkFiles(remarkDocJSON)
	env.objects.items["worklog/remark/检修一班/2026.08.18/7-1.docx"] = []byte("DOC-BYTES")
	rec := env.do(t, "GET", "/api/v1/worklog/logs/7/remark-download?key=worklog/remark/检修一班/2026.08.18/7-1.docx", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "DOC-BYTES" {
		t.Fatalf("下载内容不符: %d %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("下载文件名头缺失: %q", cd)
	}
}

// ---- 派车对齐 ----

// alignXLSX 构造派车系统导出表：B=车牌号 F=用车人 G=预计用车时间 K=目的地，M=派车单类型 N=派车单号。
func alignXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	headers := map[string]string{
		"B1": "车牌号", "F1": "用车人", "G1": "预计用车时间", "K1": "目的地",
		"M1": "派车单类型", "N1": "派车单号",
	}
	for cell, v := range headers {
		if err := f.SetCellStr(sheet, cell, v); err != nil {
			t.Fatalf("写表头失败: %v", err)
		}
	}
	for i, row := range rows {
		cells := map[string]string{
			"B": row[0], "F": row[1], "G": row[2], "K": row[3], "M": row[4], "N": row[5],
		}
		for col, v := range cells {
			if err := f.SetCellStr(sheet, col+itoa(int64(i+2)), v); err != nil {
				t.Fatalf("写单元格失败: %v", err)
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("导出 xlsx 失败: %v", err)
	}
	return buf.Bytes()
}

func TestDispatchAlign(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("FROM worklog_member WHERE status", []string{"name"},
		[][]driver.Value{{"赵登"}, {"郑海楠"}})
	env.fake.on("SELECT e.id, DATE_FORMAT(e.log_date", []string{
		"id", "log_date", "vehicle_id", "plate", "dest_id", "dest", "order_no", "patrol"},
		[][]driver.Value{
			{int64(100), "2026-08-18", int64(1), "晋A12345", int64(2), "孝义市", "", ""},
			{int64(101), "2026-08-20", int64(1), "晋E00005", int64(3), "交城县", "PC-009", ""},
		})
	env.fake.on("em.member_id, m.name", []string{"entry_id", "member_id", "name"},
		[][]driver.Value{
			{int64(100), int64(10), "赵登"},
			{int64(101), int64(10), "赵登"},
		})

	xlsx := alignXLSX(t, [][]string{
		{"晋A12345", "赵登 郑海楠", "2026-08-18 09:30", "孝义市", "用车申请调度", "PC-001"},
		{"晋B00001", "赵登", "2026/8/19", "吕梁市交城", "用车申请调度", ""},
		{"晋C00002", "外班甲", "2026-08-18 10:00", "某地", "用车申请调度", ""},
		{"晋D00003", "赵登", "2026-08-18 11:00", "某地", "维保调度", ""},
	})
	body, ct := multipartBody(t, "files", "派车单.xlsx", xlsx, nil)
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/dispatch/align", body, ct)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("对齐应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if envr.Message != "对齐完成：3 条不一致" {
		t.Fatalf("提示口径不符: %q", envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	summary, _ := data["summary"].(map[string]any)
	if summary["imported"] != float64(2) {
		t.Fatalf("导入行数应 2（剔除非本班/维保调度），实际 %v", summary)
	}
	if summary["consistent"] != float64(0) || summary["members"] != float64(1) || summary["orderNo"] != float64(1) {
		t.Fatalf("差异统计不符: %v", summary)
	}
	if summary["sheetOnly"] != float64(1) || summary["entryOnly"] != float64(1) {
		t.Fatalf("单侧统计不符: %v", summary)
	}
	items, _ := data["items"].([]any)
	kinds := map[string]int{}
	for _, it := range items {
		im, _ := it.(map[string]any)
		kinds[im["kind"].(string)]++
		if im["kind"] == "matched" {
			diffs, _ := im["diffs"].(map[string]any)
			missing, _ := diffs["missing"].([]any)
			if len(missing) != 1 || missing[0] != "郑海楠" {
				t.Fatalf("missing 应含郑海楠，实际 %v", diffs)
			}
			if diffs["orderNo"] != true {
				t.Fatalf("表格单号应判不一致，实际 %v", diffs)
			}
		}
	}
	if kinds["matched"] != 1 || kinds["sheetOnly"] != 1 || kinds["entryOnly"] != 1 {
		t.Fatalf("清单构成不符: %v", kinds)
	}
}

func TestDispatchAlignReject(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()

	// .xls 旧格式：诚实报错（不假成功）
	body, ct := multipartBody(t, "files", "旧表.xls", []byte("BIFF"), nil)
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/dispatch/align", body, ct)
	if envr := decodeEnv(t, rec); envr.Code != codeDispatchFile ||
		!strings.Contains(envr.Message, "解析依赖未接入") {
		t.Fatalf(".xls 应报依赖未接入，实际 %d %s", envr.Code, envr.Message)
	}

	// 缺「派车单类型」列
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	_ = f.SetCellStr(sheet, "B1", "车牌号")
	bad, _ := f.WriteToBuffer()
	body, ct = multipartBody(t, "files", "错表.xlsx", bad.Bytes(), nil)
	rec = env.doMultipart(t, "POST", "/api/v1/worklog/dispatch/align", body, ct)
	if envr := decodeEnv(t, rec); envr.Code != codeDispatchFile ||
		!strings.Contains(envr.Message, "缺少必需列") {
		t.Fatalf("缺列应报 40023，实际 %d %s", envr.Code, envr.Message)
	}
}

func TestDestSameFuzzy(t *testing.T) {
	if !destSame("孝义市", "吕梁市孝义") {
		t.Fatal("行政区前缀差异应视为一致")
	}
	if destSame("", "孝义市") {
		t.Fatal("卡片未选目的地应不一致")
	}
	if !destSame("交城县", "") {
		t.Fatal("表格目的地为空不约束")
	}
	if destSame("孝义市", "交城县") {
		t.Fatal("不同目的地应不一致")
	}
}

// ---- 汇总表存网盘 / 预览 ----

func TestSheetSaveNetdisk(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	saver := &memNetdisk{}
	env.module.Netdisk = saver
	env.fake.on("e.dispatch_order_no IS NOT NULL", []string{"order_no", "log_date"},
		[][]driver.Value{{"PC-001", "2026-08-18"}})

	rec := env.do(t, "POST", "/api/v1/worklog/sheet/save-netdisk", map[string]any{
		"sheet": "dispatch", "from": "2026-08-18", "to": "2026-08-18",
	})
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK || envr.Message != "已保存到网盘" {
		t.Fatalf("存网盘应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if len(saver.calls) != 1 {
		t.Fatalf("应调用网盘保存 1 次，实际 %d", len(saver.calls))
	}
	call := saver.calls[0]
	if call.Name != "派车汇总-2026-08-18.xlsx" || call.Dir != "出工日志" || call.Space != "my" {
		t.Fatalf("保存参数不符: %+v", call)
	}
	path, _ := envr.Data.(map[string]any)["path"].(string)
	if !strings.Contains(path, "派车汇总-2026-08-18.xlsx") {
		t.Fatalf("返回路径不符: %q", path)
	}

	// 自定义目录与公共区
	rec = env.do(t, "POST", "/api/v1/worklog/sheet/save-netdisk", map[string]any{
		"sheet": "dispatch", "from": "2026-08-18", "to": "2026-08-20",
		"dir": "出工日志/2026-08", "space": "public",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("公共区保存应成功，实际 %d %s", envr.Code, envr.Message)
	}
	call = saver.calls[1]
	if call.Space != "public" || call.Dir != "出工日志/2026-08" || call.Name != "派车汇总-2026-08-18至2026-08-20.xlsx" {
		t.Fatalf("公共区参数不符: %+v", call)
	}

	// 非法 sheet
	rec = env.do(t, "POST", "/api/v1/worklog/sheet/save-netdisk", map[string]any{
		"sheet": "bad", "from": "2026-08-18", "to": "2026-08-18",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeInput {
		t.Fatalf("非法 sheet 应 %d，实际 %d", codeInput, envr.Code)
	}
}

func TestSheetSaveNetdiskNotConfigured(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("e.dispatch_order_no IS NOT NULL", []string{"order_no", "log_date"},
		[][]driver.Value{{"PC-001", "2026-08-18"}})
	rec := env.do(t, "POST", "/api/v1/worklog/sheet/save-netdisk", map[string]any{
		"sheet": "dispatch", "from": "2026-08-18", "to": "2026-08-18",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeNotConfigured {
		t.Fatalf("未接入应 %d，实际 %d", codeNotConfigured, envr.Code)
	}
}

func TestSheetPreview(t *testing.T) {
	// 预检：无出车记录 → 40402
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("COUNT(*)", []string{"cnt"}, nil)
	rec := env.do(t, "GET", "/api/v1/worklog/dispatch-sheet/preview?from=2026-08-18&to=2026-08-18", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhotos {
		t.Fatalf("预检应 %d，实际 %d", codeNoPhotos, envr.Code)
	}

	// OO 承载页地址
	env2 := newTestServer(t, identAdmin).withTeam()
	env2.fake.on("COUNT(*)", []string{"cnt"}, [][]driver.Value{{int64(1)}})
	env2.module.Cfg = memConfig{"OO_PUBLIC_URL": "https://oo.test"}
	rec = env2.do(t, "GET", "/api/v1/worklog/dispatch-sheet/preview?from=2026-08-18&to=2026-08-19&team_id=1", nil)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("预览应成功，实际 %d %s", envr.Code, envr.Message)
	}
	url, _ := envr.Data.(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "/office.html?src=worklog-sheet&sheet=dispatch&from=2026-08-18&to=2026-08-19") ||
		!strings.Contains(url, "team_id=1") || !strings.Contains(url, "&name=") {
		t.Fatalf("OO 预览地址口径不符: %q", url)
	}

	// 微软查看器分支（文件名缀在下载路径尾）
	env3 := newTestServer(t, identAdmin).withTeam()
	env3.fake.on("COUNT(*)", []string{"cnt"}, [][]driver.Value{{int64(1)}})
	rec = env3.do(t, "GET", "/api/v1/worklog/dispatch-sheet/preview?from=2026-08-18&to=2026-08-18", nil)
	envr = decodeEnv(t, rec)
	url, _ = envr.Data.(map[string]any)["url"].(string)
	if envr.Code != codeOK || !strings.Contains(url, "view.officeapps.live.com") ||
		!strings.Contains(url, "dispatch-sheet") || !strings.Contains(url, ".xlsx%3F") {
		t.Fatalf("查看器地址口径不符: %d %q", envr.Code, url)
	}
}

// ---- 组合 PDF 存网盘 ----

func TestPhotosPDFSaveNetdisk(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	saver := &memNetdisk{}
	env.module.Netdisk = saver
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})

	rec := env.do(t, "POST", "/api/v1/worklog/photos-pdf/save-netdisk", map[string]any{
		"from": "2026-08-18", "to": "2026-08-18",
	})
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK || envr.Message != "已保存到网盘" {
		t.Fatalf("存网盘应成功，实际 %d %s", envr.Code, envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	if data["failed"] != float64(0) {
		t.Fatalf("failed 应 0，实际 %v", data["failed"])
	}
	if len(saver.calls) != 1 || saver.calls[0].Name != "水印照片-2026-08-18.pdf" {
		t.Fatalf("保存参数不符: %+v", saver.calls)
	}
}

func TestPhotosPDFSaveNetdiskEmpty(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.Netdisk = &memNetdisk{}
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"}, nil)
	rec := env.do(t, "POST", "/api/v1/worklog/photos-pdf/save-netdisk", map[string]any{
		"from": "2026-08-19", "to": "2026-08-19",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhotos {
		t.Fatalf("无照片应 %d，实际 %d", codeNoPhotos, envr.Code)
	}
}

// ---- 商旅照片任务化交互 ----

// photoEntryRule 上传/删除入口的卡片查询规则。
func photoEntryRule() fakeRule {
	return fakeRule{match: "FROM worklog_entry e", cols: []string{"team_id", "vehicle_id", "log_date", "dest_name", "team_name"},
		rows: [][]driver.Value{{int64(1), int64(1), "2026-08-18", "孝义市", "检修一班"}}}
}

// photoMemberRule 照片人名校验（本卡用车人）规则。
func photoMemberRule() fakeRule {
	return fakeRule{match: "SELECT m.name FROM worklog_entry_member", cols: []string{"name"},
		rows: [][]driver.Value{{"赵登"}}}
}

func TestPhotoOpTaskFlow(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	remote := &memRemote{}
	ops := &memOps{}
	env.module.SgccRemote = remote
	env.module.PhotoOps = ops
	env.fake.on(photoEntryRule().match, photoEntryRule().cols, photoEntryRule().rows)
	env.fake.on("FROM worklog_photo", []string{"id", "members"}, nil)
	env.fake.on(photoMemberRule().match, photoMemberRule().cols, photoMemberRule().rows)

	rec := env.do(t, "POST", "/api/v1/worklog/logs/1/photos", map[string]any{
		"image":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG(t)),
		"members": []string{"赵登"},
	})
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("上传应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if envr.Message != "已发起上传，商旅同步中" {
		t.Fatalf("任务化提示口径不符: %q", envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	if data["opId"] != float64(42) {
		t.Fatalf("应返回 opId，实际 %v", data)
	}
	if ops.lastKind != "upload" || remote.uploadCnt != 1 {
		t.Fatalf("远端上传未执行: kind=%s cnt=%d", ops.lastKind, remote.uploadCnt)
	}
	// 远端先行成功 → 落库 sgcc_synced=1 且 sgcc_img_id 有留存
	ex, ok := env.fake.lastExec("INSERT INTO worklog_photo")
	if !ok {
		t.Fatalf("应写入 worklog_photo")
	}
	if len(ex.args) < 7 || ex.args[6] != int64(1) {
		t.Fatalf("sgcc_synced 应 1，实际 %v", ex.args)
	}
}

func TestPhotoUploadRemoteFail(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.SgccRemote = &memRemote{uploadErr: errString("登录已过期")}
	env.fake.on(photoEntryRule().match, photoEntryRule().cols, photoEntryRule().rows)
	env.fake.on("FROM worklog_photo", []string{"id", "members"}, nil)
	env.fake.on(photoMemberRule().match, photoMemberRule().cols, photoMemberRule().rows)

	rec := env.do(t, "POST", "/api/v1/worklog/logs/1/photos", map[string]any{
		"image":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG(t)),
		"members": []string{"赵登"},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeUpstream {
		t.Fatalf("远端失败应 %d，实际 %d %s", codeUpstream, envr.Code, envr.Message)
	}
	if env.fake.execCount("INSERT INTO worklog_photo") != 0 {
		t.Fatalf("远端失败不得写库")
	}
}

func TestPhotoMembersBoundGuard(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.SgccRemote = &memRemote{}
	env.fake.on("SELECT p.entry_id, p.members, e.team_id", []string{"entry_id", "members", "team_id", "is_watermark"},
		[][]driver.Value{{int64(1), `["赵登"]`, int64(1), 1}})
	env.fake.on("SELECT m.name FROM worklog_entry_member", []string{"name"},
		[][]driver.Value{{"赵登"}, {"郑海楠"}})
	env.fake.on("a.token_status", []string{"name", "token_status"},
		[][]driver.Value{{"赵登", int64(1)}, {"郑海楠", nil}})

	rec := env.do(t, "PUT", "/api/v1/worklog/photos/9/members", map[string]any{
		"members": []string{"赵登", "郑海楠"},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeBoundMember {
		t.Fatalf("未绑定成员应 %d，实际 %d %s", codeBoundMember, envr.Code, envr.Message)
	}
}

func TestPhotoMembersRemoteFirst(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	remote := &memRemote{}
	env.module.SgccRemote = remote
	env.fake.on("SELECT p.entry_id, p.members, e.team_id", []string{"entry_id", "members", "team_id", "is_watermark"},
		[][]driver.Value{{int64(1), `["赵登","郑海楠"]`, int64(1), 1}})
	env.fake.on("SELECT m.name FROM worklog_entry_member", []string{"name"},
		[][]driver.Value{{"赵登"}, {"郑海楠"}})
	env.fake.on("a.token_status", []string{"name", "token_status"},
		[][]driver.Value{{"赵登", int64(1)}, {"郑海楠", int64(1)}})

	// 剔除 郑海楠：远端剔除 + 全量补传后才改本地
	rec := env.do(t, "PUT", "/api/v1/worklog/photos/9/members", map[string]any{
		"members": []string{"赵登"},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("变更应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if remote.removeCnt != 1 || remote.syncCnt != 1 {
		t.Fatalf("远端剔除/补传应各 1 次，实际 %d/%d", remote.removeCnt, remote.syncCnt)
	}
	if len(remote.lastRemove) != 1 || remote.lastRemove[0] != "郑海楠" {
		t.Fatalf("剔除名单不符: %v", remote.lastRemove)
	}
	if env.fake.execCount("UPDATE worklog_photo SET members") != 1 {
		t.Fatalf("应更新本地 members")
	}
}

func TestPhotoDeleteRemoteFirst(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	remote := &memRemote{}
	ops := &memOps{}
	env.module.SgccRemote = remote
	env.module.PhotoOps = ops
	env.fake.on("SELECT p.cos_key, p.entry_id, e.team_id", []string{"cos_key", "entry_id", "team_id"},
		[][]driver.Value{{"worklog/a/1-1.jpg", int64(1), int64(1)}})
	env.fake.on("FROM worklog_photo", []string{"id", "members"}, nil)

	rec := env.do(t, "DELETE", "/api/v1/worklog/photos/9", nil)
	envr := decodeEnv(t, rec)
	data, _ := envr.Data.(map[string]any)
	if envr.Code != codeOK || data["opId"] != float64(42) {
		t.Fatalf("删除应返回 opId，实际 %d %v", envr.Code, envr.Data)
	}
	if ops.lastKind != "delete" || remote.unlinkCnt != 1 {
		t.Fatalf("远端解除关联未执行: kind=%s cnt=%d", ops.lastKind, remote.unlinkCnt)
	}
	if env.fake.execCount("DELETE FROM worklog_photo") != 1 {
		t.Fatalf("应删除本地照片行")
	}
}

// TestAssetsDir 模板目录解析：运行配置 WORKLOG_ASSETS_DIR 优先 → env 兜底 →
// assetpath 布局探测（容器 assets/worklog 优先、仓根 server/assets/worklog 回退）；不联网。
func TestAssetsDir(t *testing.T) {
	// 运行配置优先
	m := &Module{Cfg: memConfig{"WORKLOG_ASSETS_DIR": "/cfg/worklog"}}
	if got := m.assetsDir(); got != "/cfg/worklog" {
		t.Fatalf("运行配置应优先，got %q", got)
	}

	// env 兜底（配置未给值）
	t.Setenv("WORKLOG_ASSETS_DIR", "/env/worklog")
	m = &Module{Cfg: memConfig{}}
	if got := m.assetsDir(); got != "/env/worklog" {
		t.Fatalf("env 应兜底，got %q", got)
	}

	// 均未给值 → 布局探测（容器布局）
	t.Setenv("WORKLOG_ASSETS_DIR", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets", "worklog"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	m = &Module{}
	if got := m.assetsDir(); got != filepath.Join("assets", "worklog") {
		t.Fatalf("容器布局探测错: %q", got)
	}

	// 仓根布局回退
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "server", "assets", "worklog"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if got := m.assetsDir(); got != filepath.Join("server", "assets", "worklog") {
		t.Fatalf("仓根布局探测错: %q", got)
	}
}
