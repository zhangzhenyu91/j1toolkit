// 补推队列测试：上送失败入队、到期待补推成功、持续失败置 dead、队列 SQL 口径。
package wechatanswer

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPushFailureEnqueuedAndRetried(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	env.sink.failNext(500, "boom")
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-q-1", "12# 杨树超高", at))

	calls := env.sink.payloads()
	if len(calls) != 1 {
		t.Fatalf("sink 调用 %d 次，期望 1 次", len(calls))
	}
	if env.queue.pending() != 1 {
		t.Fatalf("失败应入补推队列，pending=%d", env.queue.pending())
	}
	// 记录保留且标记 push_status=failed
	records := env.state.all()
	if len(records) != 1 || records[0].PushStatus != "failed" {
		t.Fatalf("记录推送状态不符: %+v", records)
	}
	// 回执「已记录」+「报送失败，请私聊报送班管及超管」
	if len(env.sender.quotes) != 2 || env.sender.quotes[1] != "报送失败，请私聊报送班管及超管" {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}

	// 队列补推：sink 恢复后一次处理即成功
	n, err := env.m.processQueueOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("processQueueOnce = (%d, %v)，期望 (1, nil)", n, err)
	}
	if env.queue.pending() != 0 {
		t.Fatalf("补推成功后应清空，pending=%d", env.queue.pending())
	}
	all := env.sink.payloads()
	if len(all) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次（原始 + 补推）", len(all))
	}
	if string(all[0].Payload) != string(all[1].Payload) {
		t.Fatal("补推载荷应与入队快照一致")
	}
}

func TestQueueRetryKeepsFailingThenDead(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	env.sink.sticky = 500
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-q-2", "12# 杨树超高", env.now.Unix()))
	if env.queue.pending() != 1 {
		t.Fatalf("应入队，pending=%d", env.queue.pending())
	}
	// 连续补推失败达到上限后置 dead（不再出现在待补推清单）
	for i := 0; i < pushMaxAttempts; i++ {
		if _, err := env.m.processQueueOnce(context.Background()); err != nil {
			t.Fatalf("第 %d 次补推出错: %v", i+1, err)
		}
	}
	if env.queue.pending() != 0 {
		t.Fatalf("达上限后不应再待补推，pending=%d", env.queue.pending())
	}
	// setteled 记录应标记 dead
	env.queue.mu.Lock()
	defer env.queue.mu.Unlock()
	dead := false
	for _, st := range env.queue.settled {
		if st.dead {
			dead = true
		}
	}
	if !dead {
		t.Fatal("连续失败达上限应置 dead")
	}
}

func TestQueueRetryOnSinkError(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{"is_crossing": "true", "crossing_type": "房屋"}
	env.sink.errNext(errors.New("网络中断"))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-q-3", "1#-3# 交跨房屋", env.now.Unix()))
	if env.queue.pending() != 1 {
		t.Fatalf("sink 错误也应入队，pending=%d", env.queue.pending())
	}
	if _, err := env.m.processQueueOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.queue.pending() != 0 {
		t.Fatalf("恢复后应补推成功，pending=%d", env.queue.pending())
	}
}

func TestDeleteFailureEnqueued(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-q-4", "12# 杨树超高", at))
	env.sink.failNext(503, "不可达")
	env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-q-4", at+10))
	// 删除推送失败：仍入队补推（本地删除照常）
	if env.queue.pending() != 1 {
		t.Fatalf("删除失败应入队，pending=%d", env.queue.pending())
	}
	if len(env.state.all()) != 0 {
		t.Fatal("本地记录仍应删除")
	}
	if _, err := env.m.processQueueOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.queue.pending() != 0 {
		t.Fatalf("删除事件补推成功后应清空，pending=%d", env.queue.pending())
	}
}

func TestPushQueueSQLStatements(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	q := &SQLPushQueue{DB: db}
	ctx := context.Background()

	if err := q.Enqueue(ctx, KindTree, []byte(`{"session_id":"s"}`)); err != nil {
		t.Fatal(err)
	}
	if fdb.execCount("INSERT INTO wa_push_queue") != 1 {
		t.Fatalf("入队 SQL 未执行: %v", fdb.execCount("INSERT INTO wa_push_queue"))
	}

	fdb.on("FROM wa_push_queue",
		[]string{"id", "kind", "payload", "attempts"},
		[][]driver.Value{{int64(7), KindTree, `{"session_id":"s"}`, int64(2)}})
	items, err := q.Due(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != 7 || items[0].Attempts != 2 {
		t.Fatalf("Due 结果不符: %+v", items)
	}

	if err := q.Settle(ctx, 7, true, "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	last, ok := fdb.lastExec("status='done'")
	if !ok {
		t.Fatal("成功结算 SQL 未执行")
	}
	if len(last.args) != 1 || last.args[0] != driver.Value(int64(7)) {
		t.Fatalf("成功结算参数不符: %v", last.args)
	}

	if err := q.Settle(ctx, 8, false, "boom", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := fdb.lastExec("attempts=attempts+1"); !ok {
		t.Fatal("失败续期 SQL 未执行")
	}

	if err := q.Settle(ctx, 9, false, "dead", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	last, ok = fdb.lastExec("status='dead'")
	if !ok {
		t.Fatal("置 dead SQL 未执行")
	}
	if !strings.Contains(last.query, "last_error") {
		t.Fatalf("置 dead SQL 未带 last_error: %s", last.query)
	}
}

func TestEnsureSchemaExecutesDDL(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	// information_schema 列检查：视为列已存在（跳过 ALTER 补列）
	fdb.on("FROM information_schema.columns", []string{"c"}, [][]driver.Value{{int64(1)}})
	m := New(db, nil)
	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fdb.execCount("CREATE TABLE IF NOT EXISTS wa_group") == 0 ||
		fdb.execCount("CREATE TABLE IF NOT EXISTS wa_rule") == 0 ||
		fdb.execCount("CREATE TABLE IF NOT EXISTS wa_push_queue") == 0 ||
		fdb.execCount("CREATE TABLE IF NOT EXISTS wa_record") == 0 ||
		fdb.execCount("CREATE TABLE IF NOT EXISTS wa_event") == 0 ||
		fdb.execCount("CREATE TABLE IF NOT EXISTS wa_media") == 0 {
		t.Fatalf("建表语句未全部执行: %d", len(fdb.execs))
	}
	if fdb.execCount("ALTER TABLE wa_image MODIFY COLUMN file_no TEXT") == 0 ||
		fdb.execCount("ALTER TABLE wa_image MODIFY COLUMN aes_key TEXT") == 0 {
		t.Fatal("旧版过短 CDN 参数列应自动迁移为 TEXT")
	}
}
