package netdisk

// 公开分享提取码限流：verify / list / download 共用同一失败计数（IP+share 与 share 全域同时生效），
// 伪造 X-Forwarded-For 轮换 IP 不能绕过全域额度；三个入口返回码与文案一致。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const shareTestID = "abcd1234ef"

// resetShareFailures 清空全局失败计数（包级状态，避免用例互相污染）。
func resetShareFailures() {
	shareLimitMu.Lock()
	shareIPFails = map[string]pwRec{}
	shareAllFails = map[string]pwRec{}
	shareLimitMu.Unlock()
}

// shareDo 发公开分享请求（remote 非回环时 XFF 不被信任；回环时按 XFF 首项计 IP）。
func shareDo(t *testing.T, h http.Handler, method, path, body, remote, xff string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertShareFail 断言公开分享失败信封。
func assertShareFail(t *testing.T, rec *httptest.ResponseRecorder, status, code int, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("HTTP = %d want %d body=%s", rec.Code, status, rec.Body)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析信封: %v body=%s", err, rec.Body)
	}
	if env.Code != code || env.Message != message {
		t.Fatalf("信封 = %d %q want %d %q", env.Code, env.Message, code, message)
	}
}

func shareVerifyReq(t *testing.T, h http.Handler, password, remote, xff string) *httptest.ResponseRecorder {
	t.Helper()
	return shareDo(t, h, http.MethodPost,
		"/api/v1/netdisk/public/share/"+shareTestID+"/verify",
		`{"password":"`+password+`"}`, remote, xff)
}

func shareListReq(t *testing.T, h http.Handler, password, remote, xff string) *httptest.ResponseRecorder {
	t.Helper()
	return shareDo(t, h, http.MethodPost,
		"/api/v1/netdisk/public/share/"+shareTestID+"/list",
		`{"password":"`+password+`","path":"/docs"}`, remote, xff)
}

func shareDownloadReq(t *testing.T, h http.Handler, password, remote, xff string) *httptest.ResponseRecorder {
	t.Helper()
	return shareDo(t, h, http.MethodGet,
		"/api/v1/netdisk/public/share/"+shareTestID+"/download?path=/file.txt&password="+password,
		"", remote, xff)
}

// TestSharePasswordFailureSharedAcrossEndpoints 三个入口共用失败计数：10 次错误口令分散在
// verify/list/download 也累计到同一 IP+share 额度，随后任何入口都 429（返回码/文案一致）。
func TestSharePasswordFailureSharedAcrossEndpoints(t *testing.T) {
	resetShareFailures()
	t.Cleanup(resetShareFailures)
	srv, _ := fakeOpenList(t)
	_, h := newShareTestModule(t, srv.URL)
	ip := "203.0.113.9:1234"

	bad := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		assertShareFail(t, rec, http.StatusForbidden, codeBadTicket, "提取码错误")
	}
	for i := 0; i < 3; i++ {
		bad(shareVerifyReq(t, h, "0000", ip, ""))
	}
	for i := 0; i < 3; i++ {
		bad(shareListReq(t, h, "0000", ip, ""))
	}
	for i := 0; i < 3; i++ {
		bad(shareDownloadReq(t, h, "0000", ip, ""))
	}
	// 第 10 次失败仍按口令错误应答；之后三个入口一律 429。
	bad(shareVerifyReq(t, h, "0000", ip, ""))
	for _, rec := range []*httptest.ResponseRecorder{
		shareVerifyReq(t, h, "1234", ip, ""),
		shareListReq(t, h, "0000", ip, ""),
		shareDownloadReq(t, h, "0000", ip, ""),
	} {
		assertShareFail(t, rec, http.StatusTooManyRequests, codeRateLimited, "尝试次数过多，请 10 分钟后再试")
	}
}

// TestSharePasswordGlobalLimitResistsForgedXFF 伪造 X-Forwarded-For 轮换 IP 不能绕过分享全局限额：
// 同一分享 100 次错误口令后，全新 IP 的 verify / list / download 均 429。
func TestSharePasswordGlobalLimitResistsForgedXFF(t *testing.T) {
	resetShareFailures()
	t.Cleanup(resetShareFailures)
	srv, _ := fakeOpenList(t)
	_, h := newShareTestModule(t, srv.URL)
	// 回环来源 + 每请求伪造不同 XFF 首项：按伪造 IP 计数不会触发单 IP 上限，但全域应兜住。
	for i := 0; i < sharePWMaxPerShare; i++ {
		xff := fmt.Sprintf("10.0.0.%d", i+1)
		rec := shareVerifyReq(t, h, "0000", "127.0.0.1:1234", xff)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("第 %d 次伪造 IP 失败应 403: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	for _, rec := range []*httptest.ResponseRecorder{
		shareVerifyReq(t, h, "0000", "127.0.0.1:1234", "10.9.9.9"),
		shareListReq(t, h, "0000", "127.0.0.1:1234", "10.9.9.10"),
		shareDownloadReq(t, h, "0000", "127.0.0.1:1234", "10.9.9.11"),
	} {
		assertShareFail(t, rec, http.StatusTooManyRequests, codeRateLimited, "尝试次数过多，请 10 分钟后再试")
	}
}

// TestSharePasswordSuccessClearsIPCounter 口令正确后清空该 IP+share 计数（成功访问不受前次误输影响），
// 全域计数不被成功请求清零（仍按窗口累计）。
func TestSharePasswordSuccessClearsIPCounter(t *testing.T) {
	resetShareFailures()
	t.Cleanup(resetShareFailures)
	srv, _ := fakeOpenList(t)
	_, h := newShareTestModule(t, srv.URL)
	ip := "203.0.113.10:1234"

	for i := 0; i < sharePWMaxPerIP-1; i++ {
		assertShareFail(t, shareVerifyReq(t, h, "0000", ip, ""), http.StatusForbidden, codeBadTicket, "提取码错误")
	}
	if rec := shareVerifyReq(t, h, "1234", ip, ""); rec.Code != http.StatusOK {
		t.Fatalf("正确口令应通过: %d %s", rec.Code, rec.Body)
	}
	// 计数清零后重新累计：再 10 次错误口令均 403，第 11 次才 429。
	for i := 0; i < sharePWMaxPerIP; i++ {
		assertShareFail(t, shareVerifyReq(t, h, "0000", ip, ""), http.StatusForbidden, codeBadTicket, "提取码错误")
	}
	assertShareFail(t, shareVerifyReq(t, h, "0000", ip, ""),
		http.StatusTooManyRequests, codeRateLimited, "尝试次数过多，请 10 分钟后再试")
}

// TestShareFailAttemptConcurrentCap 并发失败尝试的检查与累加必须原子：同一 IP+share
// 最多只记录 sharePWMaxPerIP 次，其余立即限流，不会因竞态超过上限。
func TestShareFailAttemptConcurrentCap(t *testing.T) {
	resetShareFailures()
	t.Cleanup(resetShareFailures)
	const n = 64
	start := make(chan struct{})
	limited := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			limited[i] = shareFailAttempt("203.0.113.9", "sid")
		}(i)
	}
	close(start)
	wg.Wait()
	allowed := 0
	for _, l := range limited {
		if !l {
			allowed++
		}
	}
	if allowed != sharePWMaxPerIP {
		t.Fatalf("并发允许失败 %d 次，期望恰好 %d（检查与累加未在同一临界区）", allowed, sharePWMaxPerIP)
	}
}

// TestShareIPFailTableEvictionKeepsRecent IP 表超过上限时按最旧项淘汰，不整表清空：
// 近期合法计数与全域计数保留。
func TestShareIPFailTableEvictionKeepsRecent(t *testing.T) {
	resetShareFailures()
	t.Cleanup(resetShareFailures)
	now := time.Now()
	shareLimitMu.Lock()
	for i := 0; i < shareIPFailsMax; i++ {
		shareIPFails[fmt.Sprintf("10.0.%d.%d|sid", i>>8, i&255)] = pwRec{count: 1, resetAt: now.Add(time.Duration(i) * time.Millisecond)}
	}
	legitKey := "10.9.9.9|sid"
	shareIPFails[legitKey] = pwRec{count: 5, resetAt: now.Add(sharePWWindow)}
	shareLimitMu.Unlock()

	if shareFailAttempt("192.0.2.1", "sid") {
		t.Fatal("首次失败不应被限流")
	}
	shareLimitMu.Lock()
	_, kept := shareIPFails[legitKey]
	all := shareAllFails["sid"]
	shareLimitMu.Unlock()
	if !kept {
		t.Fatal("IP 表超限不应整表清空（近期合法计数被删除）")
	}
	if all.count != 1 {
		t.Fatalf("全域计数应保留：%+v", all)
	}
}
