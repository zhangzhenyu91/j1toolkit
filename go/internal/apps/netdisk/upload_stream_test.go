// 分片上传流式中转与失败会话状态透传：init 请求 4MB 分片、chunk 边读边转、
// failed_retriable 映射 409 + restart，64MB 上限在读取前拒绝。
package netdisk

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestUploadChunkStreamsAndInitChunkSize(t *testing.T) {
	var mu sync.Mutex
	var initChunkSize, chunkBody, chunkCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case "/api/fs/multipart/init":
			mu.Lock()
			initChunkSize = r.Header.Get("X-Chunk-Size")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"data":{"upload_id":"abcd1234abcd1234","chunk_size":4194304,"total_chunks":2}}`))
		case "/api/fs/multipart/chunk":
			buf, _ := io.ReadAll(r.Body)
			mu.Lock()
			chunkBody = string(buf)
			chunkCT = r.Header.Get("Content-Type")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"data":{"upload_id":"abcd1234abcd1234","state":"receiving"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	defer srv.Close()
	_, router := newShareTestModule(t, srv.URL)

	initReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
		strings.NewReader(`{"space":"my","path":"","name":"a.bin","size":"8388608"}`)))
	initRec := httptest.NewRecorder()
	router.ServeHTTP(initRec, initReq)
	if initRec.Code != http.StatusOK {
		t.Fatalf("init 失败: %d %s", initRec.Code, initRec.Body.String())
	}
	mu.Lock()
	gotSize := initChunkSize
	mu.Unlock()
	if gotSize != "4194304" {
		t.Fatalf("init X-Chunk-Size = %q，应为 4MB", gotSize)
	}

	chunkReq := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk",
		strings.NewReader("ABCD")))
	chunkReq.Header.Set("Content-Type", "application/octet-stream")
	chunkReq.Header.Set("X-Upload-Id", "abcd1234abcd1234")
	chunkReq.Header.Set("X-Chunk-Index", "0")
	chunkRec := httptest.NewRecorder()
	router.ServeHTTP(chunkRec, chunkReq)
	if chunkRec.Code != http.StatusOK {
		t.Fatalf("chunk 失败: %d %s", chunkRec.Code, chunkRec.Body.String())
	}
	mu.Lock()
	gotBody, gotCT := chunkBody, chunkCT
	mu.Unlock()
	if gotBody != "ABCD" || gotCT != "application/octet-stream" {
		t.Fatalf("流式 body/类型不符: %q %q", gotBody, gotCT)
	}
}

func TestUploadChunkFailedRetriableMapsRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case "/api/fs/multipart/chunk":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"code":400,"message":"upload attempt failed, resend from chunk 0 to retry","data":{"state":"failed_retriable","error":"context deadline exceeded"}}`))
		case "/api/fs/multipart/complete":
			_, _ = w.Write([]byte(`{"code":400,"message":"upload failed (failed_retriable)","data":{"state":"failed_retriable","error":"driver timeout"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	defer srv.Close()
	_, router := newShareTestModule(t, srv.URL)

	chunkReq := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk",
		strings.NewReader("ABCD")))
	chunkReq.Header.Set("Content-Type", "application/octet-stream")
	chunkReq.Header.Set("X-Upload-Id", "abcd1234abcd1234")
	chunkReq.Header.Set("X-Chunk-Index", "1")
	chunkRec := httptest.NewRecorder()
	router.ServeHTTP(chunkRec, chunkReq)
	if chunkRec.Code != http.StatusConflict || !strings.Contains(chunkRec.Body.String(), `"restart":true`) {
		t.Fatalf("failed_retriable 应 409+restart: %d %s", chunkRec.Code, chunkRec.Body.String())
	}

	compReq := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/complete",
		strings.NewReader(`{"upload_id":"abcd1234abcd1234"}`)))
	compRec := httptest.NewRecorder()
	router.ServeHTTP(compRec, compReq)
	if compRec.Code != http.StatusConflict || !strings.Contains(compRec.Body.String(), `"restart":true`) {
		t.Fatalf("complete failed_retriable 应 409+restart: %d %s", compRec.Code, compRec.Body.String())
	}
}

func TestUploadChunkRejectsOverLimitBeforeRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
	}))
	defer srv.Close()
	_, router := newShareTestModule(t, srv.URL)

	req := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk",
		strings.NewReader("tiny")))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Id", "abcd1234abcd1234")
	req.Header.Set("X-Chunk-Index", "0")
	req.ContentLength = 64<<20 + 1
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413: %d %s", rec.Code, rec.Body.String())
	}
}
