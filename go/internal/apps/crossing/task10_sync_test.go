package crossing

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// crossingIntranetSheet 构造合法内网表（表头含「序号」，O 列记录 ID）。
func crossingIntranetSheet(t *testing.T, recordID string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetList()[0]
	headers := []string{"序号", "电压等级", "线路名称", "区段", "跨越类型", "距小号侧距离", "距大号侧距离",
		"交叉物距边线水平距离", "交叉垂直距离", "交叉角", "是否存在隐患", "发现人", "巡视时间", "备注", "记录ID"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	row := []string{"1", "10kV", "甲线", "15#-16#", "跨越高速", "50", "50", "12", "8", "45", "无", "李四", "2026-10-01", "", recordID}
	for i, v := range row {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(sheet, cell, v)
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("生成内网表失败: %v", err)
	}
	return buf.Bytes()
}

// newCrossingSyncEnv 构造可注入失败的内网同步环境。
func newCrossingSyncEnv(t *testing.T) (*Module, *fakeDB, *crossingSyncFakeUsb) {
	t.Helper()
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "crossing_type",
		"dist_small", "dist_big", "cross_horizontal", "cross_vertical", "cross_angle", "has_hazard",
		"finder_name", "inspect_at", "remark", "source", "crane_session_id", "crane_record_id",
		"synced_at", "created_by", "created_at", "updated_at",
	}, crossingRecordRow(1, "甲线"))
	fdb.onRowsAlways("FROM crossing_deleted_id", []string{"id"})
	m := New(db, fakeCfg{kv: map[string]string{
		"CROSSING_SYNC_TEAM":           "检修一班",
		"CROSSING_SYNC_DEVICE_DDNS":    "dev-1",
		"CROSSING_SYNC_CMD_WINDOW_MIN": "0",
	}})
	usb := &crossingSyncFakeUsb{files: map[string][]byte{}}
	m.Usb = usb
	return m, fdb, usb
}

// 删除墓碑查询失败必须终止同步。
func TestSyncFailsClosedOnDeletedIDQueryError(t *testing.T) {
	db, fdb := openFake(t)
	fdb.onRowsAlways("FROM sys_team WHERE name = ?", []string{"id", "name"},
		[]driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "crossing_type",
		"dist_small", "dist_big", "cross_horizontal", "cross_vertical", "cross_angle", "has_hazard",
		"finder_name", "inspect_at", "remark", "source", "crane_session_id", "crane_record_id",
		"synced_at", "created_by", "created_at", "updated_at",
	}, crossingRecordRow(1, "甲线"))
	fdb.onErr("FROM crossing_deleted_id", errors.New("tombstone query failed"))
	m := New(db, fakeCfg{kv: map[string]string{
		"CROSSING_SYNC_TEAM": "检修一班", "CROSSING_SYNC_DEVICE_DDNS": "dev-1", "CROSSING_SYNC_CMD_WINDOW_MIN": "0",
	}})
	usb := &crossingSyncFakeUsb{files: map[string][]byte{}}
	m.Usb = usb
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); err == nil {
		t.Fatal("墓碑查询失败必须终止同步")
	}
	if len(usb.pushed) != 1 { // 仅 snapshot 指令，不得下发统计表
		t.Fatalf("失败轮次不得下发统计表：%v", usb.pushed)
	}
}

// 裁决快照查询失败必须终止同步，不得应用 diff。
func TestSyncFailsClosedOnAdjudicatedQueryError(t *testing.T) {
	m, fdb, usb := newCrossingSyncEnv(t)
	usb.files["检修一班交跨统计表.xlsx"] = crossingIntranetSheet(t, "1")
	fdb.onErr("FROM crossing_conflict", errors.New("adjudicated query failed"))
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); err == nil {
		t.Fatal("裁决快照查询失败必须终止同步")
	}
	if n := len(fdb.callsWith("INSERT INTO crossing_record")); n != 0 {
		t.Fatalf("不得应用 diff 插入，实际 %d 次", n)
	}
	if n := len(fdb.callsWith("UPDATE crossing_record SET")); n != 0 {
		t.Fatalf("不得应用 diff 回写，实际 %d 次", n)
	}
}

// blockingCrossingUsb PushFile 阻塞。
type blockingCrossingUsb struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (b *blockingCrossingUsb) MountToHost(context.Context) error { return nil }
func (b *blockingCrossingUsb) DownloadFile(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (b *blockingCrossingUsb) ListFiles(context.Context) ([]string, error) { return nil, nil }
func (b *blockingCrossingUsb) DeleteFiles(context.Context, []string) error { return nil }
func (b *blockingCrossingUsb) PushFile(context.Context, string, []byte) error {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil
}

// 手动与排程并发时核心 Run 必须互斥。
func TestRunIntranetCycleSerializesConcurrentRuns(t *testing.T) {
	m, _, _ := newCrossingSyncEnv(t)
	usb := &blockingCrossingUsb{started: make(chan struct{}), release: make(chan struct{})}
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
	if _, err := m.RunIntranetCycle(context.Background(), "manual"); errors.Is(err, ErrSyncBusy) {
		t.Fatalf("上一轮结束后不应仍锁定: %v", err)
	}
}
