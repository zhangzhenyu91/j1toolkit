// 服务端暂存上传：分片只落 Shade 临时盘，最后一片到齐后自动后台 FsPut；
// complete 不阻塞、关页后任务继续。
package netdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type stageTestOL struct {
	mu   sync.Mutex
	abs  string
	data []byte
	done chan struct{}
	err  error
}

func (s *stageTestOL) EnsureConfigured() error { return nil }
func (s *stageTestOL) Call(context.Context, string, string, CallOpts) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (s *stageTestOL) DownloadStream(context.Context, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, nil
}
func (s *stageTestOL) EnsureDir(context.Context, string) error { return nil }
func (s *stageTestOL) PutEmpty(context.Context, string) error  { return nil }
func (s *stageTestOL) FsPut(_ context.Context, abs string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	s.mu.Lock()
	s.abs, s.data, s.err = abs, data, err
	s.mu.Unlock()
	close(s.done)
	return err
}
func (s *stageTestOL) ArchiveDownloadStream(context.Context, string, string, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, nil
}

func TestStagedUploadAutoStartsAndCompletes(t *testing.T) {
	ol := &stageTestOL{done: make(chan struct{})}
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"
	m.StageDir = t.TempDir()
	h := newTestRouter(t, m)

	initReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
		strings.NewReader(`{"space":"my","path":"","name":"stage.bin","size":"8388608","staged":true}`)))
	initRec := httptest.NewRecorder()
	h.ServeHTTP(initRec, initReq)
	if initRec.Code != http.StatusOK {
		t.Fatalf("init 失败: %d %s", initRec.Code, initRec.Body.String())
	}
	var initBody struct {
		Data struct {
			UploadID    string `json:"upload_id"`
			State       string `json:"state"`
			ChunkSize   int64  `json:"chunk_size"`
			TotalChunks int    `json:"total_chunks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initRec.Body.Bytes(), &initBody); err != nil {
		t.Fatalf("init 信封解析失败: %v", err)
	}
	id := initBody.Data.UploadID
	if id == "" || initBody.Data.State != "staging" || initBody.Data.TotalChunks != 2 || initBody.Data.ChunkSize != stageChunkSize {
		t.Fatalf("init 快照不符: %s", initRec.Body.String())
	}

	chunk := bytes.Repeat([]byte("A"), stageChunkSize)
	for i := range 2 {
		req := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk", bytes.NewReader(chunk)))
		req.ContentLength = int64(len(chunk))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Upload-Id", id)
		req.Header.Set("X-Chunk-Index", strconv.Itoa(i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("chunk %d 失败: %d %s", i, rec.Code, rec.Body.String())
		}
	}

	select {
	case <-ol.done:
	case <-time.After(2 * time.Second):
		t.Fatal("最后一片到齐后未自动启动 FsPut")
	}

	// 不调用 complete，直接 status 也应看到 completed（关页继续）。
	statusReq := withFakeAuth(httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/upload/status?upload_id="+id, nil))
	statusRec := httptest.NewRecorder()
	h.ServeHTTP(statusRec, statusReq)
	var statusBody struct {
		Data struct {
			State     string `json:"state"`
			Progress  int    `json:"storage_progress"`
			Received  int64  `json:"received_bytes"`
			Staged    bool   `json:"staged"`
			Total     int    `json:"total_chunks"`
			Error     string `json:"error"`
			UploadID  string `json:"upload_id"`
			ChunkSize int64  `json:"chunk_size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("status 信封解析失败: %v", err)
	}
	if statusBody.Data.UploadID != id || !statusBody.Data.Staged || statusBody.Data.State != "completed" ||
		statusBody.Data.Progress != 100 || statusBody.Data.Received != 8<<20 || statusBody.Data.Error != "" {
		t.Fatalf("status 未收敛 completed: %s", statusRec.Body.String())
	}

	ol.mu.Lock()
	gotAbs, gotData := ol.abs, append([]byte(nil), ol.data...)
	ol.mu.Unlock()
	if gotAbs != "/nd/my/alice/stage.bin" {
		t.Fatalf("FsPut 目标 = %q", gotAbs)
	}
	if len(gotData) != 8<<20 || !bytes.Equal(gotData[:4<<20], chunk) {
		t.Fatalf("FsPut 数据长度/内容不符: %d", len(gotData))
	}

	// complete 幂等：已完成会话再 complete 仍返回 completed。
	compReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/complete",
		strings.NewReader(`{"upload_id":"`+id+`"}`)))
	compRec := httptest.NewRecorder()
	h.ServeHTTP(compRec, compReq)
	if compRec.Code != http.StatusOK || !strings.Contains(compRec.Body.String(), `"completed"`) {
		t.Fatalf("completed 后 complete 应幂等: %d %s", compRec.Code, compRec.Body.String())
	}
}
