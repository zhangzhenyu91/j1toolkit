package usbcmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeLink struct {
	files  map[string][]byte
	mounts int
	lists  int
}

func (f *fakeLink) MountToHost(context.Context) error { f.mounts++; return nil }
func (f *fakeLink) ListFiles(context.Context) ([]string, error) {
	f.lists++
	out := make([]string, 0, len(f.files))
	for n := range f.files {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeLink) DownloadFile(_ context.Context, name string) ([]byte, error) {
	if data, ok := f.files[name]; ok {
		return data, nil
	}
	return nil, nil
}
func (f *fakeLink) DeleteFiles(_ context.Context, names []string) error {
	for _, n := range names {
		delete(f.files, n)
	}
	return nil
}

func resultJSON(t *testing.T, app, id string, ok bool) []byte {
	t.Helper()
	raw, err := json.Marshal(Result{Version: 1, ID: id, App: app, OK: ok})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPayloadKeepsLegacyAndNewFields(t *testing.T) {
	now := time.Date(2026, 11, 3, 23, 50, 0, 0, time.UTC)
	raw := Payload("tree_sync", "20261103-2350-abcd", "snapshot", "a.xlsx", now, time.Hour)
	var cmd Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ID != cmd.CycleID || cmd.App != "tree_sync" || cmd.Action != "snapshot" || cmd.Kind != "snapshot" {
		t.Fatalf("payload = %+v", cmd)
	}
	if cmd.Params["file"] != "a.xlsx" || cmd.File != "a.xlsx" {
		t.Fatalf("file = %+v", cmd)
	}
	if cmd.ExpiresAt == "" {
		t.Fatal("expires_at 缺失")
	}
}

func TestResultNameSanitizesID(t *testing.T) {
	if got := ResultName("tree_sync", "a/b:c"); got != "tree_sync_result_a_b_c.json" {
		t.Fatalf("ResultName = %q", got)
	}
}

func TestAwaitReturnsAndConsumesResult(t *testing.T) {
	const app, id = "tree_sync", "20261103-2350-abcd"
	link := &fakeLink{files: map[string][]byte{
		CmdName(app):        []byte("{}"),
		ResultName(app, id): resultJSON(t, app, id, true),
	}}
	res, err := Await(context.Background(), link, app, id, Policy{Waits: []time.Duration{0}, Total: time.Second})
	if err != nil || !res.OK {
		t.Fatalf("Await = %+v %v", res, err)
	}
	if _, ok := link.files[ResultName(app, id)]; ok {
		t.Fatal("结果文件应被消费")
	}
	if _, ok := link.files[CmdName(app)]; ok {
		t.Fatal("残留指令应被清理")
	}
}

func TestAwaitTimesOutWithoutRemountOnSingleShot(t *testing.T) {
	link := &fakeLink{files: map[string][]byte{}}
	_, err := Await(context.Background(), link, "tree_sync", "id-1",
		Policy{Waits: []time.Duration{0}, Total: time.Millisecond})
	if err == nil {
		t.Fatal("无结果应超时")
	}
	if link.mounts != 0 {
		t.Fatalf("单次策略取回后不应重挂 U 盘，mounts=%d", link.mounts)
	}
}

func TestAwaitRemountsBetweenChecks(t *testing.T) {
	link := &fakeLink{files: map[string][]byte{}}
	_, err := Await(context.Background(), link, "tree_sync", "id-1b",
		Policy{Waits: []time.Duration{0, 0}, Total: time.Second})
	if err == nil {
		t.Fatal("无结果应超时")
	}
	if link.mounts != 1 {
		t.Fatalf("只有下一检查点才重挂一次，mounts=%d", link.mounts)
	}
}

// 收尾复挂必须忽略已取消的 ctx：轮次超时/关闭时盘不能被留在非共享状态。
func TestRestoreShareMountsWhenContextCanceled(t *testing.T) {
	link := &fakeLink{files: map[string][]byte{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RestoreShare(ctx, link)
	if link.mounts != 1 {
		t.Fatalf("收尾应复挂一次，mounts=%d", link.mounts)
	}
}

func TestAwaitTimeoutIncludesClientStatus(t *testing.T) {
	status := []byte(`{"last_seen":"2026-11-04T20:11:58+08:00","state":"idle",` +
		`"usb":{"mount":"/media/lenovo/GLKVM4"},"last_error":"指令已过期（expires_at=2026-11-04T10:38:06+08:00）"}`)
	link := &fakeLink{files: map[string][]byte{StatusName: status}}
	_, err := Await(context.Background(), link, "tree_sync", "id-2",
		Policy{Waits: []time.Duration{0}, Total: time.Millisecond})
	if err == nil {
		t.Fatal("无结果应超时")
	}
	text := err.Error()
	for _, want := range []string{"2026-11-04T20:11:58", "/media/lenovo/GLKVM4", "指令已过期"} {
		if !strings.Contains(text, want) {
			t.Fatalf("超时原因缺少 %q: %s", want, text)
		}
	}
}

func TestAwaitTimeoutWithoutClientStatus(t *testing.T) {
	link := &fakeLink{files: map[string][]byte{}}
	_, err := Await(context.Background(), link, "tree_sync", "id-3",
		Policy{Waits: []time.Duration{0}, Total: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "未上报状态") {
		t.Fatalf("缺少未上报提示: %v", err)
	}
}

func TestAwaitIgnoresStaleResultAndKeepsWaitingForCurrent(t *testing.T) {
	const app, id = "tree_sync", "current"
	link := &fakeLink{files: map[string][]byte{
		ResultName(app, "old"): resultJSON(t, app, "old", true),
		ResultName(app, id):    resultJSON(t, app, id, false),
	}}
	res, err := Await(context.Background(), link, app, id, Policy{Waits: []time.Duration{0}, Total: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != id || res.OK {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := link.files[ResultName(app, "old")]; ok {
		t.Fatal("旧轮次结果应被清理")
	}
}

type fakeAudit struct {
	recorded []Result
	timeouts []string
}

func (f *fakeAudit) Record(_ context.Context, res Result) error {
	f.recorded = append(f.recorded, res)
	return nil
}
func (f *fakeAudit) Timeout(_ context.Context, app, id, errText string) error {
	f.timeouts = append(f.timeouts, app+"/"+id+"/"+errText)
	return nil
}

func TestAwaitRecordsAudit(t *testing.T) {
	const app, id = "tree_sync", "audit-1"
	sink := &fakeAudit{}
	SetAudit(sink)
	t.Cleanup(func() { SetAudit(nil) })
	link := &fakeLink{files: map[string][]byte{
		ResultName(app, id): resultJSON(t, app, id, true),
	}}
	if _, err := Await(context.Background(), link, app, id, Policy{Waits: []time.Duration{0}, Total: time.Second}); err != nil {
		t.Fatal(err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].ID != id || !sink.recorded[0].OK {
		t.Fatalf("audit = %+v", sink.recorded)
	}
}

func TestAwaitRecordsAuditTimeout(t *testing.T) {
	const app, id = "tree_sync", "audit-2"
	sink := &fakeAudit{}
	SetAudit(sink)
	t.Cleanup(func() { SetAudit(nil) })
	link := &fakeLink{files: map[string][]byte{}}
	if _, err := Await(context.Background(), link, app, id, Policy{Waits: []time.Duration{0}, Total: time.Millisecond}); err == nil {
		t.Fatal("应超时")
	}
	if len(sink.timeouts) != 1 || !strings.Contains(sink.timeouts[0], id) {
		t.Fatalf("timeouts = %+v", sink.timeouts)
	}
}

// 合批 snapshot：一次等待、一次取回两条结果，结果与各自指令都被消费。
func TestAwaitManyReturnsBothAfterOneCollection(t *testing.T) {
	const treeApp, treeID = "tree_sync", "batch-tree"
	const crossApp, crossID = "crossing_sync", "batch-cross"
	link := &fakeLink{files: map[string][]byte{
		CmdName(treeApp):              []byte("{}"),
		CmdName(crossApp):             []byte("{}"),
		ResultName(treeApp, treeID):   resultJSON(t, treeApp, treeID, true),
		ResultName(crossApp, crossID): resultJSON(t, crossApp, crossID, true),
	}}
	refs := []CommandRef{{App: treeApp, ID: treeID}, {App: crossApp, ID: crossID}}
	out := AwaitMany(context.Background(), link, refs, Policy{Waits: []time.Duration{0}, Total: time.Second})
	if len(out) != len(refs) {
		t.Fatalf("每个引用都应有结果，got %+v", out)
	}
	for _, ref := range refs {
		o, ok := out[ref]
		if !ok || o.Err != nil || !o.Result.OK || o.Result.ID != ref.ID {
			t.Fatalf("%s/%s 应成功取回，got %+v err=%v", ref.App, ref.ID, o.Result, o.Err)
		}
		if _, exists := link.files[ResultName(ref.App, ref.ID)]; exists {
			t.Fatalf("%s 结果文件应被消费", ref.ID)
		}
		if _, exists := link.files[CmdName(ref.App)]; exists {
			t.Fatalf("%s 已命中指令应被清理", ref.App)
		}
	}
	if link.lists != 1 {
		t.Fatalf("同一窗口只应列盘一次，lists=%d", link.lists)
	}
}

// 一条命中一条超时：命中的结果不被失败分支吞掉，缺失指令保留，末轮不再重挂。
func TestAwaitManyKeepsPartialSuccess(t *testing.T) {
	const treeApp, treeID = "tree_sync", "batch-tree-2"
	const crossApp, crossID = "crossing_sync", "batch-cross-2"
	status := []byte(`{"last_seen":"2026-11-04T20:11:58+08:00","state":"idle","usb":{"mount":"/media/lenovo/GLKVM4"}}`)
	link := &fakeLink{files: map[string][]byte{
		CmdName(treeApp):              []byte("{}"),
		CmdName(crossApp):             []byte("{}"),
		ResultName(crossApp, crossID): resultJSON(t, crossApp, crossID, true),
		StatusName:                    status,
	}}
	refs := []CommandRef{{App: treeApp, ID: treeID}, {App: crossApp, ID: crossID}}
	out := AwaitMany(context.Background(), link, refs, Policy{Waits: []time.Duration{0}, Total: time.Millisecond})
	cross := out[CommandRef{App: crossApp, ID: crossID}]
	if cross.Err != nil || !cross.Result.OK {
		t.Fatalf("交跨命中结果不应被树障超时吞掉，got %+v err=%v", cross.Result, cross.Err)
	}
	tree := out[CommandRef{App: treeApp, ID: treeID}]
	if tree.Err == nil || !strings.Contains(tree.Err.Error(), "超时") {
		t.Fatalf("树障应得到带超时的结果，got %v", tree.Err)
	}
	if !strings.Contains(tree.Err.Error(), "2026-11-04T20:11:58") {
		t.Fatalf("超时原因应带状态说明，got %v", tree.Err)
	}
	if _, exists := link.files[ResultName(crossApp, crossID)]; exists {
		t.Fatal("命中结果应被消费")
	}
	if _, exists := link.files[CmdName(treeApp)]; !exists {
		t.Fatal("未命中应用的指令不应被删除")
	}
	if link.mounts != 0 {
		t.Fatalf("末轮取回后不应重挂，mounts=%d", link.mounts)
	}
	if link.lists != 1 {
		t.Fatalf("单检查点只应列盘一次，lists=%d", link.lists)
	}
}

// 逐 ref 审计：命中写结果审计，缺失写对应 app/id 的超时审计。
func TestAwaitManyRecordsAuditPerCommand(t *testing.T) {
	const treeApp, treeID = "tree_sync", "audit-tree"
	const crossApp, crossID = "crossing_sync", "audit-cross"
	sink := &fakeAudit{}
	SetAudit(sink)
	t.Cleanup(func() { SetAudit(nil) })
	link := &fakeLink{files: map[string][]byte{
		ResultName(treeApp, treeID): resultJSON(t, treeApp, treeID, true),
	}}
	refs := []CommandRef{{App: treeApp, ID: treeID}, {App: crossApp, ID: crossID}}
	AwaitMany(context.Background(), link, refs, Policy{Waits: []time.Duration{0}, Total: time.Millisecond})
	if len(sink.recorded) != 1 || sink.recorded[0].ID != treeID || sink.recorded[0].App != treeApp {
		t.Fatalf("应记录树障结果审计，got %+v", sink.recorded)
	}
	if len(sink.timeouts) != 1 || !strings.HasPrefix(sink.timeouts[0], crossApp+"/"+crossID+"/") {
		t.Fatalf("应记录交跨超时审计，got %+v", sink.timeouts)
	}
}

// 多检查点：仍有缺失才在下一个检查点前重挂一次，不为单个缺失 ref 单独满等。
func TestAwaitManyRemountsOnceBetweenChecks(t *testing.T) {
	const treeApp, treeID = "tree_sync", "batch-tree-3"
	const crossApp, crossID = "crossing_sync", "batch-cross-3"
	link := &fakeLink{files: map[string][]byte{
		CmdName(treeApp):  []byte("{}"),
		CmdName(crossApp): []byte("{}"),
	}}
	refs := []CommandRef{{App: treeApp, ID: treeID}, {App: crossApp, ID: crossID}}
	out := AwaitMany(context.Background(), link, refs, Policy{Waits: []time.Duration{0, 0}, Total: time.Second})
	for _, ref := range refs {
		if out[ref].Err == nil {
			t.Fatalf("%s 无结果应超时", ref.ID)
		}
	}
	if link.mounts != 1 {
		t.Fatalf("两个检查点之间只应重挂一次，mounts=%d", link.mounts)
	}
	if link.lists != 2 {
		t.Fatalf("每个检查点应列盘一次，lists=%d", link.lists)
	}
}

func TestSessionTryAcquire(t *testing.T) {
	s := NewSession()
	if !s.TryAcquire() {
		t.Fatal("首次应抢到")
	}
	if s.TryAcquire() {
		t.Fatal("占用中不应再抢到")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Fatal("释放后应可再抢")
	}
	s.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	s.Release()
}
