package sgccclockin

// 费用照片对账与下载入库回归：source 字符串/数值口径兼容、空 URL 不误删、下载传输行为
//（禁重定向/禁默认 UA/内容魔数校验/代理口径）、单张失败计入汇总、本地扫描失败不伪装成无照片、
// 远端部分元素无 id 时整轮 fail-closed 跳过（不下载/不删库/不删 COS）。
// 全部 fake/httptest，不触真实 COS/商旅。

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"shade/internal/integrations"
	"shade/internal/logging"
)

// photoLogs 提取 photo 类型核查流水调用（args = team/member/date/scope/type/result/detail）。
func photoLogs(fdb *fakeDB) []fakeCall {
	out := []fakeCall{}
	for _, c := range fdb.callsWith("INSERT INTO worklog_sync_log") {
		if len(c.args) >= 7 && fmt.Sprint(c.args[4]) == "photo" {
			out = append(out, c)
		}
	}
	return out
}

// photoReconcileReady 对账夹具：成员/用户名/首个出工记录/班组名就绪（拉下方向用）。
func photoReconcileReady(fdb *fakeDB) {
	fdb.onRowsAlways("SELECT name, user_id FROM worklog_member", []string{"name", "user_id"},
		[]driver.Value{"张三", int64(7)})
	fdb.onRowsAlways("SELECT username FROM sys_user", []string{"username"}, []driver.Value{"zhang"})
	fdb.onRowsAlways("ORDER BY e.id LIMIT 1", []string{"id", "destination_name"},
		[]driver.Value{int64(100), "500kV变电站"})
	fdb.onRowsAlways("SELECT name FROM sys_team", []string{"name"}, []driver.Value{"检修一班"})
}

// TestSyncFeePhotosStringSourceCompatibility source 列字符串口径（worklog 新版 schema VARCHAR）与
// 数值口径并存：sgcc/app 字符串行分类正确；已知远端 ID 不重复下载；商旅已删按 source 分别整照删除/摘除。
func TestSyncFeePhotosStringSourceCompatibility(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		if !strings.HasPrefix(url, "https://") {
			return nil, fmt.Errorf("商旅侧未返回有效图片地址")
		}
		return []byte("JPEGDATA"), nil
	}
	photoReconcileReady(fdb)
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(1), "worklog/old-s.jpg", "sgcc", `{"11":"OLD-S"}`},
		[]driver.Value{int64(2), "worklog/old-a.jpg", "app", `{"11":"OLD-A"}`},
		[]driver.Value{int64(3), "worklog/keep.jpg", "sgcc", `{"11":"R1"}`})
	fdb.onRowsAlways("SELECT members, sgcc_img_id, cos_key FROM worklog_photo",
		[]string{"members", "sgcc_img_id", "cos_key"},
		[]driver.Value{`["张三","李四"]`, `{"11":"OLD-A"}`, "worklog/old-a.jpg"})
	fdb.onExec("INSERT INTO worklog_photo", 55)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"R1","url":"https://sgcc.test/1.jpg"},{"fileInfoId":"R2","url":"https://sgcc.test/2.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	// 已知远端 ID（R1 本地已有镜像）不重复下载；仅 R2 入库
	inserts := fdb.callsWith("INSERT INTO worklog_photo")
	if len(inserts) != 1 {
		t.Fatalf("仅未知远端 ID 应入库: %+v", inserts)
	}
	if !strings.Contains(fmt.Sprint(inserts[0].args), `"11":"R2"`) ||
		strings.Contains(fmt.Sprint(inserts[0].args), `"11":"R1"`) {
		t.Fatalf("入库链接不符: %+v", inserts[0].args)
	}
	// sgcc 字符串行按商旅拉取分类 → 远端已删整照删除；app 字符串行按本应用上传分类 → 摘除本成员（李四保留）
	rowDeletes := fdb.callsWith("DELETE FROM worklog_photo WHERE id = ?")
	if len(rowDeletes) != 1 || fmt.Sprint(rowDeletes[0].args) != "[1]" {
		t.Fatalf("sgcc 行应整照删除 id=1: %+v", rowDeletes)
	}
	updates := fdb.callsWith("UPDATE worklog_photo SET members")
	if len(updates) != 1 || !strings.Contains(fmt.Sprint(updates[0].args), "李四") ||
		strings.Contains(fmt.Sprint(updates[0].args), "张三") {
		t.Fatalf("app 行应摘除本成员保留李四: %+v", updates)
	}
	store.mu.Lock()
	deletes := append([]string(nil), store.deletes...)
	store.mu.Unlock()
	if len(deletes) != 1 || deletes[0] != "worklog/old-s.jpg" {
		t.Fatalf("仅整照删除行应删 COS 对象: %v", deletes)
	}
	logs := photoLogs(fdb)
	if len(logs) == 0 || fmt.Sprint(logs[len(logs)-1].args[5]) != "diff" ||
		!strings.Contains(fmt.Sprint(logs[len(logs)-1].args[6]), "拉下 1 张 / 合并 0 张 / 删除 1 张 / 摘除 1 人") {
		t.Fatalf("收尾计数日志不符: %+v", logs)
	}
}

// TestSyncFeePhotosEmptyRemoteURLKeepsLocal 远端元素有相同 fileInfoId 但 url=” 时，
// 本地拉取行与本应用上传行都不得删除（方向二按 ID 存在判断，不按 URL 非空判断）。
func TestSyncFeePhotosEmptyRemoteURLKeepsLocal(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(1), "worklog/pulled.jpg", int64(1), `{"11":"F1"}`},
		[]driver.Value{int64(2), "worklog/uploaded.jpg", int64(0), `{"11":"F1"}`})
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"F1","url":""}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	if n := len(fdb.callsWith("DELETE FROM worklog_photo")); n != 0 {
		t.Fatalf("远端 ID 存在（url 为空）不得删本地照片，实际 DELETE %d 次", n)
	}
	if n := len(fdb.callsWith("SELECT members, sgcc_img_id, cos_key FROM worklog_photo")); n != 0 {
		t.Fatalf("远端 ID 存在不得走摘除路径: %d", n)
	}
	if n := len(fdb.callsWith("INSERT INTO worklog_photo")); n != 0 {
		t.Fatalf("本地已有该 ID 不得重复下载: %d", n)
	}
	store.mu.Lock()
	n := len(store.deletes)
	store.mu.Unlock()
	if n != 0 {
		t.Fatalf("不得删 COS 对象: %d", n)
	}
	logs := photoLogs(fdb)
	if len(logs) == 0 || fmt.Sprint(logs[len(logs)-1].args[5]) != "ok" ||
		!strings.Contains(fmt.Sprint(logs[len(logs)-1].args[6]), "拉下 0 张 / 合并 0 张 / 删除 0 张 / 摘除 0 人") {
		t.Fatalf("收尾应记 ok 且零操作: %+v", logs)
	}
}

// TestFetchSgccImage 商旅图片下载传输行为：302 不跟随、默认 Go UA 不发送、200+HTML/空内容不作为图片、
// JPEG/PNG 魔数可通过、未配置 SGCC_PROXY_URL 不继承环境代理直连、显式 SGCC_PROXY_URL 走原代理。
func TestFetchSgccImage(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("JFIF-sgcc-test")...)
	pngBytes := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("PNG-sgcc-test")...)

	t.Run("302不跟随", func(t *testing.T) {
		var realHits int
		real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			realHits++
			_, _ = w.Write(jpegBytes)
		}))
		defer real.Close()
		redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, real.URL+"/x.jpg", http.StatusFound)
		}))
		defer redir.Close()
		if _, err := m.fetchSgccImage(context.Background(), redir.URL+"/x.jpg"); err == nil ||
			!strings.Contains(err.Error(), "302") {
			t.Fatalf("3xx 应作为错误返回: %v", err)
		}
		if realHits != 0 {
			t.Fatalf("不得跟随重定向: %d", realHits)
		}
	})

	t.Run("默认UA不发送", func(t *testing.T) {
		var gotUA string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			_, _ = w.Write(jpegBytes)
		}))
		defer srv.Close()
		if _, err := m.fetchSgccImage(context.Background(), srv.URL+"/x.jpg"); err != nil {
			t.Fatalf("下载应成功: %v", err)
		}
		if gotUA != "" {
			t.Fatalf("不得发送默认 User-Agent: %q", gotUA)
		}
	})

	t.Run("HTML与空内容拒绝", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body>error page</body></html>"))
		}))
		defer srv.Close()
		if _, err := m.fetchSgccImage(context.Background(), srv.URL+"/x.jpg"); err == nil ||
			!strings.Contains(err.Error(), "内容校验失败") {
			t.Fatalf("HTML 应答应拒绝: %v", err)
		}
		empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer empty.Close()
		if _, err := m.fetchSgccImage(context.Background(), empty.URL+"/x.jpg"); err == nil {
			t.Fatalf("空应答应拒绝: %v", err)
		}
	})

	t.Run("JPEG与PNG魔数通过", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/p.png" {
				_, _ = w.Write(pngBytes)
				return
			}
			_, _ = w.Write(jpegBytes)
		}))
		defer srv.Close()
		buf, err := m.fetchSgccImage(context.Background(), srv.URL+"/x.jpg")
		if err != nil || string(buf) != string(jpegBytes) {
			t.Fatalf("JPEG 魔数应通过: %v", err)
		}
		buf, err = m.fetchSgccImage(context.Background(), srv.URL+"/p.png")
		if err != nil || string(buf) != string(pngBytes) {
			t.Fatalf("PNG 魔数应通过: %v", err)
		}
	})

	t.Run("未配置代理直连", func(t *testing.T) {
		m.Cfg = nil
		var proxyHits, proxyResolveCalls int
		envProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyHits++
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("unexpected environment proxy"))
		}))
		defer envProxy.Close()
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(jpegBytes)
		}))
		defer target.Close()
		oldTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = oldTransport })
		targetAddr := strings.TrimPrefix(target.URL, "http://")
		proxyAddr := strings.TrimPrefix(envProxy.URL, "http://")
		// 注：Go 对 loopback 天然绕开环境代理且 ProxyFromEnvironment 有进程级缓存，本地行为测试
		// 测不出差异；故注入带探测计数器的 Proxy 函数——旧实现（默认传输层）会经它解析代理，
		// 修复后（directTransport，Proxy=nil）不得触碰它。
		http.DefaultTransport = &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				proxyResolveCalls++
				return url.Parse(envProxy.URL)
			},
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				if addr == proxyAddr {
					return net.Dial(network, proxyAddr)
				}
				return net.Dial(network, targetAddr)
			},
		}
		// 直连传输层现按模块缓存；本用例替换全局 default 后须丢弃旧缓存，
		// 才能验证新直连实例不继承环境代理（生产不会运行期替换 DefaultTransport）。
		m.photoTransports = transportCache{}
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
			t.Setenv(key, envProxy.URL)
		}
		if _, err := m.fetchSgccImage(context.Background(), "http://sgcc-direct.test/x.jpg"); err != nil {
			t.Fatalf("未配置 SGCC_PROXY_URL 应直连: %v", err)
		}
		if proxyResolveCalls != 0 || proxyHits != 0 {
			t.Fatalf("环境代理不得被继承（resolve=%d hits=%d）", proxyResolveCalls, proxyHits)
		}
	})

	t.Run("显式代理走原代理", func(t *testing.T) {
		var proxyHits int
		proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proxyHits++
			_, _ = w.Write(jpegBytes)
		}))
		defer proxySrv.Close()
		var originHits int
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originHits++
			_, _ = w.Write(jpegBytes)
		}))
		defer origin.Close()
		cfg := newMapCfg()
		cfg.set(CfgProxyURL, proxySrv.URL)
		m.Cfg = cfg
		defer func() { m.Cfg = nil }()
		buf, err := m.fetchSgccImage(context.Background(), origin.URL+"/x.jpg")
		if err != nil || string(buf) != string(jpegBytes) {
			t.Fatalf("经代理下载应成功: %v", err)
		}
		if proxyHits != 1 || originHits != 0 {
			t.Fatalf("显式 SGCC_PROXY_URL 应走代理（proxy=%d origin=%d）", proxyHits, originHits)
		}
	})
}

// TestSyncFeePhotosPullFailCounted 单张下载失败计入照片汇总：收尾日志必须记 fail 并展示失败张数，
// 成功张数不得把失败事实覆盖成 ok/diff。
func TestSyncFeePhotosPullFailCounted(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "bad") {
			return nil, fmt.Errorf("照片下载失败：网络不通")
		}
		return []byte("JPEGDATA"), nil
	}
	photoReconcileReady(fdb)
	fdb.onExec("INSERT INTO worklog_photo", 55)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"R1","url":"https://sgcc.test/1.jpg"},{"fileInfoId":"R2","url":"https://sgcc.test/bad.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	inserts := fdb.callsWith("INSERT INTO worklog_photo")
	if len(inserts) != 1 || !strings.Contains(fmt.Sprint(inserts[0].args), `"11":"R1"`) {
		t.Fatalf("成功一张应入库 R1: %+v", inserts)
	}
	logs := photoLogs(fdb)
	if len(logs) == 0 {
		t.Fatalf("应记核查流水")
	}
	last := logs[len(logs)-1]
	if fmt.Sprint(last.args[5]) != "fail" {
		t.Fatalf("有单张失败时收尾必须记 fail: %+v", logs)
	}
	if !strings.Contains(fmt.Sprint(last.args[6]), "拉下 1 张") ||
		!strings.Contains(fmt.Sprint(last.args[6]), "失败 1 张") {
		t.Fatalf("收尾应展示失败张数: %+v", last.args)
	}
	foundPer := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "商旅图片 R2 入库失败") {
			foundPer = true
		}
	}
	if !foundPer {
		t.Fatalf("单张失败应记核查流水: %+v", logs)
	}
}

// TestSyncFeePhotosLocalQueryErrorSkips 本地照片查询失败不得伪装成「本地无照片」继续拉下（重复入库），
// 应记 fail 并跳过对账。
func TestSyncFeePhotosLocalQueryErrorSkips(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		return []byte("JPEGDATA"), nil
	}
	fdb.onErr("FROM worklog_photo p JOIN worklog_entry e", fmt.Errorf("Error 1146: Table doesn't exist"))
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"R1","url":"https://sgcc.test/1.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	if n := len(fdb.callsWith("INSERT INTO worklog_photo")); n != 0 {
		t.Fatalf("查询失败不得继续拉下入库: %d", n)
	}
	logs := photoLogs(fdb)
	found := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "本地照片扫描失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("查询失败应记 fail 诊断: %+v", logs)
	}
}

// TestSyncFeePhotosPartialInvalidRemoteSkips 远端部分元素无图片 id（键名漂移/非对象元素）时，
// 整轮对账必须 fail-closed 跳过：不得下载、不得删本地照片、不得删 COS 对象，并记带样例键的 fail 诊断。
// 旧实现只跳过无 id 的元素，会把该元素对应本地照片当「商旅已删」误删。
func TestSyncFeePhotosPartialInvalidRemoteSkips(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	fetchHits := 0
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		fetchHits++
		return []byte("JPEGDATA"), nil
	}
	photoReconcileReady(fdb)
	// 本地：R1 已有镜像（有效），R3 对应远端无 id 异常项（旧实现会误删）
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(1), "worklog/r1.jpg", "sgcc", `{"11":"R1"}`},
		[]driver.Value{int64(2), "worklog/r3.jpg", "app", `{"11":"R3"}`})
	// R3 行为 app 上传且仅本成员，旧实现摘除后为空 → 整照删除 + 删 COS 对象
	fdb.onRowsAlways("SELECT members, sgcc_img_id, cos_key FROM worklog_photo",
		[]string{"members", "sgcc_img_id", "cos_key"},
		[]driver.Value{`["张三"]`, `{"11":"R3"}`, "worklog/r3.jpg"})
	fdb.onExec("INSERT INTO worklog_photo", 55)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	// 第 3 个元素键名漂移（fileId 不兼容）→ ParseFeeImageItem 返回 nil
	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"R1","url":"https://sgcc.test/1.jpg"},{"fileInfoId":"R2","url":"https://sgcc.test/2.jpg"},{"fileId":"R3","url":"https://sgcc.test/3.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	// 先断言误删（核心缺陷）：异常项对应本地照片不得被当成「商旅已删」
	if n := len(fdb.callsWith("DELETE FROM worklog_photo")); n != 0 {
		t.Fatalf("含无 id 异常项时不得删除本地照片，实际 DELETE %d 次", n)
	}
	if n := len(fdb.callsWith("UPDATE worklog_photo SET members")); n != 0 {
		t.Fatalf("含无 id 异常项时不得摘除成员，实际 UPDATE %d 次", n)
	}
	store.mu.Lock()
	deletes := append([]string(nil), store.deletes...)
	store.mu.Unlock()
	if len(deletes) != 0 {
		t.Fatalf("含无 id 异常项时不得删除 COS 对象: %v", deletes)
	}
	if fetchHits != 0 {
		t.Fatalf("含无 id 异常项时不得下载，实际下载 %d 次", fetchHits)
	}
	if n := len(fdb.callsWith("INSERT INTO worklog_photo")); n != 0 {
		t.Fatalf("含无 id 异常项时不得入库，实际 INSERT %d 次", n)
	}
	logs := photoLogs(fdb)
	if len(logs) == 0 {
		t.Fatalf("应记 fail 核查流水")
	}
	last := logs[len(logs)-1]
	if fmt.Sprint(last.args[5]) != "fail" {
		t.Fatalf("跳过整轮对账必须记 fail: %+v", logs)
	}
	if detail := fmt.Sprint(last.args[6]); !strings.Contains(detail, "无可识别图片 id") ||
		!strings.Contains(detail, "fileId/url") {
		t.Fatalf("fail 诊断应含无 id 提示与样例键: %q", detail)
	}
}

// sgccJPEG 合法 JPEG 魔数（http.DetectContentType 识别为 image/jpeg）。
var sgccJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

// TestFetchSgccImageLogsRedactedURLBeforeRealDownload 真实下载前先记 sgcc.photo.download.start：
// 完整长 path 不截断，fileInfoId 保留，Signature/token/userinfo 脱敏，禁止出现秘密值。
func TestFetchSgccImageLogsRedactedURLBeforeRealDownload(t *testing.T) {
	fl := &fakeAppLogger{}
	m := New(nil)
	m.Log = fl
	longPath := "/very/long/" + strings.Repeat("segment/", 40) + "photo.jpg"
	var sawStartBeforeDownload atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, e := range fl.snapshot() {
			if e.event == "sgcc.photo.download.start" {
				sawStartBeforeDownload.Store(true)
			}
		}
		_, _ = w.Write(sgccJPEG)
	}))
	defer srv.Close()

	rawURL := strings.Replace(srv.URL, "http://", "http://bob:p%40ss@", 1) +
		longPath + "?fileInfoId=abc123&Signature=SECRETSIG&token=SECRETTOK"
	ctx := withPhotoDownloadInfo(context.Background(), photoDownloadInfo{MemberID: 11, TeamID: 22, ImgID: "img-9"})
	buf, err := m.fetchSgccImage(ctx, rawURL)
	if err != nil {
		t.Fatalf("fetchSgccImage: %v", err)
	}
	if len(buf) != len(sgccJPEG) {
		t.Fatalf("图片长度 = %d", len(buf))
	}
	if !sawStartBeforeDownload.Load() {
		t.Fatal("下载前未记录 sgcc.photo.download.start")
	}
	entries := fl.snapshot()
	var start *fakeAppLogEntry
	for i := range entries {
		if entries[i].event == "sgcc.photo.download.start" {
			start = &entries[i]
			break
		}
	}
	if start == nil {
		t.Fatalf("缺 sgcc.photo.download.start：%+v", entries)
	}
	if start.level != "info" || start.category != "business" {
		t.Fatalf("事件级别/类别不符：%+v", start)
	}
	safe, _ := start.fields["url"].(string)
	for _, want := range []string{longPath, "fileInfoId=abc123", "Signature=****", "token=****"} {
		if !strings.Contains(safe, want) {
			t.Fatalf("url 缺 %q：%s", want, safe)
		}
	}
	for _, secret := range []string{"SECRETSIG", "SECRETTOK", "bob", "p%40ss"} {
		if strings.Contains(safe, secret) || strings.Contains(start.raw, secret) {
			t.Fatalf("秘密 %q 泄漏：url=%s raw=%s", secret, safe, start.raw)
		}
	}
	if start.fields["member_id"] != int64(11) || start.fields["team_id"] != int64(22) || start.fields["img_id"] != "img-9" {
		t.Fatalf("归属字段不符：%+v", start.fields)
	}
}

// TestFetchSgccImageInjectedStillLogsStart 注入 FetchSgccImage 时也必须在 fake 调用前记录同一事件；
// 缺上下文时字段仍存在（零值），不能省略。
func TestFetchSgccImageInjectedStillLogsStart(t *testing.T) {
	fl := &fakeAppLogger{}
	m := New(nil)
	m.Log = fl
	seen := false
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		for _, e := range fl.snapshot() {
			if e.event == "sgcc.photo.download.start" {
				seen = true
			}
		}
		return sgccJPEG, nil
	}
	if _, err := m.fetchSgccImage(context.Background(), "https://sgcc.test/a.jpg?Signature=S&fileInfoId=F1"); err != nil {
		t.Fatalf("fetchSgccImage: %v", err)
	}
	if !seen {
		t.Fatal("注入下载前未记录事件")
	}
	entries := fl.snapshot()
	start := entries[len(entries)-1]
	for _, key := range []string{"url", "member_id", "team_id", "img_id"} {
		if _, ok := start.fields[key]; !ok {
			t.Fatalf("缺字段 %s：%+v", key, start.fields)
		}
	}
	if start.fields["member_id"] != int64(0) || start.fields["team_id"] != int64(0) || start.fields["img_id"] != "" {
		t.Fatalf("缺上下文时零值不符：%+v", start.fields)
	}
}

// TestPullFeePhotoCarriesDownloadContext 对账调用链（pullFeePhoto → fetchSgccImage）必须补齐
// member/team/img 上下文，并在 fake 下载前产生 start 事件。
func TestPullFeePhotoCarriesDownloadContext(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	fl := &fakeAppLogger{}
	m := New(db)
	m.Log = fl
	m.Store = &fakeStore{}
	fdb.onExec("INSERT INTO worklog_photo", 55)
	longURL := "https://sgcc.test/very/long/" + strings.Repeat("p/", 30) + "x.jpg?fileInfoId=F9&Signature=SIG9"
	var ctxOK, eventBefore atomic.Bool
	m.FetchSgccImage = func(ctx context.Context, url string) ([]byte, error) {
		info, ok := photoDownloadFromContext(ctx)
		ctxOK.Store(ok && info.MemberID == 11 && info.TeamID == 22 && info.ImgID == "img-9")
		for _, e := range fl.snapshot() {
			if e.event == "sgcc.photo.download.start" {
				eventBefore.Store(true)
			}
		}
		return sgccJPEG, nil
	}
	a := pullArgs{
		teamID: 22, date: "2026-10-28", memberID: 11, memberKey: "11", memberName: "张三",
		username: "zhang", entryID: 100, destName: "500kV变电站", teamName: "检修一班",
		prefix: "worklog/", imgID: "img-9", imgURL: longURL,
	}
	var pulled, merged int
	if err := m.pullFeePhoto(context.Background(), a, &pulled, &merged, func(string, string, string) {}); err != nil {
		t.Fatalf("pullFeePhoto: %v", err)
	}
	if !ctxOK.Load() {
		t.Fatal("调用链未补齐 member/team/img 上下文")
	}
	if !eventBefore.Load() {
		t.Fatal("fake 下载前未记录 start 事件")
	}
	entries := fl.snapshot()
	start := entries[0]
	if start.fields["member_id"] != int64(11) || start.fields["team_id"] != int64(22) || start.fields["img_id"] != "img-9" {
		t.Fatalf("事件归属不符：%+v", start.fields)
	}
	if safe, _ := start.fields["url"].(string); !strings.Contains(safe, "fileInfoId=F9") || strings.Contains(safe, "SIG9") {
		t.Fatalf("URL 脱敏不符：%s", safe)
	}
}

// 商旅下载失败：返回错误与用户可见同步流水都必须先脱敏（短 URL 秘密测试）。
func TestPullFeePhotoFailureSanitizesURL(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.Log = &fakeAppLogger{}
	secretURL := "https://bob:p%40ss@example.com/x?fileInfoId=F1&Signature=SIG1"
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		return nil, fmt.Errorf("照片下载失败：Get %q: 403", url)
	}
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	a := pullArgs{
		teamID: 22, date: "2026-10-28", memberID: 11, memberKey: "11", memberName: "张三",
		username: "zhang", entryID: 100, teamName: "检修一班", prefix: "worklog/",
		imgID: "F1", imgURL: secretURL,
	}
	var pulled, merged int
	err := m.pullFeePhoto(context.Background(), a, &pulled, &merged, func(typ, result, detail string) {
		m.AddSyncLog(context.Background(), 22, 11, "2026-10-28", "daily", typ, result, detail)
	})
	if err == nil {
		t.Fatal("期望下载失败")
	}
	for _, secret := range []string{"bob", "p%40ss", "SIG1"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("返回错误泄漏 %q：%s", secret, err.Error())
		}
	}
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	if len(logs) == 0 {
		t.Fatal("缺同步流水")
	}
	detail := fmt.Sprint(logs[len(logs)-1].args[6])
	for _, secret := range []string{"bob", "p%40ss", "SIG1"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("用户可见流水泄漏 %q：%s", secret, detail)
		}
	}
	if !strings.Contains(detail, "fileInfoId=F1") {
		t.Fatalf("非敏感 fileInfoId 被误伤：%s", detail)
	}
}

func TestAddSyncLogSanitizesDetail(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	secretURL := "https://bob:p%40ss@example.com/x?fileInfoId=F2&Signature=SIG2"
	m.AddSyncLog(context.Background(), 1, 2, "2026-10-28", "daily", "photo", "fail", "失败 "+secretURL)
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	if len(logs) != 1 {
		t.Fatalf("流水 = %d", len(logs))
	}
	detail := fmt.Sprint(logs[0].args[6])
	for _, secret := range []string{"bob", "p%40ss", "SIG2"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("流水泄漏 %q：%s", secret, detail)
		}
	}
	if !strings.Contains(detail, "fileInfoId=F2") {
		t.Fatalf("fileInfoId 被误伤：%s", detail)
	}
}

func TestSyncPullMemberLogSanitizes(t *testing.T) {
	fl := &fakeAppLogger{}
	m := New(nil)
	m.Log = fl
	secretURL := "https://bob:p%40ss@example.com/x?Signature=SIG3"
	m.syncPullMemberLog(context.Background(), 1, "member", "张三", "2026-10-28", 11, "失败 "+secretURL)
	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("事件 = %d", len(entries))
	}
	text := entries[0].messageCN + entries[0].raw + fmt.Sprint(entries[0].fields)
	for _, secret := range []string{"bob", "p%40ss", "SIG3"} {
		if strings.Contains(text, secret) {
			t.Fatalf("应用日志泄漏 %q：%s", secret, text)
		}
	}
}

// traceRecorder 捕获出站观测事件及 ctx trace。
type traceRecorder struct {
	mu     sync.Mutex
	events []traceEvent
}

type traceEvent struct {
	level, category, event, messageCN, raw string
	fields                                 map[string]any
	traceID                                string
}

func (r *traceRecorder) Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, traceEvent{level, category, event, messageCN, raw, fields, logging.TraceIDFromContext(ctx)})
	return nil
}

func (r *traceRecorder) find(event string) (traceEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.event == event {
			return e, true
		}
	}
	return traceEvent{}, false
}

// TestFetchSgccImageSuccessSharesTrace 成功下载：start 与 http.outbound 同一 trace，后者含 status/duration。
func TestFetchSgccImageSuccessSharesTrace(t *testing.T) {
	fl := &fakeAppLogger{}
	m := New(nil)
	m.Log = fl
	rec := &traceRecorder{}
	integrations.SetObservedRecorder(rec)
	t.Cleanup(func() { integrations.SetObservedRecorder(nil) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sgccJPEG)
	}))
	defer srv.Close()

	if _, err := m.fetchSgccImage(context.Background(), srv.URL+"/x.jpg?Signature=SIG&fileInfoId=F1"); err != nil {
		t.Fatalf("fetchSgccImage: %v", err)
	}
	entries := fl.snapshot()
	var start *fakeAppLogEntry
	for i := range entries {
		if entries[i].event == "sgcc.photo.download.start" {
			start = &entries[i]
		}
	}
	if start == nil || start.traceID == "" {
		t.Fatalf("start 事件缺 trace：%+v", entries)
	}
	out, ok := rec.find("http.outbound")
	if !ok {
		t.Fatalf("缺成功出站事件：%+v", rec.events)
	}
	if out.traceID != start.traceID {
		t.Fatalf("start/observer trace 不一致：%q vs %q", start.traceID, out.traceID)
	}
	if out.fields["status"] != 200 {
		t.Fatalf("observer status = %v", out.fields["status"])
	}
	if _, ok := out.fields["duration_ms"].(float64); !ok {
		t.Fatalf("observer 缺 duration_ms：%v", out.fields)
	}
}

func TestSyncPullEndLogSanitizes(t *testing.T) {
	fl := &fakeAppLogger{}
	m := New(nil)
	m.Log = fl
	secretURL := "https://bob:p%40ss@example.com/x?Signature=SIG4"
	m.syncPullEndLog(context.Background(), 1, "member", "scope", 1, 1, 1, "中断 "+secretURL)
	entries := fl.snapshot()
	if len(entries) != 1 {
		t.Fatalf("事件 = %d", len(entries))
	}
	text := entries[0].messageCN + entries[0].raw + fmt.Sprint(entries[0].fields)
	for _, secret := range []string{"bob", "p%40ss", "SIG4"} {
		if strings.Contains(text, secret) {
			t.Fatalf("收尾日志泄漏 %q：%s", secret, text)
		}
	}
}
