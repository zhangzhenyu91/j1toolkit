package authwx

// Task 6 边界回归：登录请求体在解析前限总量（超大 JSON 不再整读解码）。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeJSONBodyLimit 超过请求体预算的 JSON 必须报错（不能先整读再解析）。
func TestDecodeJSONBodyLimit(t *testing.T) {
	big := `{"username":"` + strings.Repeat("a", maxAuthBodyBytes) + `"}`
	req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(big))
	var body struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(req, &body); err == nil {
		t.Fatal("超请求体预算应报错")
	}

	req = httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":"alice"}`))
	if err := decodeJSON(req, &body); err != nil || body.Username != "alice" {
		t.Fatalf("正常请求应解析: %v %q", err, body.Username)
	}
}
