// Task 5 round1 回归：过期删除必须先于插入、共享照片行保留仍有效链接、日志按 detailId 去重。
package sgccclockin

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// clockinFixture 打卡对账夹具：本地 A(seq1)、B(seq2)，远端 B、C。
func clockinFixture(t *testing.T) (*Module, *fakeDB, func()) {
	t.Helper()
	db, fdb := openFake(t)
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{
			"clockInDetailList": []any{
				map[string]any{"detailId": "B", "clockInTime": "2026-10-27 08:00:00"},
				map[string]any{"detailId": "C", "clockInTime": "2026-10-27 17:30:00"},
			},
		}},
	}}
	fdb.onRowsAlways("SELECT id, seq, detail_id FROM worklog_clockin", []string{"id", "seq", "detail_id"},
		[]driver.Value{int64(1), int64(1), "A"}, []driver.Value{int64(2), int64(2), "B"})
	fdb.onRowsAlways("SELECT COUNT(*) FROM worklog_clockin", []string{"n"}, []driver.Value{int64(2)})
	fdb.onExec("INSERT INTO worklog_clockin", 0)
	fdb.onExec("DELETE FROM worklog_clockin", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	return m, fdb, func() { db.Close() }
}

// TestSyncClockinPruneDeletesStaleBeforeInsert 真实 uk_member_date_seq 下，INSERT ON DUPLICATE
// 会复用旧行 id；必须先 DELETE 过期行再 INSERT/UPDATE 新记录，否则后续按旧 id 删除会删掉新记录。
func TestSyncClockinPruneDeletesStaleBeforeInsert(t *testing.T) {
	m, fdb, done := clockinFixture(t)
	defer done()
	acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
	if err := m.syncClockinPart(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
		t.Fatalf("打卡对账失败: %v", err)
	}
	delIdx, insIdx := -1, -1
	for i, c := range fdb.callsWith("") {
		if strings.Contains(c.query, "DELETE FROM worklog_clockin WHERE id = ?") && delIdx < 0 {
			delIdx = i
		}
		if strings.Contains(c.query, "INSERT INTO worklog_clockin") && insIdx < 0 {
			insIdx = i
		}
	}
	if delIdx < 0 || insIdx < 0 {
		t.Fatalf("缺少删除/插入语句: del=%d ins=%d", delIdx, insIdx)
	}
	if delIdx > insIdx {
		t.Fatalf("过期行删除必须先于插入（del=%d, ins=%d）；否则唯一键复用旧 id 后会被删掉", delIdx, insIdx)
	}
}

// TestSyncFeePhotosSharedPulledRowKeepsLiveLinks 商旅拉回共享行：当前成员链接失效但其他成员
// 最新 link 仍有效时，只能摘除当前成员，不得删除整行与 COS 对象。
func TestSyncFeePhotosSharedPulledRowKeepsLiveLinks(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	store := &fakeStore{}
	m.Store = store
	photoReconcileReady(fdb)
	fdb.onRowsAlways("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "source", "sgcc_img_id"},
		[]driver.Value{int64(5), "worklog/shared.jpg", "sgcc", `{"11":"DEAD","12":"LIVE"}`},
		[]driver.Value{int64(6), "worklog/other.jpg", "app", `{"11":"LIVE"}`})
	fdb.onRowsAlways("SELECT members, sgcc_img_id, cos_key FROM worklog_photo",
		[]string{"members", "sgcc_img_id", "cos_key"},
		[]driver.Value{`["张三","李四"]`, `{"11":"DEAD","12":"LIVE"}`, "worklog/shared.jpg"})
	fdb.onExec("DELETE FROM worklog_photo", 0)
	fdb.onExec("UPDATE worklog_photo SET members = ?", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)

	tpl := map[string]any{"dtComponentList": []any{map[string]any{"id": float64(5),
		"value": `[{"fileInfoId":"LIVE","url":"https://sgcc.test/live.jpg"}]`}}}
	acc := &Account{ID: 3, TeamID: 1, UserID: 7, MemberID: 11, Token: "tk", TokenStatus: 1}
	m.syncPhotoPart(context.Background(), acc, "2026-10-28", "daily", 1, 11, tpl)

	for _, del := range fdb.callsWith("DELETE FROM worklog_photo") {
		if len(del.args) > 0 && fmt.Sprint(del.args[0]) == "5" {
			t.Fatalf("其他成员链接仍有效，不得删除共享行: %v", del.args)
		}
	}
	if len(store.deletes) != 0 {
		t.Fatalf("不得删除共享行的 COS 对象: %v", store.deletes)
	}
	var sharedUpdate bool
	for _, upd := range fdb.callsWith("UPDATE worklog_photo SET members = ?") {
		if len(upd.args) < 3 || fmt.Sprint(upd.args[2]) != "5" {
			continue
		}
		body := fmt.Sprint(upd.args[0])
		links := fmt.Sprint(upd.args[1])
		if strings.Contains(body, "张三") || strings.Contains(links, `"11"`) {
			t.Fatalf("应摘除失效成员 11/张三，实际 %v", upd.args)
		}
		if !strings.Contains(body, "李四") || !strings.Contains(links, `"12":"LIVE"`) {
			t.Fatalf("必须保留成员 12/李四 及其有效链接，实际 %v", upd.args)
		}
		sharedUpdate = true
	}
	if !sharedUpdate {
		t.Fatalf("共享行应只摘除当前成员，实际更新 %+v", fdb.callsWith("UPDATE worklog_photo"))
	}
}

// TestSyncClockinLogDedupesDetailID 远端重复 detailId 时日志按去重后的条数统计。
func TestSyncClockinLogDedupesDetailID(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{
		"statusCode": float64(200),
		"data": map[string]any{"body": map[string]any{
			"clockInDetailList": []any{
				map[string]any{"detailId": "D1", "clockInTime": "2026-10-27 08:00:00"},
				map[string]any{"detailId": "D1", "clockInTime": "2026-10-27 08:00:01"},
			},
		}},
	}}
	fdb.onRowsAlways("SELECT id, seq, detail_id FROM worklog_clockin", []string{"id", "seq", "detail_id"})
	fdb.onRowsAlways("SELECT COUNT(*) FROM worklog_clockin", []string{"n"}, []driver.Value{int64(1)})
	fdb.onExec("INSERT INTO worklog_clockin", 0)
	fdb.onExec("INSERT INTO worklog_sync_log", 0)
	acc := &Account{ID: 3, MemberID: 11, TokenStatus: 1, Token: "tk"}
	if err := m.syncClockinPart(context.Background(), acc, "2026-10-27", "daily", 1); err != nil {
		t.Fatalf("打卡对账失败: %v", err)
	}
	logs := fdb.callsWith("INSERT INTO worklog_sync_log")
	found := false
	for _, c := range logs {
		if strings.Contains(fmt.Sprint(c.args), "商旅 1 条覆盖本地") {
			found = true
		}
	}
	if !found {
		t.Fatalf("重复 detailId 应按 1 条统计，实际 %+v", logs)
	}
}
