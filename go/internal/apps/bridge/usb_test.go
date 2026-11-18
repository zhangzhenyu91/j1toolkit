package bridge

// UsbLink / GlkvmFileshare / GlkvmFinder 测试：设备定位（ddns+MAC）与 fileshare 转发（httptest）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"shade/internal/apps/kvm"
)

// fakeDevice UsbDevice 桩。
type fakeDevice struct {
	mounted  int
	pushed   map[string][]byte
	deleted  [][]string
	files    map[string][]byte
	listResp []string
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{pushed: map[string][]byte{}, files: map[string][]byte{}}
}

func (f *fakeDevice) MountToHost(context.Context, DeviceRef, string) error { f.mounted++; return nil }

func (f *fakeDevice) DownloadFile(_ context.Context, _ DeviceRef, _, name string) ([]byte, error) {
	b, ok := f.files[name]
	if !ok {
		return nil, nil
	}
	return b, nil
}

func (f *fakeDevice) ListFiles(context.Context, DeviceRef, string) ([]string, error) {
	return f.listResp, nil
}

func (f *fakeDevice) DeleteFiles(_ context.Context, _ DeviceRef, _ string, names []string) error {
	f.deleted = append(f.deleted, names)
	return nil
}

func (f *fakeDevice) PushFile(_ context.Context, _ DeviceRef, _, name string, data []byte) error {
	f.pushed[name] = data
	return nil
}

var _ DeviceClient = (*fakeDevice)(nil)

// fakeFinder 设备定位桩。
type fakeFinder struct {
	ref DeviceRef
	err error
	got struct{ ddns, mac, user string }
}

func (f *fakeFinder) FindByDdns(_ context.Context, username, ddns, mac string) (DeviceRef, error) {
	f.got.ddns, f.got.mac, f.got.user = ddns, mac, username
	return f.ref, f.err
}

var _ DeviceFinder = (*fakeFinder)(nil)

func TestUsbLinkResolvesAndOperates(t *testing.T) {
	dev := newFakeDevice()
	dev.files["a.txt"] = []byte("hi")
	dev.listResp = []string{"a.txt"}
	find := &fakeFinder{ref: DeviceRef{ID: "11", Ddns: "dev-1"}}
	cfg := fakeCfg{
		"TREEHAZARD_SYNC_DEVICE_DDNS": "dev-1",
		"TREEHAZARD_SYNC_DEVICE_MAC":  "AA:BB",
		"TREEHAZARD_SYNC_KVM_USER":    "ops",
	}
	u := &UsbLink{Dev: dev, Find: find, Cfg: cfg,
		DdnsKey: "TREEHAZARD_SYNC_DEVICE_DDNS", MacKey: "TREEHAZARD_SYNC_DEVICE_MAC", UserKey: "TREEHAZARD_SYNC_KVM_USER"}
	ctx := context.Background()
	if err := u.MountToHost(ctx); err != nil {
		t.Fatalf("MountToHost: %v", err)
	}
	if dev.mounted != 1 {
		t.Fatalf("mounted = %d", dev.mounted)
	}
	if find.got.ddns != "dev-1" || find.got.user != "ops" {
		t.Fatalf("resolve got = %+v", find.got)
	}
	buf, err := u.DownloadFile(ctx, "a.txt")
	if err != nil || string(buf) != "hi" {
		t.Fatalf("download = %q %v", buf, err)
	}
	// 文件不存在 → (nil, nil)。
	buf, err = u.DownloadFile(ctx, "missing")
	if err != nil || buf != nil {
		t.Fatalf("missing = %v %v", buf, err)
	}
	names, err := u.ListFiles(ctx)
	if err != nil || len(names) != 1 || names[0] != "a.txt" {
		t.Fatalf("list = %v %v", names, err)
	}
	if err := u.PushFile(ctx, "b.txt", []byte("x")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := u.DeleteFiles(ctx, []string{"a.txt"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestUsbLinkNotConfigured(t *testing.T) {
	u := &UsbLink{Dev: newFakeDevice(), Find: &fakeFinder{}, Cfg: fakeCfg{},
		DdnsKey: "TREEHAZARD_SYNC_DEVICE_DDNS"}
	if err := u.MountToHost(context.Background()); err != ErrUsbNotConfigured {
		t.Fatalf("err = %v", err)
	}
	// 回退 ddns（派车单同机）后可用。
	u.FallbackDdns = "dev-2"
	find := &fakeFinder{ref: DeviceRef{ID: "22"}}
	u.Find = find
	if err := u.MountToHost(context.Background()); err != nil {
		t.Fatalf("fallback mount: %v", err)
	}
	if find.got.ddns != "dev-2" {
		t.Fatalf("ddns = %q", find.got.ddns)
	}
}

// fakePlatform kvm.Platform 桩（Finder / Fileshare 共用）。
type fakePlatform struct {
	devices []kvm.Device
	ps      kvm.ProxySession
}

func (p *fakePlatform) BaseURL() string { return "https://glkvm.example" }

func (p *fakePlatform) ListDevices(context.Context, url.Values, string) ([]kvm.Device, error) {
	return p.devices, nil
}

func (p *fakePlatform) SessionToken(context.Context, string) (string, error) { return "tok", nil }

func (p *fakePlatform) ProxySession(context.Context, string, string) (kvm.ProxySession, error) {
	return p.ps, nil
}

var _ kvm.Platform = (*fakePlatform)(nil)

func TestGlkvmFinderMacCheck(t *testing.T) {
	p := &fakePlatform{devices: []kvm.Device{
		{ID: 5, Ddns: "dev-1", Raw: map[string]any{"mac": "AA-BB-CC"}},
	}}
	g := &GlkvmFinder{Platform: p}
	ctx := context.Background()
	ref, err := g.FindByDdns(ctx, "ops", "dev-1", "aabbcc")
	if err != nil || ref.ID != "5" {
		t.Fatalf("find = %+v %v", ref, err)
	}
	// MAC 不符 → 中止防误操作。
	if _, err := g.FindByDdns(ctx, "ops", "dev-1", "ffff"); err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("err = %v", err)
	}
	// 未找到设备。
	if _, err := g.FindByDdns(ctx, "ops", "nope", ""); err == nil || !strings.Contains(err.Error(), "未找到设备") {
		t.Fatalf("err = %v", err)
	}
}

func TestGlkvmFileshareHTTP(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Cookie") == "" {
			t.Error("missing Cookie header")
		}
		switch {
		case r.URL.Path == "/api/fileshare/mount":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "shared": true})
		case r.URL.Path == "/api/fileshare/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files": []map[string]any{{"name": "a.txt", "size": 3}}})
		case r.URL.Path == "/api/fileshare/download/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":"文件不存在"}`))
		case strings.HasPrefix(r.URL.Path, "/api/fileshare/download/"):
			_, _ = w.Write([]byte("DATA"))
		case r.URL.Path == "/api/fileshare/delete":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.URL.Path == "/api/fileshare/push":
			// 镜像设备 fileshare.py：解析 multipart 后回 {ok,saved:[{name,size}],shared}。
			file, fh, err := r.FormFile("files")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"error":"未收到文件"}`))
				return
			}
			buf, _ := io.ReadAll(file)
			_ = file.Close()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "shared": false,
				"saved": []map[string]any{{"name": fh.Filename, "size": len(buf)}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &fakePlatform{ps: kvm.ProxySession{Origin: srv.URL, Cookie: "rtty-http-sid=1"}}
	f := &GlkvmFileshare{Platform: p}
	dev := DeviceRef{ID: "9", Ddns: "d"}
	ctx := context.Background()

	if err := f.MountToHost(ctx, dev, "ops"); err != nil {
		t.Fatalf("mount: %v", err)
	}
	names, err := f.ListFiles(ctx, dev, "ops")
	if err != nil || len(names) != 1 || names[0] != "a.txt" {
		t.Fatalf("list = %v %v", names, err)
	}
	buf, err := f.DownloadFile(ctx, dev, "ops", "a.txt")
	if err != nil || string(buf) != "DATA" {
		t.Fatalf("download = %q %v", buf, err)
	}
	buf, err = f.DownloadFile(ctx, dev, "ops", "missing")
	if err != nil || buf != nil {
		t.Fatalf("404 → nil,nil, got %v %v", buf, err)
	}
	if err := f.DeleteFiles(ctx, dev, "ops", []string{"a.txt"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.PushFile(ctx, dev, "ops", "b.txt", []byte("x")); err != nil {
		t.Fatalf("push: %v", err)
	}
	// 响应体应按 body 读完（无泄漏）——检查转发路径齐备。
	want := map[string]bool{
		"POST /api/fileshare/mount": true, "GET /api/fileshare/list": true,
		"GET /api/fileshare/download/a.txt": true, "GET /api/fileshare/download/missing": true,
		"POST /api/fileshare/delete": true, "POST /api/fileshare/push": true,
	}
	for _, pth := range paths {
		delete(want, pth)
	}
	if len(want) != 0 {
		t.Fatalf("missing calls: %v (got %v)", want, paths)
	}
}

// 设备调用失败重试（Node deviceCall 口径：共试 Attempts 次，每次换新代理会话；404 不重试）。
func TestGlkvmFileshareRetry(t *testing.T) {
	var mountHits, dlHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/fileshare/mount":
			mountHits++
			if mountHits < 3 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"ok":false,"error":"转发抖动"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "shared": true})
		case strings.HasPrefix(r.URL.Path, "/api/fileshare/download/"):
			dlHits++
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &fakePlatform{ps: kvm.ProxySession{Origin: srv.URL, Cookie: "rtty-http-sid=1"}}
	f := &GlkvmFileshare{Platform: p, Attempts: 3, RetryGap: 0}
	dev := DeviceRef{ID: "9", Ddns: "d"}
	ctx := context.Background()

	if err := f.MountToHost(ctx, dev, "ops"); err != nil {
		t.Fatalf("mount should retry and succeed: %v", err)
	}
	if mountHits != 3 {
		t.Fatalf("mountHits = %d, want 3", mountHits)
	}
	// 404 = 文件不存在：接受不重试。
	if buf, err := f.DownloadFile(ctx, dev, "ops", "none.xlsx"); err != nil || buf != nil {
		t.Fatalf("download 404 = %v %v", buf, err)
	}
	if dlHits != 1 {
		t.Fatalf("dlHits = %d, want 1（404 不重试）", dlHits)
	}
}

// fakeFileshare 设备桩：信封镜像 server/public/kvm-device/fileshare.py
// （push 回 {ok,saved:[{name,size}],shared}；download 回设备侧落盘字节）。
type fakeFileshare struct {
	mu      sync.Mutex
	files   map[string][]byte
	paths   []string
	reads   []string
	deleted []string
	// saved 覆盖 push 响应的 saved 列表（nil = 按真实落盘字节回）。
	saved []map[string]any
	// dropSaved true 时 push 响应不带 saved 字段。
	dropSaved bool
	// dlBody 非 nil 时 download 回该内容（模拟落盘/回读不一致）。
	dlBody []byte
	// dl404 true 时 download 一律 404（模拟设备侧丢文件）。
	dl404 bool
	// delErr 非空时 delete 回 500（模拟清理失败）。
	delErr string
}

func newFakeFileshare() *fakeFileshare {
	return &fakeFileshare{files: map[string][]byte{}}
}

func (d *fakeFileshare) downloads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reads...)
}

func (d *fakeFileshare) hits() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.paths...)
}

func (d *fakeFileshare) deletions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.deleted...)
}

func (d *fakeFileshare) hasFile(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.files[name]
	return ok
}

// newFakeFileshareTarget 起 httptest 设备并返回 fileshare 客户端。
func newFakeFileshareTarget(t *testing.T, d *fakeFileshare) *GlkvmFileshare {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.paths = append(d.paths, r.Method+" "+r.URL.Path)
		d.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/fileshare/push":
			file, fh, err := r.FormFile("files")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"error":"未收到文件"}`))
				return
			}
			buf, _ := io.ReadAll(file)
			_ = file.Close()
			d.mu.Lock()
			d.files[fh.Filename] = buf
			saved, drop := d.saved, d.dropSaved
			d.mu.Unlock()
			resp := map[string]any{"ok": true, "shared": false}
			if !drop {
				if saved == nil {
					saved = []map[string]any{{"name": fh.Filename, "size": len(buf)}}
				}
				resp["saved"] = saved
			}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.HasPrefix(r.URL.Path, "/api/fileshare/download/"):
			name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/fileshare/download/"))
			d.mu.Lock()
			d.reads = append(d.reads, name)
			body, ok := d.files[name]
			over, missing := d.dlBody, d.dl404
			d.mu.Unlock()
			if over != nil {
				_, _ = w.Write(over)
				return
			}
			if !ok || missing {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"ok":false,"error":"文件不存在"}`))
				return
			}
			_, _ = w.Write(body)
		case r.Method == http.MethodPost && r.URL.Path == "/api/fileshare/delete":
			var body struct {
				Names []string `json:"names"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.mu.Lock()
			d.deleted = append(d.deleted, body.Names...)
			delErr := d.delErr
			if delErr == "" {
				for _, n := range body.Names {
					delete(d.files, n)
				}
			}
			d.mu.Unlock()
			if delErr != "" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"ok":false,"error":"` + delErr + `"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": body.Names})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	p := &fakePlatform{ps: kvm.ProxySession{Origin: srv.URL, Cookie: "rtty-http-sid=1"}}
	return &GlkvmFileshare{Platform: p}
}

// 合法 cmd：上传后按 saved 校验，并立即回读、逐字节比对。
func TestPushFileCmdSuccessVerifiesReadback(t *testing.T) {
	d := newFakeFileshare()
	f := newFakeFileshareTarget(t, d)
	dev := DeviceRef{ID: "9"}
	name := "daily_sync_cmd.json"
	payload := []byte(`{"app":"daily_sync","cycle":"c1","action":"export"}`)

	if err := f.PushFile(context.Background(), dev, "ops", name, payload); err != nil {
		t.Fatalf("push = %v", err)
	}
	if got := d.downloads(); len(got) != 1 || got[0] != name {
		t.Fatalf("downloads = %v, want 回读一次 %s", got, name)
	}
	if got := d.deletions(); len(got) != 0 {
		t.Fatalf("deletions = %v, want 成功路径不删文件", got)
	}
}

// saved.size 与 payload 长度不符 → 报错、不进入回读，并清理疑似坏文件。
func TestPushFileRejectsSavedSizeMismatch(t *testing.T) {
	d := newFakeFileshare()
	payload := []byte(`{"a":1}`)
	name := "daily_sync_cmd.json"
	d.saved = []map[string]any{{"name": name, "size": len(payload) - 1}}
	f := newFakeFileshareTarget(t, d)

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", name, payload)
	if err == nil || !strings.Contains(err.Error(), "大小") {
		t.Fatalf("err = %v, want 大小不符", err)
	}
	if got := d.downloads(); len(got) != 0 {
		t.Fatalf("downloads = %v, want 不回读", got)
	}
	assertCleanedUp(t, d, name)
}

// saved 中无同名文件（设备另存/改名）→ 报错，并清理疑似坏文件。
func TestPushFileRejectsMissingSavedName(t *testing.T) {
	d := newFakeFileshare()
	d.saved = []map[string]any{{"name": "other_cmd.json", "size": 2}}
	f := newFakeFileshareTarget(t, d)

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", "daily_sync_cmd.json", []byte("{}"))
	if err == nil || !strings.Contains(err.Error(), "daily_sync_cmd.json") {
		t.Fatalf("err = %v, want 未保存该文件", err)
	}
	if got := d.downloads(); len(got) != 0 {
		t.Fatalf("downloads = %v, want 不回读", got)
	}
	assertCleanedUp(t, d, "daily_sync_cmd.json")
}

// 回读字节与上传内容不一致 → 报错（不得进入后续 Mount），并清理疑似坏文件。
func TestPushFileRejectsReadbackMismatch(t *testing.T) {
	d := newFakeFileshare()
	d.dlBody = []byte(`{"a":2}`)
	f := newFakeFileshareTarget(t, d)
	payload := []byte(`{"a":1}`)
	name := "daily_sync_cmd.json"

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", name, payload)
	if err == nil || !strings.Contains(err.Error(), "回读") {
		t.Fatalf("err = %v, want 回读不一致", err)
	}
	assertCleanedUp(t, d, name)
}

// 回读时设备侧不存在刚上传的文件 → 报错并清理同名残留。
func TestPushFileRejectsReadbackMissing(t *testing.T) {
	d := newFakeFileshare()
	d.dl404 = true
	f := newFakeFileshareTarget(t, d)
	name := "daily_sync_cmd.json"

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", name, []byte(`{"a":1}`))
	if err == nil || !strings.Contains(err.Error(), "回读") {
		t.Fatalf("err = %v, want 回读失败", err)
	}
	assertCleanedUp(t, d, name)
}

// 清理失败不得掩盖原校验错误（原错误 + 清理附注）。
func TestPushFileCleanupFailureKeepsOriginalError(t *testing.T) {
	d := newFakeFileshare()
	d.delErr = "删除失败"
	d.saved = []map[string]any{{"name": "daily_sync_cmd.json", "size": 1}}
	f := newFakeFileshareTarget(t, d)

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", "daily_sync_cmd.json", []byte(`{"a":1}`))
	if err == nil {
		t.Fatal("err = nil, want 校验失败报错")
	}
	if !strings.Contains(err.Error(), "大小") {
		t.Fatalf("err = %v, want 保留原错误（大小不符）", err)
	}
	if !strings.Contains(err.Error(), "清理") {
		t.Fatalf("err = %v, want 附注清理失败", err)
	}
}

// assertCleanedUp 校验失败后已 best-effort 删除同名文件。
func assertCleanedUp(t *testing.T, d *fakeFileshare, name string) {
	t.Helper()
	if got := d.deletions(); len(got) != 1 || got[0] != name {
		t.Fatalf("deletions = %v, want 清理 %s", got, name)
	}
	if d.hasFile(name) {
		t.Fatalf("设备侧仍残留 %s，want 已删除", name)
	}
}

// 非法 JSON 的 *_cmd.json：上传前即拒绝，设备零请求、不动旧文件。
func TestPushFileRejectsInvalidCmdJSON(t *testing.T) {
	d := newFakeFileshare()
	f := newFakeFileshareTarget(t, d)

	err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", "daily_sync_cmd.json", []byte(`{"a":`))
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("err = %v, want JSON 非法", err)
	}
	if got := d.hits(); len(got) != 0 {
		t.Fatalf("paths = %v, want 零请求", got)
	}
	if got := d.deletions(); len(got) != 0 {
		t.Fatalf("deletions = %v, want 不删旧文件", got)
	}
}

// 合法 JSON 但顶层非对象（null/数组/字符串/数字）：上传前拒绝，设备零请求。
func TestPushFileRejectsNonObjectCmdJSON(t *testing.T) {
	cases := map[string]string{
		"null": `null`,
		"数组":   `[1,2]`,
		"字符串":  `"x"`,
		"数字":   `1`,
	}
	for label, payload := range cases {
		t.Run(label, func(t *testing.T) {
			d := newFakeFileshare()
			f := newFakeFileshareTarget(t, d)

			err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", "daily_sync_cmd.json", []byte(payload))
			if err == nil || !strings.Contains(err.Error(), "对象") {
				t.Fatalf("err = %v, want 顶层非对象拒绝", err)
			}
			if got := d.hits(); len(got) != 0 {
				t.Fatalf("paths = %v, want 零请求", got)
			}
		})
	}
}

// 非 cmd 文件：只校验 saved，不回读。
func TestPushFileNonCmdSkipsReadback(t *testing.T) {
	d := newFakeFileshare()
	f := newFakeFileshareTarget(t, d)

	if err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", "export.xlsx", []byte("XLSX")); err != nil {
		t.Fatalf("push = %v", err)
	}
	if got := d.downloads(); len(got) != 0 {
		t.Fatalf("downloads = %v, want 不回读", got)
	}
	if hits := d.hits(); len(hits) != 1 || hits[0] != "POST /api/fileshare/push" {
		t.Fatalf("paths = %v, want 仅一次 push", hits)
	}
	if got := d.deletions(); len(got) != 0 {
		t.Fatalf("deletions = %v, want 非 cmd 不删文件", got)
	}
}

// 设备响应缺 saved 字段 → 视为不可信，报错并清理。
func TestPushFileRejectsResponseWithoutSaved(t *testing.T) {
	d := newFakeFileshare()
	d.dropSaved = true
	f := newFakeFileshareTarget(t, d)
	name := "export.xlsx"

	if err := f.PushFile(context.Background(), DeviceRef{ID: "9"}, "ops", name, []byte("X")); err == nil {
		t.Fatal("err = nil, want 缺少 saved 报错")
	}
	assertCleanedUp(t, d, name)
}
