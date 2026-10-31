// Task 5 round1 回归：读图失败整组省略、补推队列记录级锁串行化、老库索引与回填。
package wechatanswer

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPayloadOmitsEvidenceWhenAnyPhotoUnreadable 任一辅证文件读取失败时整组视为未加载，
// 必须省略 evidence_photos；不得缩短数组后把其余照片当权威集合清除旧图。
func TestPayloadOmitsEvidenceWhenAnyPhotoUnreadable(t *testing.T) {
	env := newTestEnv(t, treeRule())
	okRel := "data/wechatanswer/attachments/ok.jpg"
	abs := env.m.photoPath(okRel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("JPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &Record{
		Key: "rec-photos", Kind: KindTree, GroupID: testGroup, SenderID: "u1",
		CapturedAt: env.now.Unix(), Status: "active", PhotosLoaded: true,
		Extracted: map[string]any{"line_name": "10kV某线"},
		Photos: []Photo{
			{ID: 1, MsgID: "img-ok", File: okRel},
			{ID: 2, MsgID: "img-missing", File: "data/wechatanswer/attachments/missing.jpg"},
		},
	}
	body := env.m.buildRecordPayload(rec)
	if _, has := body["evidence_photos"]; has {
		t.Fatalf("任一照片读取失败必须整组省略 evidence_photos，实际 %#v", body["evidence_photos"])
	}
}

// TestPayloadKeepsAllReadablePhotos 全部可读时仍输出完整数组（含空数组=权威清除）。
func TestPayloadKeepsAllReadablePhotos(t *testing.T) {
	env := newTestEnv(t, treeRule())
	rel := "data/wechatanswer/attachments/ok2.jpg"
	abs := env.m.photoPath(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("JPEG2"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &Record{
		Key: "rec-photos-ok", Kind: KindTree, GroupID: testGroup, SenderID: "u1",
		CapturedAt: env.now.Unix(), Status: "active", PhotosLoaded: true,
		Extracted: map[string]any{"line_name": "10kV某线"},
		Photos:    []Photo{{ID: 1, MsgID: "img-ok", File: rel}},
	}
	body := env.m.buildRecordPayload(rec)
	photos, _ := body["evidence_photos"].([]map[string]string)
	if len(photos) != 1 || photos[0]["msg_id"] != "img-ok" {
		t.Fatalf("全部可读应输出完整照片数组，实际 %#v", body["evidence_photos"])
	}
}

// TestPushQueueLocksRecordRowBeforeMutation 同 record_key 的 enqueue/delete 必须先在 wa_record
// 行上 SELECT ... FOR UPDATE，再执行队列删改；不得只靠 DELETE+INSERT 自身原子性。
func TestPushQueueLocksRecordRowBeforeMutation(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	q := &SQLPushQueue{DB: db}
	if err := q.Enqueue(context.Background(), KindTree, "rec-lock", "upsert", []byte(`{"records":[]}`)); err != nil {
		t.Fatal(err)
	}
	lockIdx, delIdx, insIdx := -1, -1, -1
	var lockTx, delTx, insTx int64
	for i, e := range fdb.execs {
		switch {
		case strings.Contains(e.query, "FROM wa_record WHERE record_key = ? FOR UPDATE"):
			if lockIdx < 0 {
				lockIdx, lockTx = i, e.txID
			}
		case strings.Contains(e.query, "DELETE FROM wa_push_queue"):
			if delIdx < 0 {
				delIdx, delTx = i, e.txID
			}
		case strings.Contains(e.query, "INSERT INTO wa_push_queue"):
			if insIdx < 0 {
				insIdx, insTx = i, e.txID
			}
		}
	}
	if lockIdx < 0 {
		t.Fatalf("入队必须在 wa_record 行上加锁，实际 %+v", fdb.execs)
	}
	if !(lockIdx < delIdx && delIdx < insIdx) {
		t.Fatalf("锁必须先于队列删改（lock=%d del=%d ins=%d）", lockIdx, delIdx, insIdx)
	}
	if lockTx == 0 || lockTx != delTx || lockTx != insTx {
		t.Fatalf("锁与队列删改必须同事务: lock=%d del=%d ins=%d", lockTx, delTx, insTx)
	}
}

// TestPushQueueSerializesConcurrentSameRecord 同 record_key 并发入队在行锁上串行：
// 第二个事务必须等第一个提交后才能开始其删改。
func TestPushQueueSerializesConcurrentSameRecord(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	q := &SQLPushQueue{DB: db}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fdb.mu.Lock()
	fdb.afterExec = func(e fakeExec) {
		if e.txID > 0 && strings.Contains(e.query, "INSERT INTO wa_push_queue") {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
	}
	fdb.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := q.Enqueue(context.Background(), KindTree, "rec-con", "upsert", []byte(`{"records":[]}`)); err != nil {
			t.Errorf("upsert: %v", err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("首个入队未进入 INSERT")
	}
	fdb.mu.Lock()
	firstTx := int64(0)
	for _, e := range fdb.execs {
		if strings.Contains(e.query, "INSERT INTO wa_push_queue") {
			firstTx = e.txID
		}
	}
	fdb.mu.Unlock()
	if firstTx == 0 {
		t.Fatalf("未记录首个事务: %+v", fdb.execs)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := q.Enqueue(context.Background(), KindTree, "rec-con", "delete", []byte(`{"deletes":[]}`)); err != nil {
			t.Errorf("delete: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	fdb.mu.Lock()
	early := ""
	for _, e := range fdb.execs {
		if e.txID == firstTx || e.txID == 0 {
			continue
		}
		if strings.Contains(e.query, "wa_push_queue") {
			early = e.query
			break
		}
	}
	fdb.mu.Unlock()
	close(release)
	wg.Wait()
	if early != "" {
		t.Fatalf("第二个事务在第一个提交前已执行队列删改（行锁未生效）: %s", early)
	}

	fdb.mu.Lock()
	execs := append([]fakeExec(nil), fdb.execs...)
	fdb.mu.Unlock()
	var secondTx int64
	for _, e := range execs {
		if e.txID != 0 && e.txID != firstTx && strings.Contains(e.query, "INSERT INTO wa_push_queue") {
			secondTx = e.txID
		}
	}
	if secondTx == 0 {
		t.Fatalf("第二个事务未写入队列: %+v", execs)
	}
	maxFirst, minSecond := -1, len(execs)
	for i, e := range execs {
		switch e.txID {
		case firstTx:
			maxFirst = i
		case secondTx:
			if i < minSecond {
				minSecond = i
			}
		}
	}
	if maxFirst > minSecond {
		t.Fatalf("第二个事务在第一个提交前已执行（maxFirst=%d minSecond=%d）", maxFirst, minSecond)
	}
}

// TestEnsureSchemaBackfillsPushQueueRecordKey 老库补 idx_record_status，并按载荷回填 record_key/op；
// 不得新增唯一键（老库存在大量 record_key=” 行）。
func TestEnsureSchemaBackfillsPushQueueRecordKey(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	fdb.on("SELECT COUNT(*) FROM information_schema.columns", []string{"COUNT(*)"},
		[][]driver.Value{{int64(1)}})
	fdb.on("SELECT INDEX_NAME FROM information_schema.STATISTICS", []string{"INDEX_NAME"}, nil)
	m := New(db, nil)
	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if fdb.execCount("ALTER TABLE wa_push_queue ADD KEY idx_record_status") != 1 {
		t.Fatalf("老库应补 idx_record_status，实际 %+v", fdb.execs)
	}
	backfill, ok := fdb.lastExec("UPDATE wa_push_queue SET record_key")
	if !ok {
		t.Fatalf("应回填 record_key，实际 %+v", fdb.execs)
	}
	if !strings.Contains(backfill.query, "$.records[0].crane_record_id") ||
		!strings.Contains(backfill.query, "$.deletes[0].crane_record_id") {
		t.Fatalf("回填需兼容 upsert/deletes 载荷: %s", backfill.query)
	}
	if fdb.execCount("UPDATE wa_push_queue SET op = 'delete'") != 1 {
		t.Fatalf("应回填 op=delete，实际 %+v", fdb.execs)
	}
	for _, e := range fdb.execs {
		if strings.Contains(e.query, "wa_push_queue") && strings.Contains(e.query, "UNIQUE") {
			t.Fatalf("不得给老库重复 record_key='' 行加强制唯一键: %s", e.query)
		}
	}
}
