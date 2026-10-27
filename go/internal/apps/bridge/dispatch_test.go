package bridge

// 派车单取件建卡测试：解析（excelize 生成导出件）、建卡计划（纯函数）与 SyncNow 全流程（fake 设备/库）。

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// buildDispatchXlsx 生成被控机导出口径的派车单 xlsx（A/C/E/F/J/M/N/T 列位）。
func buildDispatchXlsx(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	headers := make([]string, 20)
	headers[dispatchColPlate] = "车牌号码"
	headers[dispatchColMembers] = "用车人"
	headers[dispatchColTo] = "目的地"
	headers[dispatchColState] = "派车单状态"
	write := func(row int, cells []string) {
		for i, v := range cells {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	write(1, headers)
	for i, r := range rows {
		write(i+2, r)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

// dispatchCells 按列位铺一行。
func dispatchCells(plate, members, planTime, to, orderNo, state string) []string {
	cells := make([]string, 20)
	cells[dispatchColPlate] = plate
	cells[dispatchColMembers] = members
	cells[dispatchColPlanTime] = planTime
	cells[dispatchColTo] = to
	cells[dispatchColOrderNo] = orderNo
	cells[dispatchColState] = state
	return cells
}

func TestParseDispatchOrders(t *testing.T) {
	data := buildDispatchXlsx(t, [][]string{
		dispatchCells("晋JBA773(蓝)", "张三 李四", "2026-10-27 08:00", "变电站", "D-1", "正常"),
		dispatchCells("晋J1", "张三", "2026-10-26 08:00", "旧县", "", "正常"), // 非当日 → stale
		dispatchCells("晋J2", "王五", "2026-10-27", "X", "", "已取消"),       // 已取消 → dropped
		dispatchCells("(蓝)", "赵六", "2026-10-27", "X", "", "正常"),        // 无车牌 → dropped
		dispatchCells("晋J3", "孙七", "", "X", "D-3", "正常"),               // F 空 → T 兜底？均无 → stale
	})
	rows, dropped, stale, err := parseDispatchOrders(data, "2026-10-27")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 || dropped != 2 || stale != 2 {
		t.Fatalf("rows=%d dropped=%d stale=%d", len(rows), dropped, stale)
	}
	if rows[0].Plate != "晋JBA773" || rows[0].OrderNo != "D-1" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestParseDispatchOrdersHeaderMismatch(t *testing.T) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	_ = f.SetCellValue(sheet, "A1", "错误表头")
	var buf bytes.Buffer
	_ = f.Write(&buf)
	_, _, _, err := parseDispatchOrders(buf.Bytes(), "2026-10-27")
	if err == nil || !strings.Contains(err.Error(), "格式与预期不符") {
		t.Fatalf("err = %v", err)
	}
}

func TestDispatchRowDateFallback(t *testing.T) {
	cells := make([]string, 20)
	cells[dispatchColPlanTime] = "无法解析"
	cells[dispatchColCreatedAt] = "2026/10/27" // 非 yyyy-M-d 形态
	if got := dispatchRowDate(cells); got != "" {
		t.Fatalf("got = %q", got)
	}
	cells[dispatchColCreatedAt] = "2026-10-7 09:00"
	if got := dispatchRowDate(cells); got != "2026-10-07" {
		t.Fatalf("got = %q", got)
	}
}

func TestPlanDispatchCards(t *testing.T) {
	members := []dispatchMember{
		{ID: 1, Name: "张三", Sort: 1, TeamID: 9},
		{ID: 2, Name: "李四", Sort: 2, TeamID: 9},
	}
	vehicles := []dispatchVehicle{{ID: 11, Plate: "晋A1"}}
	dests := []dispatchDest{{ID: 21, Name: "西郊变电站"}}
	rows := []dispatchRow{
		{Plate: "晋A1", UserText: "张三 李四", To: "西郊变电站", OrderNo: "D-1"}, // 2 匹配胜出
		{Plate: "晋A1", UserText: "张三", To: "西郊变电站", OrderNo: "D-2"},    // 张三已被上一行挂走
		{Plate: "晋A9", UserText: "李四", To: "西郊变电站"},                    // 车牌未入字典
		{Plate: "晋A1", UserText: "李四", To: "不存在的地方"},                   // 目的地未入字典
	}
	plans, skipped := planDispatchCards(rows, members, vehicles, dests, nil)
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	if len(plans[0].Members) != 2 || plans[0].Row.OrderNo != "D-1" {
		t.Fatalf("plan = %+v", plans[0])
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v", skipped)
	}
	if skipped[0].Reason != "车牌未入字典" || skipped[1].Reason != "目的地未入字典" {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestBucketDispatchRows(t *testing.T) {
	all := []dispatchMember{
		{ID: 1, Name: "张三", TeamID: 9},
		{ID: 2, Name: "王五", TeamID: 9},
		{ID: 3, Name: "赵六", TeamID: 7}, // 未开启班组
	}
	rows := []dispatchRow{
		{Plate: "a", UserText: "张三 王五"}, // 本班
		{Plate: "b", UserText: "张三 赵六"}, // 跨班 → cross
		{Plate: "c", UserText: "路人"},    // outside
		{Plate: "d", UserText: "赵六"},    // 仅未开启班组
	}
	b := bucketDispatchRows(rows, all, map[int64]bool{9: true})
	if len(b.ByTeam[9]) != 1 || b.ByTeam[9][0].Plate != "a" {
		t.Fatalf("byTeam = %+v", b.ByTeam)
	}
	if len(b.Cross[9]) != 1 || len(b.Cross[7]) != 1 {
		t.Fatalf("cross = %+v", b.Cross)
	}
	if b.Outside != 1 || b.OtherTeam != 1 {
		t.Fatalf("outside=%d other=%d", b.Outside, b.OtherTeam)
	}
}

// fakeUsb UsbFetcher 桩。
type fakeUsb struct {
	file    []byte
	deleted [][]string
	dlName  string
}

func (f *fakeUsb) DownloadFile(_ context.Context, name string) ([]byte, error) {
	f.dlName = name
	return f.file, nil
}

func (f *fakeUsb) DeleteFiles(_ context.Context, names []string) error {
	f.deleted = append(f.deleted, names)
	return nil
}

// fakeStore DispatchStore 桩。
type fakeStore struct {
	members   []dispatchMember
	vehicles  []dispatchVehicle
	dests     []dispatchDest
	occupied  map[int64]bool
	entries   []dispatchEntry
	created   []CreateCardRequest
	savedNo   [][2]any
	notifyIDs []int64
	teamName  string
}

func (s *fakeStore) AllEnabledMembers(context.Context) ([]dispatchMember, error) {
	return s.members, nil
}

func (s *fakeStore) TeamVehicles(context.Context, int64) ([]dispatchVehicle, error) {
	return s.vehicles, nil
}

func (s *fakeStore) TeamDestinations(context.Context, int64) ([]dispatchDest, error) {
	return s.dests, nil
}

func (s *fakeStore) OccupiedMembers(context.Context, string) (map[int64]bool, error) {
	if s.occupied == nil {
		return map[int64]bool{}, nil
	}
	return s.occupied, nil
}

func (s *fakeStore) TeamEntries(context.Context, int64, string) ([]dispatchEntry, error) {
	return s.entries, nil
}

func (s *fakeStore) CreatorID(context.Context) (int64, error) { return 1, nil }

func (s *fakeStore) CreateCard(_ context.Context, req CreateCardRequest) (int64, error) {
	s.created = append(s.created, req)
	return int64(100 + len(s.created)), nil
}

func (s *fakeStore) SaveOrderNo(_ context.Context, entryID int64, orderNo string) error {
	s.savedNo = append(s.savedNo, [2]any{entryID, orderNo})
	return nil
}

func (s *fakeStore) NotifyTargets(context.Context, int64) ([]int64, error) {
	if s.notifyIDs == nil {
		return []int64{1, 2}, nil
	}
	return s.notifyIDs, nil
}

func (s *fakeStore) TeamName(context.Context, int64) (string, error) {
	if s.teamName == "" {
		return "检修一班", nil
	}
	return s.teamName, nil
}

func TestDispatchSyncerSyncNow(t *testing.T) {
	data := buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1(绿)", "张三 李四", "2026-10-27 08:00", "西郊变电站", "D-1", "正常"),
		dispatchCells("晋A2", "王五", "2026-10-27 09:00", "东郊", "D-2", "正常"),
	})
	usb := &fakeUsb{file: data}
	store := &fakeStore{
		members: []dispatchMember{
			{ID: 1, Name: "张三", Sort: 1, TeamID: 9},
			{ID: 2, Name: "李四", Sort: 2, TeamID: 9},
			{ID: 3, Name: "王五", Sort: 3, TeamID: 9},
		},
		vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}, {ID: 12, Plate: "晋A2"}},
		dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}, {ID: 22, Name: "东郊变电站"}},
	}
	notice := &fakeNotice{}
	unlocked := 0
	s := &DispatchSyncer{
		Usb: usb, Store: store, Notice: notice,
		Unlock: func(context.Context) error { unlocked++; return nil },
		// 北京时间 2026-10-27 上午（当日口径）。
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
	}

	n, err := s.SyncNow(context.Background(), 9)
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced = %d, created = %+v", n, store.created)
	}
	if usb.dlName != "2026-10-27-派车单.xlsx" {
		t.Fatalf("dlName = %q", usb.dlName)
	}
	if len(usb.deleted) != 1 || usb.deleted[0][0] != "2026-10-27-派车单.xlsx" {
		t.Fatalf("deleted = %+v", usb.deleted)
	}
	if unlocked != 1 {
		t.Fatalf("unlocked = %d", unlocked)
	}
	if store.created[0].OrderNo != "D-1" || store.created[0].DestID == nil || *store.created[0].DestID != 21 {
		t.Fatalf("created[0] = %+v", store.created[0])
	}
	if notice.gotTitle != "派车单同步完成（检修一班）" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	if !strings.Contains(notice.gotContent, "新建 2 张出车卡片") {
		t.Fatalf("notice content = %s", notice.gotContent)
	}
}

func TestDispatchSyncerNoFile(t *testing.T) {
	usb := &fakeUsb{file: nil} // (nil,nil) = 文件不存在
	s := &DispatchSyncer{Usb: usb, Store: &fakeStore{},
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}
	_, err := s.SyncNow(context.Background(), 9)
	if err == nil || !strings.Contains(err.Error(), "未在设备 U 盘找到") {
		t.Fatalf("err = %v", err)
	}
}

func TestDispatchSyncerOverlapCompare(t *testing.T) {
	data := buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1", "张三", "2026-10-27 08:00", "西郊变电站", "D-9", "正常"),
	})
	usb := &fakeUsb{file: data}
	store := &fakeStore{
		members:  []dispatchMember{{ID: 1, Name: "张三", TeamID: 9}},
		vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}, {ID: 12, Plate: "晋A9"}},
		dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}},
		entries: []dispatchEntry{{
			ID: 55, Plate: "晋A9", DestName: "东郊变电站", MemberNames: []string{"张三", "王五"},
		}},
	}
	notice := &fakeNotice{}
	s := &DispatchSyncer{Usb: usb, Store: store, Notice: notice,
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}
	n, err := s.SyncNow(context.Background(), 9)
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if n != 0 {
		t.Fatalf("should not create, got %d", n)
	}
	// 派车单号回填已有单卡。
	if len(store.savedNo) != 1 || store.savedNo[0][1] != "D-9" {
		t.Fatalf("savedNo = %+v", store.savedNo)
	}
	for _, want := range []string{"车牌不一致", "目的地不一致", "用车人多出：王五"} {
		if !strings.Contains(notice.gotContent, want) {
			t.Fatalf("missing %q in %s", want, notice.gotContent)
		}
	}
}

func TestDispatchSyncerNotWired(t *testing.T) {
	s := &DispatchSyncer{}
	if _, err := s.SyncNow(context.Background(), 1); err == nil {
		t.Fatal("expect not-wired error")
	}
}
