// worklog.DispatchSyncer 真实适配：派车单「取件建卡」（迁移 server/src/worklog/dispatch-sync.js
// fetchJob / importOrdersMulti 的全量开启班组建卡口径：一次导出件覆盖全部开启班组，范围以
// worklog_dispatch_sync_team 开关表为准）：U 盘取「yyyy-MM-dd-派车单.xlsx」→ 解析
// （A=车牌 C=驾驶员 E=用车人 F=预计用车时间 J=目的地 M=派车单号 N=状态 T=创建时间）→
// 剔除已取消/非当日/无车牌行 → 用车人子串匹配（跨班仅通知不建卡）→ 车牌/目的地字典命中 →
// 事务建卡（派车单号随卡写 dispatch_order_no）→ 已有手动卡比对（仅通知不改库）→ 结果通知。
// 设备取件复用 UsbLink 侧小型接口（UsbFetcher），库操作收口 DispatchStore（SQLDispatchStore 真实现，
// 测试注入 fake）；解析与建卡计划为纯函数（与 Node 同名段同口径，便于本地自测）。
package bridge

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"shade/internal/apps/kvm"
	"shade/internal/apps/worklog"
)

// ---- 导出表解析（parseOrders 口径）----

// 导出表固定列位（被控机导出脚本生成口径）。
const (
	dispatchColPlate     = 0  // A 车牌号码
	dispatchColDriver    = 2  // C 驾驶员
	dispatchColMembers   = 4  // E 用车人
	dispatchColPlanTime  = 5  // F 预计用车时间
	dispatchColTo        = 9  // J 目的地
	dispatchColOrderNo   = 12 // M 派车单号
	dispatchColState     = 13 // N 派车单状态
	dispatchColCreatedAt = 19 // T 创建时间
)

// dispatchStateCancelled 已取消行剔除标记。
const dispatchStateCancelled = "已取消"

// dispatchRow 解析后的有效派车单行。
type dispatchRow struct {
	Plate    string
	Driver   string
	UserText string // 用车人原始串（空格分隔，偶有连写）
	To       string
	OrderNo  string
	State    string
}

// dispatchDateRe 行日期提取（F 列优先、T 列兜底）。
var dispatchDateRe = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})`)

// dispatchPlateColorRe 车牌颜色后缀（全/半角括号，如「晋JBA773(蓝)」）。
var dispatchPlateColorRe = regexp.MustCompile(`[（(]\s*(蓝|绿|黄|白)\s*[）)]`)

// stripPlateColor 车牌剔除颜色后缀（全/半角括号均剔除）。
func stripPlateColor(plate string) string {
	return strings.TrimSpace(dispatchPlateColorRe.ReplaceAllString(plate, ""))
}

// dispatchRowDate 行日期（F 列预计用车时间优先、T 列创建时间兜底）；均不可解析返回空串。
func dispatchRowDate(cells []string) string {
	for _, idx := range []int{dispatchColPlanTime, dispatchColCreatedAt} {
		if idx >= len(cells) {
			continue
		}
		if m := dispatchDateRe.FindStringSubmatch(cells[idx]); m != nil {
			mo, _ := strconv.Atoi(m[2])
			d, _ := strconv.Atoi(m[3])
			return fmt.Sprintf("%s-%02d-%02d", m[1], mo, d)
		}
	}
	return ""
}

// parseDispatchOrders 解析导出表 → 有效行 + 剔除计数（已取消/无车牌 dropped、非当日 stale）。
// 表头 A/E/J 不符报错（导出表格式与预期不符，不假成功）。
func parseDispatchOrders(data []byte, today string) (rows []dispatchRow, dropped, stale int, err error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("导出表解析失败：%w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, 0, 0, errors.New("导出表为空")
	}
	grid, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, 0, 0, fmt.Errorf("导出表解析失败：%w", err)
	}
	if len(grid) == 0 {
		return nil, 0, 0, errors.New("导出表为空")
	}
	head := map[int]string{}
	for i, c := range grid[0] {
		head[i] = strings.TrimSpace(c)
	}
	if head[dispatchColPlate] != "车牌号码" || head[dispatchColMembers] != "用车人" || head[dispatchColTo] != "目的地" {
		return nil, 0, 0, errors.New("导出表格式与预期不符（A 列应为车牌号码、E 列应为用车人、J 列应为目的地）")
	}
	for i := 1; i < len(grid); i++ {
		cells := make([]string, dispatchColCreatedAt+1)
		for j := range cells {
			if j < len(grid[i]) {
				cells[j] = strings.TrimSpace(grid[i][j])
			}
		}
		allEmpty := true
		for _, c := range cells {
			if c != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			continue
		}
		if cells[dispatchColState] == dispatchStateCancelled {
			dropped++
			continue
		}
		if dispatchRowDate(cells) != today {
			stale++
			continue
		}
		plate := stripPlateColor(cells[dispatchColPlate])
		if plate == "" {
			dropped++
			continue
		}
		rows = append(rows, dispatchRow{
			Plate:    plate,
			Driver:   cells[dispatchColDriver],
			UserText: cells[dispatchColMembers],
			To:       cells[dispatchColTo],
			OrderNo:  cells[dispatchColOrderNo],
			State:    cells[dispatchColState],
		})
	}
	return rows, dropped, stale, nil
}

// ---- 建卡计划（planCards 口径，纯函数）----

// dispatchMember 启用成员字典项。
type dispatchMember struct {
	ID     int64
	Name   string
	Sort   int
	TeamID int64
}

// dispatchVehicle 本班启用车牌。
type dispatchVehicle struct {
	ID    int64
	Plate string
}

// dispatchDest 本班启用目的地。
type dispatchDest struct {
	ID   int64
	Name string
}

// dispatchTeam 开启派车同步的启用班组（worklog_dispatch_sync_team 开关表）。
type dispatchTeam struct {
	ID   int64
	Name string
}

// cardPlan 单张建卡计划。
type cardPlan struct {
	Vehicle dispatchVehicle
	Dest    *dispatchDest
	Members []dispatchMember
	Row     dispatchRow
}

// skippedRow 字典未命中行（不建卡、不消耗成员）。
type skippedRow struct {
	Row    dispatchRow
	Reason string
}

// destSameDispatch 目的地模糊比对（去「中国/省/市/县/区」后互相包含；表内目的地为空不约束）。
func destSameDispatch(cardDest, sheetTo string) bool {
	card := strings.TrimSpace(cardDest)
	sheet := strings.TrimSpace(sheetTo)
	if sheet == "" {
		return true
	}
	if card == "" {
		return false
	}
	if card == sheet || strings.Contains(card, sheet) || strings.Contains(sheet, card) {
		return true
	}
	reg := strings.NewReplacer("中国", "", "省", "", "市", "", "县", "", "区", "")
	nc := reg.Replace(card)
	ns := reg.Replace(sheet)
	if len([]rune(nc)) < 2 || len([]rune(ns)) < 2 {
		return false
	}
	return strings.Contains(nc, ns) || strings.Contains(ns, nc)
}

// planDispatchCards 建卡计划：用车人串「含有」本班成员姓名者匹配；候选按匹配数降序（并列按行序）
// 贪心，同一人同日只挂一张卡（assigned 预置占用）；车牌精确、目的地 destSame 模糊命中，
// 未命中不建卡不消耗成员记 skippedDict。
func planDispatchCards(rows []dispatchRow, members []dispatchMember, vehicles []dispatchVehicle, dests []dispatchDest, assigned map[int64]bool) (plans []cardPlan, skipped []skippedRow) {
	if assigned == nil {
		assigned = map[int64]bool{}
	}
	type cand struct {
		row     dispatchRow
		idx     int
		matched []dispatchMember
	}
	var candidates []cand
	for i, row := range rows {
		var matched []dispatchMember
		for _, m := range members {
			if m.Name != "" && strings.Contains(row.UserText, m.Name) {
				matched = append(matched, m)
			}
		}
		if len(matched) > 0 {
			candidates = append(candidates, cand{row: row, idx: i, matched: matched})
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		if len(candidates[a].matched) != len(candidates[b].matched) {
			return len(candidates[a].matched) > len(candidates[b].matched)
		}
		return candidates[a].idx < candidates[b].idx
	})
	for _, c := range candidates {
		var vehicle *dispatchVehicle
		for i := range vehicles {
			if vehicles[i].Plate == c.row.Plate {
				vehicle = &vehicles[i]
				break
			}
		}
		var dest *dispatchDest
		if c.row.To != "" {
			for i := range dests {
				if destSameDispatch(dests[i].Name, c.row.To) {
					dest = &dests[i]
					break
				}
			}
		}
		if vehicle == nil || (c.row.To != "" && dest == nil) {
			reason := "车牌未入字典"
			if vehicle != nil {
				reason = "目的地未入字典"
			}
			skipped = append(skipped, skippedRow{Row: c.row, Reason: reason})
			continue
		}
		var members []dispatchMember
		for _, m := range c.matched {
			if !assigned[m.ID] {
				members = append(members, m)
				assigned[m.ID] = true
			}
		}
		if len(members) == 0 {
			continue // 本记录匹配的成员均已挂卡
		}
		plans = append(plans, cardPlan{Vehicle: *vehicle, Dest: dest, Members: members, Row: c.row})
	}
	return plans, skipped
}

// dispatchBuckets 行级班组判定结果（importOrdersMulti 口径）。
type dispatchBuckets struct {
	ByTeam    map[int64][]dispatchRow // 开启班组 → 候选行
	Cross     map[int64][]dispatchRow // 含多班组人名的行（按涉及班组分发，仅通知不建卡）
	Outside   int                     // 不含任何班组成员
	OtherTeam int                     // 仅含未开启班组成员
}

// bucketDispatchRows 行级班组判定：用车人串子串含有的成员所属班组集合（同名跨班则多班都算）；
// 多班 → cross，零班 → outside，未开启班组 → otherTeam，其余进 ByTeam。
func bucketDispatchRows(rows []dispatchRow, allMembers []dispatchMember, enabled map[int64]bool) dispatchBuckets {
	out := dispatchBuckets{ByTeam: map[int64][]dispatchRow{}, Cross: map[int64][]dispatchRow{}}
	for _, row := range rows {
		teams := map[int64]bool{}
		for _, m := range allMembers {
			if m.Name != "" && strings.Contains(row.UserText, m.Name) {
				teams[m.TeamID] = true
			}
		}
		switch len(teams) {
		case 0:
			out.Outside++
		case 1:
			for tid := range teams {
				if enabled[tid] {
					out.ByTeam[tid] = append(out.ByTeam[tid], row)
				} else {
					out.OtherTeam++
				}
			}
		default:
			for tid := range teams {
				out.Cross[tid] = append(out.Cross[tid], row)
			}
		}
	}
	return out
}

// ---- 设备取件 / 库操作小接口 ----

// UsbFetcher U 盘取件最小面（*UsbLink 天然满足；测试注入 fake）。
type UsbFetcher interface {
	// MountToHost 挂载（共享）U 盘至被控机端（每日准备用）。
	MountToHost(ctx context.Context) error
	// DownloadFile 下载 U 盘内文件；文件不存在返回 (nil, nil)。
	DownloadFile(ctx context.Context, name string) ([]byte, error)
	// DeleteFiles 删除 U 盘内文件（下载完毕即删，同 Node 口径）。
	DeleteFiles(ctx context.Context, names []string) error
}

// dispatchEntry 本班当日已有出车卡片（已有卡比对用）。
type dispatchEntry struct {
	ID          int64
	Plate       string
	DestName    string
	MemberNames []string
}

// CreateCardRequest 建卡入参（事务：worklog_entry + worklog_entry_member）。
type CreateCardRequest struct {
	TeamID    int64
	LogDate   string
	VehicleID int64
	DestID    *int64 // nil = 未选目的地
	OrderNo   string // 空不写
	CreatedBy int64
	Members   []dispatchMember
}

// DispatchStore 派车单同步库操作（SQLDispatchStore 真实现；测试注入 fake）。
type DispatchStore interface {
	// AllEnabledMembers 全部启用班组的启用成员（跨班判定口径，含未开启班组）。
	AllEnabledMembers(ctx context.Context) ([]dispatchMember, error)
	// TeamVehicles / TeamDestinations 本班启用字典。
	TeamVehicles(ctx context.Context, teamID int64) ([]dispatchVehicle, error)
	TeamDestinations(ctx context.Context, teamID int64) ([]dispatchDest, error)
	// OccupiedMembers 当日全局已挂卡成员（含别班/跨班卡）。
	OccupiedMembers(ctx context.Context, date string) (map[int64]bool, error)
	// EnabledTeams 开启派车同步的全部启用班组（worklog_dispatch_sync_team 开关表；一次导出件
	// 服务全部开启班组，同 Node resolveTeams，取件建卡与结果通知范围均以此为准）。
	EnabledTeams(ctx context.Context) ([]dispatchTeam, error)
	// TeamEntries 本班当日已有出车卡片（含成员名单）。
	TeamEntries(ctx context.Context, teamID int64, date string) ([]dispatchEntry, error)
	// CreatorID 系统建卡人（管理员账号，缺省回退首个超管）。
	CreatorID(ctx context.Context) (int64, error)
	// CreateCard 事务建卡（主行 + 成员行）；返回卡片 id。
	CreateCard(ctx context.Context, req CreateCardRequest) (int64, error)
	// SaveOrderNo 派车单号回填已有卡片（空单号不写）。
	SaveOrderNo(ctx context.Context, entryID int64, orderNo string) error
	// NotifyTargets 结果通知接收人（超管 + 本班启用班组管理员）。
	NotifyTargets(ctx context.Context, teamID int64) ([]int64, error)
}

// SQLDispatchStore DispatchStore 真实现（worklog_* / sys_* 表，dispatch-sync.js SQL 同口径）。
type SQLDispatchStore struct {
	DB *sql.DB
	// CreatorUsername 系统建卡人账号（Node config.admin.username；缺省回退首个超管）。
	CreatorUsername string
}

// AllEnabledMembers 全部启用班组的启用成员。
func (s SQLDispatchStore) AllEnabledMembers(ctx context.Context) ([]dispatchMember, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT m.id, m.name, m.sort, m.team_id
		 FROM worklog_member m JOIN sys_team t ON t.id = m.team_id AND t.status = 1
		 WHERE m.status = 1 ORDER BY m.team_id, m.sort, m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dispatchMember
	for rows.Next() {
		var m dispatchMember
		if rows.Scan(&m.ID, &m.Name, &m.Sort, &m.TeamID) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// TeamVehicles 本班启用车牌。
func (s SQLDispatchStore) TeamVehicles(ctx context.Context, teamID int64) ([]dispatchVehicle, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dispatchVehicle
	for rows.Next() {
		var v dispatchVehicle
		if rows.Scan(&v.ID, &v.Plate) == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// TeamDestinations 本班启用目的地。
func (s SQLDispatchStore) TeamDestinations(ctx context.Context, teamID int64) ([]dispatchDest, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name FROM worklog_destination WHERE status = 1 AND team_id = ?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dispatchDest
	for rows.Next() {
		var d dispatchDest
		if rows.Scan(&d.ID, &d.Name) == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// OccupiedMembers 当日全局已挂卡成员。
func (s SQLDispatchStore) OccupiedMembers(ctx context.Context, date string) (map[int64]bool, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT em.member_id FROM worklog_entry_member em JOIN worklog_entry e ON e.id = em.entry_id
		 WHERE e.log_date = ?`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out, nil
}

// TeamEntries 本班当日已有出车卡片（含成员名单）。
func (s SQLDispatchStore) TeamEntries(ctx context.Context, teamID int64, date string) ([]dispatchEntry, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT e.id, COALESCE(v.plate_no, ''), COALESCE(d.name, '')
		 FROM worklog_entry e
		 LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE e.team_id = ? AND e.log_date = ?`, teamID, date)
	if err != nil {
		return nil, err
	}
	var out []dispatchEntry
	byID := map[int64]int{}
	for rows.Next() {
		var e dispatchEntry
		if rows.Scan(&e.ID, &e.Plate, &e.DestName) == nil {
			byID[e.ID] = len(out)
			out = append(out, e)
		}
	}
	rows.Close()
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]any, 0, len(out))
	ph := make([]string, 0, len(out))
	for _, e := range out {
		ids = append(ids, e.ID)
		ph = append(ph, "?")
	}
	mrows, err := s.DB.QueryContext(ctx,
		`SELECT em.entry_id, m.name FROM worklog_entry_member em
		 JOIN worklog_member m ON m.id = em.member_id
		 WHERE em.entry_id IN (`+strings.Join(ph, ",")+`)
		 ORDER BY em.entry_id, em.sort, em.id`, ids...)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var entryID int64
		var name string
		if mrows.Scan(&entryID, &name) == nil {
			if i, ok := byID[entryID]; ok {
				out[i].MemberNames = append(out[i].MemberNames, name)
			}
		}
	}
	return out, nil
}

// CreatorID 系统建卡人（同 Node：username 命中优先，缺省首个超管）。
func (s SQLDispatchStore) CreatorID(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT id FROM sys_user WHERE username = ? OR role = 'admin'
		 ORDER BY (username = ?) DESC, id LIMIT 1`,
		s.CreatorUsername, s.CreatorUsername).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// CreateCard 事务建卡（同 POST /logs 口径，避免半截卡片）。
func (s SQLDispatchStore) CreateCard(ctx context.Context, req CreateCardRequest) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	orderNo := any(nil)
	if strings.TrimSpace(req.OrderNo) != "" {
		orderNo = req.OrderNo
	}
	var destID any
	if req.DestID != nil {
		destID = *req.DestID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, dispatch_order_no, created_by)
		 VALUES (?, ?, '', ?, ?, ?, ?)`,
		req.TeamID, req.LogDate, req.VehicleID, destID, orderNo, req.CreatedBy)
	if err != nil {
		return 0, err
	}
	entryID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, m := range req.Members {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO worklog_entry_member (entry_id, member_id, sort) VALUES (?, ?, ?)`,
			entryID, m.ID, m.Sort); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return entryID, nil
}

// SaveOrderNo 派车单号回填（幂等同值覆盖）。
func (s SQLDispatchStore) SaveOrderNo(ctx context.Context, entryID int64, orderNo string) error {
	if strings.TrimSpace(orderNo) == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx,
		`UPDATE worklog_entry SET dispatch_order_no = ? WHERE id = ?`, orderNo, entryID)
	return err
}

// NotifyTargets 超管 + 本班启用班组管理员。
func (s SQLDispatchStore) NotifyTargets(ctx context.Context, teamID int64) ([]int64, error) {
	var out []int64
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM sys_user WHERE (role = 'admin' OR (role = 'team_admin' AND team_id = ?)) AND status = 1`,
		teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// EnabledTeams 开启派车同步的全部启用班组（dispatch-sync.js resolveTeams 同 SQL）。
func (s SQLDispatchStore) EnabledTeams(ctx context.Context) ([]dispatchTeam, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.id, t.name FROM worklog_dispatch_sync_team s
		 JOIN sys_team t ON t.id = s.team_id AND t.status = 1 ORDER BY t.sort, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dispatchTeam
	for rows.Next() {
		var t dispatchTeam
		if rows.Scan(&t.ID, &t.Name) == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// ---- DispatchSyncer ----

// DispatchSyncer worklog.DispatchSyncer 实现（「取件建卡」手动触发口径；范围 = 开关表
// worklog_dispatch_sync_team 全部开启班组，同 Node runFetchNow → fetchJob）。
type DispatchSyncer struct {
	// Usb U 盘取件（*UsbLink 满足；nil 报链路未接入）。
	Usb UsbFetcher
	// Store 库操作（SQLDispatchStore 真实现）。
	Store DispatchStore
	// Notice 结果通知（站内）；nil 跳过通知不影响建卡。
	Notice NoticePusher
	// Wx 结果通知微信外发（可 nil）。
	Wx WxPusher
	// Locker 设备维护锁（可 nil）：取件结束无论成败解禁（Node fetchJob finally 口径）。
	Locker kvm.Locker
	// UnlockDdns 解禁设备 ddns（与 UsbLink 解析同一配置值；空则跳过解禁）。
	UnlockDdns string
	// Lock 每日准备锁定设备（可 nil：不锁直接挂载；Node prepareJob devlock.lock 同口径）。
	Lock func(ctx context.Context) error
	// Unlock 取件/准备结束解禁钩子（可 nil；优先于 Locker/UnlockDdns 组合，支持热配置解析）。
	Unlock func(ctx context.Context) error
	// Now 时间源（测试可注入）；nil = time.Now（北京时间自然日）。
	Now func() time.Time

	// mu/running 取件重入守卫（定时与手动触发共用；Node fetchJob fetching 口径）。
	mu      sync.Mutex
	running bool
}

// dispatchFileName 当日派车单文件名。
func dispatchFileName(today string) string { return today + "-派车单.xlsx" }

// nowCn 当前北京时间（缺省 time.Now）。
func (s *DispatchSyncer) nowCn() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// begin/end 取件重入守卫（占用中 begin 返回 false；Node fetchJob fetching 口径）。
func (s *DispatchSyncer) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *DispatchSyncer) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

// dispatchLockReason 每日准备锁定设备原因（Node dispatch-sync.js LOCK_REASON）。
const dispatchLockReason = "每日派车单同步（预计 09:45 恢复）"

// dispatchLockTTLSec 维护锁 TTL 兜底自动解禁（Node devlock.lock 缺省 7200s）。
const dispatchLockTTLSec = 7200

// unlockDevice 取件/准备结束解禁（无论成败；Unlock 钩子优先，回退 Locker+UnlockDdns）。
func (s *DispatchSyncer) unlockDevice(ctx context.Context) {
	if s.Unlock != nil {
		_ = s.Unlock(ctx)
		return
	}
	if s.Locker != nil && s.UnlockDdns != "" {
		_ = s.Locker.Unlock(ctx, s.UnlockDdns)
	}
}

// PrepareNow 每日准备（Node prepareJob）：锁定设备并挂载（共享）U 盘至被控机。
// 无开启班组跳过（不锁不挂）；失败无论此前是否锁定都解禁、多班组合并通知
// 「派车单同步准备失败」，并返回可读 error 供排程侧记日志。
func (s *DispatchSyncer) PrepareNow(ctx context.Context) error {
	if s == nil || s.Usb == nil || s.Store == nil {
		return errors.New("派车单同步服务未接入")
	}
	var teams []dispatchTeam
	var err error
	fail := func(err error) error {
		s.unlockDevice(ctx)
		s.notifyTeams(ctx, teams, "派车单同步准备失败", []string{
			"锁定设备 / 挂载 U 盘失败：" + err.Error(),
			"设备已解禁，取件任务仍按排程执行。",
		})
		return fmt.Errorf("锁定设备 / 挂载 U 盘失败：%w", err)
	}
	teams, err = s.Store.EnabledTeams(ctx)
	if err != nil {
		return fail(err) // Node prepareJob catch：班组解析失败同样解禁并通知（仅超管）
	}
	if len(teams) == 0 {
		return nil // 无开启班组，准备任务跳过（Node prepareJob 口径）
	}
	if s.Lock != nil {
		if err := s.Lock(ctx); err != nil {
			return fail(err)
		}
	}
	if err := s.Usb.MountToHost(ctx); err != nil {
		return fail(err)
	}
	return nil
}

// SyncNow 立即同步一次派车单（取件建卡）；返回新建出车卡片张数（全部开启班组合计）。
// teamID 为请求上下文班组，仅作接口兼容保留、不圈定同步范围：一次导出件覆盖全部开启班组，
// 只处理单班会吞掉他班行（文件下载即删），范围以 worklog_dispatch_sync_team 开关表为准
// （Node runFetchNow → fetchJob resolveTeams 口径）；无开启班组时不取件不删件直接报可读错误。
// 当日无文件按旧版口径发「今日无文件」通知后返回 (0, nil)（正常结果，sync-now 不报 500）；
// 取件/解析/建卡失败发「派车单同步失败」通知并返回可读 error（Node fetchJob catch 口径）；
// 结果通知失败不影响返回值。
func (s *DispatchSyncer) SyncNow(ctx context.Context, teamID int64) (int, error) {
	if s == nil || s.Usb == nil || s.Store == nil {
		return 0, errors.New("派车单同步服务未接入")
	}
	if !s.begin() {
		return 0, errors.New("已有取件任务在执行中，请稍后再试")
	}
	defer s.end()
	defer s.unlockDevice(ctx)
	teams, err := s.Store.EnabledTeams(ctx)
	if err != nil {
		return 0, s.fetchFail(ctx, nil, err)
	}
	if len(teams) == 0 {
		return 0, errors.New("当前没有开启派车同步的班组（在「派车对齐」页按班组开启）")
	}
	_ = teamID // 范围以开关表全部开启班组为准（见上）
	today := s.nowCn().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	fileName := dispatchFileName(today)
	buf, err := s.Usb.DownloadFile(ctx, fileName)
	if err != nil {
		return 0, s.fetchFail(ctx, teams, err)
	}
	if buf == nil {
		// 当日无文件（Node fetchJob 口径）：正常通知后返回，不是失败（sync-now 不报 500）。
		s.notifyTeams(ctx, teams, "派车单同步：今日无文件", []string{
			fmt.Sprintf("未在设备 U 盘找到「%s」。", fileName),
			"可能当日无派车单，或被控机导出客户端（内网工具箱）异常。",
		})
		return 0, nil
	}
	// 口径：下载完毕即删除 U 盘中派车单文件（删除失败不阻断建卡）。
	_ = s.Usb.DeleteFiles(ctx, []string{fileName})

	n, err := s.importOrders(ctx, teams, buf, today)
	if err != nil {
		return n, s.fetchFail(ctx, teams, err)
	}
	return n, nil
}

// fetchFail 取件失败通知（Node fetchJob catch 口径：「取件失败：…」+ 解禁补跑指引），
// 并返回带「取件失败：」前缀的可读 error。
func (s *DispatchSyncer) fetchFail(ctx context.Context, teams []dispatchTeam, err error) error {
	wrapped := fmt.Errorf("取件失败：%w", err)
	s.notifyTeams(ctx, teams, "派车单同步失败", []string{
		wrapped.Error(),
		"设备已解禁，可在出工日志「派车对齐」人工导入导出件或手动补跑。",
	})
	return wrapped
}

// importOrders 解析 + 行级班组判定 + 逐班组建卡/已有卡比对/结果通知（importOrdersMulti 口径）；
// 返回全部开启班组新建卡片合计。
func (s *DispatchSyncer) importOrders(ctx context.Context, teams []dispatchTeam, buf []byte, today string) (int, error) {
	rows, dropped, stale, err := parseDispatchOrders(buf, today)
	if err != nil {
		return 0, err
	}
	allMembers, err := s.Store.AllEnabledMembers(ctx)
	if err != nil {
		return 0, err
	}
	enabled := map[int64]bool{}
	for _, t := range teams {
		enabled[t.ID] = true
	}
	buckets := bucketDispatchRows(rows, allMembers, enabled)

	occupied, err := s.Store.OccupiedMembers(ctx, today)
	if err != nil {
		return 0, err
	}
	createdBy, err := s.Store.CreatorID(ctx)
	if err != nil {
		return 0, fmt.Errorf("未找到管理员账号作为建卡人：%w", err)
	}
	total := 0
	for _, team := range teams {
		n, err := s.buildTeamCards(ctx, team, buckets, allMembers, occupied, createdBy,
			today, len(rows), dropped, stale)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// buildTeamCards 单班组建卡 + 已有卡比对 + 结果通知（importOrdersMulti 单班组段，逐班组分发）；
// 返回本班新建卡片数。
func (s *DispatchSyncer) buildTeamCards(ctx context.Context, team dispatchTeam, buckets dispatchBuckets,
	allMembers []dispatchMember, occupied map[int64]bool, createdBy int64,
	today string, total, dropped, stale int) (int, error) {
	teamRows := buckets.ByTeam[team.ID]
	var members []dispatchMember
	for _, m := range allMembers {
		if m.TeamID == team.ID {
			members = append(members, m)
		}
	}
	vehicles, err := s.Store.TeamVehicles(ctx, team.ID)
	if err != nil {
		return 0, err
	}
	dests, err := s.Store.TeamDestinations(ctx, team.ID)
	if err != nil {
		return 0, err
	}
	entries, err := s.Store.TeamEntries(ctx, team.ID, today)
	if err != nil {
		return 0, err
	}
	entryByMember := map[int64]*dispatchEntry{}
	for i := range entries {
		for _, name := range entries[i].MemberNames {
			for _, m := range members {
				if m.Name == name {
					entryByMember[m.ID] = &entries[i]
				}
			}
		}
	}

	// 行分桶：用车人当日已有卡片（本班或别班/跨班）→ 比对桶（仅通知不改库）；无占用 → 建卡候选。
	var freshRows, overlapRows []dispatchRow
	for _, row := range teamRows {
		var matched []dispatchMember
		for _, m := range members {
			if m.Name != "" && strings.Contains(row.UserText, m.Name) {
				matched = append(matched, m)
			}
		}
		localCards := map[int64]*dispatchEntry{}
		var foreign []string
		for _, m := range matched {
			if e := entryByMember[m.ID]; e != nil {
				localCards[e.ID] = e
			} else if occupied[m.ID] {
				foreign = append(foreign, m.Name)
			}
		}
		if len(localCards) > 0 || len(foreign) > 0 {
			overlapRows = append(overlapRows, row)
		} else {
			freshRows = append(freshRows, row)
		}
	}

	plans, skipped := planDispatchCards(freshRows, members, vehicles, dests, nil)
	created := make([]cardPlan, 0, len(plans))
	for _, p := range plans {
		req := CreateCardRequest{
			TeamID: team.ID, LogDate: today, VehicleID: p.Vehicle.ID,
			DestID: nil, OrderNo: p.Row.OrderNo, CreatedBy: createdBy, Members: p.Members,
		}
		if p.Dest != nil {
			id := p.Dest.ID
			req.DestID = &id
		}
		if _, err := s.Store.CreateCard(ctx, req); err != nil {
			return len(created), err
		}
		created = append(created, p)
	}

	// 已有卡比对（派车单号回填 + 差异指引；全项一致仅计数）。
	_, mismatches := s.compareOverlaps(ctx, overlapRows, members, vehicles, occupied, entryByMember)

	lines := buildDispatchLines(today, total, created, skipped, buckets, dropped, stale, len(overlapRows), mismatches, buckets.Cross[team.ID])
	s.notify(ctx, team.ID, fmt.Sprintf("派车单同步完成（%s）", team.Name), lines)
	return len(created), nil
}

// overlapMismatch 已有卡比对差异（通知文本行）。
type overlapMismatch struct {
	Row    dispatchRow
	Issues []string
}

// compareOverlaps 已有卡比对：单卡回填派车单号；车牌精确、目的地 destSame、用车人集合三件套
// 比对产出修改指引（dispatch-sync.js 同文案）。
func (s *DispatchSyncer) compareOverlaps(ctx context.Context, overlaps []dispatchRow,
	members []dispatchMember, vehicles []dispatchVehicle, occupied map[int64]bool,
	entryByMember map[int64]*dispatchEntry) (aligned int, mismatches []overlapMismatch) {
	for _, row := range overlaps {
		var matched []dispatchMember
		for _, m := range members {
			if m.Name != "" && strings.Contains(row.UserText, m.Name) {
				matched = append(matched, m)
			}
		}
		cardMap := map[int64]*dispatchEntry{}
		var foreign []string
		for _, m := range matched {
			if e := entryByMember[m.ID]; e != nil {
				cardMap[e.ID] = e
			} else if occupied[m.ID] {
				foreign = append(foreign, m.Name)
			}
		}
		var cards []*dispatchEntry
		for _, e := range cardMap {
			cards = append(cards, e)
		}
		sort.Slice(cards, func(a, b int) bool { return cards[a].ID < cards[b].ID })
		// 派车单号回填已有卡片（多张卡时不确定归属不写）。
		if len(cards) == 1 && strings.TrimSpace(row.OrderNo) != "" {
			_ = s.Store.SaveOrderNo(ctx, cards[0].ID, row.OrderNo)
		}
		var issues []string
		for _, card := range cards {
			label := ""
			if len(cards) > 1 {
				label = fmt.Sprintf("卡片「%s」（%s）：", card.Plate, strings.Join(card.MemberNames, "、"))
			}
			if card.Plate != row.Plate {
				inDict := false
				for _, v := range vehicles {
					if v.Plate == row.Plate {
						inDict = true
						break
					}
				}
				hint := ""
				if !inDict {
					hint = "（下拉无此车牌，请先在数据管理添加）"
				}
				issues = append(issues, fmt.Sprintf("%s车牌不一致：卡片「%s」≠ 派车单「%s」→ 请在卡片「修改派车」改为 %s%s",
					label, card.Plate, row.Plate, row.Plate, hint))
			}
			if row.To != "" {
				if card.DestName == "" {
					issues = append(issues, fmt.Sprintf("%s目的地未选（派车单「%s」）→ 请补选目的地（无匹配项请先在数据管理添加）", label, row.To))
				} else if !destSameDispatch(card.DestName, row.To) {
					issues = append(issues, fmt.Sprintf("%s目的地不一致：卡片「%s」≠ 派车单「%s」→ 请核对修改", label, card.DestName, row.To))
				}
			}
			cardNames := map[string]bool{}
			for _, n := range card.MemberNames {
				cardNames[n] = true
			}
			var missing, extra []string
			for _, m := range matched {
				if !cardNames[m.Name] {
					missing = append(missing, m.Name)
				}
			}
			for _, n := range card.MemberNames {
				if !strings.Contains(row.UserText, n) {
					extra = append(extra, n)
				}
			}
			if len(missing) > 0 {
				issues = append(issues, fmt.Sprintf("%s用车人缺少：%s → 请在卡片点亮补入（其当日若另有卡片请先调整，同一人同日只能挂一张卡）",
					label, strings.Join(missing, "、")))
			}
			if len(extra) > 0 {
				issues = append(issues, fmt.Sprintf("%s用车人多出：%s（派车单无）→ 请确认是否取消点亮", label, strings.Join(extra, "、")))
			}
		}
		if len(cards) > 1 {
			issues = append(issues, fmt.Sprintf("用车人分散在 %d 张卡片 → 请合并为一张（保留与派车单最一致者，其余删除）", len(cards)))
		}
		if len(foreign) > 0 {
			issues = append(issues, fmt.Sprintf("用车人 %s 当日已在其他班组卡片（含跨班日志）→ 本班不再为其建卡，如需调整请联系超级管理员",
				strings.Join(foreign, "、")))
		}
		if len(issues) > 0 {
			mismatches = append(mismatches, overlapMismatch{Row: row, Issues: issues})
		} else {
			aligned++
		}
	}
	return aligned, mismatches
}

// buildDispatchLines 结果通知正文（dispatch-sync.js fetchJob 同构）。
func buildDispatchLines(today string, total int, created []cardPlan, skipped []skippedRow, buckets dispatchBuckets,
	dropped, stale, overlapCount int, mismatches []overlapMismatch, cross []dispatchRow) []string {
	outside := buckets.Outside
	otherTeam := buckets.OtherTeam
	head := fmt.Sprintf("%s 解析 %d 行，新建 %d 张出车卡片（剔除：非当日 %d / 已取消或无车牌 %d / 非班组用车 %d / 仅属未开启班组 %d）",
		today, total, len(created), stale, dropped, outside, otherTeam)
	if overlapCount > 0 {
		head += fmt.Sprintf("；%d 条用车人当日已有手动卡（不一致 %d 条见下文）", overlapCount, len(mismatches))
	}
	head += "。"
	lines := []string{head}
	for _, c := range created {
		line := "· " + c.Vehicle.Plate
		if c.Dest != nil {
			line += " → " + c.Dest.Name
		}
		var names []string
		for _, m := range c.Members {
			names = append(names, m.Name)
		}
		lines = append(lines, line+"："+strings.Join(names, "、"))
	}
	if len(mismatches) > 0 {
		lines = append(lines, fmt.Sprintf("以下 %d 条已有手动卡未改动，请核对：", len(mismatches)))
		for _, mm := range mismatches {
			line := "· " + mm.Row.Plate
			if mm.Row.To != "" {
				line += " → " + mm.Row.To
			}
			line += "（派车单用车人：" + mm.Row.UserText + "）"
			lines = append(lines, line)
			for _, it := range mm.Issues {
				lines = append(lines, "  "+it)
			}
		}
	}
	if len(skipped) > 0 {
		lines = append(lines, fmt.Sprintf("以下 %d 条字典未命中未建卡，请先将车牌/目的地入字典再人工建卡：", len(skipped)))
		for _, s := range skipped {
			line := "· " + s.Row.Plate
			if s.Row.To != "" {
				line += " → " + s.Row.To
			}
			lines = append(lines, fmt.Sprintf("%s（用车人：%s）：%s", line, s.Row.UserText, s.Reason))
		}
	}
	if len(cross) > 0 {
		lines = append(lines, fmt.Sprintf("以下 %d 条含跨班用车人，仅通知不建卡（需建卡请联系超管建跨班日志）：", len(cross)))
		for _, s := range cross {
			line := "· " + s.Plate
			if s.To != "" {
				line += " → " + s.To
			}
			lines = append(lines, fmt.Sprintf("%s（用车人：%s）", line, s.UserText))
		}
	}
	return lines
}

// notify 结果通知：站内（超管 + 本班班组管理员）+ 微信外发到班组群；失败仅忽略（Node 同口径）。
func (s *DispatchSyncer) notify(ctx context.Context, teamID int64, title string, lines []string) {
	s.notifyTeams(ctx, []dispatchTeam{{ID: teamID}}, title, lines)
}

// notifyTeams 结果通知（Node notify 同口径：站内收件人合并去重一条、微信外发各班组群；
// 无收件人整体不发，失败仅忽略）。
func (s *DispatchSyncer) notifyTeams(ctx context.Context, teams []dispatchTeam, title string, lines []string) {
	if s.Notice == nil {
		return
	}
	targets := teams
	if len(targets) == 0 {
		// Node notify 口径：teamIds 空时仅超管（如班组解析失败场景）；不发班组群微信。
		targets = []dispatchTeam{{ID: 0}}
	}
	var users []int64
	var teamIDs []int64
	seen := map[int64]bool{}
	for _, t := range targets {
		us, err := s.Store.NotifyTargets(ctx, t.ID)
		if err != nil {
			continue
		}
		for _, id := range us {
			if id > 0 && !seen[id] {
				seen[id] = true
				users = append(users, id)
			}
		}
		if t.ID > 0 {
			teamIDs = append(teamIDs, t.ID)
		}
	}
	if len(users) == 0 {
		return
	}
	content := strings.Join(lines, "\n")
	if err := s.Notice.PushNotice(ctx, nil, users, title, content); err != nil {
		return
	}
	// 成功、今日无文件等日常结果仅站内；失败才发微信群。
	if s.Wx != nil && len(teamIDs) > 0 && strings.Contains(title, "失败") {
		_, _ = s.Wx.Send(ctx, nil, teamIDs, title+"\n"+content, nil)
	}
}

// NewDispatchSyncer 构造派车单「取件建卡」执行器（worklog.DispatchSyncer）：
// 设备链路 NewUsbLink（WORKLOG_DISPATCH_DEVICE_DDNS/MAC/KVM_USER 键组）+ SQLDispatchStore
// （系统建卡人 adminUsername，缺省首个超管）+ 站内/微信结果通知 + 每日准备锁定设备
// （Node prepareJob devlock.lock）与取件结束解禁（Node fetchJob finally 口径）。
func NewDispatchSyncer(db *sql.DB, platform kvm.Platform, cfg ConfigGet, adminUsername string, locker kvm.Locker) *DispatchSyncer {
	s := &DispatchSyncer{
		Usb:    NewUsbLink(platform, cfg, UsbKeys{DdnsKey: "WORKLOG_DISPATCH_DEVICE_DDNS", MacKey: "WORKLOG_DISPATCH_DEVICE_MAC", UserKey: "WORKLOG_DISPATCH_KVM_USER"}, adminUsername),
		Store:  SQLDispatchStore{DB: db, CreatorUsername: adminUsername},
		Notice: SQLNoticeWriter{DB: db},
	}
	if cfg != nil {
		s.Wx = NewPadProWxPusher(NewPadPro(cfg), SQLWxidResolver{DB: db})
	}
	if locker != nil {
		s.Lock = func(ctx context.Context) error {
			ddns := tokenOf(ctx, cfg, "WORKLOG_DISPATCH_DEVICE_DDNS")
			if ddns == "" {
				return errors.New("未配置派车同步设备（WORKLOG_DISPATCH_DEVICE_DDNS）")
			}
			return locker.Lock(ctx, ddns, dispatchLockReason, dispatchLockTTLSec)
		}
		s.Unlock = func(ctx context.Context) error {
			ddns := tokenOf(ctx, cfg, "WORKLOG_DISPATCH_DEVICE_DDNS")
			if ddns == "" {
				return nil
			}
			return locker.Unlock(ctx, ddns)
		}
	}
	return s
}

// 编译期契约：DispatchSyncer 满足 worklog.DispatchSyncer（main 经 bridge.Wire 注入）。
var _ worklog.DispatchSyncer = (*DispatchSyncer)(nil)
