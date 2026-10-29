package kvm

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// fakeAuth 把固定身份注入 httpapi.AuthMiddleware 的上下文（与生产同一条装配路径）。
type fakeAuth struct {
	ident auth.Identity
}

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: f.ident}, nil
}

func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) {
	return f.ident, nil
}

func (f fakeAuth) Logout(context.Context, string, time.Time) error { return nil }

type testEnv struct {
	handler  http.Handler
	fake     *fakeDB
	module   *Module
	platform *fakePlatform
	locker   *MemoryLocker
	netdisk  *fakeNetdisk
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	platform := &fakePlatform{
		devices: []Device{
			{ID: 11, Ddns: "dev-a", DeviceGroupName: "检修一班", Raw: map[string]any{"id": float64(11), "ddns": "dev-a", "deviceGroupName": "检修一班", "status": "online"}},
			{ID: 12, Ddns: "dev-b", DeviceGroupName: "检修二班", Raw: map[string]any{"id": float64(12), "ddns": "dev-b", "deviceGroupName": "检修二班", "status": "offline"}},
		},
	}
	locker := NewMemoryLocker()
	m := New(db, platform)
	m.Permit = fakePermit{allow: map[string]bool{"kvm": true, "file-transfer": true, "netdisk": true}}
	m.Locker = locker
	m.Netdisk = &fakeNetdisk{files: map[string][]byte{"/docs/a.txt": []byte("hello-netdisk")}}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler:  httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:     fake,
		module:   m,
		platform: platform,
		locker:   locker,
		netdisk:  m.Netdisk.(*fakeNetdisk),
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) (int, map[string]any) {
	t.Helper()
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	data := map[string]any{}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		_ = json.Unmarshal(env.Data, &data)
	}
	return env.Code, data
}

var (
	identAdmin = auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1}
	identUser  = auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1}
)

func teamRows() [][]driver.Value { return [][]driver.Value{{int64(1), "检修一班"}} }

func teamRule() fakeRule {
	return fakeRule{match: "FROM sys_team", cols: []string{"id", "name"}, rows: teamRows()}
}

// TestDeviceList 班组过滤 + 维护锁标注。
func TestDeviceList(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	_ = env.locker.Lock(context.Background(), "dev-a", "派车单同步中", 7200)

	rec := env.do(t, http.MethodGet, "/api/v1/kvm/devices", nil)
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("设备列表应成功: %s", rec.Body.String())
	}
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("班组过滤后应 1 台，实际 %v", data)
	}
	item := items[0].(map[string]any)
	if item["ddns"] != "dev-a" || item["maintaining"] != true {
		t.Fatalf("设备字段不符: %v", item)
	}
	if data["team"] != "检修一班" || data["fallback"] != false {
		t.Fatalf("班组信息不符: %v", data)
	}
}

// TestDeviceListFallback 本班组 0 台时回退默认班组设备并带 fallback 标记。
func TestDeviceListFallback(t *testing.T) {
	env := newTestServer(t, identAdmin)
	// 生效班组 = 检修二班（超管 ?team_id=2），本班组只有 dev-b；再构造 0 台场景：
	// 让 dev-b 组名不是检修二班 → 回退默认班组 检修一班 的 dev-a
	env.platform.devices = []Device{
		{ID: 11, Ddns: "dev-a", DeviceGroupName: "检修一班", Raw: map[string]any{"ddns": "dev-a"}},
	}
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(2), "检修二班"}})
	env.fake.on("status = 1 ORDER BY sort, id LIMIT 1", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})

	rec := env.do(t, http.MethodGet, "/api/v1/kvm/devices?team_id=2", nil)
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("设备列表应成功: %s", rec.Body.String())
	}
	items, _ := data["items"].([]any)
	if len(items) != 1 || data["fallback"] != true || data["fallback_team"] != "检修一班" {
		t.Fatalf("回退口径不符: %v", data)
	}
}

// TestJump 签发跳转地址：参数校验、白名单、维护锁 423。
func TestJump(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)

	rec := env.do(t, http.MethodPost, "/api/v1/kvm/jump", map[string]any{"to": "bad", "ddns": "dev-a"})
	if code, _ := decodeData(t, rec); code != codeParam {
		t.Fatalf("非法 to 应 %d: %s", codeParam, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/v1/kvm/jump", map[string]any{"to": "term", "ddns": "../etc"})
	if code, _ := decodeData(t, rec); code != codeParam {
		t.Fatalf("非法 ddns 应 %d: %s", codeParam, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/v1/kvm/jump", map[string]any{"to": "term", "ddns": "dev-a"})
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("签发应成功: %s", rec.Body.String())
	}
	u, _ := data["url"].(string)
	if !strings.HasPrefix(u, "https://kvm.example.invalid/jump.html#sid=sid-token&to=") ||
		!strings.Contains(u, url.QueryEscape("/#/rtty/dev-a")) {
		t.Fatalf("跳转地址不符: %s", u)
	}

	// 维护锁命中 42301
	_ = env.locker.Lock(context.Background(), "dev-a", "每日同步", 7200)
	rec = env.do(t, http.MethodPost, "/api/v1/kvm/jump", map[string]any{"to": "desk", "ddns": "dev-a"})
	if rec.Code != http.StatusLocked {
		t.Fatalf("锁定应 423: %s", rec.Body.String())
	}
	if code, _ := decodeData(t, rec); code != codeLocked {
		t.Fatalf("锁定业务码应 %d: %s", codeLocked, rec.Body.String())
	}
}

// TestCrossTeamDevice 跨班组设备直访拒绝（404 + 可读文案）。
func TestCrossTeamDevice(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	rec := env.do(t, http.MethodGet, "/api/v1/kvm/devices/12/files", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨班组应 404: %s", rec.Body.String())
	}
	var envBody struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envBody); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envBody.Code != codePlatform || envBody.Message != "设备不存在，或不属于当前班组" {
		t.Fatalf("跨班组文案不符: %s", rec.Body.String())
	}
}

// fileshareServer 设备 fileshare 桩（挂载/列表/删除/下载/推送）。
func fileshareServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	calls := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Cookie") != "rtty-http-sid=rtty-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"会话失效"}`))
			return
		}
		switch {
		case r.URL.Path == "/api/fileshare/status":
			_, _ = w.Write([]byte(`{"shared":false}`))
		case r.URL.Path == "/api/fileshare/list":
			_, _ = w.Write([]byte(`{"files":["a.txt"]}`))
		case r.URL.Path == "/api/fileshare/mount":
			_, _ = w.Write([]byte(`{"mounted":true}`))
		case r.URL.Path == "/api/fileshare/delete":
			var body struct {
				Names []string `json:"names"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if len(body.Names) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"names 为空"}`))
				return
			}
			_, _ = w.Write([]byte(`{"deleted":[` + strings.Join(quoteAll(body.Names), ",") + `]}`))
		case strings.HasPrefix(r.URL.Path, "/api/fileshare/download/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/fileshare/download/")
			if name == "missing.bin" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"文件不存在"}`))
				return
			}
			if name == "big.bin" {
				// 大文件（1MB）：断言两跳保存走流式且 size 透传
				w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
				w.Header().Set("Content-Length", strconv.Itoa(1<<20))
				_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="a.txt"`)
			_, _ = w.Write([]byte("device-file-content"))
		case r.URL.Path == "/api/fileshare/push":
			_, fh, err := r.FormFile("files")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"缺少 files"}`))
				return
			}
			_ = fh
			_, _ = w.Write([]byte(`{"pushed":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, calls
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		b, _ := json.Marshal(n)
		out[i] = string(b)
	}
	return out
}

// withProxy 把桩平台指向设备 fileshare 服务器。
func (e *testEnv) withProxy(origin string) {
	e.platform.proxy = ProxySession{Origin: origin, Cookie: "rtty-http-sid=rtty-1"}
}

// TestFileRelay 文件转发点：status/list/mount/delete/download/push 全链（httptest 设备桩）。
// JSON 响应须为统一信封 {code:0,data}（对齐 Node ok(res, r.data)），data 为设备本体。
func TestFileRelay(t *testing.T) {
	srv, calls := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	// status
	rec := env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status HTTP 应 200: %d %s", rec.Code, rec.Body.String())
	}
	code, data := decodeData(t, rec)
	if code != 0 || data["shared"] != false {
		t.Fatalf("status 信封不符: %s", rec.Body.String())
	}
	// list
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/files", nil)
	code, data = decodeData(t, rec)
	if code != 0 {
		t.Fatalf("list 应成功: %s", rec.Body.String())
	}
	if files, _ := data["files"].([]any); len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("list data 不符: %v", data)
	}
	// mount
	rec = env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/mount", nil)
	code, data = decodeData(t, rec)
	if code != 0 || data["mounted"] != true {
		t.Fatalf("mount 信封不符: %s", rec.Body.String())
	}
	// delete
	rec = env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/delete", map[string]any{"names": []string{"a.txt"}})
	code, data = decodeData(t, rec)
	if code != 0 {
		t.Fatalf("delete 应成功: %s", rec.Body.String())
	}
	if got, _ := data["deleted"].([]any); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("delete data 不符: %v", data)
	}
	// download
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/download?name=a.txt", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "device-file-content" {
		t.Fatalf("download 不符: %d %s", rec.Code, rec.Body.String())
	}
	// download 缺 name
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/download", nil)
	if code, _ := decodeData(t, rec); code != codeParam {
		t.Fatalf("缺 name 应 %d: %s", codeParam, rec.Body.String())
	}

	// push（multipart）
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("files", "报告.txt")
	_, _ = part.Write([]byte("file-bytes"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kvm/devices/11/push", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	code, data = decodeData(t, rec)
	if code != 0 || data["pushed"] != float64(1) {
		t.Fatalf("push 信封不符: %d %s", rec.Code, rec.Body.String())
	}
	if len(*calls) < 6 {
		t.Fatalf("设备端调用不足: %v", *calls)
	}
}

// TestFileRelayErrorEnvelope 设备侧失败走统一错误信封（{code,message} 可读文案，HTTP 跟设备状态）。
func TestFileRelayErrorEnvelope(t *testing.T) {
	srv, _ := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	// 设备 404 + {"error":...}：下载缺失文件 → 可读文案透传
	rec := env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/download?name=missing.bin", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("设备 404 应原样 404: %d %s", rec.Code, rec.Body.String())
	}
	var envBody struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envBody); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envBody.Code != codePlatform || envBody.Message != "文件不存在" {
		t.Fatalf("错误信封不符: %s", rec.Body.String())
	}

	// 设备 401：映射 502 + 会话失效文案（防把上游 401 原样回给前端触发登出）
	env.platform.proxy = ProxySession{Origin: srv.URL, Cookie: "rtty-http-sid=bad"}
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices/11/status", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("设备 401 应映射 502: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envBody); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envBody.Code != codePlatform || envBody.Message != "会话失效" {
		t.Fatalf("401 映射信封不符: %s", rec.Body.String())
	}
}

// TestPullFromNetdisk 网盘 → 设备：路径穿越拒绝 + 逐条成败汇总。
func TestPullFromNetdisk(t *testing.T) {
	srv, _ := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	rec := env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/pull-from-netdisk", map[string]any{
		"space": "my",
		"paths": []string{"/docs/a.txt", "/docs/missing.txt", "/../etc/passwd"},
	})
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("部分失败应 200+汇总: %s", rec.Body.String())
	}
	items, _ := data["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("应 3 条结果: %v", data)
	}
	// 第一条成功；第二条网盘缺失失败；第三条路径不合法
	if items[0].(map[string]any)["ok"] != true {
		t.Fatalf("第一条应成功: %v", items[0])
	}
	if items[1].(map[string]any)["ok"] != false {
		t.Fatalf("第二条应失败: %v", items[1])
	}
	if items[2].(map[string]any)["error"] != "路径不合法" {
		t.Fatalf("第三条应路径不合法: %v", items[2])
	}

	// 全部失败 502
	rec = env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/pull-from-netdisk", map[string]any{
		"paths": []string{"/docs/missing.txt"},
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("全部失败应 502: %s", rec.Body.String())
	}
}

// TestSaveToNetdisk 设备 → 网盘保存（流式：内容与 size 透传断言）。
func TestSaveToNetdisk(t *testing.T) {
	srv, _ := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	rec := env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/save-to-netdisk", map[string]any{"name": "a.txt"})
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("保存应成功: %s", rec.Body.String())
	}
	path, _ := data["path"].(string)
	if path != "my/文件传输/a.txt" {
		t.Fatalf("保存路径不符: %v", data)
	}
	if len(env.netdisk.saved) != 1 {
		t.Fatalf("网盘保存未调用: %v", env.netdisk.saved)
	}
	if env.netdisk.savedLen != len("device-file-content") || env.netdisk.savedSize != int64(len("device-file-content")) {
		t.Fatalf("流式保存内容/长度不符: len=%d size=%d", env.netdisk.savedLen, env.netdisk.savedSize)
	}
}

// TestSaveToNetdiskLargeStream 大文件两跳保存：1MB 响应体流式入网盘，size 按 Content-Length 透传。
func TestSaveToNetdiskLargeStream(t *testing.T) {
	srv, _ := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	rec := env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/save-to-netdisk", map[string]any{"name": "big.bin"})
	if code, _ := decodeData(t, rec); code != 0 {
		t.Fatalf("大文件保存应成功: %s", rec.Body.String())
	}
	if env.netdisk.savedLen != 1<<20 || env.netdisk.savedSize != 1<<20 {
		t.Fatalf("大文件流式保存不符: len=%d size=%d", env.netdisk.savedLen, env.netdisk.savedSize)
	}
}

// TestPushLimit 超 20 个文件拒绝（Node multer LIMIT_FILE_COUNT 同文案）。
func TestPushLimit(t *testing.T) {
	srv, _ := fileshareServer(t)
	defer srv.Close()
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i := 0; i < maxPushFiles+1; i++ {
		part, _ := mw.CreateFormFile("files", "f"+strconv.Itoa(i)+".txt")
		_, _ = part.Write([]byte("x"))
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kvm/devices/11/push", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if code, _ := decodeData(t, rec); code != codeParam {
		t.Fatalf("超量应 %d: %s", codeParam, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "文件数量超出限制") {
		t.Fatalf("超量文案不符: %s", rec.Body.String())
	}
}

// TestPermission 仅 kvm 可 jump；devices 需 kvm 或 file-transfer 任一。
func TestPermission(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.module.Permit = fakePermit{allow: map[string]bool{"file-transfer": true}}
	rec := env.do(t, http.MethodPost, "/api/v1/kvm/jump", map[string]any{"to": "term", "ddns": "dev-a"})
	if code, _ := decodeData(t, rec); code != codeNoApp {
		t.Fatalf("jump 应仅 kvm 权限: %s", rec.Body.String())
	}
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices", nil)
	if code, _ := decodeData(t, rec); code != 0 {
		t.Fatalf("file-transfer 也应放行设备列表: %s", rec.Body.String())
	}
	env.module.Permit = fakePermit{allow: map[string]bool{}}
	rec = env.do(t, http.MethodGet, "/api/v1/kvm/devices", nil)
	if code, _ := decodeData(t, rec); code != codeNoApp {
		t.Fatalf("无权限应 40301: %s", rec.Body.String())
	}
}

// TestGLKVMClient 登录/会话缓存/AUTH 失效重登（httptest 平台桩）。
func TestGLKVMClient(t *testing.T) {
	logins := 0
	deviceCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			logins++
			_, _ = w.Write([]byte(`{"ok":true,"data":{"token":"tok-` + strings.Repeat("x", 0) + `"}}`))
		case "/api/devices":
			deviceCalls++
			authz := r.Header.Get("Authorization")
			if authz == "Bearer stale" {
				_, _ = w.Write([]byte(`{"ok":false,"code":"AUTH_EXPIRED","message":"会话过期"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[{"id":1,"ddns":"d1","deviceGroupName":"检修一班"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewGLKVMClient(srv.URL, "pw")
	c.HTTP = srv.Client()
	items, err := c.ListDevices(context.Background(), url.Values{"q": []string{"关键字"}, "status": []string{"online"}}, "emp")
	if err != nil || len(items) != 1 || items[0].Ddns != "d1" {
		t.Fatalf("设备列表不符: %+v err=%v", items, err)
	}
	// 会话缓存：第二次不再登录
	_, _ = c.ListDevices(context.Background(), url.Values{}, "emp")
	if logins != 1 {
		t.Fatalf("会话应缓存，登录次数 %d", logins)
	}

	// AUTH_EXPIRED → 清缓存重登重试
	c.mu.Lock()
	c.sessions["emp"] = cachedSession{token: "stale", expiresAt: time.Now().Add(time.Hour)}
	c.mu.Unlock()
	items, err = c.ListDevices(context.Background(), url.Values{}, "emp")
	if err != nil || len(items) != 1 {
		t.Fatalf("失效重登后应取到设备: %+v err=%v", items, err)
	}
	if logins != 2 {
		t.Fatalf("失效应重登一次，登录次数 %d", logins)
	}
	if deviceCalls < 3 {
		t.Fatalf("设备调用次数不符: %d", deviceCalls)
	}
}

// TestGLKVMProxySession 平台 /web/ 302 换 rttysid（Location 解析）。
func TestGLKVMProxySession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			_, _ = w.Write([]byte(`{"ok":true,"data":{"token":"tok"}}`))
		case "/api/devices":
			_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[{"id":7,"ddns":"dev-x","deviceGroupName":"G"}]}}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/web/") {
				if r.Header.Get("Cookie") != "sid=tok" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Location", "https://dev-x.kvm.example.invalid/hook?rttysid=RS-1&x=1")
				w.WriteHeader(http.StatusFound)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewGLKVMClient(srv.URL, "pw")
	c.HTTP = srv.Client()
	ps, err := c.ProxySession(context.Background(), "7", "emp")
	if err != nil {
		t.Fatalf("代理会话获取失败: %v", err)
	}
	if ps.Origin != "https://dev-x.kvm.example.invalid" || ps.Cookie != "rtty-http-sid=RS-1" {
		t.Fatalf("代理会话不符: %+v", ps)
	}
}

// TestNormRel 路径穿越防护。
func TestNormRel(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"/", "/", true},
		{"docs/a.txt", "/docs/a.txt", true},
		{"\\docs\\b.txt", "/docs/b.txt", true},
		{"//docs//c.txt", "/docs/c.txt", true},
		{"/../secret", "", false},
		{"/a/./b", "", false},
		{"/ok/" + strings.Repeat("x", 400), "", false},
	}
	for _, c := range cases {
		got, ok := normRel(c.in)
		if got != c.want || ok != c.valid {
			t.Fatalf("normRel(%q) = (%q,%v)，应 (%q,%v)", c.in, got, ok, c.want, c.valid)
		}
	}
	name, ok := sanitizeName("../evil.txt")
	if !ok || strings.ContainsAny(name, "/\\") {
		t.Fatalf("文件名净化失败: %q", name)
	}
}

// glkvmStub 平台桩：登录记录所用密码并签发序号 token，设备接口校验 token 前缀。
type glkvmStub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	password []string // 各次登录使用的密码
}

func newGlkvmStub(t *testing.T) *glkvmStub {
	t.Helper()
	s := &glkvmStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			var body struct {
				Password string `json:"password"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.password = append(s.password, body.Password)
			n := len(s.password)
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"data":{"token":"tok-` + strconv.Itoa(n) + `"}}`))
		case "/api/devices":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
				_, _ = w.Write([]byte(`{"ok":false,"code":"AUTH_REQUIRED","message":"未登录"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[{"id":1,"ddns":"d1","deviceGroupName":"G"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *glkvmStub) logins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.password...)
}

// TestGLKVMRuntimeConfig 动态运行时配置：URL/密码逐请求取自配置源；
// 配置变更自动清员工会话缓存重新登录；InvalidateSessions 显式清缓存同效。
func TestGLKVMRuntimeConfig(t *testing.T) {
	stub := newGlkvmStub(t)
	defer stub.srv.Close()
	cfg := &fakeConfig{vals: map[string]string{
		"GLKVM_URL":      stub.srv.URL,
		"GLKVM_PASSWORD": "pw1",
	}}
	c := NewGLKVMClient("", "") // 固定值留空，完全走运行时配置
	c.HTTP = stub.srv.Client()
	c.Cfg = cfg

	if _, err := c.ListDevices(context.Background(), url.Values{}, "emp"); err != nil {
		t.Fatalf("设备列表失败: %v", err)
	}
	if got := stub.logins(); len(got) != 1 || got[0] != "pw1" {
		t.Fatalf("首次登录应 pw1: %v", got)
	}
	// 会话缓存：不重复登录
	_, _ = c.ListDevices(context.Background(), url.Values{}, "emp")
	if got := stub.logins(); len(got) != 1 {
		t.Fatalf("会话应缓存: %v", got)
	}
	if c.BaseURL() != stub.srv.URL {
		t.Fatalf("BaseURL 应回显生效配置: %s", c.BaseURL())
	}

	// 配置变更（密码）→ 自动清员工会话缓存并以新密码重登
	cfg.set("GLKVM_PASSWORD", "pw2")
	if _, err := c.ListDevices(context.Background(), url.Values{}, "emp"); err != nil {
		t.Fatalf("改密后列表失败: %v", err)
	}
	if got := stub.logins(); len(got) != 2 || got[1] != "pw2" {
		t.Fatalf("改密后应以 pw2 重登: %v", got)
	}

	// 配置变更（URL）→ 切换平台基址且会话作废
	stub2 := newGlkvmStub(t)
	defer stub2.srv.Close()
	cfg.set("GLKVM_URL", stub2.srv.URL)
	if _, err := c.ListDevices(context.Background(), url.Values{}, "emp"); err != nil {
		t.Fatalf("改址后列表失败: %v", err)
	}
	if got := stub2.logins(); len(got) != 1 || got[0] != "pw2" {
		t.Fatalf("改址后应登录新平台: %v", got)
	}
	if c.BaseURL() != stub2.srv.URL {
		t.Fatalf("BaseURL 应跟随新址: %s", c.BaseURL())
	}

	// InvalidateSessions 显式清缓存（配置中心 OnChange 装配用）
	c.InvalidateSessions()
	if _, err := c.ListDevices(context.Background(), url.Values{}, "emp"); err != nil {
		t.Fatalf("清缓存后列表失败: %v", err)
	}
	if got := stub2.logins(); len(got) != 2 {
		t.Fatalf("清缓存后应重新登录: %v", got)
	}
}

// TestGLKVMUnconfigured 未配置 GLKVM_URL/GLKVM_PASSWORD 报可读文案。
func TestGLKVMUnconfigured(t *testing.T) {
	c := NewGLKVMClient("", "")
	c.Cfg = &fakeConfig{vals: map[string]string{}}
	_, err := c.ListDevices(context.Background(), url.Values{}, "emp")
	if err == nil || !strings.Contains(err.Error(), "GLKVM Cloud 未配置") {
		t.Fatalf("未配置应可读文案: %v", err)
	}
}
