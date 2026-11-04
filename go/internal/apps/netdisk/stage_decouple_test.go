package netdisk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stageUnavailableOL struct {
	*stageTestOL
	calls      atomic.Int64
	background chan struct{}
}

func (o *stageUnavailableOL) notify() {
	o.calls.Add(1)
	select {
	case o.background <- struct{}{}:
	default:
	}
}

func (o *stageUnavailableOL) EnsureConfigured() error {
	o.notify()
	return errors.New("OpenList 不可用")
}

func (o *stageUnavailableOL) EnsureDir(context.Context, string) error {
	o.notify()
	return errors.New("OpenList 不可用")
}

func (o *stageUnavailableOL) Call(context.Context, string, string, CallOpts) (json.RawMessage, error) {
	o.notify()
	return nil, errors.New("OpenList 不可用")
}

func TestStagedUploadAcceptsFileWhileOpenListUnavailable(t *testing.T) {
	ol := &stageUnavailableOL{stageTestOL: &stageTestOL{}, background: make(chan struct{}, 1)}
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.StageDir = t.TempDir()
	h := newTestRouter(t, m)

	initReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
		strings.NewReader(`{"space":"my","path":"","name":"offline.bin","size":"2","staged":true}`)))
	initRec := httptest.NewRecorder()
	h.ServeHTTP(initRec, initReq)
	if initRec.Code != http.StatusOK {
		t.Fatalf("OpenList 不可用不应影响暂存 init: %d %s", initRec.Code, initRec.Body.String())
	}
	if got := ol.calls.Load(); got != 0 {
		t.Fatalf("暂存 init 调用了 %d 次 OpenList，应完全解耦", got)
	}
	var initBody struct {
		Data struct {
			UploadID string `json:"upload_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initRec.Body.Bytes(), &initBody); err != nil || initBody.Data.UploadID == "" {
		t.Fatalf("init 响应异常: %v %s", err, initRec.Body.String())
	}

	chunkReq := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk", strings.NewReader("AB")))
	chunkReq.Header.Set("Content-Type", "application/octet-stream")
	chunkReq.Header.Set("X-Upload-Id", initBody.Data.UploadID)
	chunkReq.Header.Set("X-Chunk-Index", "0")
	chunkRec := httptest.NewRecorder()
	h.ServeHTTP(chunkRec, chunkReq)
	if chunkRec.Code != http.StatusOK {
		t.Fatalf("OpenList 不可用不应影响分片接收: %d %s", chunkRec.Code, chunkRec.Body.String())
	}

	select {
	case <-ol.background:
	case <-time.After(2 * time.Second):
		t.Fatal("接收完成后未启动独立后台同步")
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		statusReq := withFakeAuth(httptest.NewRequest(http.MethodGet,
			"/api/v1/netdisk/upload/status?upload_id="+initBody.Data.UploadID, nil))
		statusRec := httptest.NewRecorder()
		h.ServeHTTP(statusRec, statusReq)
		var statusBody struct {
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
			t.Fatalf("status 响应异常: %v %s", err, statusRec.Body.String())
		}
		if statusBody.Data.State == "failed_permanent" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("持续故障的后台同步未落定: %s", statusRec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := os.ReadDir(m.StageDir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".part") {
		t.Fatalf("后台同步失败必须保留已接收的暂存文件: entries=%v err=%v", entries, err)
	}
}
