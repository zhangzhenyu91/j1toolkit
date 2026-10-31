// 服务端暂存上传：分片只落 Shade 临时盘，最后一片到齐后自动后台 OpenList multipart；
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
	mu        sync.Mutex
	initPath  string
	chunks    map[int][]byte
	completed bool
	done      chan struct{}
}

func (s *stageTestOL) EnsureConfigured() error { return nil }
func (s *stageTestOL) Call(_ context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch apiPath {
	case "/api/fs/multipart/init":
		s.initPath = opts.Headers["File-Path"]
		s.chunks = map[int][]byte{}
		return json.Marshal(map[string]any{
			"upload_id": "stage0123456789abcdef", "chunk_size": stageChunkSize, "total_chunks": 2,
		})
	case "/api/fs/multipart/chunk":
		idx, _ := strconv.Atoi(opts.Headers["X-Chunk-Index"])
		data, _ := io.ReadAll(opts.Body.(io.Reader))
		s.chunks[idx] = data
		return json.Marshal(map[string]any{
			"state": "receiving", "received_bytes": len(data), "storage_progress": 50,
		})
	case "/api/fs/multipart/status":
		if s.completed {
			return json.Marshal(map[string]any{"state": "completed", "storage_progress": 100})
		}
		return json.Marshal(map[string]any{"state": "receiving", "storage_progress": 50})
	case "/api/fs/multipart/complete":
		s.completed = true
		close(s.done)
		return json.Marshal(map[string]any{"state": "completed", "storage_progress": 100})
	}
	return json.RawMessage("null"), nil
}
func (s *stageTestOL) DownloadStream(context.Context, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, nil
}
func (s *stageTestOL) EnsureDir(context.Context, string) error { return nil }
func (s *stageTestOL) PutEmpty(context.Context, string) error  { return nil }
func (s *stageTestOL) FsPut(context.Context, string, io.Reader, int64) error {
	return nil
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
		if i == 0 {
			// 模拟 nginx HTTP/2 + proxy_request_buffering off 的 chunked 转发。
			req.ContentLength = -1
		}
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
		t.Fatal("最后一片到齐后未自动启动 OpenList multipart")
	}

	// 不调用 complete，直接 status 轮询到 completed（关页继续）。
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
	var statusRaw string
	deadline := time.Now().Add(2 * time.Second)
	for {
		statusReq := withFakeAuth(httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/upload/status?upload_id="+id, nil))
		statusRec := httptest.NewRecorder()
		h.ServeHTTP(statusRec, statusReq)
		statusRaw = statusRec.Body.String()
		if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
			t.Fatalf("status 信封解析失败: %v", err)
		}
		if statusBody.Data.State == "completed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if statusBody.Data.UploadID != id || !statusBody.Data.Staged || statusBody.Data.State != "completed" ||
		statusBody.Data.Progress != 100 || statusBody.Data.Received != 8<<20 || statusBody.Data.Error != "" {
		t.Fatalf("status 未收敛 completed: %s", statusRaw)
	}

	ol.mu.Lock()
	gotAbs := ol.initPath
	ol.mu.Unlock()
	if gotAbs != "/nd/my/alice/stage.bin" {
		t.Fatalf("multipart 目标 = %q", gotAbs)
	}
	ol.mu.Lock()
	gotData := append(append([]byte(nil), ol.chunks[0]...), ol.chunks[1]...)
	ol.mu.Unlock()
	if len(gotData) != 8<<20 || !bytes.Equal(gotData[:4<<20], chunk) {
		t.Fatalf("multipart 数据长度/内容不符: %d", len(gotData))
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
