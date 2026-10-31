package callme

// Task 6 fix round 1：群内提问 ingest 请求体在解析前限总量。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadIngestBodyLimited 超过预算的请求体在 JSON 解析前报错。
func TestReadIngestBodyLimited(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/callme/ingest-chat", strings.NewReader("0123456789"))
	limit := int64(8)
	if _, err := readIngestBody(req, limit); err == nil {
		t.Fatal("超过预算应报错")
	}
	req = httptest.NewRequest("POST", "/api/v1/callme/ingest-chat", strings.NewReader("12345678"))
	if buf, err := readIngestBody(req, limit); err != nil || string(buf) != "12345678" {
		t.Fatalf("预算内应读取成功: %q %v", buf, err)
	}
}
