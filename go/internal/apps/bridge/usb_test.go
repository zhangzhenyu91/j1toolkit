package bridge

// UsbLink / GlkvmFileshare / GlkvmFinder 测试：设备定位（ddns+MAC）与 fileshare 转发（httptest）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
			_ = r.ParseMultipartForm(1 << 20)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
