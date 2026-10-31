package netdisk

// Task 6 fix round 1：缩略图闸门等待超时必须返回明确错误，不能 200 空体。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestThumbSemaphoreWaitTimeoutWritesError 闸门等待超时必须返回明确错误，不能 200 空体。
func TestThumbSemaphoreWaitTimeoutWritesError(t *testing.T) {
	ol := newTask6MetaOL()
	ol.size = 1
	ol.body = "x"
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.Thumb = &countingThumb{}
	m.ThumbTimeout = 20 * time.Millisecond
	m.thumbSem <- struct{}{}
	m.thumbSem <- struct{}{}
	r := newTestRouter(t, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/thumb?space=my&path=/pics/a.png", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("闸门等待超时应 504，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "超时") {
		t.Fatalf("应返回明确超时错误：%s", rec.Body.String())
	}
}
