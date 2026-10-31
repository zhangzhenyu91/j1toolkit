package netdisk

// office 桥路径口径测试（fake OpenList 记录请求绝对路径）：无前导斜杠/反斜杠/中文多级目录
// 均归一放行，越界与空间/班组口径继续拦截；不访问真实 OpenList。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// recOL 假 OpenList：记录 fs/get、fs/put 请求体里的 path；fs/get 恒返回文件元信息。
func recOL(t *testing.T, gotPaths *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		switch {
		case r.URL.Path == "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case r.URL.Path == "/api/fs/get":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Path != "" {
				*gotPaths = append(*gotPaths, req.Path)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"name":"x","is_dir":false,"size":3,"modified":"2026-10-27 10:00:00"}}`))
		case r.URL.Path == "/api/fs/put":
			// FsPut 路径在 File-Path 头（URL 编码），body 为文件字节。
			p := r.Header.Get("File-Path")
			if dec, err := url.PathUnescape(p); err == nil {
				p = dec
			}
			if p != "" {
				*gotPaths = append(*gotPaths, p)
			}
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBridgePathForms(t *testing.T) {
	var got []string
	srv := recOL(t, &got)
	m := New(openFakeDB(t))
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	// 无前导斜杠（前端 _rel 口径）/ 反斜杠 / 重复斜杠：均归一为同一绝对路径。
	want := "/nd/my/alice/报表/2026 年度/汇总.xlsx"
	for _, rel := range []string{
		"报表/2026 年度/汇总.xlsx",
		"/报表/2026 年度/汇总.xlsx",
		`\报表\2026 年度\汇总.xlsx`,
		"//报表///2026 年度/汇总.xlsx",
	} {
		got = got[:0]
		if _, _, err := m.ReadFileBytes(ctx, "my", "alice", 0, rel); err != nil {
			t.Fatalf("ReadFileBytes(%q): %v", rel, err)
		}
		// /api/fs/get 元信息 + 下载取签（均带 path）：每条请求都必须是同一归一绝对路径。
		if len(got) == 0 {
			t.Fatalf("ReadFileBytes(%q) 未触达 OpenList", rel)
		}
		for _, g := range got {
			if g != want {
				t.Fatalf("ReadFileBytes(%q) 归一错误: %q want %q", rel, g, want)
			}
		}
	}

	// 写回同口径归一。
	got = got[:0]
	if err := m.WriteFileBytes(ctx, "my", "alice", 0, "报表/2026 年度/汇总.xlsx", []byte("NEW")); err != nil {
		t.Fatalf("WriteFileBytes: %v", err)
	}
	// 原子写：上传落在同目录隐藏 staging（由归一后的目标路径派生），不得写到其他路径。
	stagingPrefix := want[:strings.LastIndexByte(want, '/')+1] + "." + want[strings.LastIndexByte(want, '/')+1:] + ".shade-tmp-"
	if len(got) == 0 {
		t.Fatalf("WriteFileBytes 未触达 OpenList")
	}
	for _, g := range got {
		if g != want && !strings.HasPrefix(g, stagingPrefix) {
			t.Fatalf("WriteFileBytes 归一错误: %q want %q 或 %q*", g, want, stagingPrefix)
		}
	}

	// 文档 key 同口径（fs/get 路径归一）。
	got = got[:0]
	if _, err := m.DocKey(ctx, "my", "alice", 0, "报表/2026 年度/汇总.xlsx"); err != nil {
		t.Fatalf("DocKey: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("DocKey 归一错误: %v", got)
	}
}

// TestBridgeSpaceRoots 空间/班组核对：my 个人根；public 按班组隔离；未分配班组无公共区；
// 非法空间报「参数错误：space」。
func TestBridgeSpaceRoots(t *testing.T) {
	var got []string
	srv := recOL(t, &got)
	m := New(openFakeDB(t)) // fakeDB：sys_team 名称查询返回 检修一班
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	// my 个人空间 = {personal}/{username}。
	got = got[:0]
	if _, _, err := m.ReadFileBytes(ctx, "my", "alice", 5, "a.docx"); err != nil {
		t.Fatalf("my 空间: %v", err)
	}
	if got[0] != "/nd/my/alice/a.docx" {
		t.Fatalf("my 空间根错误: %q", got[0])
	}

	// public 公共区 = {public}/{班组名}（fakeDB 返回 检修一班）。
	got = got[:0]
	if _, _, err := m.ReadFileBytes(ctx, "public", "alice", 5, "a.docx"); err != nil {
		t.Fatalf("public 空间: %v", err)
	}
	if got[0] != "/nd/pub/检修一班/a.docx" {
		t.Fatalf("public 空间根错误: %q", got[0])
	}

	// 未分配班组（teamID=0）：无公共区。
	var nf *NotFoundError
	_, _, err := m.ReadFileBytes(ctx, "public", "alice", 0, "a.docx")
	if !errors.As(err, &nf) || nf.Message != "未分配班组，无公共区" {
		t.Fatalf("未分配班组口径错误: %v", err)
	}

	// 非法空间（space=team 等）：参数错误：space。
	_, _, err = m.ReadFileBytes(ctx, "team", "alice", 5, "a.docx")
	if !errors.As(err, &nf) || nf.Message != "参数错误：space" {
		t.Fatalf("非法空间口径错误: %v", err)
	}
}

// TestBridgePathTraversalBlocked 越界与根路径继续拦截（officeAbs 不放宽 NormRel 口径）。
func TestBridgePathTraversalBlocked(t *testing.T) {
	var got []string
	srv := recOL(t, &got)
	m := New(openFakeDB(t))
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	var nf *NotFoundError
	for _, rel := range []string{"/../etc/passwd", "a/../../b.docx", "/a/./b.docx", "/"} {
		_, _, err := m.ReadFileBytes(ctx, "my", "alice", 0, rel)
		if !errors.As(err, &nf) {
			t.Fatalf("rel(%q) 应 NotFoundError: %v", rel, err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("越界不应触达 OpenList: %v", got)
	}
}

// TestBridgePublicTeamOwnership 公共区兜底归属校验：Office 已按身份收敛 team_id 后，网盘桥
// 仍按 sys_user 角色/班组复核——非超管只能读写本班公共区，超管保留跨班能力；拒绝时不触达 OpenList。
func TestBridgePublicTeamOwnership(t *testing.T) {
	var got []string
	srv := recOL(t, &got)
	db, _ := openFakeDBUser(t, "user", 5)
	m := New(db)
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	// 本班公共区：放行。
	if _, _, err := m.ReadFileBytes(ctx, "public", "alice", 5, "a.docx"); err != nil {
		t.Fatalf("本班公共区应放行: %v", err)
	}

	// 他班公共区：拒绝且不触达 OpenList。
	got = got[:0]
	var nf *NotFoundError
	_, _, err := m.ReadFileBytes(ctx, "public", "alice", 6, "a.docx")
	if !errors.As(err, &nf) || nf.Message != "无权访问其他班组的数据" {
		t.Fatalf("他班公共区应拒绝: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("拒绝时不应触达 OpenList: %v", got)
	}

	// 写回同口径拒绝。
	got = got[:0]
	if err := m.WriteFileBytes(ctx, "public", "alice", 6, "a.docx", []byte("NEW")); !errors.As(err, &nf) {
		t.Fatalf("他班写回应拒绝: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("写回拒绝时不应触达 OpenList: %v", got)
	}

	// 超管保留跨班能力。
	adminDB, _ := openFakeDBUser(t, "admin", 9)
	m2 := New(adminDB)
	m2.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m2.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	if _, _, err := m2.ReadFileBytes(ctx, "public", "alice", 6, "a.docx"); err != nil {
		t.Fatalf("超管跨班读取应放行: %v", err)
	}
}
