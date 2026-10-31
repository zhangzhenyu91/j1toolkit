package worklog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSweepPendingExcludesInFlight 清扫 SQL 排除在飞照片（JSON 中的 id 参数）。
func TestSweepPendingExcludesInFlight(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	dify := &blockingDify{release: make(chan struct{})}
	env.module.Dify = dify
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"log_date", "destination_name"}, [][]driver.Value{{"2026-08-18", "孝义市"}})

	done := make(chan struct{})
	go func() {
		env.module.VerifyAndWriteBack(context.Background(), 321, "u", "2026-08-18", "孝义市", "https://cos.test/x.jpg")
		close(done)
	}()
	waitUntil(t, "验证进入在飞集合", func() bool {
		_, calls := dify.snapshot()
		return calls >= 1
	})
	if err := env.module.sweepPendingPhotos(context.Background()); err != nil {
		t.Fatalf("清扫失败: %v", err)
	}
	ex, ok := env.fake.lastExec("verify_status = 'pending' AND created_at")
	if !ok {
		t.Fatalf("未执行 pending 清扫：%v", env.fake.allQueries())
	}
	if !strings.Contains(ex.query, "id NOT IN") {
		t.Fatalf("清扫 SQL 应排除在飞照片，实际 %s", ex.query)
	}
	if len(ex.args) != 1 || fmt.Sprint(ex.args[0]) != "321" {
		t.Fatalf("清扫应排除在飞照片 id 321，实际参数 %v", ex.args)
	}
	close(dify.release)
	<-done
}

// TestSweepPendingNoInFlight 无在飞照片时清扫 SQL 不带 NOT IN（不产生空 IN 语法）。
func TestSweepPendingNoInFlight(t *testing.T) {
	env := newTestServer(t, identUser)
	if err := env.module.sweepPendingPhotos(context.Background()); err != nil {
		t.Fatalf("清扫失败: %v", err)
	}
	ex, ok := env.fake.lastExec("verify_status = 'pending' AND created_at")
	if !ok {
		t.Fatalf("未执行 pending 清扫：%v", env.fake.allQueries())
	}
	if strings.Contains(ex.query, "NOT IN") || len(ex.args) != 0 {
		t.Fatalf("无在飞照片时不应带 NOT IN，实际 %s args=%v", ex.query, ex.args)
	}
}

// TestStartPhotoSweeperImmediateAndIdempotentStop 启动即跑一次；stop 可重复调用且不再清扫。
func TestStartPhotoSweeperImmediateAndIdempotentStop(t *testing.T) {
	if defaultPhotoSweepInterval != 10*time.Minute {
		t.Fatalf("默认清扫周期应为 10 分钟，实际 %v", defaultPhotoSweepInterval)
	}
	env := newTestServer(t, identUser)
	env.module.SweepInterval = time.Hour // 只验证启动即跑，避免定时干扰
	stop := env.module.StartPhotoSweeper(context.Background())
	waitUntil(t, "启动即跑一次清扫", func() bool {
		return env.fake.execCount("verify_status = 'pending' AND created_at") >= 1
	})
	stop()
	stop() // 幂等：重复调用不 panic、不重复关闭
	n := env.fake.execCount("verify_status = 'pending' AND created_at")
	time.Sleep(20 * time.Millisecond)
	if got := env.fake.execCount("verify_status = 'pending' AND created_at"); got != n {
		t.Fatalf("stop 后不应再清扫，实际 %d -> %d", n, got)
	}
}

// TestEnsureSchemaAddsVerifyIndex 幂等补 (verify_status, created_at) 索引：缺则 ALTER，有则跳过。
func TestEnsureSchemaAddsVerifyIndex(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.onErr("INDEX_NAME = 'idx_verify_created'", sql.ErrNoRows)
	if err := env.module.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if got := env.fake.execCount("ADD KEY idx_verify_created"); got != 1 {
		t.Fatalf("缺索引应补齐一次，实际 %d 次", got)
	}

	env2 := newTestServer(t, identUser)
	env2.fake.on("INDEX_NAME = 'idx_verify_created'", []string{"INDEX_NAME"},
		[][]driver.Value{{"idx_verify_created"}})
	if err := env2.module.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if got := env2.fake.execCount("ADD KEY idx_verify_created"); got != 0 {
		t.Fatalf("索引已存在不应重复 ALTER，实际 %d 次", got)
	}
}
