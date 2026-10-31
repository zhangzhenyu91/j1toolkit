// Task 5 回归：seq 回收/刷新/删除同事务、费用模板缺组件保留本地值、照片对账使用最新链接。
package sgccclockin

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// TestSyncClockinReleasesSeqAndPrunesInTx detailId 换批时先释放旧 seq 再分配新记录，
// 刷新与过期删除处于同一事务（不得因旧 detailId 占位把新记录丢弃）。
func TestSyncClockinReleasesSeqAndPrunesInTx(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	dayNew := map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{
			"clockInDetailList": []any{
				map[string]any{"detailId": "B", "clockInTime": "2026-10-27 08:00:00"},
				map[string]any{"detailId": "C", "clockInTime": "2026-10-27 17:30:00"},
			},
		}},
	}
	m.API = &fakeAPI{dayNew: dayNew}
	fdb.onRowsAlways("SELECT id, seq, detail_id FROM worklog_clockin", []string{"id", "seq", "detail_id"},
		[]driver.Value{int64(1), int64(1), "A"}, []driver.Value{int64(2), int64(2), "B"})
	fdb.onRowsAlways("SELECT COUNT(*) FROM worklog_clockin", []string{"n"}, []driver.Value{int64(2)})
	fdb.onExec("INSERT INTO worklog_clockin", 0)
	fdb.onExec("DELETE FROM worklog_clockin", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
	if err := m.syncClockinPart(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
		t.Fatalf("打卡对账失败: %v", err)
	}
	inserts := fdb.callsWith("INSERT INTO worklog_clockin")
	if len(inserts) != 1 || !strings.Contains(fmt.Sprint(inserts[0].args), "C") || fmt.Sprint(inserts[0].args[3]) != "1" {
		t.Fatalf("新 detailId C 应以回收的 seq=1 插入，实际 %+v", inserts)
	}
	updates := fdb.callsWith("UPDATE worklog_clockin SET clock_time")
	if len(updates) != 1 || len(updates[0].args) == 0 ||
		fmt.Sprint(updates[0].args[len(updates[0].args)-1]) != "2" {
		t.Fatalf("保留的 detailId B 应按 id=2 更新，实际 %+v", updates)
	}
	var deletedA bool
	for _, del := range fdb.callsWith("DELETE FROM worklog_clockin WHERE id = ?") {
		if len(del.args) > 0 && fmt.Sprint(del.args[0]) == "1" {
			deletedA = true
		}
	}
	if !deletedA {
		t.Fatalf("过期 detailId A 应被删除，实际 %+v", fdb.callsWith("DELETE FROM worklog_clockin"))
	}
	events := fdb.eventLog()
	commits := 0
	for _, e := range events {
		if e == "commit" {
			commits++
		}
	}
	if commits != 1 || len(events) == 0 || events[len(events)-1] != "commit" {
		t.Fatalf("刷新与删除应在同一事务一次提交，事件 %v", events)
	}
}

// TestSyncFeePartKeepsLocalOnBadComponent 费用模板 id=10 组件缺失或坏 JSON 时保留本地伙食/交通费，
// 不得把本地值清零。
func TestSyncFeePartKeepsLocalOnBadComponent(t *testing.T) {
	cases := map[string][]any{
		"组件缺失":   {},
		"坏 JSON": {map[string]any{"id": float64(10), "value": "{not-json"}},
	}
	for name, comps := range cases {
		db, fdb := openFake(t)
		m := New(db)
		m.API = &fakeAPI{feeTpl: map[string]any{
			"statusCode": float64(200),
			"data": map[string]any{"body": map[string]any{"clockTemplate": map[string]any{
				"id": 7, "name": "T", "dtComponentList": comps,
			}}},
		}}
		fdb.onRowsAlways("SELECT position, city_code, city_name FROM worklog_clockin",
			[]string{"position", "city_code", "city_name"}, []driver.Value{"", "", ""})
		fdb.onRowsAlways("SELECT food_fee, transit_fee, cost_center_code, cost_center_name",
			[]string{"food_fee", "transit_fee", "cost_center_code", "cost_center_name"},
			[]driver.Value{float64(60), float64(10), "CC1", "一中心"})
		fdb.onExec("INSERT INTO worklog_fee", 0)
		fdb.onExec("INSERT INTO worklog_sync_log", 0)
		acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
		if _, err := m.syncFeePart(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
			t.Fatalf("%s：费用对账失败: %v", name, err)
		}
		ins := fdb.callsWith("INSERT INTO worklog_fee")
		if len(ins) != 1 {
			t.Fatalf("%s：应回写一条费用摘要，实际 %d", name, len(ins))
		}
		args := ins[0].args
		if fmt.Sprint(args[3]) != "60" || fmt.Sprint(args[4]) != "10" {
			t.Fatalf("%s：模板组件异常必须保留本地费用，实际 args=%v", name, args)
		}
		db.Close()
	}
}

// TestSyncFeePhotosUsesFreshLinks 共享照片合并后，删除方向必须读行内最新 links；
// 旧扫描 imgID 已被新链接替换时不得摘除/删除仍存在于商旅的照片。
func TestSyncFeePhotosUsesFreshLinks(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	m.FetchSgccImage = func(context.Context, string) ([]byte, error) { return []byte("JPEGDATA"), nil }
	photoReconcileReady(fdb)
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(5), "worklog/shared.jpg", "app", `{"11":"OLD"}`})
	// 合并路径按 MD5 命中同一条共享照片（当前 links 仍是旧 ID）
	fdb.onRowsAlways("SELECT p.id, p.members, p.sgcc_img_id FROM worklog_photo p",
		[]string{"id", "members", "sgcc_img_id"},
		[]driver.Value{int64(5), `["张三"]`, `{"11":"OLD"}`})
	// 删除方向逐行重读：并发合并已写入最新链接 LIVE
	fdb.onRowsAlways("SELECT members, sgcc_img_id, cos_key FROM worklog_photo",
		[]string{"members", "sgcc_img_id", "cos_key"},
		[]driver.Value{`["张三"]`, `{"11":"LIVE"}`, "worklog/shared.jpg"})
	fdb.onExec("INSERT INTO worklog_photo", 55)
	fdb.onExec("UPDATE worklog_photo SET members = ?", 0)
	fdb.onExec("DELETE FROM worklog_photo", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"LIVE","url":"https://sgcc.test/live.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	if n := len(fdb.callsWith("DELETE FROM worklog_photo")); n != 0 {
		t.Fatalf("最新链接仍在商旅，不得删除共享照片（DELETE %d 次）", n)
	}
	if n := len(store.deletes); n != 0 {
		t.Fatalf("不得删除仍有效的 COS 对象: %v", store.deletes)
	}
	// 只允许合并更新（写入 LIVE）；不得再执行摘除本成员的第二次 UPDATE
	updates := fdb.callsWith("UPDATE worklog_photo SET members = ?")
	if len(updates) != 1 {
		t.Fatalf("只应合并一次最新链接，实际 UPDATE %d 次: %+v", len(updates), updates)
	}
	if !strings.Contains(fmt.Sprint(updates[0].args), "LIVE") {
		t.Fatalf("合并更新应写入最新链接，实际 %v", updates[0].args)
	}
}
