// Final review fix：同记录辅证照片重推必须串行整个读取—上传—替换—提交过程，
// 事务内重读旧集合；对象 key 必须带不可重复随机段，避免同毫秒并发碰撞。
package treehazard

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReplaceEvidencePhotosSameRecordSerialized 同记录并发调用不得在上传阶段重叠：
// 修复前两个调用可同时进入 Put，maxConcurrent > 1。
func TestReplaceEvidencePhotosSameRecordSerialized(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)
	var running, maxRunning atomic.Int32
	env.objects.putHook = func(string) error {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
		running.Add(-1)
		return nil
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
		}()
	}
	close(start)
	wg.Wait()
	if got := maxRunning.Load(); got > 1 {
		t.Fatalf("同记录替换必须串行，观察到 %d 个并发上传", got)
	}
}

// TestReplaceEvidencePhotosRereadsOldInsideTx 事务内必须重读旧集合：
// 上传前的旧集合可能已过期，删除/清对象只能按事务内读到的 id/Key。
func TestReplaceEvidencePhotosRereadsOldInsideTx(t *testing.T) {
	env := newTestServer(t, identUser)
	// 先注册事务内查询（含 FOR UPDATE），旧接口规则随后；fakeDB 按注册顺序匹配子串。
	env.fake.on("SELECT id, cos_key FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence' FOR UPDATE",
		[]string{"id", "cos_key"}, [][]driver.Value{{int64(2), "treehazard/t/5/old-2.jpg"}})
	oldEvidenceRule(env.fake)

	stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
	if stored != 2 || failed != 0 {
		t.Fatalf("成功应 stored=2 failed=0，实际 %d/%d", stored, failed)
	}
	del, ok := env.fake.lastExec("DELETE FROM treehazard_photo WHERE id = ?")
	if !ok {
		t.Fatal("应删除旧表行")
	}
	if len(del.args) != 1 || del.args[0] != driver.Value(int64(2)) {
		t.Fatalf("应按事务内重读的旧 id=2 删除，实际 %v", del.args)
	}
	sawOld2, sawOld1 := false, false
	for _, key := range env.objects.deletes {
		if strings.Contains(key, "old-2") {
			sawOld2 = true
		}
		if strings.Contains(key, "old-1") {
			sawOld1 = true
		}
	}
	if !sawOld2 || sawOld1 {
		t.Fatalf("提交后只应清事务内旧对象 old-2: %v", env.objects.deletes)
	}
}

// TestReplaceEvidencePhotosKeysUniqueWithinSameMillisecond 同记录并发替换时对象 key
// 不得因同毫秒+同下标碰撞（旧实现仅用 UnixMilli 与下标）。
func TestReplaceEvidencePhotosKeysUniqueWithinSameMillisecond(t *testing.T) {
	env := newTestServer(t, identUser)
	oldEvidenceRule(env.fake)
	const calls = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stored, failed := env.module.replaceEvidencePhotos(context.Background(), "检修一班", 5, newPhotos())
			if stored != 2 || failed != 0 {
				t.Errorf("并发替换失败 stored=%d failed=%d", stored, failed)
			}
		}()
	}
	close(start)
	wg.Wait()
	env.objects.mu.Lock()
	keys := append([]string{}, env.objects.puts...)
	env.objects.mu.Unlock()
	if len(keys) != calls*2 {
		t.Fatalf("应上传 %d 个对象，实际 %d", calls*2, len(keys))
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("对象 key 碰撞: %s", key)
		}
		seen[key] = true
	}
}
