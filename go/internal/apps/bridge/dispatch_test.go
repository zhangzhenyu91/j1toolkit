package bridge

// 派车单取件建卡测试：解析（excelize 生成导出件）、建卡计划（纯函数）与 SyncNow 全流程（fake 设备/库）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"shade/internal/apps/usbcmd"
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
	file       []byte
	files      map[string][]byte
	pushed     map[string][]byte
	deleted    [][]string
	dlName     string
	dlErr      error
	mounted    int
	mountErr   error
	onDownload func()
	// noResult 不下发结果文件（模拟被控机未执行 → 等待超时）。
	noResult bool
	// resultErr 非空时下发 ok=false 的失败回执（id/app 沿用指令原值，error 原样回带）。
	resultErr string
	// lastCmdID 最近一次下发指令的原 id（断言回执关联用）。
	lastCmdID string
	// lastResult 最近一次自动生成的结果文件内容（消费删除后仍可断言）。
	lastResult []byte
}

func (f *fakeUsb) MountToHost(context.Context) error {
	f.mounted++
	return f.mountErr
}

func (f *fakeUsb) DownloadFile(_ context.Context, name string) ([]byte, error) {
	f.dlName = name
	if f.dlErr != nil {
		return nil, f.dlErr
	}
	if strings.Contains(name, "_result_") {
		if data, ok := f.files[name]; ok {
			return data, nil
		}
		return nil, nil
	}
	if f.onDownload != nil {
		f.onDownload()
	}
	return f.file, nil
}

func (f *fakeUsb) DeleteFiles(_ context.Context, names []string) error {
	f.deleted = append(f.deleted, names)
	for _, n := range names {
		delete(f.files, n)
	}
	return nil
}

func deletedFile(batches [][]string, name string) bool {
	for _, names := range batches {
		for _, n := range names {
			if n == name {
				return true
			}
		}
	}
	return false
}

func (f *fakeUsb) ListFiles(context.Context) ([]string, error) {
	out := make([]string, 0, len(f.files))
	for n := range f.files {
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeUsb) PushFile(_ context.Context, name string, data []byte) error {
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	if f.pushed == nil {
		f.pushed = map[string][]byte{}
	}
	f.files[name] = data
	f.pushed[name] = data
	if strings.HasSuffix(name, "_cmd.json") && !f.noResult {
		var cmd struct {
			ID  string `json:"id"`
			App string `json:"app"`
		}
		if json.Unmarshal(data, &cmd) == nil && cmd.ID != "" && cmd.App != "" {
			f.lastCmdID = cmd.ID
			res := usbcmd.Result{Version: 1, ID: cmd.ID, App: cmd.App, OK: true, Data: map[string]any{}}
			if f.resultErr != "" {
				// 失败回执沿用指令原 id/app，服务端 Await 按 id 命中后应原样暴露 error。
				res.OK = false
				res.Error = f.resultErr
			}
			raw, _ := json.Marshal(res)
			f.lastResult = raw
			f.files[cmd.App+"_result_"+cmd.ID+".json"] = raw
		}
	}
	return nil
}

func testCmdPolicy() *usbcmd.Policy {
	return &usbcmd.Policy{Waits: []time.Duration{0}, Total: time.Second}
}

// fakeStore DispatchStore 桩。
type fakeStore struct {
	members   []dispatchMember
	vehicles  []dispatchVehicle
	dests     []dispatchDest
	occupied  map[int64]bool
	entries   []dispatchEntry
	teams     []dispatchTeam // nil = 回退单班 {9, teamName}（既有用例缺省）；空切片 = 无开启班组
	created   []CreateCardRequest
	savedNo   [][2]any
	notifyIDs []int64
	teamName  string
}

// EnabledTeams 开启派车同步班组（teams nil 回退单班 {9, teamName}；空切片视为无开启班组）。
func (s *fakeStore) EnabledTeams(context.Context) ([]dispatchTeam, error) {
	if s.teams == nil {
		name := s.teamName
		if name == "" {
			name = "检修一班"
		}
		return []dispatchTeam{{ID: 9, Name: name}}, nil
	}
	return s.teams, nil
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
	if usb.mounted != 1 {
		t.Fatalf("mounted = %d（成功取件后收尾复挂恰好一次）", usb.mounted)
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

// 当日无文件：按旧版口径发「今日无文件」通知后正常返回（sync-now 不报 500），不删件。
func TestDispatchSyncerNoFile(t *testing.T) {
	usb := &fakeUsb{file: nil} // (nil,nil) = 文件不存在
	notice := &fakeNotice{}
	unlocked := 0
	s := &DispatchSyncer{Usb: usb, Store: &fakeStore{}, Notice: notice,
		Unlock: func(context.Context) error { unlocked++; return nil },
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}
	n, err := s.SyncNow(context.Background(), 9)
	if err != nil || n != 0 {
		t.Fatalf("无文件应正常返回: n=%d err=%v", n, err)
	}
	if unlocked != 1 {
		t.Fatalf("unlocked = %d", unlocked)
	}
	if usb.mounted != 1 {
		t.Fatalf("mounted = %d（无文件也应复挂一次）", usb.mounted)
	}
	if notice.gotTitle != "派车单同步：今日无文件" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	for _, want := range []string{"未在设备 U 盘找到「2026-10-27-派车单.xlsx」", "可能当日无派车单"} {
		if !strings.Contains(notice.gotContent, want) {
			t.Fatalf("missing %q in %s", want, notice.gotContent)
		}
	}
	if len(usb.deleted) != 0 {
		t.Fatalf("无文件不应删件: %v", usb.deleted)
	}
}

// 取件失败：通知「派车单同步失败」（含解禁补跑指引）并返回可读 error（Node fetchJob catch 口径）。
func TestDispatchSyncerFetchFailNotifies(t *testing.T) {
	usb := &fakeUsb{dlErr: errors.New("设备不在线")}
	notice := &fakeNotice{}
	s := &DispatchSyncer{Usb: usb, Store: &fakeStore{}, Notice: notice,
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}
	_, err := s.SyncNow(context.Background(), 9)
	if err == nil || !strings.Contains(err.Error(), "取件失败") {
		t.Fatalf("err = %v", err)
	}
	if notice.gotTitle != "派车单同步失败" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	for _, want := range []string{"取件失败：", "设备不在线", "设备已解禁", "手动补跑"} {
		if !strings.Contains(notice.gotContent, want) {
			t.Fatalf("missing %q in %s", want, notice.gotContent)
		}
	}
}

// 手动同步收尾复挂：成功取件、当日无文件、下载失败三条路径都恰好复挂一次
// （Await 单次策略取回后不再重挂，不复挂会让被控机长时间看不到 U 盘）。
func TestDispatchSyncNowRestoresShare(t *testing.T) {
	data := buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1", "张三", "2026-10-27 08:00", "西郊变电站", "D-1", "正常"),
	})
	newSyncer := func(usb *fakeUsb) *DispatchSyncer {
		return &DispatchSyncer{
			Usb: usb,
			Store: &fakeStore{
				members:  []dispatchMember{{ID: 1, Name: "张三", Sort: 1, TeamID: 9}},
				vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}},
				dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}},
			},
			Notice: &fakeNotice{},
			Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
		}
	}

	t.Run("成功取件", func(t *testing.T) {
		usb := &fakeUsb{file: data}
		if _, err := newSyncer(usb).SyncNow(context.Background(), 9); err != nil {
			t.Fatalf("SyncNow: %v", err)
		}
		if usb.mounted != 1 {
			t.Fatalf("mounted = %d（成功取件后复挂恰好一次）", usb.mounted)
		}
	})

	t.Run("当日无文件", func(t *testing.T) {
		usb := &fakeUsb{file: nil}
		if _, err := newSyncer(usb).SyncNow(context.Background(), 9); err != nil {
			t.Fatalf("SyncNow: %v", err)
		}
		if usb.mounted != 1 {
			t.Fatalf("mounted = %d（无文件也应复挂恰好一次）", usb.mounted)
		}
	})

	t.Run("下载失败", func(t *testing.T) {
		usb := &fakeUsb{dlErr: errors.New("设备不在线")}
		if _, err := newSyncer(usb).SyncNow(context.Background(), 9); err == nil {
			t.Fatal("expect fetch error")
		}
		if usb.mounted != 1 {
			t.Fatalf("mounted = %d（下载失败也应复挂恰好一次）", usb.mounted)
		}
	})
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

// 多开启班组一次导出件全部建卡（Node importOrdersMulti 口径：单班处理会吞掉他班行）。
func TestDispatchSyncerMultiTeam(t *testing.T) {
	data := buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1", "张三", "2026-10-27 08:00", "西郊变电站", "D-1", "正常"),
		dispatchCells("晋A2", "王五", "2026-10-27 09:00", "东郊", "D-2", "正常"),
	})
	usb := &fakeUsb{file: data}
	store := &fakeStore{
		teams: []dispatchTeam{{ID: 9, Name: "检修一班"}, {ID: 7, Name: "检修二班"}},
		members: []dispatchMember{
			{ID: 1, Name: "张三", Sort: 1, TeamID: 9},
			{ID: 3, Name: "王五", Sort: 1, TeamID: 7},
		},
		vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}, {ID: 12, Plate: "晋A2"}},
		dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}, {ID: 22, Name: "东郊变电站"}},
	}
	notice := &fakeNotice{}
	s := &DispatchSyncer{Usb: usb, Store: store, Notice: notice,
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}

	// 请求上下文班组（9）不圈定范围：两班的行都要建卡。
	n, err := s.SyncNow(context.Background(), 9)
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if n != 2 || len(store.created) != 2 {
		t.Fatalf("synced = %d, created = %+v", n, store.created)
	}
	got := map[int64]string{}
	for _, c := range store.created {
		got[c.TeamID] = c.OrderNo
	}
	if got[9] != "D-1" || got[7] != "D-2" {
		t.Fatalf("created by team = %+v", store.created)
	}
	// 结果通知逐班组分发（同 Node fetchJob 每班组一条）。
	if len(notice.titles) != 2 ||
		notice.titles[0] != "派车单同步完成（检修一班）" ||
		notice.titles[1] != "派车单同步完成（检修二班）" {
		t.Fatalf("notice titles = %v", notice.titles)
	}
}

// 无开启班组：不取件不删件（文件留给开启后再跑），报可读错误；也不复挂。
func TestDispatchSyncerNoEnabledTeams(t *testing.T) {
	usb := &fakeUsb{file: []byte("x")}
	store := &fakeStore{teams: []dispatchTeam{}}
	s := &DispatchSyncer{Usb: usb, Store: store,
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }}
	_, err := s.SyncNow(context.Background(), 9)
	if err == nil || !strings.Contains(err.Error(), "没有开启派车同步的班组") {
		t.Fatalf("err = %v", err)
	}
	if usb.dlName != "" || len(usb.deleted) != 0 {
		t.Fatalf("不应取件：dlName=%q deleted=%v", usb.dlName, usb.deleted)
	}
	if usb.mounted != 0 {
		t.Fatalf("无班组不应复挂：mounted=%d", usb.mounted)
	}
}

// 取件重入守卫：并行第二次直接报「已有取件任务在执行中」。
func TestDispatchSyncerReentryGuard(t *testing.T) {
	store := &fakeStore{}
	s := &DispatchSyncer{
		Usb: &fakeUsb{file: nil}, Store: store,
		Now: func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
	}
	if !s.begin() {
		t.Fatal("first begin should succeed")
	}
	if s.begin() {
		t.Fatal("second begin should fail")
	}
	s.end()
	if !s.begin() {
		t.Fatal("begin after end should succeed")
	}
	s.end()
}

// 每日准备：锁设备 + 挂载 U 盘 + 单次取件后解禁（原 09:20 取件已合并）。
func TestDispatchPrepareNow(t *testing.T) {
	usb := &fakeUsb{}
	locked, unlocked := 0, 0
	s := &DispatchSyncer{
		Usb: usb, Store: &fakeStore{}, CmdPolicy: testCmdPolicy(),
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
		Lock:   func(context.Context, int) error { locked++; return nil },
		Unlock: func(context.Context) error { unlocked++; return nil },
	}
	if err := s.PrepareNow(context.Background()); err != nil {
		t.Fatalf("PrepareNow: %v", err)
	}
	if locked != 1 || usb.mounted != 2 {
		t.Fatalf("locked=%d mounted=%d（初次挂载一次 + 收尾复挂一次）", locked, usb.mounted)
	}
	if usb.dlName != "2026-10-27-派车单.xlsx" {
		t.Fatalf("dlName = %q（准备阶段应同轮取件）", usb.dlName)
	}
	if unlocked != 1 {
		t.Fatalf("unlocked = %d（成功也应在取件后解禁）", unlocked)
	}
}

// 每日准备：无开启班组跳过（不锁不挂）。
func TestDispatchPrepareNowNoTeams(t *testing.T) {
	usb := &fakeUsb{}
	locked := 0
	s := &DispatchSyncer{
		Usb: usb, Store: &fakeStore{teams: []dispatchTeam{}}, CmdPolicy: testCmdPolicy(),
		Lock: func(context.Context, int) error { locked++; return nil },
	}
	if err := s.PrepareNow(context.Background()); err != nil {
		t.Fatalf("PrepareNow: %v", err)
	}
	if locked != 0 || usb.mounted != 0 {
		t.Fatalf("locked=%d mounted=%d", locked, usb.mounted)
	}
}

// 每日准备：挂载失败 → 解禁 + 多班组合并通知「派车单同步准备失败」+ 可读 error；
// 收尾仍尝试复挂（best-effort，避免被控机长时间看不到 U 盘）。
func TestDispatchPrepareNowRestoresShareOnMountFailure(t *testing.T) {
	usb := &fakeUsb{mountErr: errors.New("设备不在线")}
	locked, unlocked := 0, 0
	notice := &fakeNotice{}
	w := &fakeWx{}
	s := &DispatchSyncer{
		Usb: usb, Store: &fakeStore{teams: []dispatchTeam{{ID: 9, Name: "检修一班"}, {ID: 7, Name: "检修二班"}}},
		CmdPolicy: testCmdPolicy(),
		Notice:    notice, Wx: w,
		Lock:   func(context.Context, int) error { locked++; return nil },
		Unlock: func(context.Context) error { unlocked++; return nil },
	}
	err := s.PrepareNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "挂载 U 盘失败") {
		t.Fatalf("err = %v", err)
	}
	if locked != 1 || unlocked != 1 || usb.mounted != 2 {
		t.Fatalf("locked=%d unlocked=%d mounted=%d（初次挂载失败后仍做一次收尾复挂）", locked, unlocked, usb.mounted)
	}
	if notice.gotTitle != "派车单同步准备失败" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	for _, want := range []string{"设备不在线", "手动补跑"} {
		if !strings.Contains(notice.gotContent, want) {
			t.Fatalf("content = %s", notice.gotContent)
		}
	}
	// 班组群外发含全部开启班组（Node notify wxTeamIds 同口径）。
	if w.calls != 1 || len(w.teams) != 2 {
		t.Fatalf("wx = %+v", w)
	}
}

// 每日准备：被控机未回结果 → 等待超时；初次挂载一次 + 收尾再复挂一次。
func TestDispatchPrepareNowRestoresShareOnTimeout(t *testing.T) {
	usb := &fakeUsb{noResult: true}
	locked, unlocked := 0, 0
	notice := &fakeNotice{}
	s := &DispatchSyncer{
		Usb: usb, Store: &fakeStore{}, CmdPolicy: testCmdPolicy(), Notice: notice,
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
		Lock:   func(context.Context, int) error { locked++; return nil },
		Unlock: func(context.Context) error { unlocked++; return nil },
	}
	err := s.PrepareNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "等待被控机导出结果失败") {
		t.Fatalf("err = %v（应报等待超时）", err)
	}
	if usb.mounted != 2 {
		t.Fatalf("mounted = %d（初次挂载一次 + 收尾复挂一次）", usb.mounted)
	}
	if locked != 1 || unlocked != 1 {
		t.Fatalf("locked=%d unlocked=%d", locked, unlocked)
	}
	if notice.gotTitle != "派车单同步准备失败" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
}

// 每日准备：被控机认领指令但回执失败（如指令/参数 JSON 非法）：恢复 id 后的失败回执必须被
// Await 按原 id 关联，PrepareNow 报「被控机导出失败」并带出具体原因，不得退化成「结果超时」。
func TestDispatchPrepareNowSurfacesInvalidCommandResult(t *testing.T) {
	const cmdErr = "指令格式非法: JSON 解析失败: params.date 不是合法日期"
	usb := &fakeUsb{resultErr: cmdErr}
	locked, unlocked := 0, 0
	notice := &fakeNotice{}
	s := &DispatchSyncer{
		Usb: usb, Store: &fakeStore{}, CmdPolicy: testCmdPolicy(), Notice: notice,
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
		Lock:   func(context.Context, int) error { locked++; return nil },
		Unlock: func(context.Context) error { unlocked++; return nil },
	}
	err := s.PrepareNow(context.Background())
	if err == nil {
		t.Fatal("expect export failure")
	}
	if !strings.Contains(err.Error(), "被控机导出失败") {
		t.Fatalf("err = %v（应为被控机导出失败）", err)
	}
	if !strings.Contains(err.Error(), "指令格式非法") || !strings.Contains(err.Error(), "JSON 解析失败") {
		t.Fatalf("err = %v（应带出被控机具体错误）", err)
	}
	if strings.Contains(err.Error(), "结果超时") {
		t.Fatalf("err = %v（失败回执不应被当成等待超时）", err)
	}
	// 回执与指令原 id/app 一致（服务端 Await 靠 id 关联，恢复 id 后才不会误判超时）。
	rawCmd, ok := usb.pushed[usbcmd.CmdName("daily_sync")]
	if !ok || usb.lastCmdID == "" {
		t.Fatalf("未下发指令：pushed=%v lastCmdID=%q", usb.pushed, usb.lastCmdID)
	}
	var cmd usbcmd.Command
	if err := json.Unmarshal(rawCmd, &cmd); err != nil {
		t.Fatalf("指令解析失败：%v", err)
	}
	if cmd.ID != usb.lastCmdID {
		t.Fatalf("回执 id = %q，指令 id = %q", usb.lastCmdID, cmd.ID)
	}
	var res usbcmd.Result
	if err := json.Unmarshal(usb.lastResult, &res); err != nil {
		t.Fatalf("回执解析失败：%v", err)
	}
	if res.ID != cmd.ID || res.App != "daily_sync" || res.OK || !strings.Contains(res.Error, cmdErr) {
		t.Fatalf("回执 = %+v（应为同 id/daily_sync/ok=false/含具体错误）", res)
	}
	// 失败路径保留解禁与收尾复挂（与超时路径一致：初次挂载一次 + 收尾复挂一次）。
	if usb.mounted != 2 {
		t.Fatalf("mounted = %d（初次挂载一次 + 收尾复挂一次）", usb.mounted)
	}
	if locked != 1 || unlocked != 1 {
		t.Fatalf("locked=%d unlocked=%d", locked, unlocked)
	}
	if notice.gotTitle != "派车单同步准备失败" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	if !strings.Contains(notice.gotContent, "指令格式非法") {
		t.Fatalf("content = %s", notice.gotContent)
	}
}

// 每日准备：指令有效期 = 等待窗口 + 5 分钟（不再固定 4 小时）。
func TestDispatchPrepareNowCommandTTLMatchesWait(t *testing.T) {
	issuedTTL := func(t *testing.T, s *DispatchSyncer) time.Duration {
		t.Helper()
		usb := &fakeUsb{}
		s.Usb = usb
		s.Store = &fakeStore{}
		s.Now = func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) }
		// 锁定失败即中止（指令已下发），无需真正等待窗口。
		s.Lock = func(context.Context, int) error { return errors.New("锁定失败") }
		if err := s.PrepareNow(context.Background()); err == nil {
			t.Fatal("expect lock failure")
		}
		raw, ok := usb.pushed[usbcmd.CmdName("daily_sync")]
		if !ok {
			t.Fatalf("未下发指令：%v", usb.pushed)
		}
		var cmd usbcmd.Command
		if err := json.Unmarshal(raw, &cmd); err != nil {
			t.Fatalf("指令解析失败：%v", err)
		}
		issued, err := time.Parse(time.RFC3339, cmd.IssuedAt)
		if err != nil {
			t.Fatalf("issued_at = %q: %v", cmd.IssuedAt, err)
		}
		expires, err := time.Parse(time.RFC3339, cmd.ExpiresAt)
		if err != nil {
			t.Fatalf("expires_at = %q: %v", cmd.ExpiresAt, err)
		}
		return expires.Sub(issued)
	}
	// 缺省等待窗口 10 分钟 → 指令有效期 15 分钟。
	if got := issuedTTL(t, &DispatchSyncer{}); got != 15*time.Minute {
		t.Fatalf("default ttl = %v", got)
	}
	// 注入 2 分钟等待窗口 → 指令有效期 7 分钟。
	injected := usbcmd.Policy{Waits: []time.Duration{2 * time.Minute}, Total: 3 * time.Minute}
	if got := issuedTTL(t, &DispatchSyncer{CmdPolicy: &injected}); got != 7*time.Minute {
		t.Fatalf("2m wait ttl = %v", got)
	}
}

// 每日准备成功后立即下载导出件建卡（不再等 09:20 独立任务）。
func TestDispatchPrepareNowFetches(t *testing.T) {
	usb := &fakeUsb{file: buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1", "张三", "2026-10-27 08:00", "西郊变电站", "D-1", "正常"),
	})}
	store := &fakeStore{
		members:  []dispatchMember{{ID: 1, Name: "张三", Sort: 1, TeamID: 9}},
		vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}},
		dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}},
	}
	notice := &fakeNotice{}
	unlocked := 0
	usb.onDownload = func() {
		if unlocked != 1 {
			t.Fatalf("下载建卡前应已恢复文件传输，unlocked=%d", unlocked)
		}
	}
	s := &DispatchSyncer{
		Usb: usb, Store: store, CmdPolicy: testCmdPolicy(), Notice: notice,
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
		Lock:   func(context.Context, int) error { return nil },
		Unlock: func(context.Context) error { unlocked++; return nil },
	}
	if err := s.PrepareNow(context.Background()); err != nil {
		t.Fatalf("PrepareNow: %v", err)
	}
	if usb.dlName != "2026-10-27-派车单.xlsx" {
		t.Fatalf("dlName = %q", usb.dlName)
	}
	if !deletedFile(usb.deleted, "2026-10-27-派车单.xlsx") {
		t.Fatalf("下载后应删除 U 盘导出件，deleted = %+v", usb.deleted)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %+v", store.created)
	}
	if notice.gotTitle != "派车单同步完成（检修一班）" {
		t.Fatalf("notice title = %q", notice.gotTitle)
	}
	if unlocked != 1 {
		t.Fatalf("unlocked = %d", unlocked)
	}
	if usb.mounted != 2 {
		t.Fatalf("mounted = %d（初次挂载一次 + 收尾复挂一次）", usb.mounted)
	}
}

// 指令等待策略：测试注入 > CmdWait > 运行时配置 > 10 分钟缺省。
func TestDispatchCmdPolicy(t *testing.T) {
	ctx := context.Background()
	s := &DispatchSyncer{}
	if p := s.cmdPolicy(ctx); len(p.Waits) != 1 || p.Waits[0] != usbcmd.DefaultResultWait {
		t.Fatalf("default policy = %+v", p)
	}
	s = &DispatchSyncer{CmdWait: 2 * time.Minute}
	if p := s.cmdPolicy(ctx); p.Waits[0] != 2*time.Minute {
		t.Fatalf("CmdWait policy = %+v", p)
	}
	s = &DispatchSyncer{Cfg: fakeCfg{"WORKLOG_DISPATCH_CMD_WAIT_MINUTES": "7"}}
	if p := s.cmdPolicy(ctx); p.Waits[0] != 7*time.Minute {
		t.Fatalf("config policy = %+v", p)
	}
	injected := usbcmd.Once(0)
	s = &DispatchSyncer{CmdWait: 3 * time.Minute, CmdPolicy: &injected}
	if p := s.cmdPolicy(ctx); p.Waits[0] != usbcmd.DefaultResultWait {
		t.Fatalf("injected policy = %+v", p)
	}
}

func TestDispatchPolicyTTL(t *testing.T) {
	if got := policyTTLSec(usbcmd.Once(10 * time.Minute)); got != 900 {
		t.Fatalf("10m wait ttl = %d", got)
	}
	if got := policyTTLSec(usbcmd.Policy{Waits: []time.Duration{2 * time.Minute, 3 * time.Minute}}); got != 600 {
		t.Fatalf("5m wait ttl = %d", got)
	}
	if got := policyTTLSec(usbcmd.Policy{Waits: []time.Duration{0}}); got != 900 {
		t.Fatalf("zero wait ttl = %d", got)
	}
}

// 派车排程作业体（bridge/wire.go）：Prepare 与执行器对接，未接入触发即报可读错误。
func TestDispatchJobs(t *testing.T) {
	usb := &fakeUsb{file: buildDispatchXlsx(t, [][]string{
		dispatchCells("晋A1", "张三", "2026-10-27 08:00", "西郊变电站", "D-1", "正常"),
	})}
	store := &fakeStore{
		members:  []dispatchMember{{ID: 1, Name: "张三", Sort: 1, TeamID: 9}},
		vehicles: []dispatchVehicle{{ID: 11, Plate: "晋A1"}},
		dests:    []dispatchDest{{ID: 21, Name: "西郊变电站"}},
	}
	s := &DispatchSyncer{
		Usb: usb, Store: store, CmdPolicy: testCmdPolicy(),
		Lock:   func(context.Context, int) error { return nil },
		Unlock: func(context.Context) error { return nil },
		Now:    func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)) },
	}
	jobs := NewDispatchJobs(s)
	summary, err := jobs.Prepare(context.Background())
	if err != nil || usb.mounted != 2 || !strings.Contains(summary, "取件建卡") {
		t.Fatalf("prepare: %q %v", summary, err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %+v", store.created)
	}
	empty := NewDispatchJobs(nil)
	if _, err := empty.Prepare(context.Background()); err == nil {
		t.Fatal("expect not-wired prepare error")
	}
}
