package netdisk

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type stageRetryOL struct {
	*stageTestOL
	failPath  string
	retryMu   sync.Mutex
	initCalls int
	failed    bool
}

func (s *stageRetryOL) Call(ctx context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error) {
	if apiPath == "/api/fs/multipart/init" {
		s.retryMu.Lock()
		s.initCalls++
		s.retryMu.Unlock()
		s.stageTestOL.mu.Lock()
		s.stageTestOL.chunks = map[int][]byte{}
		s.stageTestOL.mu.Unlock()
		return json.Marshal(map[string]any{
			"upload_id": "stage0123456789abcdef", "chunk_size": 2, "total_chunks": 1,
		})
	}
	if apiPath == s.failPath {
		s.retryMu.Lock()
		fail := !s.failed
		s.failed = true
		s.retryMu.Unlock()
		if fail {
			if opts.Body != nil {
				_, _ = io.Copy(io.Discard, opts.Body.(io.Reader))
			}
			return nil, &UpstreamError{State: "failed_retriable", Message: "resend from chunk 0"}
		}
	}
	return s.stageTestOL.Call(ctx, method, apiPath, opts)
}

func TestStagedMultipartRetriesFailedSessionFromLocalFile(t *testing.T) {
	for _, failPath := range []string{"/api/fs/multipart/chunk", "/api/fs/multipart/complete"} {
		t.Run(failPath, func(t *testing.T) {
			ol := &stageRetryOL{stageTestOL: &stageTestOL{done: make(chan struct{})}, failPath: failPath}
			m := New(nil)
			m.OL = ol
			m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
			m.StageDir = t.TempDir()
			h := newTestRouter(t, m)

			initReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
				strings.NewReader(`{"space":"my","path":"","name":"retry.bin","size":"2","staged":true}`)))
			initRec := httptest.NewRecorder()
			h.ServeHTTP(initRec, initReq)
			if initRec.Code != http.StatusOK {
				t.Fatalf("init 失败: %d %s", initRec.Code, initRec.Body.String())
			}
			var initBody struct {
				Data struct {
					UploadID string `json:"upload_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(initRec.Body.Bytes(), &initBody); err != nil || initBody.Data.UploadID == "" {
				t.Fatalf("init 响应异常: %v %s", err, initRec.Body.String())
			}

			chunkReq := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk", bytes.NewReader([]byte("AB"))))
			chunkReq.Header.Set("Content-Type", "application/octet-stream")
			chunkReq.Header.Set("X-Upload-Id", initBody.Data.UploadID)
			chunkReq.Header.Set("X-Chunk-Index", "0")
			chunkRec := httptest.NewRecorder()
			h.ServeHTTP(chunkRec, chunkReq)
			if chunkRec.Code != http.StatusOK {
				t.Fatalf("chunk 失败: %d %s", chunkRec.Code, chunkRec.Body.String())
			}

			deadline := time.Now().Add(5 * time.Second)
			for {
				statusReq := withFakeAuth(httptest.NewRequest(http.MethodGet,
					"/api/v1/netdisk/upload/status?upload_id="+initBody.Data.UploadID, nil))
				statusRec := httptest.NewRecorder()
				h.ServeHTTP(statusRec, statusReq)
				var statusBody struct {
					Data struct {
						State string `json:"state"`
						Error string `json:"error"`
					} `json:"data"`
				}
				if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
					t.Fatalf("status 响应异常: %v %s", err, statusRec.Body.String())
				}
				if statusBody.Data.State == "completed" {
					break
				}
				if statusBody.Data.State == "failed_permanent" {
					t.Fatalf("可重试会话被直接判为永久失败: %s", statusBody.Data.Error)
				}
				if time.Now().After(deadline) {
					t.Fatalf("后台重试后仍未完成: %s", statusRec.Body.String())
				}
				time.Sleep(10 * time.Millisecond)
			}

			ol.retryMu.Lock()
			inits := ol.initCalls
			ol.retryMu.Unlock()
			if inits != 2 {
				t.Fatalf("multipart init 次数 = %d，应在失败后重建一次", inits)
			}
			ol.stageTestOL.mu.Lock()
			got := append([]byte(nil), ol.stageTestOL.chunks[0]...)
			ol.stageTestOL.mu.Unlock()
			if !bytes.Equal(got, []byte("AB")) {
				t.Fatalf("重试后网盘内容 = %q，应为 AB", got)
			}
			entries, err := os.ReadDir(m.StageDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("成功后暂存文件应清理: entries=%d err=%v", len(entries), err)
			}
		})
	}
}

type stageTransientOL struct {
	*stageRetryOL
}

func (s *stageTransientOL) Call(ctx context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error) {
	if apiPath == "/api/fs/multipart/complete" {
		s.retryMu.Lock()
		fail := !s.failed
		s.failed = true
		s.retryMu.Unlock()
		if fail {
			return nil, &UpstreamError{Status: http.StatusBadGateway, Message: "temporary network failure"}
		}
	}
	return s.stageRetryOL.Call(ctx, method, apiPath, opts)
}

func TestStagedMultipartRetriesTemporaryNetworkFailure(t *testing.T) {
	retryOL := &stageRetryOL{stageTestOL: &stageTestOL{done: make(chan struct{})}}
	ol := &stageTransientOL{stageRetryOL: retryOL}
	path := t.TempDir() + "/transient.part"
	if err := os.WriteFile(path, []byte("AB"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &stageUpload{
		m: New(nil), ol: ol, path: path, abs: "/nd/my/alice/transient.bin",
		size: 2, state: "uploading",
	}
	if err := st.runMultipart(context.Background()); err != nil {
		t.Fatalf("临时网络故障应由后台独立重试: %v", err)
	}
	retryOL.retryMu.Lock()
	inits := retryOL.initCalls
	retryOL.retryMu.Unlock()
	if inits != 2 {
		t.Fatalf("临时故障后 multipart init 次数 = %d，应为 2", inits)
	}
}
