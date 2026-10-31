// Final review fix：OpenList FsPut 必须是同目录 staging + 完整上传/大小校验 + rename 替换，
// 上传或校验失败不得截断原文件；rename 不覆盖时走备份/恢复。全部本地 httptest，不连真实 OpenList。
package netdisk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type atomicOLRename struct {
	Path string
	Name string
}

type atomicOLState struct {
	mu           sync.Mutex
	files        map[string][]byte
	puts         []string
	renames      []atomicOLRename
	removes      []string
	failPut      bool
	sizeMismatch bool
	noOverwrite  bool
	// failRenameTmpPrefix 非空：source 文件名含该前缀时 rename 失败（模拟替换阶段持续失败）。
	failRenameTmpPrefix string
	// failRenameBakPrefix 非空：source 文件名含该前缀时 rename 失败（模拟备份恢复失败）。
	failRenameBakPrefix string
}

type atomicOLServer struct {
	*atomicOLState
	srv *httptest.Server
}

func newAtomicOL(t *testing.T, files map[string]string) *atomicOLServer {
	t.Helper()
	st := &atomicOLState{files: map[string][]byte{}}
	for k, v := range files {
		st.files[k] = []byte(v)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		case "/api/fs/get":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(body, &req)
			data, ok := st.files[req.Path]
			if !ok {
				_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"size":` + itoa(len(data)) + `,"is_dir":false}}`))
		case "/api/fs/put":
			p := r.Header.Get("File-Path")
			if dec, err := url.PathUnescape(p); err == nil {
				p = dec
			}
			st.puts = append(st.puts, p)
			if st.failPut {
				_, _ = w.Write([]byte(`{"code":500,"message":"put failed"}`))
				return
			}
			if st.sizeMismatch {
				st.files[p] = append(body, 'X')
			} else {
				st.files[p] = append([]byte(nil), body...)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case "/api/fs/rename":
			var req struct {
				Path string `json:"path"`
				Name string `json:"name"`
			}
			_ = json.Unmarshal(body, &req)
			st.renames = append(st.renames, atomicOLRename{Path: req.Path, Name: req.Name})
			dir := req.Path
			if i := strings.LastIndexByte(dir, '/'); i >= 0 {
				dir = dir[:i]
			}
			dst := dir + "/" + req.Name
			if _, exists := st.files[dst]; exists && st.noOverwrite {
				_, _ = w.Write([]byte(`{"code":409,"message":"exists"}`))
				return
			}
			if st.failRenameTmpPrefix != "" && strings.Contains(req.Path, st.failRenameTmpPrefix) {
				_, _ = w.Write([]byte(`{"code":500,"message":"rename failed"}`))
				return
			}
			if st.failRenameBakPrefix != "" && strings.Contains(req.Path, st.failRenameBakPrefix) {
				_, _ = w.Write([]byte(`{"code":500,"message":"restore failed"}`))
				return
			}
			data, ok := st.files[req.Path]
			if !ok {
				_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
				return
			}
			delete(st.files, req.Path)
			st.files[dst] = data
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case "/api/fs/remove":
			var req struct {
				Path  string   `json:"path"`
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Path != "" {
				st.removes = append(st.removes, req.Path)
				delete(st.files, req.Path)
			}
			for _, name := range req.Names {
				p := strings.TrimRight(req.Dir, "/") + "/" + name
				st.removes = append(st.removes, p)
				delete(st.files, p)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &atomicOLServer{atomicOLState: st, srv: srv}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func newAtomicClient(srv *httptest.Server) *OpenListHTTP {
	return &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
}

// 成功路径：上传写同目录隐藏 staging，完整后 rename 到原路径；原文件被替换且无残留。
func TestFsPutAtomicStagingRename(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	c := newAtomicClient(ol.srv)
	if err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3); err != nil {
		t.Fatalf("FsPut: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if got := string(ol.files["/nd/docs/a.docx"]); got != "NEW" {
		t.Fatalf("目标文件应被替换为 NEW，实际 %q", got)
	}
	if len(ol.puts) != 1 {
		t.Fatalf("应只有一次上传，实际 %v", ol.puts)
	}
	staging := ol.puts[0]
	if staging == "/nd/docs/a.docx" {
		t.Fatal("不得直接覆盖原文件，必须先写 staging")
	}
	if !strings.HasPrefix(staging, "/nd/docs/.") || !strings.Contains(staging, ".shade-tmp-") {
		t.Fatalf("staging 必须在同目录且唯一: %s", staging)
	}
	if _, exists := ol.files[staging]; exists {
		t.Fatalf("成功后 staging 不得残留: %v", ol.files)
	}
	if len(ol.renames) != 1 || ol.renames[0].Path != staging || ol.renames[0].Name != "a.docx" {
		t.Fatalf("rename 应把 staging 替换为原文件名: %+v", ol.renames)
	}
}

// 上传失败：原文件保持可读，不产生 rename，staging 尽力清理。
func TestFsPutUploadFailureKeepsOriginal(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	ol.failPut = true
	c := newAtomicClient(ol.srv)
	if err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3); err == nil {
		t.Fatal("上传失败应返回错误")
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if got := string(ol.files["/nd/docs/a.docx"]); got != "OLD" {
		t.Fatalf("上传失败不得截断原文件，实际 %q", got)
	}
	if len(ol.renames) != 0 {
		t.Fatalf("上传失败不得 rename: %+v", ol.renames)
	}
	for p := range ol.files {
		if strings.Contains(p, ".shade-tmp-") {
			t.Fatalf("失败后 staging 应清理: %s", p)
		}
	}
}

// 大小校验失败：不得替换原文件，staging 清理。
func TestFsPutSizeMismatchKeepsOriginal(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	ol.sizeMismatch = true
	c := newAtomicClient(ol.srv)
	if err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3); err == nil {
		t.Fatal("大小不符应返回错误")
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if got := string(ol.files["/nd/docs/a.docx"]); got != "OLD" {
		t.Fatalf("校验失败不得替换原文件，实际 %q", got)
	}
	if len(ol.renames) != 0 {
		t.Fatalf("校验失败不得 rename: %+v", ol.renames)
	}
	for p := range ol.files {
		if strings.Contains(p, ".shade-tmp-") {
			t.Fatalf("失败后 staging 应清理: %s", p)
		}
	}
}

// rename 不覆盖：先备份原文件，再替换；成功后删除备份。
func TestFsPutRenameNoOverwriteUsesBackup(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	ol.noOverwrite = true
	c := newAtomicClient(ol.srv)
	if err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3); err != nil {
		t.Fatalf("rename 不覆盖时应走备份流程成功: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if got := string(ol.files["/nd/docs/a.docx"]); got != "NEW" {
		t.Fatalf("目标应为 NEW，实际 %q", got)
	}
	for p := range ol.files {
		if strings.Contains(p, ".shade-tmp-") || strings.Contains(p, ".shade-bak-") {
			t.Fatalf("成功后悔不应残留 staging/备份: %s", p)
		}
	}
}

// 备份后替换仍失败：恢复原文件，staging 清理。
func TestFsPutReplaceFailureRestoresOriginal(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	ol.noOverwrite = true
	// 首次 rename 探路与备份后的替换都失败 → 必须恢复备份。
	ol.failRenameTmpPrefix = ".a.docx.shade-tmp-"
	c := newAtomicClient(ol.srv)
	err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3)
	if err == nil {
		t.Fatal("替换持续失败应返回错误")
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	if got := string(ol.files["/nd/docs/a.docx"]); got != "OLD" {
		t.Fatalf("替换失败必须恢复原文件，实际 %q", got)
	}
	for p := range ol.files {
		if strings.Contains(p, ".shade-tmp-") || strings.Contains(p, ".shade-bak-") {
			t.Fatalf("失败后不得残留 staging/备份: %s", p)
		}
	}
}

// PutEmpty 必须复用原子写层：覆盖已有文件时先 staging 再 rename，不得直接截断原路径。
func TestPutEmptyUsesAtomicLayerToOverwrite(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.txt": "OLD"})
	c := newAtomicClient(ol.srv)
	if err := c.PutEmpty(context.Background(), "/nd/docs/a.txt"); err != nil {
		t.Fatalf("PutEmpty: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	data, exists := ol.files["/nd/docs/a.txt"]
	if !exists || len(data) != 0 {
		t.Fatalf("目标应为 0 字节: exists=%v len=%d", exists, len(data))
	}
	if len(ol.puts) != 1 || ol.puts[0] == "/nd/docs/a.txt" {
		t.Fatalf("PutEmpty 必须写 staging 而非直写原路径: %v", ol.puts)
	}
	if !strings.HasPrefix(ol.puts[0], "/nd/docs/.") || !strings.Contains(ol.puts[0], ".shade-tmp-") {
		t.Fatalf("staging 必须同目录且唯一: %s", ol.puts[0])
	}
	for p := range ol.files {
		if strings.Contains(p, ".shade-tmp-") {
			t.Fatalf("成功后不得残留 staging: %s", p)
		}
	}
}

// 备份恢复失败：不得声称已恢复，错误必须点名备份路径，且备份文件仍可读。
func TestFsPutRestoreFailureReportsBackupPath(t *testing.T) {
	ol := newAtomicOL(t, map[string]string{"/nd/docs/a.docx": "OLD"})
	ol.noOverwrite = true
	ol.failRenameTmpPrefix = ".a.docx.shade-tmp-"
	ol.failRenameBakPrefix = ".a.docx.shade-bak-"
	c := newAtomicClient(ol.srv)
	err := c.FsPut(context.Background(), "/nd/docs/a.docx", strings.NewReader("NEW"), 3)
	if err == nil {
		t.Fatal("恢复失败必须返回错误")
	}
	if !strings.Contains(err.Error(), "恢复失败") || !strings.Contains(err.Error(), ".shade-bak-") {
		t.Fatalf("错误必须明确恢复失败并给出备份路径: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	var backup string
	for p, data := range ol.files {
		if strings.Contains(p, ".shade-bak-") {
			backup = p
			if string(data) != "OLD" {
				t.Fatalf("备份内容应为原文件 OLD: %q", data)
			}
		}
		if strings.Contains(p, ".shade-tmp-") {
			t.Fatalf("失败后不得残留 staging: %s", p)
		}
	}
	if backup == "" || !strings.Contains(err.Error(), backup) {
		t.Fatalf("错误应包含保留的备份路径 %q: %v", backup, err)
	}
}

// 空文件：允许 size=0 且 body 为空，最终目标为 0 字节。
func TestFsPutEmptyFile(t *testing.T) {
	ol := newAtomicOL(t, nil)
	c := newAtomicClient(ol.srv)
	if err := c.FsPut(context.Background(), "/nd/docs/empty.txt", strings.NewReader(""), 0); err != nil {
		t.Fatalf("空文件写入失败: %v", err)
	}
	ol.mu.Lock()
	defer ol.mu.Unlock()
	data, exists := ol.files["/nd/docs/empty.txt"]
	if !exists || len(data) != 0 {
		t.Fatalf("空文件应存在且 0 字节: exists=%v len=%d", exists, len(data))
	}
}
