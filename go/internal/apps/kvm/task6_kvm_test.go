package kvm

// Task 6 回归：设备 fileshare 转发响应必须有字节上限（limit+1 检出），不能把截断内容当完整结果。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFileshareDoResponseByteBudget 常规 JSON 转发响应超预算必须显式报错。
func TestFileshareDoResponseByteBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	defer srv.Close()
	old := maxKVMResponseBytes
	maxKVMResponseBytes = 16
	defer func() { maxKVMResponseBytes = old }()

	fs := fileshare{ps: ProxySession{Origin: srv.URL}, std: srv.Client(), big: srv.Client()}
	status, data, err := fs.do(context.Background(), http.MethodGet, "/api/fileshare/list", "", nil, false)
	if err == nil {
		t.Fatalf("超预算响应应报错，实际 status=%d data=%q", status, data)
	}
	if len(data) != 0 {
		t.Fatalf("超预算不得返回截断内容：%q", data)
	}

	maxKVMResponseBytes = 64
	status, data, err = fs.do(context.Background(), http.MethodGet, "/api/fileshare/list", "", nil, false)
	if err != nil || status != http.StatusOK || len(data) != 32 {
		t.Fatalf("预算内响应应正常: status=%d len=%d err=%v", status, len(data), err)
	}
}
