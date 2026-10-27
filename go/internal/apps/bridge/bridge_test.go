// 桥接装配测试：网盘三适配（保存路径/流式直传/错误映射/权限门）与 CaptureSink
// （kind 分派、token 传递、/ingest 原 JSON 应答重建）。依赖均可注入假实现，不访问真实云服务。
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"shade/internal/apps/crossing"
	"shade/internal/apps/kvm"
	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
	"shade/internal/apps/safeday"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/worklog"
	"shade/internal/auth"
)

// ---- 假 Ops ----

// fakeOps 可控网盘读写桩：记录 Save 入参并按脚本返回。
type fakeOps struct {
	savedSpace, savedDir, savedName string
	savedBody                       []byte
	savedSize                       int64
	savePath                        string
	saveErr                         error
	downloadData                    []byte
	downloadErr                     error
	allow                           bool
	allowErr                        error
}

func (f *fakeOps) Save(_ context.Context, _ auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error) {
	f.savedSpace, f.savedDir, f.savedName, f.savedSize = space, dir, name, size
	if body != nil {
		b, _ := io.ReadAll(body)
		f.savedBody = b
	}
	if f.saveErr != nil {
		return "", f.saveErr
	}
	return f.savePath, nil
}

func (f *fakeOps) Download(context.Context, auth.Identity, string, string) ([]byte, error) {
	return f.downloadData, f.downloadErr
}

func (f *fakeOps) Allow(context.Context, int64) (bool, error) {
	if f.allowErr != nil {
		return false, f.allowErr
	}
	return f.allow, nil
}

// ---- 网盘保存适配 ----

func TestWorklogSaverSave(t *testing.T) {
	ops := &fakeOps{savePath: "我的空间/出工日志/工作任务单.docx"}
	s := &WorklogSaver{O: ops}
	user := auth.Identity{ID: 1, Username: "zhang", TeamID: 2}
	path, err := s.SaveToNetdisk(context.Background(), user, worklog.NetdiskSaveRequest{
		Space: "my", Dir: "出工日志", Name: "工作任务单.docx", Body: []byte("docx-bytes"),
	})
	if err != nil {
		t.Fatalf("SaveToNetdisk: %v", err)
	}
	if path != "我的空间/出工日志/工作任务单.docx" {
		t.Fatalf("path = %q", path)
	}
	if ops.savedSpace != "my" || ops.savedDir != "出工日志" || ops.savedName != "工作任务单.docx" {
		t.Fatalf("args = %q/%q/%q", ops.savedSpace, ops.savedDir, ops.savedName)
	}
	if string(ops.savedBody) != "docx-bytes" || ops.savedSize != 10 {
		t.Fatalf("body = %q size = %d", ops.savedBody, ops.savedSize)
	}
}

func TestWorklogSaverBizError(t *testing.T) {
	ops := &fakeOps{saveErr: &opError{status: 400, code: 40310, msg: "未分配班组，无公共区"}}
	s := &WorklogSaver{O: ops}
	_, err := s.SaveToNetdisk(context.Background(), auth.Identity{}, worklog.NetdiskSaveRequest{Name: "a.docx"})
	var nerr *worklog.NetdiskSaveError
	if !errors.As(err, &nerr) {
		t.Fatalf("err = %v, want *worklog.NetdiskSaveError", err)
	}
	if nerr.Status != 400 || nerr.Code != 40310 || nerr.Message != "未分配班组，无公共区" {
		t.Fatalf("NetdiskSaveError = %+v", nerr)
	}
}

func TestWorklogSaverUpstreamPassthrough(t *testing.T) {
	boom := errors.New("网盘服务异常，请稍后再试")
	ops := &fakeOps{saveErr: boom}
	s := &WorklogSaver{O: ops}
	_, err := s.SaveToNetdisk(context.Background(), auth.Identity{}, worklog.NetdiskSaveRequest{Name: "a.docx"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want passthrough", err)
	}
	var nerr *worklog.NetdiskSaveError
	if errors.As(err, &nerr) {
		t.Fatalf("上游错误不应映射 NetdiskSaveError：%v", err)
	}
}

func TestSafedaySaverSave(t *testing.T) {
	ops := &fakeOps{savePath: "公共区/安全日记录/2026.01.01.docx"}
	s := &SafedaySaver{O: ops}
	user := auth.Identity{ID: 3, Username: "li", TeamID: 9}
	path, err := s.SaveToNetdisk(context.Background(), user, safeday.NetdiskSaveRequest{
		Space: "public", Dir: "安全日记录", Name: "2026.01.01.docx", Body: []byte("sd"),
	})
	if err != nil || path != "公共区/安全日记录/2026.01.01.docx" {
		t.Fatalf("path = %q err = %v", path, err)
	}
	if ops.savedSpace != "public" || string(ops.savedBody) != "sd" {
		t.Fatalf("args = %q body = %q", ops.savedSpace, ops.savedBody)
	}
}

// ---- kvm 互推桥 ----

func TestKvmBridgeAssertAccess(t *testing.T) {
	b := &KvmBridge{O: &fakeOps{allow: false}}
	err := b.AssertAccess(context.Background(), 7)
	var be *kvm.BizError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *kvm.BizError", err)
	}
	if be.Code != 40301 || be.Status != 403 {
		t.Fatalf("BizError = %+v", be)
	}
	b = &KvmBridge{O: &fakeOps{allow: true}}
	if err := b.AssertAccess(context.Background(), 7); err != nil {
		t.Fatalf("allow: %v", err)
	}
}

func TestKvmBridgeSaveStreams(t *testing.T) {
	ops := &fakeOps{savePath: "我的空间/文件传输/录屏.mp4"}
	b := &KvmBridge{O: ops}
	user := auth.Identity{ID: 1, Username: "zhang", TeamID: 2}
	body := strings.NewReader("stream-bytes")
	path, err := b.Save(context.Background(), user, "my", "文件传输", "录屏.mp4", body, 12)
	if err != nil || path != "我的空间/文件传输/录屏.mp4" {
		t.Fatalf("path = %q err = %v", path, err)
	}
	// 流式口径：io.Reader 原样交底层直传（size 按已知字节数透传，≤0 亦放行）
	if string(ops.savedBody) != "stream-bytes" || ops.savedSize != 12 {
		t.Fatalf("body = %q size = %d", ops.savedBody, ops.savedSize)
	}
}

func TestKvmBridgeDownload(t *testing.T) {
	ops := &fakeOps{downloadData: []byte("file-bytes")}
	b := &KvmBridge{O: ops}
	data, err := b.Download(context.Background(), auth.Identity{}, "public", "/a/b.txt")
	if err != nil || string(data) != "file-bytes" {
		t.Fatalf("data = %q err = %v", data, err)
	}
	ops.downloadErr = &opError{status: 400, code: 40001, msg: "路径不合法"}
	_, err = b.Download(context.Background(), auth.Identity{}, "public", "/../x")
	var be *kvm.BizError
	if !errors.As(err, &be) || be.Message != "路径不合法" {
		t.Fatalf("err = %v, want BizError 路径不合法", err)
	}
}

// ---- 网盘底层 ops（真实实现的关键口径） ----

func TestNetdiskOpsSavePathLabel(t *testing.T) {
	ol := &fakeOL{}
	nd := &netdisk.Module{OL: ol, Base: netdisk.BaseDirs{PersonalRoot: "/p", PublicRoot: "/u"}}
	o := NewOps(nd)
	user := auth.Identity{Username: "zhang", TeamID: 2}
	path, err := o.Save(context.Background(), user, "my", "出工日志", "a.docx", strings.NewReader("x"), 1)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if path != "我的空间/出工日志/a.docx" {
		t.Fatalf("path = %q", path)
	}
	if ol.putPath != "/p/zhang/出工日志/a.docx" {
		t.Fatalf("putPath = %q", ol.putPath)
	}
	if ol.ensureDir != "/p/zhang/出工日志" {
		t.Fatalf("ensureDir = %q", ol.ensureDir)
	}
	// 名称净化：路径分隔符替换为 _
	if _, err := o.Save(context.Background(), user, "my", "", "a/b.docx", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("Save sanitize: %v", err)
	}
	if ol.putPath != "/p/zhang/a_b.docx" {
		t.Fatalf("putPath = %q", ol.putPath)
	}
	// 非法空间 → 业务错误
	_, err = o.Save(context.Background(), user, "other", "", "a.docx", strings.NewReader("x"), 1)
	var oe *opError
	if !errors.As(err, &oe) || oe.msg != "参数错误：space" {
		t.Fatalf("err = %v", err)
	}
}

// fakeOL OpenList 桩（EnsureDir/FsPut 记录入参）。
type fakeOL struct {
	ensureDir string
	putPath   string
	putBody   []byte
}

func (f *fakeOL) EnsureConfigured() error { return nil }

func (f *fakeOL) Call(context.Context, string, string, netdisk.CallOpts) (json.RawMessage, error) {
	return nil, nil
}

func (f *fakeOL) DownloadStream(context.Context, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, errors.New("not used")
}

func (f *fakeOL) EnsureDir(_ context.Context, abs string) error {
	f.ensureDir = abs
	return nil
}

func (f *fakeOL) PutEmpty(context.Context, string) error { return nil }

func (f *fakeOL) FsPut(_ context.Context, abs string, body io.Reader, _ int64) error {
	f.putPath = abs
	b, _ := io.ReadAll(body)
	f.putBody = b
	return nil
}

func (f *fakeOL) ArchiveDownloadStream(context.Context, string, string, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, errors.New("not used")
}

// ---- CaptureSink ----

// fakeTree / fakeCross 可控直推桩：记录 token 与载荷，按脚本返回。
type fakeTree struct {
	token  string
	raw    []byte
	result *treehazard.IngestResult
	err    error
}

func (f *fakeTree) Ingest(_ context.Context, token string, raw []byte) (*treehazard.IngestResult, error) {
	f.token, f.raw = token, raw
	return f.result, f.err
}

type fakeCross struct {
	token  string
	raw    []byte
	result *crossing.IngestResult
	err    error
}

func (f *fakeCross) Ingest(_ context.Context, token string, raw []byte) (*crossing.IngestResult, error) {
	f.token, f.raw = token, raw
	return f.result, f.err
}

// fakeCfg 配置桩。
type fakeCfg map[string]string

func (c fakeCfg) Get(_ context.Context, key string) (string, error) { return c[key], nil }

func TestCaptureSinkTreeDirect(t *testing.T) {
	ft := &fakeTree{result: &treehazard.IngestResult{Mode: "direct", Inserted: 1, Updated: 2, Skipped: 3, Photos: 4, PhotosFailed: 5, Deleted: 6}}
	s := &captureSink{tree: ft, cfg: fakeCfg{"TREEHAZARD_INGEST_TOKEN": "tk-tree"}}
	status, body, err := s.Process(context.Background(), KindTree, []byte(`{"mode":"direct"}`))
	if err != nil || status != 200 {
		t.Fatalf("status = %d err = %v", status, err)
	}
	if ft.token != "tk-tree" {
		t.Fatalf("token = %q（必须经 Ingest 密钥校验）", ft.token)
	}
	want := `{"code":0,"data":{"deleted":6,"inserted":1,"photos":4,"photos_failed":5,"skipped":3,"updated":2},"message":"ok"}`
	if string(body) != want {
		t.Fatalf("body = %s\nwant %s", body, want)
	}
}

func TestCaptureSinkTreeErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"token", treehazard.ErrIngestToken, 401, `"code":40104`},
		{"not-configured", treehazard.ErrIngestNotConfigured, 503, `"code":50301`},
		{"bad-json", treehazard.ErrIngestBadJSON, 400, `"code":40001`},
		{"param", &treehazard.IngestParamError{Msg: "参数错误：session_id 不能为空"}, 400, `"code":40001`},
		{"team", treehazard.ErrIngestTeamNotFound, 404, `"code":40400`},
		{"other", errors.New("db down"), 500, `"code":40001`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &captureSink{tree: &fakeTree{err: tc.err}}
			status, body, err := s.Process(context.Background(), KindTree, []byte(`{}`))
			if err != nil || status != tc.status {
				t.Fatalf("status = %d err = %v", status, err)
			}
			if !strings.Contains(string(body), tc.code) {
				t.Fatalf("body = %s, want %s", body, tc.code)
			}
		})
	}
}

func TestCaptureSinkCrossingStaging(t *testing.T) {
	fc := &fakeCross{result: &crossing.IngestResult{Mode: "staging", BatchID: 42, Count: 7, Deleted: 1}}
	s := &captureSink{cross: fc, cfg: fakeCfg{"CROSSING_INGEST_TOKEN": "tk-cross"}}
	status, body, err := s.Process(context.Background(), KindCrossing, []byte(`{"mode":"staging"}`))
	if err != nil || status != 200 {
		t.Fatalf("status = %d err = %v", status, err)
	}
	if fc.token != "tk-cross" {
		t.Fatalf("token = %q", fc.token)
	}
	want := `{"code":0,"data":{"batch_id":42,"count":7,"deleted":1},"message":"ok"}`
	if string(body) != want {
		t.Fatalf("body = %s\nwant %s", body, want)
	}
}

func TestCaptureSinkCrossingErrors(t *testing.T) {
	s := &captureSink{cross: &fakeCross{err: crossing.ErrIngestTeamNotFound}}
	status, body, _ := s.Process(context.Background(), KindCrossing, []byte(`{}`))
	if status != 404 || !strings.Contains(string(body), `"code":40400`) {
		t.Fatalf("status = %d body = %s", status, body)
	}
	s = &captureSink{cross: &fakeCross{err: errors.New("boom")}}
	status, body, _ = s.Process(context.Background(), KindCrossing, []byte(`{}`))
	if status != 500 || !strings.Contains(string(body), `"code":50000`) {
		t.Fatalf("status = %d body = %s", status, body)
	}
}

func TestCaptureSinkFallbacks(t *testing.T) {
	// kind 未知 → 400
	s := &captureSink{}
	status, body, err := s.Process(context.Background(), "unknown", []byte(`{}`))
	if err != nil || status != 400 || !strings.Contains(string(body), `"code":40001`) {
		t.Fatalf("status = %d body = %s err = %v", status, body, err)
	}
	// 模块未接入 → 503（fail-closed，交补推队列）
	status, _, _ = s.Process(context.Background(), KindTree, []byte(`{}`))
	if status != 503 {
		t.Fatalf("status = %d", status)
	}
	// 未配置 token：空串传入，由 Ingest 密钥校验 fail-closed（不绕开）
	ft := &fakeTree{result: &treehazard.IngestResult{Mode: "direct"}}
	s = &captureSink{tree: ft}
	_, _, _ = s.Process(context.Background(), KindTree, []byte(`{}`))
	if ft.token != "" {
		t.Fatalf("token = %q, want 空串交 Ingest 校验", ft.token)
	}
}

// ---- 装配 ----

func TestOfficeSourcesSkipsNil(t *testing.T) {
	if got := OfficeSources(nil, nil, nil, nil); len(got) != 0 {
		t.Fatalf("sources = %v, want 空", got)
	}
	wl := worklog.New(nil)
	sd := safeday.New(nil, nil)
	th := treehazard.New(nil)
	cr := crossing.New(nil, nil)
	got := OfficeSources(wl, sd, th, cr)
	for _, key := range []string{"wls", "sdr", "wlr", "thd", "crs"} {
		if got[key] == nil {
			t.Fatalf("缺少源 %s", key)
		}
	}
}

func TestWireSkipsNil(t *testing.T) {
	if sink := Wire(Deps{}); sink != nil {
		t.Fatalf("sink = %v, want nil", sink)
	}
	wl := worklog.New(nil)
	wm := Wire(Deps{Worklog: wl, Tree: treehazard.New(nil)})
	if wm == nil {
		t.Fatal("sink 不应为 nil")
	}
	if wl.Netdisk != nil {
		t.Fatal("未注入 netdisk 时不应挂网盘保存")
	}
}

// 编译期契约：装配产物可直接赋给各模块注入字段（main 一行 Wire 同口径）。
var (
	_ = func() {
		var wm *worklog.Module
		var sm *safeday.Module
		var km *kvm.Module
		var om *office.Module
		wm.Netdisk = NewWorklogSaver(nil)
		sm.Netdisk = NewSafedaySaver(nil)
		km.Netdisk = NewKvmBridge(nil)
		om.Netdisk = office.NewNetdiskDoc(nil)
	}
)
