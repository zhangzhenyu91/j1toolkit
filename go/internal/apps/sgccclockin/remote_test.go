package sgccclockin

// 商旅费用照片远端联动测试：COS 回源（httptest）→ uploadImageV2 → 费用模板 id=5 关联 →
// 本地 sgcc_img_id/sgcc_synced 收尾；远端先行失败本地零假成功；任务串行与 op/status、sync/active 一致。
// 全部 fake/httptest，不访问真实云服务/商旅。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"shade/internal/apps/worklog"
)

// remoteAPI 在 fakeAPI 之上捕获上传/费用保存调用（可按序注入失败）。
type remoteAPI struct {
	*fakeAPI
	mu           sync.Mutex
	uploaded     [][]byte
	uploadSeq    []UploadedImage // 依次返回的上传结果（耗尽后复用最后一个）
	uploadFailAt int             // 第 N 次上传失败（1 起；0 不失败）
	saveOvs      []map[string]string
	saveFailAt   int // 第 N 次费用保存返回失败应答（1 起；0 不失败）
}

func newRemoteAPI() *remoteAPI {
	return &remoteAPI{fakeAPI: &fakeAPI{feeSave: map[string]any{"statusCode": 200}}}
}

func (a *remoteAPI) UploadImageV2(_ context.Context, _ string, buf []byte, _ CallOpts) (UploadedImage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.uploaded = append(a.uploaded, buf)
	n := len(a.uploaded)
	if a.uploadFailAt != 0 && n == a.uploadFailAt {
		return UploadedImage{}, &remoteNamedError{msg: "商旅图片上传失败"}
	}
	if len(a.uploadSeq) == 0 {
		return UploadedImage{FileInfoID: "F0", HTTPURL: "http://img/0"}, nil
	}
	img := a.uploadSeq[0]
	if len(a.uploadSeq) > 1 {
		a.uploadSeq = a.uploadSeq[1:]
	}
	return img, nil
}

func (a *remoteAPI) SaveFeeInfoNew(_ context.Context, _ string, _ string, _ map[string]any, ov map[string]string, _ CallOpts) (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saveOvs = append(a.saveOvs, ov)
	if a.saveFailAt != 0 && len(a.saveOvs) == a.saveFailAt {
		return map[string]any{"statusCode": 500, "msg": "保存失败"}, nil
	}
	return a.feeSave, nil
}

// feeTplWithImages getFeeInfoNew 应答形状（data.body.clockTemplate，id=5 上传图片组件 value 为 JSON 数组）。
func feeTplWithImages(value string) map[string]any {
	return map[string]any{
		"statusCode": 200,
		"data": map[string]any{
			"body": map[string]any{
				"clockTemplate": map[string]any{
					"id": "T1", "name": "费用模板", "createtime": float64(0), "type": "1", "no": "N1",
					"dtComponentList": []any{
						map[string]any{"id": float64(5), "value": value},
					},
				},
			},
		},
	}
}

// TestUploadToMembersSuccess 两名绑定成员依次上传关联，返回 sgcc_img_id 链接 JSON。
func TestUploadToMembersSuccess(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{
		{FileInfoID: "F1", HTTPURL: "http://img/1"},
		{FileInfoID: "F2", HTTPURL: "http://img/2"},
	}
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	fdb.onRowsSeq("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsSeq("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{int64(12), int64(1), int64(8), int64(12), "139", "tok-b", "d", "s", int64(1)})

	links, err := m.UploadToMembers(context.Background(), 1, "2026-10-27", []string{"张三", "李四"}, []byte("JPEGDATA"))
	if err != nil {
		t.Fatalf("上传关联应成功: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(links), &got); err != nil {
		t.Fatalf("链接 JSON 不符: %s", links)
	}
	if got["11"] != "F1" || got["12"] != "F2" {
		t.Fatalf("链接留存不符: %v", got)
	}
	if len(api.uploaded) != 2 {
		t.Fatalf("应上传两次: %d", len(api.uploaded))
	}
	// 费用模板组件 id=5 追加 {fileInfoId,url}
	if len(api.saveOvs) != 2 {
		t.Fatalf("应保存两次费用模板: %d", len(api.saveOvs))
	}
	if !strings.Contains(api.saveOvs[0]["5"], `"fileInfoId":"F1"`) {
		t.Fatalf("组件 id=5 未追加上传图: %s", api.saveOvs[0]["5"])
	}
}

// TestUploadToMembersTokenExpired 登录过期硬失败：带人名抛错，落核查流水，不假成功。
func TestUploadToMembersTokenExpired(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{{FileInfoID: "F1"}}
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	fdb.onRowsSeq("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsSeq("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{int64(12), int64(1), int64(8), int64(12), "139", "tok-b", "d", "s", int64(0)})

	_, err := m.UploadToMembers(context.Background(), 1, "2026-10-27", []string{"张三", "李四"}, []byte("JPEG"))
	if err == nil {
		t.Fatalf("登录过期应失败")
	}
	if failedNameOf(err) != "李四" {
		t.Fatalf("失败人名不符: %v", err)
	}
	if !strings.Contains(err.Error(), "登录已过期") {
		t.Fatalf("错误文案不符: %v", err)
	}
	if len(fdb.callsWith("INSERT INTO worklog_sync_log")) == 0 {
		t.Fatalf("应记核查失败流水")
	}
}

// TestUploadToMembersUnboundSkipped 未绑定成员跳过不计失败（记核查流水）。
func TestUploadToMembersUnboundSkipped(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{{FileInfoID: "F1"}}
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	// 张三→11（有绑定账号）；王五→12（无绑定账号）
	fdb.onRowsSeq("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsSeq("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{})

	links, err := m.UploadToMembers(context.Background(), 1, "2026-10-27", []string{"张三", "王五"}, []byte("JPEG"))
	if err != nil {
		t.Fatalf("未绑定成员应跳过: %v", err)
	}
	var got map[string]string
	_ = json.Unmarshal([]byte(links), &got)
	if got["11"] != "F1" || len(got) != 1 {
		t.Fatalf("仅绑定成员留存链接: %v", got)
	}
	if len(fdb.callsWith("INSERT INTO worklog_sync_log")) == 0 {
		t.Fatalf("未绑定跳过应记核查流水")
	}
}

// TestSyncMembersCOSFetch 测试 SyncMembers：COS 回源（httptest）→ 缺链接补传 → sgcc_synced=1。
func TestSyncMembersCOSFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PHOTOBYTES"))
	}))
	defer srv.Close()
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{{FileInfoID: "F9", HTTPURL: "http://img/9"}}
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	fdb.onRowsAlways("SELECT p.url, p.members, p.sgcc_img_id", []string{"url", "members", "sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{srv.URL + "/p.jpg", `["张三"]`, "", "2026-10-27", int64(1)})
	fdb.onRowsAlways("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)})

	if err := m.SyncMembers(context.Background(), 9, nil); err != nil {
		t.Fatalf("补传应成功: %v", err)
	}
	updates := fdb.callsWith("UPDATE worklog_photo SET sgcc_img_id = ?, sgcc_synced = ?")
	if len(updates) != 1 {
		t.Fatalf("应落库一次链接与状态: %d", len(updates))
	}
	if fmt.Sprint(updates[0].args[1]) != "1" {
		t.Fatalf("成功应置 sgcc_synced=1: %v", updates[0].args)
	}
	if len(api.uploaded) != 1 || string(api.uploaded[0]) != "PHOTOBYTES" {
		t.Fatalf("应上传回源原文: %v", api.uploaded)
	}
}

// TestSyncMembersPartialFail 部分成功：已成链接即时留存，sgcc_synced=2（失败不假成功），抛错带人名。
func TestSyncMembersPartialFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PHOTOBYTES"))
	}))
	defer srv.Close()
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{{FileInfoID: "F1"}}
	api.uploadFailAt = 2 // 第二名成员上传失败
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	fdb.onRowsAlways("SELECT p.url, p.members, p.sgcc_img_id", []string{"url", "members", "sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{srv.URL + "/p.jpg", `["张三","李四"]`, "", "2026-10-27", int64(1)})
	fdb.onRowsSeq("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsSeq("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{int64(12), int64(1), int64(8), int64(12), "139", "tok-b", "d", "s", int64(1)})

	err := m.SyncMembers(context.Background(), 9, nil)
	if err == nil {
		t.Fatalf("部分失败应抛错")
	}
	if failedNameOf(err) != "李四" {
		t.Fatalf("失败人名不符: %v", err)
	}
	updates := fdb.callsWith("UPDATE worklog_photo SET sgcc_img_id = ?, sgcc_synced = ?")
	if len(updates) != 1 {
		t.Fatalf("失败也应落库状态: %d", len(updates))
	}
	if fmt.Sprint(updates[0].args[1]) != "2" || !strings.Contains(itoaArgs(updates[0].args), `"11":"F1"`) {
		t.Fatalf("部分链接留存 + sgcc_synced=2 不符: %v", updates[0].args)
	}
}

// TestSyncMembersFetchFail 回源失败：远端先行中止，本地不落任何状态（不假成功）。
func TestSyncMembersFetchFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = newRemoteAPI()
	fdb.onRowsAlways("SELECT p.url, p.members, p.sgcc_img_id", []string{"url", "members", "sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{srv.URL + "/p.jpg", `["张三"]`, "", "2026-10-27", int64(1)})

	err := m.SyncMembers(context.Background(), 9, nil)
	if err == nil || !strings.Contains(err.Error(), "照片回源失败") {
		t.Fatalf("应报回源失败: %v", err)
	}
	if len(fdb.callsWith("UPDATE worklog_photo")) != 0 {
		t.Fatalf("回源失败不应落库")
	}
}

// TestRemoveMembersRemoteFirst 剔除人名：远端删除成功一个后另一个失败 → 已删链接即时断开留存，抛错带人名。
func TestRemoveMembersRemoteFirst(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.feeTpl = feeTplWithImages(`[{"fileInfoId":"F1","url":"u1"},{"fileInfoId":"F2","url":"u2"}]`)
	api.saveFailAt = 2 // 第二次费用保存（第二个成员删除）失败
	m.API = api
	fdb.onRowsAlways("SELECT p.sgcc_img_id, DATE_FORMAT", []string{"sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{`{"11":"F1","12":"F2"}`, "2026-10-27", int64(1)})
	fdb.onRowsSeq("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsSeq("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{int64(12), int64(1), int64(8), int64(12), "139", "tok-b", "d", "s", int64(1)})

	err := m.RemoveMembers(context.Background(), 9, []string{"张三", "李四"})
	if err == nil {
		t.Fatalf("远端删除失败应抛错")
	}
	if failedNameOf(err) != "李四" {
		t.Fatalf("失败人名不符: %v", err)
	}
	// 第一人成功后链接即时断开落库（保留 12）；本地人名由 worklog 侧保持不变
	updates := fdb.callsWith("UPDATE worklog_photo SET sgcc_img_id = ? WHERE id = ?")
	if len(updates) != 1 {
		t.Fatalf("成功一人应即时断链落库: %d", len(updates))
	}
	if !strings.Contains(itoaArgs(updates[0].args), `"12":"F2"`) || strings.Contains(itoaArgs(updates[0].args), `"11"`) {
		t.Fatalf("断链落库不符: %v", updates[0].args)
	}
	if len(fdb.callsWith("INSERT INTO worklog_sync_log")) == 0 {
		t.Fatalf("失败应记核查流水")
	}
}

// TestUnlinkBeforeLocalDelete 删除场景：全部远端解链成功才清空链接（本地删除由调用方随后执行）。
func TestUnlinkBeforeLocalDelete(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.feeTpl = feeTplWithImages(`[{"fileInfoId":"F1","url":"u1"},{"fileInfoId":"F2","url":"u2"}]`)
	m.API = api
	fdb.onRowsAlways("SELECT p.sgcc_img_id, DATE_FORMAT", []string{"sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{`{"11":"F1","12":"F2"}`, "2026-10-27", int64(1)})
	fdb.onRowsAlways("FROM worklog_member WHERE id = ?", []string{"name"},
		[]driver.Value{"张三"}, []driver.Value{"李四"})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)},
		[]driver.Value{int64(12), int64(1), int64(8), int64(12), "139", "tok-b", "d", "s", int64(1)})

	if err := m.Unlink(context.Background(), 9); err != nil {
		t.Fatalf("解链应成功: %v", err)
	}
	if len(api.saveOvs) != 2 {
		t.Fatalf("两名成员应各远端删除一次: %d", len(api.saveOvs))
	}
	updates := fdb.callsWith("UPDATE worklog_photo SET sgcc_img_id = ? WHERE id = ?")
	if len(updates) != 2 {
		t.Fatalf("应逐人断链落库: %d", len(updates))
	}
	last := itoaArgs(updates[len(updates)-1].args)
	if strings.Contains(last, "F1") || strings.Contains(last, "F2") {
		t.Fatalf("最终链接应清空: %v", updates[len(updates)-1].args)
	}
}

// TestUnlinkNoLinks 无商旅链接：直接成功，不发远端调用（本地删除可继续）。
func TestUnlinkNoLinks(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	m.API = api
	fdb.onRowsAlways("SELECT p.sgcc_img_id, DATE_FORMAT", []string{"sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{"", "2026-10-27", int64(1)})

	if err := m.Unlink(context.Background(), 9); err != nil {
		t.Fatalf("无链接应成功: %v", err)
	}
	if len(api.saveOvs) != 0 {
		t.Fatalf("不应有远端调用: %d", len(api.saveOvs))
	}
}

// TestStartPhotoOpLifecycle 任务启动/完成与 /op/status、/sync/active 口径一致（rec 收尾产物/失败人名）。
func TestStartPhotoOpLifecycle(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	started := make(chan struct{})
	release := make(chan struct{})
	opID := m.StartPhotoOp("upload", 1, 100, 0, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		close(started)
		<-release
		rec.SetPhoto(55, "http://img/55")
		return nil
	})
	if opID == 0 {
		t.Fatalf("应登记任务")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("任务未启动")
	}
	if !m.Ops.ActivePhotoOp(1, 100) {
		t.Fatalf("进行中应锁定卡片")
	}
	if len(m.Ops.GetActive()) != 1 {
		t.Fatalf("sync/active 应见进行中任务")
	}
	close(release)
	waitOp(t, m, opID, "done")
	if m.Ops.ActivePhotoOp(1, 100) {
		t.Fatalf("完成后应解锁卡片")
	}
	st, _ := m.Ops.Status(opID)
	if st.Progress != 100 || st.PhotoIDOut != 55 || st.PhotoURL != "http://img/55" {
		t.Fatalf("任务收尾产物不符: %+v", st)
	}
}

// TestStartPhotoOpFail 任务失败：failedName/error/failed 一致，卡片解锁。
func TestStartPhotoOpFail(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	opID := m.StartPhotoOp("members", 1, 100, 9, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		rec.SetFailedName("李四")
		return &remoteNamedError{name: "李四", msg: "商旅费用照片删除失败"}
	})
	waitOp(t, m, opID, "failed")
	st, _ := m.Ops.Status(opID)
	if !st.Failed || st.FailedName != "李四" || !strings.Contains(st.Error, "删除失败") {
		t.Fatalf("失败状态不符: %+v", st)
	}
	if m.Ops.ActivePhotoOp(1, 100) {
		t.Fatalf("失败后应解锁卡片")
	}
}

// TestStartPhotoOpBusy 同卡片已有进行中任务 → 返回 0（不重复登记）。
func TestStartPhotoOpBusy(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	release := make(chan struct{})
	first := m.StartPhotoOp("upload", 1, 100, 0, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		<-release
		return nil
	})
	if first == 0 {
		t.Fatalf("首个任务应登记")
	}
	second := m.StartPhotoOp("upload", 1, 100, 0, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		return nil
	})
	if second != 0 {
		t.Fatalf("同卡片应拒绝第二个任务: %d", second)
	}
	close(release)
	waitOp(t, m, first, "done")
}

// TestPhotoOpSerialQueue 任务全局串行：后一个任务在前一个收尾后才启动（费用模板 read-modify-write 防互踩）。
func TestPhotoOpSerialQueue(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	order := make(chan string, 2)
	release := make(chan struct{})
	m.StartPhotoOp("upload", 1, 100, 0, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		order <- "first-start"
		<-release
		order <- "first-end"
		return nil
	})
	m.StartPhotoOp("upload", 2, 200, 0, func(ctx context.Context, rec worklog.PhotoOpRec) error {
		order <- "second-start"
		return nil
	})
	select {
	case got := <-order:
		if got != "first-start" {
			t.Fatalf("首任务应先启动: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("首任务未启动")
	}
	select {
	case got := <-order:
		t.Fatalf("串行被破坏，第二任务提前启动: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for _, want := range []string{"first-end", "second-start"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("次序不符: 得 %s 想 %s", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("任务未按序完成: %s", want)
		}
	}
}

// TestPhotoResyncRouteOpStatus 重同步路由：opId 即回，/op/status 与 /sync/active 随任务启动/完成一致。
func TestPhotoResyncRouteOpStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PHOTOBYTES"))
	}))
	defer srv.Close()
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	api := newRemoteAPI()
	api.uploadSeq = []UploadedImage{{FileInfoID: "F1"}}
	api.feeTpl = feeTplWithImages("[]")
	m.API = api
	h := newTestRouter(m, testIdentity())
	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("p.entry_id, e.team_id", []string{"entry_id", "team_id"},
		[]driver.Value{int64(100), int64(1)})
	fdb.onRowsAlways("SELECT p.url, p.members, p.sgcc_img_id", []string{"url", "members", "sgcc_img_id", "log_date", "team_id"},
		[]driver.Value{srv.URL + "/p.jpg", `["张三"]`, `{"11":"F1"}`, "2026-10-27", int64(1)})
	fdb.onRowsAlways("FROM worklog_member WHERE team_id = ? AND name = ?", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id = ?", []string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(11), int64(1), int64(7), int64(11), "138", "tok-a", "d", "s", int64(1)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/photos/9/resync", "")
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("应发起重同步: %v", env)
	}
	data := env["data"].(map[string]any)
	opID := int64(data["opId"].(float64))
	if opID == 0 {
		t.Fatalf("应返回 opId")
	}
	waitOp(t, m, opID, "done")
	st, _ := m.Ops.Status(opID)
	if st.Progress != 100 {
		t.Fatalf("完成后进度应 100: %+v", st)
	}
	rec2 := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/op/status?op_id="+strconv.FormatInt(opID, 10), "")
	env2 := decodeEnvelope(t, rec2)
	stData := env2["data"].(map[string]any)
	if stData["step"] != "done" || stData["failed"] != false {
		t.Fatalf("op/status 不符: %v", stData)
	}
	rec3 := doJSON(t, h, http.MethodGet, "/api/v1/sgcc/sync/active", "")
	env3 := decodeEnvelope(t, rec3)
	ops := env3["data"].(map[string]any)["ops"].([]any)
	if len(ops) != 0 {
		t.Fatalf("完成后 sync/active 不应再含任务: %v", ops)
	}
}

// waitOp 轮询任务到终态（done/failed）。
func waitOp(t *testing.T, m *Module, opID int64, step string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := m.Ops.Status(opID); ok && st.Step == step {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, _ := m.Ops.Status(opID)
	t.Fatalf("任务未达 %s: %+v", step, st)
}

// itoaArgs 参数拼串（断言用）。
func itoaArgs(args []driver.Value) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, fmt.Sprint(a))
	}
	return strings.Join(parts, ",")
}

// ---- 费用照片双向对账（syncFeePhotos）----

// fakeStore 对象存储 fake（记录上传/删除，生成可断言的访问地址）。
type fakeStore struct {
	mu      sync.Mutex
	puts    []string
	deletes []string
}

func (s *fakeStore) PutBuffer(_ context.Context, key string, _ []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts = append(s.puts, key)
	return nil
}

func (s *fakeStore) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, key)
	return nil
}

func (s *fakeStore) PublicURL(key string) string { return "https://cos.test/" + key }

// fakeVerifier 水印识别钩子 fake（done 通道回传触发信号，测试阻塞等待，消除轮询时序抖动）。
type fakeVerifier struct {
	mu    sync.Mutex
	calls []int64
	done  chan int64 // 可空：非空时每次回调后投递 photoID（缓冲 1 + 非阻塞投递）
}

func (v *fakeVerifier) VerifyAndWriteBack(_ context.Context, photoID int64, _, _, _, _ string) {
	v.mu.Lock()
	v.calls = append(v.calls, photoID)
	v.mu.Unlock()
	if v.done != nil {
		select {
		case v.done <- photoID:
		default:
		}
	}
}

// TestSyncFeePhotosReconcile 费用照片双向对账：商旅有本地无 → 下载入库（识别钩子触发）；
// 商旅已删 → source=1 整照删除、source=0 摘空整照删除；收尾计数日志。
func TestSyncFeePhotosReconcile(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	verifier := &fakeVerifier{done: make(chan int64, 1)}
	m.Store = store
	m.VerifyPhoto = verifier
	m.FetchSgccImage = func(_ context.Context, url string) ([]byte, error) {
		if !strings.HasPrefix(url, "https://") {
			return nil, fmt.Errorf("商旅侧未返回有效图片地址")
		}
		return []byte("JPEGDATA"), nil
	}
	// 成员 / username / 首个出工记录 / 班组名
	fdb.onRowsAlways("SELECT name, user_id FROM worklog_member", []string{"name", "user_id"},
		[]driver.Value{"张三", int64(7)})
	fdb.onRowsAlways("SELECT username FROM sys_user", []string{"username"}, []driver.Value{"zhang"})
	fdb.onRowsAlways("ORDER BY e.id LIMIT 1", []string{"id", "destination_name"},
		[]driver.Value{int64(100), "500kV变电站"})
	fdb.onRowsAlways("SELECT name FROM sys_team", []string{"name"}, []driver.Value{"检修一班"})
	// 本地照片：source=1 商旅镜像（商旅已删）与 source=0 壹匣上传（商旅已删，摘空）
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(1), "worklog/old1.jpg", int64(1), `{"11":"OLD1"}`},
		[]driver.Value{int64(2), "worklog/old2.jpg", int64(0), `{"11":"UP1"}`})
	// 同 MD5 合并查询无命中 → 走入库
	fdb.onExec("INSERT INTO worklog_photo", 55)
	// 摘除路径重读当前值：只剩本成员 → 摘空删除
	fdb.onRowsAlways("SELECT members, sgcc_img_id, cos_key FROM worklog_photo",
		[]string{"members", "sgcc_img_id", "cos_key"},
		[]driver.Value{`["张三"]`, `{"11":"UP1"}`, "worklog/old2.jpg"})
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{
		"dtComponentList": []any{
			map[string]any{"id": float64(5), "value": `[{"fileInfoId":"R1","url":"https://sgcc.test/1.jpg"}]`},
		},
	}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	// 方向一：下载入库（COS key 规则 + 识别钩子）
	store.mu.Lock()
	puts := append([]string(nil), store.puts...)
	deletes := append([]string(nil), store.deletes...)
	store.mu.Unlock()
	if len(puts) != 1 || !strings.HasPrefix(puts[0], "worklog/检修一班/2026.10.28/100-") ||
		!strings.HasSuffix(puts[0], "-R1.jpg") {
		t.Fatalf("入库 key 不符: %v", puts)
	}
	// 识别钩子为 fire-and-forget 异步：通道握手等待落地（done 缓冲投递，无轮询时序抖动）
	select {
	case <-verifier.done:
	case <-time.After(5 * time.Second):
		t.Fatal("水印识别钩子超时未触发")
	}
	verifier.mu.Lock()
	vcalls := append([]int64(nil), verifier.calls...)
	verifier.mu.Unlock()
	if len(vcalls) != 1 || vcalls[0] != 55 {
		t.Fatalf("水印识别钩子不符: %v", vcalls)
	}
	inserts := fdb.callsWith("INSERT INTO worklog_photo")
	if len(inserts) != 1 || !strings.Contains(fmt.Sprint(inserts[0].args), "worklog/检修一班/2026.10.28/100-") ||
		!strings.Contains(fmt.Sprint(inserts[0].args), `{"11":"R1"}`) {
		t.Fatalf("入库行不符: %+v", inserts)
	}
	// 方向二：source=1 整照删除、source=0 摘空整照删除
	if len(deletes) != 2 || !strings.Contains(strings.Join(deletes, ","), "worklog/old1.jpg") ||
		!strings.Contains(strings.Join(deletes, ","), "worklog/old2.jpg") {
		t.Fatalf("删除对象不符: %v", deletes)
	}
	rowDeletes := fdb.callsWith("DELETE FROM worklog_photo")
	if len(rowDeletes) != 2 {
		t.Fatalf("应删除两行: %+v", rowDeletes)
	}
	// 收尾计数日志
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	found := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "费用照片：拉下 1 张 / 合并 0 张 / 删除 2 张 / 摘除 0 人") {
			found = true
		}
	}
	if !found {
		t.Fatalf("收尾计数日志不符: %+v", logs)
	}
}

// TestSyncFeePhotosShapeGuard comp5 value 非 JSON 数组 → 跳过对账并记诊断（保护本地）。
func TestSyncFeePhotosShapeGuard(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	tpl := map[string]any{
		"dtComponentList": []any{
			map[string]any{"id": float64(5), "value": `{"oops":true}`},
		},
	}
	acc := &Account{ID: 3, TeamID: 1, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	found := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "上传图片组件形态异常") {
			found = true
		}
	}
	if !found {
		t.Fatalf("形状守卫诊断缺失: %+v", logs)
	}
	// 未识别 id（键名漂移）→ 跳过保护本地
	tpl2 := map[string]any{
		"dtComponentList": []any{
			map[string]any{"id": float64(5), "value": `[{"oops":"x"}]`},
		},
	}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl2)
	logs = fdb.callsWith("INSERT INTO worklog_sync_log")
	found = false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "无可识别图片 id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("键名漂移守卫缺失: %+v", logs)
	}
}
