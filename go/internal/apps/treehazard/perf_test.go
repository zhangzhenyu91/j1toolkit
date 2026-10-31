package treehazard

// Task 7 树障热路径测试：照片批量装配复用一次 COS 访问前缀（空前缀回退历史地址）、
// 暂存导入事务内只写 DB，旧辅证 COS 对象删除全部在 Commit 之后（失败仅记录、不回滚已导入数据）。

import (
	"context"
	"database/sql/driver"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingObjects 统计 PublicURL 调用次数的对象存储桩；base 为空模拟运行时未配置。
type countingObjects struct {
	*memObjects
	mu    sync.Mutex
	calls int
	base  string
}

func (c *countingObjects) PublicURL(key string) string {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.base == "" {
		return ""
	}
	return c.base + key
}

func (c *countingObjects) urlCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestAttachPhotosURLBaseResolvedOnce 批量装配多张照片只解析一次 PublicURL。
func TestAttachPhotosURLBaseResolvedOnce(t *testing.T) {
	env := newTestServer(t, identUser)
	co := &countingObjects{memObjects: env.objects, base: "https://cos.test/"}
	env.module.Objects = co
	now := time.Now()
	env.fake.on("WHERE record_id IN",
		[]string{"id", "record_id", "kind", "cos_key", "url", "uploaded_by", "created_at"},
		[][]driver.Value{
			{int64(1), int64(11), "before", "treehazard/a/1.jpg", "https://legacy.example/1.jpg", int64(4), now},
			{int64(2), int64(12), "before", "treehazard/a/2.jpg", "https://legacy.example/2.jpg", int64(4), now},
			{int64(3), int64(12), "after", "treehazard/a/3.jpg", "https://legacy.example/3.jpg", int64(4), now},
		})
	records := []Record{{ID: 11}, {ID: 12}}
	if err := env.module.attachPhotos(context.Background(), records); err != nil {
		t.Fatalf("装配照片失败: %v", err)
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("批量装配应只解析一次 PublicURL，实际 %d 次", got)
	}
	if len(records[0].Photos) != 1 || records[0].Photos[0].URL != "https://cos.test/treehazard/a/1.jpg" {
		t.Fatalf("照片地址应按复用前缀拼接，实际 %#v", records[0].Photos)
	}
	if len(records[1].Photos) != 2 || records[1].Photos[1].URL != "https://cos.test/treehazard/a/3.jpg" {
		t.Fatalf("第二组照片地址应按复用前缀拼接，实际 %#v", records[1].Photos)
	}
}

// TestAttachPhotosURLBaseEmptyFallback 前缀未配置时回退库内历史地址。
func TestAttachPhotosURLBaseEmptyFallback(t *testing.T) {
	env := newTestServer(t, identUser)
	co := &countingObjects{memObjects: env.objects}
	env.module.Objects = co
	env.fake.on("WHERE record_id IN",
		[]string{"id", "record_id", "kind", "cos_key", "url", "uploaded_by", "created_at"},
		[][]driver.Value{
			{int64(1), int64(11), "before", "treehazard/a/1.jpg", "https://legacy.example/1.jpg", nil, time.Now()},
		})
	records := []Record{{ID: 11}}
	if err := env.module.attachPhotos(context.Background(), records); err != nil {
		t.Fatalf("装配照片失败: %v", err)
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("空 base 也应只解析一次 PublicURL，实际 %d 次", got)
	}
	if len(records[0].Photos) != 1 || records[0].Photos[0].URL != "https://legacy.example/1.jpg" {
		t.Fatalf("空 base 应回退库内历史地址，实际 %#v", records[0].Photos)
	}
}

// stagingImportEnv 暂存导入测试骨架：批次一条记录撞幂等键、带一张暂存辅证照片与一张旧辅证照片。
func stagingImportEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT crane_session_id, status FROM treehazard_staging_batch",
		[]string{"crane_session_id", "status"}, [][]driver.Value{{"sess-1", "pending"}})
	env.fake.on("FROM treehazard_staging_record WHERE batch_id", []string{
		"id", "batch_id", "crane_record_id", "voltage_level", "line_name", "section", "position_note", "tree_species",
		"tree_count", "dist_small", "dist_big", "diameter", "horizontal", "vertical", "vertical_num",
		"finder_name", "found_at", "handler_name", "handle_note", "handled_at",
	}, [][]driver.Value{{
		int64(11), int64(3), "c1", "110kV", "甲线", "3#-4#", "", "杨树",
		"2", "5", "6", "20", "3", "超高2", float64(2),
		"张三", "2026-10-20", "", "", "",
	}})
	env.fake.on("SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ? AND (crane_record_id",
		[]string{"cnt"}, [][]driver.Value{{int64(1)}})
	env.fake.on("crane_session_id = ? AND crane_record_id = ?", []string{"id"}, [][]driver.Value{{int64(55)}})
	env.fake.on("FROM treehazard_staging_photo WHERE batch_id", []string{"id", "cos_key", "url"},
		[][]driver.Value{{int64(7), "treehazard/new.jpg", "https://legacy.example/new.jpg"}})
	env.fake.on("FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'",
		[]string{"id", "cos_key"}, [][]driver.Value{{int64(8), "treehazard/old.jpg"}})
	env.fake.on("SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ?",
		[]string{"cnt"}, [][]driver.Value{{int64(0)}})
	return env
}

// eventIndexes 返回 commit 与 COS 删除事件的下标。
func eventIndexes(events []string) (commitAt int, deleteAts []int) {
	commitAt = -1
	for i, e := range events {
		switch {
		case e == "commit":
			commitAt = i
		case strings.HasPrefix(e, "cos-delete"):
			deleteAts = append(deleteAts, i)
		}
	}
	return commitAt, deleteAts
}

// TestStagingImportCOSDeleteAfterCommit 暂存导入：旧辅证 COS 删除全部在 Commit 之后，且导入地址复用前缀。
func TestStagingImportCOSDeleteAfterCommit(t *testing.T) {
	env := stagingImportEnv(t)
	co := &countingObjects{memObjects: env.objects, base: "https://cos.test/"}
	co.deleteHook = func(key string) { env.fake.addEvent("cos-delete:" + key) }
	env.module.Objects = co

	rec := env.do(t, "POST", "/api/v1/treehazard/staging/batches/3/import", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("导入应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["imported"]) != "1" {
		t.Fatalf("应 imported=1，实际 %v", data)
	}
	commitAt, deleteAts := eventIndexes(env.fake.eventLog())
	if commitAt < 0 {
		t.Fatalf("未记录 Commit 事件：%v", env.fake.eventLog())
	}
	if len(deleteAts) == 0 {
		t.Fatalf("旧辅证 COS 对象应删除：%v", env.fake.eventLog())
	}
	for _, at := range deleteAts {
		if at < commitAt {
			t.Fatalf("COS 删除必须发生在 Commit 之后，事件序：%v", env.fake.eventLog())
		}
	}
	if len(co.deletes) != 1 || co.deletes[0] != "treehazard/old.jpg" {
		t.Fatalf("应删除旧辅证对象 treehazard/old.jpg，实际 %v", co.deletes)
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("导入每请求应只解析一次 PublicURL，实际 %d 次", got)
	}
	ins, ok := env.fake.lastExec("INSERT INTO treehazard_photo")
	if !ok {
		t.Fatalf("未找到辅证照片插入语句")
	}
	if fmt.Sprint(ins.args[2]) != "https://cos.test/treehazard/new.jpg" {
		t.Fatalf("辅证照片地址应按复用前缀拼接，实际 %v", ins.args)
	}
}

// TestStagingImportCOSDeleteFailureKeepsImported COS 删除失败仅记录，不回滚已导入数据。
func TestStagingImportCOSDeleteFailureKeepsImported(t *testing.T) {
	env := stagingImportEnv(t)
	co := &countingObjects{memObjects: env.objects, base: "https://cos.test/"}
	co.failDelete = true
	co.deleteHook = func(key string) { env.fake.addEvent("cos-delete:" + key) }
	env.module.Objects = co

	rec := env.do(t, "POST", "/api/v1/treehazard/staging/batches/3/import", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("COS 删除失败不应影响导入响应，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["imported"]) != "1" {
		t.Fatalf("应 imported=1，实际 %v", data)
	}
	for _, e := range env.fake.eventLog() {
		if e == "rollback" {
			t.Fatalf("COS 删除失败不得回滚导入事务，事件序：%v", env.fake.eventLog())
		}
	}
	if env.fake.execCount("UPDATE treehazard_staging_batch SET status = 'imported'") != 1 {
		t.Fatalf("批次应正常置 imported：%v", env.fake.allQueries())
	}
}
