package netdisk

// Task 6 fix round 1：/save 在读取 JSON body 前按编码总量限流；缩略图闸门超时写明确错误；
// dirsize 频率窗口过期用户回收。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSaveJSONBodyLimitedBeforeParse body 超编码总量时在 JSON 解析前 413（data 本身很小也不能绕过）。
func TestSaveJSONBodyLimitedBeforeParse(t *testing.T) {
	m := New(nil)
	m.OL = newTask6MetaOL()
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.MaxUploadMB = 1
	r := newTestRouter(t, m)

	padding := strings.Repeat("p", 3<<20)
	body := `{"space":"my","dir":"docs","name":"x.txt","data":"aGk=","padding":"` + padding + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/save", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超编码总量应 413，实际 %d %s", rec.Code, rec.Body.String())
	}

	// 正常小文件不受影响。
	small := `{"space":"my","dir":"docs","name":"x.txt","data":"aGk="}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/save", strings.NewReader(small))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正常上传应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestDirSizeRateWindowSweep 频率窗口过期用户条目应被回收，避免 map 无界增长。
func TestDirSizeRateWindowSweep(t *testing.T) {
	m := New(nil)
	now := time.Now()
	old := now.Add(-2 * dirSizeUserWindow)
	m.dirSizeRate = map[int64][]time.Time{}
	for i := int64(1); i <= 300; i++ {
		m.dirSizeRate[i] = []time.Time{old}
	}
	m.dirSize.mu.Lock()
	if !m.allowDirSizeLocked(9999, now) {
		m.dirSize.mu.Unlock()
		t.Fatal("新用户应放行")
	}
	size := len(m.dirSizeRate)
	m.dirSize.mu.Unlock()
	if size > 64 {
		t.Fatalf("过期用户条目未回收：%d", size)
	}
}
