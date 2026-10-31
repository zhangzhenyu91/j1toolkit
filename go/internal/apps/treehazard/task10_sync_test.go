package treehazard

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

// prependRule 把错误规则放到最前（fake 规则首条命中生效）。
func prependRule(f *fakeDB, match string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append([]fakeRule{{match: match, err: err}}, f.rules...)
}

// 删除墓碑查询失败必须终止同步，不能按“无墓碑”继续 diff。
func TestSyncFailsClosedOnDeletedIDQueryError(t *testing.T) {
	m, fake, usb := syncEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	prependRule(fake, "FROM treehazard_deleted_id", errors.New("tombstone query failed"))
	_, err := m.RunIntranetCycle(context.Background(), "manual")
	if err == nil {
		t.Fatal("墓碑查询失败必须终止同步")
	}
	if fake.execCount("UPDATE treehazard_record SET synced_at = NOW()") != 0 {
		t.Fatal("墓碑查询失败不得继续下发/落同步点")
	}
	if containsStr(usb.pushed, "检修一班树木统计表.xlsx") {
		t.Fatalf("失败轮次不得下发统计表：%v", usb.pushed)
	}
}

// 裁决快照查询失败同样 fail-closed：不得应用 diff 回写。
func TestSyncFailsClosedOnAdjudicatedQueryError(t *testing.T) {
	m, fake, usb := syncEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	usb.files["检修一班树木统计表.xlsx"] = intranetSheet(t, []string{
		"1", "10kV", "甲线", "3#-4#", "柳树", "3", "5", "5", "20", "2", "4.5", "张三", "2026.10.01",
		"", "", "", "1",
	})
	prependRule(fake, "FROM treehazard_conflict", errors.New("adjudicated query failed"))
	_, err := m.RunIntranetCycle(context.Background(), "manual")
	if err == nil {
		t.Fatal("裁决快照查询失败必须终止同步")
	}
	if fake.execCount("INSERT INTO treehazard_record") != 0 || fake.execCount("UPDATE treehazard_record SET") != 0 {
		t.Fatal("裁决快照查询失败不得应用 diff")
	}
}

// blockingUsb 在 Push snapshot 指令时阻塞，供并发重入测试。
type blockingUsb struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (b *blockingUsb) MountToHost(context.Context) error { return nil }
func (b *blockingUsb) DownloadFile(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (b *blockingUsb) ListFiles(context.Context) ([]string, error) { return nil, nil }
func (b *blockingUsb) DeleteFiles(context.Context, []string) error { return nil }
func (b *blockingUsb) PushFile(ctx context.Context, name string, data []byte) error {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil
}

// 手动与排程都走核心 Run：并发第二轮必须立即返回 ErrSyncBusy，不得重叠执行。
func TestRunIntranetCycleSerializesConcurrentRuns(t *testing.T) {
	m, _, _ := syncEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	usb := &blockingUsb{started: make(chan struct{}), release: make(chan struct{})}
	m.Usb = usb
	done := make(chan error, 1)
	go func() {
		_, err := m.RunIntranetCycle(context.Background(), "night")
		done <- err
	}()
	select {
	case <-usb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("第一轮未进入同步")
	}
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); !errors.Is(err, ErrSyncBusy) {
		t.Fatalf("并发轮次应返回 ErrSyncBusy，实际 %v", err)
	}
	close(usb.release)
	if err := <-done; err != nil {
		t.Fatalf("第一轮应完成: %v", err)
	}
	// 释放后锁应归还，可再次运行。
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); errors.Is(err, ErrSyncBusy) {
		t.Fatalf("上一轮结束后不应仍锁定: %v", err)
	}
}
