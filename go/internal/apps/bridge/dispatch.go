// worklog.DispatchSyncer 真实适配：派车单「取件建卡」（迁移 server/src/worklog/dispatch-sync.js
// fetchJob / importOrdersMulti 的单班组口径）：U 盘取「yyyy-MM-dd-派车单.xlsx」→ 解析
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
	// TeamName 班组名（通知标题用）。
	TeamName(ctx context.Context, teamID int64) (string, error)
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

// TeamName 班组名。
func (s SQLDispatchStore) TeamName(ctx context.Context, teamID int64) (string, error) {
	var name string
	err := s.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&name)
	return name, err
}

// ---- DispatchSyncer ----

// DispatchSyncer worklog.DispatchSyncer 实现（单班组「取件建卡」，手动触发口径）。
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
	// Unlock 取件结束解禁钩子（可 nil；优先于 Locker/UnlockDdns 组合，支持热配置解析）。
	Unlock func(ctx context.Context) error
	// Now 时间源（测试可注入）；nil = time.Now（北京时间自然日）。
	Now func() time.Time
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

// SyncNow 立即同步一次派车单（取件建卡）；返回新建出车卡片张数。
// 当日无文件 / 解析失败 / 链路未接入均返回可读 error（不假成功）；结果通知失败不影响返回值。
func (s *DispatchSyncer) SyncNow(ctx context.Context, teamID int64) (int, error) {
	if s == nil || s.Usb == nil || s.Store == nil {
		return 0, errors.New("派车单同步服务未接入")
	}
	if s.Unlock != nil {
		defer func() { _ = s.Unlock(ctx) }()
	} else if s.Locker != nil && s.UnlockDdns != "" {
		defer func() { _ = s.Locker.Unlock(ctx, s.UnlockDdns) }()
	}
	today := s.nowCn().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	fileName := dispatchFileName(today)
	buf, err := s.Usb.DownloadFile(ctx, fileName)
	if err != nil {
		return 0, fmt.Errorf("取件失败：%w", err)
	}
	if buf == nil {
		return 0, fmt.Errorf("未在设备 U 盘找到「%s」（可能当日无派车单，或被控机导出客户端异常）", fileName)
	}
	// 口径：下载完毕即删除 U 盘中派车单文件（删除失败不阻断建卡）。
	_ = s.Usb.DeleteFiles(ctx, []string{fileName})

	created, err := s.buildCards(ctx, teamID, buf, today)
	if err != nil {
		return 0, err
	}
	return len(created), nil
}

// buildCards 解析 + 行级班组判定 + 建卡/已有卡比对 + 结果通知（importOrdersMulti 单班组口径）。
func (s *DispatchSyncer) buildCards(ctx context.Context, teamID int64, buf []byte, today string) ([]cardPlan, error) {
	rows, dropped, stale, err := parseDispatchOrders(buf, today)
	if err != nil {
		return nil, err
	}
	allMembers, err := s.Store.AllEnabledMembers(ctx)
	if err != nil {
		return nil, err
	}
	buckets := bucketDispatchRows(rows, allMembers, map[int64]bool{teamID: true})
	teamRows := buckets.ByTeam[teamID]

	occupied, err := s.Store.OccupiedMembers(ctx, today)
	if err != nil {
		return nil, err
	}
	createdBy, err := s.Store.CreatorID(ctx)
	if err != nil {
		return nil, fmt.Errorf("未找到管理员账号作为建卡人：%w", err)
	}
	var members []dispatchMember
	for _, m := range allMembers {
		if m.TeamID == teamID {
			members = append(members, m)
		}
	}
	vehicles, err := s.Store.TeamVehicles(ctx, teamID)
	if err != nil {
		return nil, err
	}
	dests, err := s.Store.TeamDestinations(ctx, teamID)
	if err != nil {
		return nil, err
	}
	entries, err := s.Store.TeamEntries(ctx, teamID, today)
	if err != nil {
		return nil, err
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
			TeamID: teamID, LogDate: today, VehicleID: p.Vehicle.ID,
			DestID: nil, OrderNo: p.Row.OrderNo, CreatedBy: createdBy, Members: p.Members,
		}
		if p.Dest != nil {
			id := p.Dest.ID
			req.DestID = &id
		}
		if _, err := s.Store.CreateCard(ctx, req); err != nil {
			return created, err
		}
		created = append(created, p)
	}

	// 已有卡比对（派车单号回填 + 差异指引；全项一致仅计数）。
	_, mismatches := s.compareOverlaps(ctx, overlapRows, members, vehicles, occupied, entryByMember)

	teamName, _ := s.Store.TeamName(ctx, teamID)
	lines := buildDispatchLines(today, len(rows), created, skipped, buckets, dropped, stale, len(overlapRows), mismatches, buckets.Cross[teamID])
	s.notify(ctx, teamID, fmt.Sprintf("派车单同步完成（%s）", teamName), lines)
	return created, nil
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
	if s.Notice == nil {
		return
	}
	users, err := s.Store.NotifyTargets(ctx, teamID)
	if err != nil || len(users) == 0 {
		return
	}
	content := strings.Join(lines, "\n")
	if err := s.Notice.PushNotice(ctx, nil, users, title, content); err != nil {
		return
	}
	if s.Wx != nil {
		_, _ = s.Wx.Send(ctx, nil, []int64{teamID}, title+"\n"+content, nil)
	}
}

// NewDispatchSyncer 构造派车单「取件建卡」执行器（worklog.DispatchSyncer）：
// 设备链路 NewUsbLink（WORKLOG_DISPATCH_DEVICE_DDNS/MAC/KVM_USER 键组）+ SQLDispatchStore
// （系统建卡人 adminUsername，缺省首个超管）+ 站内/微信结果通知 + 取件结束设备解禁（Node finally 口径）。
func NewDispatchSyncer(db *sql.DB, platform kvm.Platform, cfg ConfigGet, adminUsername string, locker kvm.Locker) *DispatchSyncer {
	s := &DispatchSyncer{
		Usb:    NewUsbLink(platform, cfg, UsbKeys{DdnsKey: "WORKLOG_DISPATCH_DEVICE_DDNS", MacKey: "WORKLOG_DISPATCH_DEVICE_MAC", UserKey: "WORKLOG_DISPATCH_KVM_USER"}, adminUsername),
		Store:  SQLDispatchStore{DB: db, CreatorUsername: adminUsername},
		Notice: SQLNoticeWriter{DB: db},
	}
	if cfg != nil {
		s.Wx = NewDifyWxPusher(cfg, SQLWxidResolver{DB: db})
	}
	if locker != nil {
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
