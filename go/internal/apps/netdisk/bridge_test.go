package netdisk

// office 文档源桥测试：ReadFileBytes / WriteFileBytes / DocKey（含越界与不存在口径）；不访问真实 OpenList。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bridgeOL 假 OpenList：fs/get 元信息 + /d/ 内容 + fs/put 记录写回；路径按请求体判别。
func bridgeOL(t *testing.T, putGot *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		switch {
		case r.URL.Path == "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case r.URL.Path == "/api/fs/get":
			if strings.Contains(string(body), "missing") {
				_, _ = w.Write([]byte(`{"code":500,"message":"object not found"}`))
				return
			}
			isDir := "false"
			if strings.Contains(string(body), "adir") {
				isDir = "true"
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"name":"a.docx","is_dir":` + isDir +
				`,"size":3,"modified":"2026-10-27 10:00:00","sign":"s1"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			_, _ = w.Write([]byte("DOC-BYTES"))
		case r.URL.Path == "/api/fs/put":
			*putGot = append(*putGot, body...)
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBridgeReadWriteAndDocKey(t *testing.T) {
	var putGot []byte
	srv := bridgeOL(t, &putGot)
	m := New(openFakeDB(t))
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	// 个人空间回源。
	data, contentType, err := m.ReadFileBytes(ctx, "my", "alice", 0, "/docs/a.docx")
	if err != nil || string(data) != "DOC-BYTES" {
		t.Fatalf("ReadFileBytes: %q %v", data, err)
	}
	if contentType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("contentType 错误: %s", contentType)
	}
	// 文档版本 key = sha1(绝对路径|size|modified)。
	key, err := m.DocKey(ctx, "my", "alice", 0, "/docs/a.docx")
	if err != nil {
		t.Fatalf("DocKey: %v", err)
	}
	h := sha1.Sum([]byte("/nd/my/alice/docs/a.docx|3|2026-10-27 10:00:00"))
	if key != hex.EncodeToString(h[:]) {
		t.Fatalf("key 错误: %s", key)
	}
	// 写回。
	if err := m.WriteFileBytes(ctx, "my", "alice", 0, "/docs/a.docx", []byte("NEW")); err != nil {
		t.Fatalf("WriteFileBytes: %v", err)
	}
	if string(putGot) != "NEW" {
		t.Fatalf("写回内容错误: %q", putGot)
	}

	// 目录越界拒绝（越界校验口径不放宽）。
	var nf *NotFoundError
	_, _, err = m.ReadFileBytes(ctx, "my", "alice", 0, "/../etc/passwd")
	if !errors.As(err, &nf) {
		t.Fatalf("越界应报 NotFoundError, got %v", err)
	}
	_, _, err = m.ReadFileBytes(ctx, "my", "alice", 0, "/")
	if !errors.As(err, &nf) {
		t.Fatalf("根路径应拒绝, got %v", err)
	}
	// 目录判非法 / 不存在归一。
	_, _, err = m.ReadFileBytes(ctx, "my", "alice", 0, "/adir")
	if !errors.As(err, &nf) || !nf.IsNotFound() {
		t.Fatalf("目录应 NotFoundError, got %v", err)
	}
	_, _, err = m.ReadFileBytes(ctx, "my", "alice", 0, "/missing.docx")
	if !errors.As(err, &nf) {
		t.Fatalf("不存在应 NotFoundError, got %v", err)
	}
	// 公共区按班组隔离（fakeDB 返回班组名 检修一班）。
	if _, _, err = m.ReadFileBytes(ctx, "public", "alice", 5, "/a.docx"); err != nil {
		t.Fatalf("公共区回源: %v", err)
	}
}

func TestBridgeNotConfigured(t *testing.T) {
	// 有客户端但未配置账户。
	m := New(openFakeDB(t))
	m.OL = &OpenListHTTP{BaseURL: "http://127.0.0.1:0"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my"}
	_, _, err := m.ReadFileBytes(context.Background(), "my", "alice", 0, "/a.docx")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("未配置应 ErrNotConfigured, got %v", err)
	}
	// OL 缺失：普通依赖错误。
	m2 := New(openFakeDB(t))
	m2.Base = BaseDirs{PersonalRoot: "/nd/my"}
	_, _, err = m2.ReadFileBytes(context.Background(), "my", "alice", 0, "/a.docx")
	if err == nil {
		t.Fatalf("OL 缺失应报错")
	}
}
