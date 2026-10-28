// 同步/过期核心：登录态探测（probeAuth）、打卡对账刷新（refreshClockins）、
// 同步任务进度（SyncJob）、核查流水（worklog_sync_log）与 day/clockin/fee/sync 路由。
package sgccclockin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/httpapi"
)

// TodayCN 今日（UTC+8 固定偏移）'YYYY-MM-DD'。
func TodayCN() string {
	return time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02")
}

// FmtCnDateTime 商旅 clockInTime 口径混杂（毫秒/秒时间戳、'YYYY-MM-DD HH:mm:ss'、ISO），
// 统一转北京时间 DATETIME 字符串；空/不可解析返回空串。
func FmtCnDateTime(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(jsString(v))
	if s == "" || s == "<nil>" {
		return ""
	}
	cst := time.FixedZone("CST", 8*3600)
	var t time.Time
	var err error
	switch {
	case len(s) == 13 && isDigits(s):
		ms, _ := strconv.ParseInt(s, 10, 64)
		t = time.UnixMilli(ms).In(cst)
	case len(s) == 10 && isDigits(s):
		sec, _ := strconv.ParseInt(s, 10, 64)
		t = time.Unix(sec, 0).In(cst)
	default:
		layouts := []string{"2006-01-02 15:04:05", time.RFC3339, "2006/01/02 15:04:05"}
		for _, layout := range layouts {
			if t, err = time.ParseInLocation(layout, s, cst); err == nil {
				break
			}
		}
		if err != nil {
			return ""
		}
	}
	return t.In(cst).Format("2006-01-02 15:04:05")
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// probeAuth 登录态探测：dayNew statusCode=200 为有效；应答不可解/网络异常保持原值不翻转；
// 1→0 跳变落库并通知（Notifier）。返回有效与否。
func (m *Module) probeAuth(ctx context.Context, acc *Account) bool {
	valid := acc.TokenStatus == 1
	if m.API != nil {
		d, err := m.API.DayNew(ctx, acc.Token, TodayCN(), CallOpts{
			Token: acc.Token, Mobile: acc.Mobile,
			DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
		})
		switch {
		case err != nil:
			// 网络异常：token_status 保持原值
			return acc.TokenStatus == 1
		case d == nil:
			// 平台应答不可解：同网络异常口径
			return acc.TokenStatus == 1
		default:
			valid = num(d["statusCode"]) == 200
		}
	}
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_sgcc_account SET token_status = ?, last_check_at = NOW() WHERE id = ?`,
		boolInt(valid), acc.ID)
	if !valid && acc.TokenStatus == 1 {
		acc.TokenStatus = 0
		if m.Notifier != nil {
			_ = m.Notifier.NotifyTokenExpired(ctx, *acc)
		}
	}
	return valid
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ClockinRecordLocal 本地打卡行。
type ClockinRecordLocal struct {
	MemberID  int64
	ClockDate string
	Seq       int
	DetailID  string
	ClockTime string
	Position  string
	Lng       string
	Lat       string
	CityCode  string
	CityName  string
	WorkHours string
	Source    int
}

// RefreshClockins 打卡成功后用 dayNew 全量刷新该日打卡流水（工时一并回写）。
// seq 分配规则：按 detailId 匹配本地已有记录保留原 seq；新记录按时间排序分配到空余 seq 位。
func (m *Module) RefreshClockins(ctx context.Context, teamID, memberID int64, date string, remote []ClockinRecordLocal) error {
	// 本地已有 seq 按 detailId 保留
	existing := map[string]int{}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT seq, detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ?`, memberID, date)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int
		var detailID string
		if err := rows.Scan(&seq, &detailID); err == nil && detailID != "" {
			existing[detailID] = seq
		}
	}
	rows.Close()
	used := map[int]bool{}
	for _, seq := range existing {
		used[seq] = true
	}
	// 新记录按时间排序分配空余 seq
	news := make([]ClockinRecordLocal, 0, len(remote))
	for _, rec := range remote {
		if _, ok := existing[rec.DetailID]; !ok {
			news = append(news, rec)
		}
	}
	sort.SliceStable(news, func(i, j int) bool { return news[i].ClockTime < news[j].ClockTime })
	for _, rec := range news {
		for seq := 1; seq <= 2; seq++ {
			if !used[seq] {
				existing[rec.DetailID] = seq
				used[seq] = true
				break
			}
		}
	}
	for _, rec := range remote {
		seq := existing[rec.DetailID]
		if seq == 0 {
			continue
		}
		// 列集与 Node refreshClockins 一致：不写 city_code/city_name（dayNew 明细不带城市，
		// 由打卡路由事后补写，避免对账把已有城市信息冲成空）。
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO worklog_clockin (team_id, member_id, clock_date, seq, detail_id, clock_time,
			   position, longitude, latitude, work_hours, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE detail_id = VALUES(detail_id), clock_time = VALUES(clock_time),
			   position = VALUES(position), longitude = VALUES(longitude), latitude = VALUES(latitude),
			   work_hours = VALUES(work_hours)`,
			teamID, memberID, date, seq, rec.DetailID, nullIfEmpty(rec.ClockTime),
			rec.Position, rec.Lng, rec.Lat, rec.WorkHours, rec.Source); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// DayBody 取 dayNew 应答正文（data.body；平台实测形状，见 private/esgcc/sgcc/交接文档.md）。
func DayBody(decoded map[string]any) map[string]any {
	if decoded == nil {
		return nil
	}
	data, _ := decoded["data"].(map[string]any)
	body, _ := data["body"].(map[string]any)
	return body
}

// ParseDayClockins 解析 dayNew 应答中的打卡数组（data.body.clockInDetailList，与
// server/src/sgccclockin/index.js refreshClockins 同口径）：detailId ?? id、
// clockInTime ?? createTime，工时为 body 级 workHours（服务端算好，逐行同值）。
func ParseDayClockins(decoded map[string]any) []ClockinRecordLocal {
	body := DayBody(decoded)
	if body == nil {
		return nil
	}
	data, _ := body["clockInDetailList"].([]any)
	workHours := str(body["workHours"])
	out := make([]ClockinRecordLocal, 0, len(data))
	for _, it := range data {
		row, ok := it.(map[string]any)
		if !ok {
			continue
		}
		rec := ClockinRecordLocal{
			DetailID:  str(pick(row, "detailId", "id")),
			ClockTime: FmtCnDateTime(pick(row, "clockInTime", "createTime")),
			Position:  str(pick(row, "position", "address")),
			Lng:       str(pick(row, "longitude", "lng")),
			Lat:       str(pick(row, "latitude", "lat")),
			CityCode:  str(pick(row, "cityCode")),
			CityName:  str(pick(row, "cityName")),
			WorkHours: workHours,
		}
		// seq：商旅打卡类型 1 开始 2 结束（type/seq 字段）
		rec.Seq = int(num(pick(row, "type", "seq")))
		if rec.Seq != 1 && rec.Seq != 2 {
			rec.Seq = 0 // 由 RefreshClockins 分配
		}
		out = append(out, rec)
	}
	return out
}

func pick(row map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := row[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// jsString JS String(v) 同口径（大整数不落科学计数法：时间戳/明细 id 按十进制展开）。
func jsString(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(jsString(v))
}

// SyncLogRow 核查流水行。
type SyncLogRow struct {
	ID         int64  `json:"id"`
	TeamID     int64  `json:"team_id"`
	MemberID   int64  `json:"member_id"`
	MemberName string `json:"member_name,omitempty"`
	SyncDate   string `json:"sync_date"`
	Scope      string `json:"scope"`
	Type       string `json:"type"`
	Result     string `json:"result"`
	Detail     string `json:"detail"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// AddSyncLog 写一条核查流水。
func (m *Module) AddSyncLog(ctx context.Context, teamID, memberID int64, date, scope, typ, result, detail string) {
	_, _ = m.DB.ExecContext(ctx,
		`INSERT INTO worklog_sync_log (team_id, member_id, sync_date, scope, type, result, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullIfZeroID(teamID), nullIfZeroID(memberID), date, scope, typ, result, truncateStr(detail, 512))
}

func nullIfZeroID(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// SyncOne 单成员某日对账（scope=daily）：登录态探测 + 打卡对账 + 费用对账 + 费用照片双向对账
// （与 Node syncOne 同口径：一律以商旅为准覆盖本地）。
// 分段异常记对应类型 fail 行（`<阶段>异常：<原因>`）后原样上抛；登录失效记 auth fail 行返回错误。
func (m *Module) SyncOne(ctx context.Context, acc *Account, date, scope string, teamID int64) error {
	if err := m.SyncOneClockin(ctx, acc, date, scope, teamID); err != nil {
		return err
	}
	if m.API == nil {
		return nil
	}
	tpl, err := m.syncFeePart(ctx, acc, date, scope, teamID)
	if err != nil {
		return err
	}
	if tpl != nil {
		m.syncPhotoPart(ctx, acc, date, scope, teamID, ctxMember(acc), tpl)
	}
	return nil
}

// ctxMember 卡片上下文成员（打卡/费用/照片本地行以 member_id 为人级稳定键；
// 代打卡场景落在卡片成员名下，缺省账号自身成员）。
func ctxMember(acc *Account) int64 {
	if acc.CardMemberID != 0 {
		return acc.CardMemberID
	}
	return acc.MemberID
}

// SyncOneClockin 提醒前轻量同步（Node syncCardClockinsForRemind 同口径）：仅登录态 + 打卡对账，
// 不同步费用（非提醒口径，省整班全量核查开销）。
func (m *Module) SyncOneClockin(ctx context.Context, acc *Account, date, scope string, teamID int64) error {
	if !m.probeAuth(ctx, acc) {
		m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "auth", "fail", "登录已失效，已标记置灰")
		return fmt.Errorf("登录已失效，已标记置灰")
	}
	if m.API == nil {
		return nil
	}
	return m.syncClockinPart(ctx, acc, date, scope, teamID)
}

// syncClockinPart 打卡对账：先全量刷新，再删除本地 detailId 不在商旅返回集合内的行
// （商旅 0 条则全删，一律以商旅为准；与 Node syncClockinPart 同口径）。
func (m *Module) syncClockinPart(ctx context.Context, acc *Account, date, scope string, teamID int64) (err error) {
	memberID := ctxMember(acc)
	defer func() {
		if err != nil {
			m.AddSyncLog(ctx, teamID, memberID, date, scope, "clockin", "fail",
				truncateStr("打卡对账异常："+err.Error(), 200))
		}
	}()
	d, derr := m.API.DayNew(ctx, acc.Token, date, CallOpts{
		Token: acc.Token, Mobile: acc.Mobile,
		DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
	})
	if derr != nil {
		return derr
	}
	if d == nil {
		return fmt.Errorf("平台应答不可解")
	}
	before := m.countClockins(ctx, memberID, date)
	remote := ParseDayClockins(d)
	for i := range remote {
		remote[i].Source = 1
	}
	if rerr := m.RefreshClockins(ctx, teamID, memberID, date, remote); rerr != nil {
		return rerr
	}
	remoteIDs := map[string]bool{}
	for _, rec := range remote {
		if rec.DetailID != "" {
			remoteIDs[rec.DetailID] = true
		}
	}
	if len(remoteIDs) == 0 {
		_, _ = m.DB.ExecContext(ctx,
			`DELETE FROM worklog_clockin WHERE member_id = ? AND clock_date = ?`, memberID, date)
	} else {
		localRows, lerr := m.DB.QueryContext(ctx,
			`SELECT id, detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ?`,
			memberID, date)
		if lerr == nil {
			for localRows.Next() {
				var id int64
				var detailID string
				if err := localRows.Scan(&id, &detailID); err == nil && !remoteIDs[detailID] {
					_, _ = m.DB.ExecContext(ctx, `DELETE FROM worklog_clockin WHERE id = ?`, id)
				}
			}
			localRows.Close()
		}
	}
	result := "diff"
	if before == minInt(len(remoteIDs), 2) {
		result = "ok"
	}
	m.AddSyncLog(ctx, teamID, memberID, date, scope, "clockin", result,
		fmt.Sprintf("打卡：商旅 %d 条覆盖本地（本地原 %d 条）", len(remoteIDs), before))
	return nil
}

// syncFeePart 费用对账：以商旅模板为准回写本地摘要（含成本分配；与 Node syncOne 费用段同口径）。
func (m *Module) syncFeePart(ctx context.Context, acc *Account, date, scope string, teamID int64) (tpl map[string]any, err error) {
	memberID := ctxMember(acc)
	defer func() {
		if err != nil {
			m.AddSyncLog(ctx, teamID, memberID, date, scope, "fee", "fail",
				truncateStr("费用对账异常："+err.Error(), 200))
		}
	}()
	fi, ferr := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, date), devOpt(acc))
	if ferr != nil {
		return nil, ferr
	}
	tpl = ExtractClockTemplate(fi)
	if tpl == nil {
		return nil, nil // 平台未给模板（如当日未开费用）：不落对账行、不进照片对账
	}
	food, transit := ExtractSubsidy(tpl)
	ccCode, ccName := ExtractCostCenter(tpl)
	changed := true
	var oldFood, oldTransit float64
	var oldCode string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT food_fee, transit_fee, cost_center_code FROM worklog_fee WHERE member_id = ? AND fee_date = ?`,
		memberID, date).Scan(&oldFood, &oldTransit, &oldCode); err != nil {
		changed = true
	} else {
		changed = oldFood != food || oldTransit != transit || oldCode != ccCode
	}
	if _, werr := m.DB.ExecContext(ctx,
		`INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, cost_center_code, cost_center_name, synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
		 ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee),
		   cost_center_code = VALUES(cost_center_code), cost_center_name = VALUES(cost_center_name), synced_at = NOW()`,
		teamID, memberID, date, food, transit, ccCode, ccName); werr != nil {
		return nil, werr
	}
	result := "ok"
	suffix := "，一致"
	if changed {
		result, suffix = "diff", "，已按商旅覆盖"
	}
	m.AddSyncLog(ctx, teamID, memberID, date, scope, "fee", result,
		fmt.Sprintf("费用：伙食 %s / 交通 %s / 成本中心 %s%s",
			jsNumberString(food), jsNumberString(transit), orEmpty(ccCode, "无"), suffix))
	return tpl, nil
}

// ExtractSubsidy 从费用模板取补助明细（组件 id=10）：{foodFee, arrive} 转数字（坏 JSON 记 0）。
func ExtractSubsidy(tpl map[string]any) (food, transit float64) {
	comp := findComp(tpl, "10")
	if comp == nil {
		return 0, 0
	}
	s, _ := comp["value"].(string)
	if strings.TrimSpace(s) == "" {
		return 0, 0
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return 0, 0
	}
	return num(rawStringOf(v["foodFee"])), num(rawStringOf(v["arrive"]))
}

// rawStringOf 取任意值文本（JSON 数字/字符串同口径）。
func rawStringOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

func orEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// countClockins 当日本地打卡行数（对账前后对比口径）。
func (m *Module) countClockins(ctx context.Context, memberID int64, date string) int {
	var n int
	if err := m.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM worklog_clockin WHERE member_id = ? AND clock_date = ?`,
		memberID, date).Scan(&n); err != nil {
		return 0
	}
	return n
}

// clockinSummary 当日打卡摘要 {seq: {time, position}}。
func (m *Module) clockinSummary(ctx context.Context, memberID int64, date string) map[string]any {
	out := map[string]any{}
	if memberID == 0 {
		return out
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT seq, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s'), position, work_hours
		 FROM worklog_clockin WHERE member_id = ? AND clock_date = ?`, memberID, date)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var clockTime, position, workHours sql.NullString
		if err := rows.Scan(&seq, &clockTime, &position, &workHours); err == nil {
			out[strconv.Itoa(seq)] = map[string]any{
				"time": clockTime.String, "position": position.String, "work_hours": workHours.String,
			}
		}
	}
	return out
}

// feeSummary 当日费用摘要。
func (m *Module) feeSummary(ctx context.Context, memberID int64, date string) map[string]any {
	out := map[string]any{"filled": false}
	if memberID == 0 {
		return out
	}
	var food, transit float64
	err := m.DB.QueryRowContext(ctx,
		`SELECT food_fee, transit_fee FROM worklog_fee WHERE member_id = ? AND fee_date = ?`,
		memberID, date).Scan(&food, &transit)
	if err != nil {
		return out
	}
	return map[string]any{"filled": true, "food_fee": food, "transit_fee": transit}
}

// requireMemberAccount 打卡/费用操作前置：成员有效性 + 取绑定行 + 登录态校验（失效实时探测兜底）。
func (m *Module) requireMemberAccount(w http.ResponseWriter, r *http.Request, memberID int64, date string) *Account {
	_, team, ok := m.enter(w, r)
	if !ok {
		return nil
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return nil
	}
	var exists int
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM worklog_member m
		 WHERE m.id = ? AND (m.team_id = ? OR EXISTS (
		   SELECT 1 FROM worklog_entry e JOIN worklog_entry_member em ON em.entry_id = e.id
		   WHERE e.team_id = ? AND em.member_id = m.id AND e.log_date = ?))`,
		memberID, team.ID, team.ID, date).Scan(&exists)
	if err != nil || exists == 0 {
		fail(w, http.StatusNotFound, codeMissing, "成员不存在或不属于本班组")
		return nil
	}
	var acc Account
	var accMemberID sql.NullInt64
	err = m.DB.QueryRowContext(r.Context(),
		`SELECT id, team_id, user_id, member_id, mobile, token, device_type, system_version, token_status
		 FROM worklog_sgcc_account WHERE member_id = ? OR (user_id = (SELECT user_id FROM worklog_member WHERE id = ?))
		 ORDER BY (member_id = ?) DESC LIMIT 1`,
		memberID, memberID, memberID,
	).Scan(&acc.ID, &acc.TeamID, &acc.UserID, &accMemberID, &acc.Mobile, &acc.Token,
		&acc.DeviceType, &acc.SystemVersion, &acc.TokenStatus)
	if err != nil {
		fail(w, http.StatusBadRequest, 40020, "该成员未绑定商旅账号，请其本人在「我的 → 绑定商旅」绑定")
		return nil
	}
	if accMemberID.Valid {
		acc.MemberID = accMemberID.Int64
	}
	acc.CardMemberID = memberID
	if acc.TokenStatus != 1 {
		if !m.probeAuth(r.Context(), &acc) {
			fail(w, http.StatusBadRequest, 40021, "该成员商旅登录已过期，请其本人重新登录")
			return nil
		}
		acc.TokenStatus = 1
	}
	return &acc
}

// dayGet GET /day?date=&member_id=：当日打卡/费用摘要。
// Node 口径：全班 members 形状（members[成员id] = {bound, tokenStatus, seq1/seq2, workHours, fee}）；
// 传 member_id 时另回单成员 date/clockin/fee 摘要（网页端旧键，保持兼容）。
func (m *Module) dayGet(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	if date == "" {
		date = TodayCN()
	}
	if !validDate(date) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"members": map[string]any{}})
		return
	}
	members := m.dayMembers(r.Context(), team.ID, date)
	out := map[string]any{"members": members}
	if memberID := rawNumQuery(r, "member_id"); memberID > 0 {
		out["date"] = date
		out["clockin"] = m.clockinSummary(r.Context(), memberID, date)
		out["fee"] = m.feeSummary(r.Context(), memberID, date)
	}
	writeOK(w, out)
}

// dayMembers 全班当日成员视图（Node /day members 形状）：
// 成员集合 = 班组成员 ∪ 当日卡上用车人；绑定行按人级解析（member_id 直连优先、user_id 兜底）。
func (m *Module) dayMembers(ctx context.Context, teamID int64, date string) map[string]any {
	members := map[string]any{}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id FROM worklog_member WHERE team_id = ?
		 UNION
		 SELECT em.member_id AS id FROM worklog_entry e
		 JOIN worklog_entry_member em ON em.entry_id = e.id
		 WHERE e.team_id = ? AND e.log_date = ?`, teamID, teamID, date)
	if err != nil {
		return members
	}
	memberIDs := make([]int64, 0)
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			memberIDs = append(memberIDs, id)
		}
	}
	rows.Close()
	if len(memberIDs) == 0 {
		return members
	}
	// 绑定态（人级解析，一名成员多行时去重取首条：member_id 直连行优先）
	accRows, err := m.DB.QueryContext(ctx,
		`SELECT m.id, a.token_status
		 FROM worklog_member m
		 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
		 WHERE m.id IN (`+placeholders(len(memberIDs))+`)
		 ORDER BY (a.member_id = m.id) DESC`, anySlice(memberIDs)...)
	if err == nil {
		for accRows.Next() {
			var id int64
			var tokenStatus int
			if accRows.Scan(&id, &tokenStatus) == nil {
				key := strconv.FormatInt(id, 10)
				if _, exists := members[key]; !exists {
					members[key] = map[string]any{"bound": true, "tokenStatus": tokenStatus}
				}
			}
		}
		accRows.Close()
	}
	// 打卡流水
	ckRows, err := m.DB.QueryContext(ctx,
		`SELECT member_id, seq, detail_id, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s'), position, work_hours
		 FROM worklog_clockin WHERE member_id IN (`+placeholders(len(memberIDs))+`) AND clock_date = ?`,
		append(anySlice(memberIDs), date)...)
	if err == nil {
		for ckRows.Next() {
			var memberID, seq int64
			var detailID, clockTime, position, workHours sql.NullString
			if ckRows.Scan(&memberID, &seq, &detailID, &clockTime, &position, &workHours) == nil {
				key := strconv.FormatInt(memberID, 10)
				m, _ := members[key].(map[string]any)
				if m == nil {
					m = map[string]any{}
					members[key] = m
				}
				m["seq"+strconv.FormatInt(seq, 10)] = map[string]any{
					"detailId": detailID.String, "time": clockTime.String, "position": position.String,
				}
				if workHours.String != "" {
					m["workHours"] = workHours.String
				}
			}
		}
		ckRows.Close()
	}
	// 费用摘要
	feeRows, err := m.DB.QueryContext(ctx,
		`SELECT member_id, food_fee, transit_fee, cost_center_code, cost_center_name
		 FROM worklog_fee WHERE member_id IN (`+placeholders(len(memberIDs))+`) AND fee_date = ?`,
		append(anySlice(memberIDs), date)...)
	if err == nil {
		for feeRows.Next() {
			var memberID int64
			var food, transit float64
			var ccCode, ccName sql.NullString
			if feeRows.Scan(&memberID, &food, &transit, &ccCode, &ccName) == nil {
				key := strconv.FormatInt(memberID, 10)
				m, _ := members[key].(map[string]any)
				if m == nil {
					m = map[string]any{}
					members[key] = m
				}
				m["fee"] = map[string]any{
					"foodFee": food, "transitFee": transit,
					"costCenterCode": ccCode.String, "costCenterName": ccName.String,
				}
			}
		}
		feeRows.Close()
	}
	return members
}

// anySlice int64 切片转 any 切片（SQL 参数用）。
func anySlice(ids []int64) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

func rawNumQuery(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
	return n
}

// clockinPost POST /clockin：打卡（seq 1 首次 / 2 结束 / 0 或 action=update 更新地点）。
// 请求形状兼容双端：小程序（entry_id/action/cityCode/cityName/remarks，Node 口径）与
// 网页端（seq 0+detail_id/city_code/city_name）；失败口径同 Node：重复打卡 40037、
// 登录过期探测 40021、平台失败 40036（带平台原文）。
func (m *Module) clockinPost(w http.ResponseWriter, r *http.Request) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	memberID := rawNum(body["member_id"])
	entryID := rawNum(body["entry_id"])
	date := rawString(body["date"])
	action := rawString(body["action"])
	seq := int(num(rawString(body["seq"])))
	if memberID == 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数不完整")
		return
	}
	if seq != 0 && seq != 1 && seq != 2 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	position := truncateStr(rawString(body["position"]), 255)
	longitude := rawString(body["longitude"])
	latitude := rawString(body["latitude"])
	if longitude == "" || latitude == "" {
		fail(w, http.StatusBadRequest, codeNeedRisk, "缺少打卡位置信息")
		return
	}
	// 备注默认「110kV及220kV输电线路巡视」（商旅 remarks，与记录卡片备注无关；空值走默认同 Node）
	remarks := rawString(body["remarks"])
	if remarks == "" {
		remarks = "110kV及220kV输电线路巡视"
	}
	remarks = truncateStr(remarks, 255)

	// 记录与成员归属校验（entry_id 驱动（小程序口径）：日志归属 + 卡上用车人；无 entry_id（网页端）按 body.date）
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	if entryID != 0 {
		var logDate string
		if err := m.DB.QueryRowContext(r.Context(),
			`SELECT DATE_FORMAT(log_date, '%Y-%m-%d') FROM worklog_entry WHERE id = ? AND team_id = ?`,
			entryID, team.ID).Scan(&logDate); err != nil {
			fail(w, http.StatusNotFound, codeMissing, "日志不存在")
			return
		}
		var hit int
		if err := m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM worklog_entry_member WHERE entry_id = ? AND member_id = ?`,
			entryID, memberID).Scan(&hit); err != nil || hit == 0 {
			fail(w, http.StatusBadRequest, 40007, "该成员不是本卡用车人")
			return
		}
		date = logDate
	} else if date == "" {
		date = TodayCN()
	}
	// 打卡仅对当日开放（含更新）；费用修改不受此限
	if date != TodayCN() {
		fail(w, http.StatusBadRequest, 40041, "仅当日日期可打卡")
		return
	}

	// 定位兜底：未带地址时按经纬度服务端逆编码（完整地址串 + 城市编码）；带地址缺城市时补齐
	//（不覆盖已带入的地址串），否则上游报「打卡城市为空」
	cityCode := firstNonEmpty(rawString(body["city_code"]), rawString(body["cityCode"]))
	cityName := firstNonEmpty(rawString(body["city_name"]), rawString(body["cityName"]))
	if position == "" {
		rev := m.reverseGeoQuiet(r.Context(), longitude, latitude)
		if rev.Position == "" {
			fail(w, http.StatusBadRequest, codeNeedRisk, "定位逆编码失败，请重新定位或选择杆塔")
			return
		}
		position, cityCode, cityName = rev.Position, rev.CityCode, rev.CityName
	} else if cityCode == "" || cityName == "" {
		rev := m.reverseGeoQuiet(r.Context(), longitude, latitude)
		if cityCode == "" {
			cityCode = rev.CityCode
		}
		if cityName == "" {
			cityName = rev.CityName
		}
	}

	acc := m.requireMemberAccount(w, r, memberID, date)
	if acc == nil {
		return
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
		return
	}
	opts := CallOpts{
		Token: acc.Token, Mobile: acc.Mobile,
		DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
	}
	isUpdate := action == "update" || seq == 0
	seqNum := seq
	if seqNum == 0 {
		seqNum = 1
	}
	if seqNum != 2 {
		seqNum = 1
	}
	failMsg := "商旅打卡失败，请重试"
	if isUpdate {
		failMsg = "商旅更新打卡失败，请重试"
		// 更新：按本地存的 detailId 调用 updateMark（仅改地点）
		detailID := rawString(body["detail_id"])
		if detailID == "" {
			// Node 口径：detailId 取本地该 seq 已有记录
			lookupSeq := seq
			if lookupSeq == 0 {
				lookupSeq = seqNum
			}
			_ = m.DB.QueryRowContext(r.Context(),
				`SELECT detail_id FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq = ?`,
				memberID, date, lookupSeq).Scan(&detailID)
			seqNum = lookupSeq
		} else {
			var localSeq sql.NullInt64
			if err := m.DB.QueryRowContext(r.Context(),
				`SELECT seq FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND detail_id = ?`,
				memberID, date, detailID).Scan(&localSeq); err == nil && localSeq.Valid {
				seqNum = int(localSeq.Int64)
			}
		}
		if detailID == "" {
			fail(w, http.StatusBadRequest, codeCaptchaOff, "未找到该次打卡记录，无法更新")
			return
		}
		d, callErr := m.API.MarkClockin(r.Context(), acc.Token, map[string]any{
			"seq":       0,
			"detailId":  detailID,
			"cityCode":  cityCode,
			"cityName":  cityName,
			"position":  position,
			"longitude": longitude,
			"latitude":  latitude,
			"date":      date,
		}, opts)
		if callErr != nil {
			writeErr(w, callErr)
			return
		}
		if d == nil || num(d["statusCode"]) != 200 {
			m.failMark(w, r, acc, d, failMsg)
			return
		}
	} else {
		var exists int
		_ = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM worklog_clockin WHERE member_id = ? AND clock_date = ? AND seq = ?`,
			memberID, date, seqNum).Scan(&exists)
		if exists > 0 {
			fail(w, http.StatusBadRequest, codePlatform, map[bool]string{true: "已开始打卡", false: "已结束打卡"}[seqNum == 1])
			return
		}
		payload := map[string]any{
			"seq":         seqNum,
			"clockInDate": date,
			"cityCode":    cityCode,
			"cityName":    cityName,
			"position":    position,
			"longitude":   longitude,
			"latitude":    latitude,
			"remarks":     remarks,
			"date":        date,
		}
		d, callErr := m.API.MarkClockin(r.Context(), acc.Token, payload, opts)
		if callErr != nil {
			writeErr(w, callErr)
			return
		}
		if d == nil || num(d["statusCode"]) != 200 {
			m.failMark(w, r, acc, d, failMsg)
			return
		}
	}

	// 双写：从商旅全量刷新该日打卡流水（含工时）；随后补写城市信息
	//（dayNew 明细不带城市编码，落库供后续打卡/费用模板带入全套定位信息）
	if d, derr := m.API.DayNew(r.Context(), acc.Token, date, opts); derr == nil && d != nil {
		_ = m.RefreshClockins(r.Context(), acc.TeamID, memberID, date, ParseDayClockins(d))
	}
	_, _ = m.DB.ExecContext(r.Context(),
		`UPDATE worklog_clockin SET city_code = ?, city_name = ? WHERE member_id = ? AND clock_date = ? AND seq = ?`,
		cityCode, cityName, memberID, date, seqNum)
	writeOK(w, map[string]any{"clockin": m.clockinSummary(r.Context(), memberID, date)}, "打卡成功")
}

// failMark 打卡失败口径（Node /clockin 同）：先探测登录态，已过期则 40021 置灰提示重新登录，
// 否则 40036 带平台原文（区别于一般失败）。
func (m *Module) failMark(w http.ResponseWriter, r *http.Request, acc *Account, d map[string]any, failMsg string) {
	if !m.probeAuth(r.Context(), acc) {
		fail(w, http.StatusBadRequest, 40021, "该成员商旅登录已过期，请其本人重新登录")
		return
	}
	msg := ""
	if d != nil {
		if s, ok := d["msg"].(string); ok {
			msg = s
		}
	}
	if msg == "" {
		msg = failMsg
	}
	fail(w, http.StatusBadRequest, 40036, msg)
}

// firstNonEmpty 取首个非空文本。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// feeTplParams 费用模板取参：城市参数取当日打卡流水首条（缺省模板可能不带默认成本中心）。
func (m *Module) feeTplParams(ctx context.Context, memberID int64, date string) FeeTplParams {
	params := FeeTplParams{ClockInDate: date}
	var position, cityCode, cityName sql.NullString
	err := m.DB.QueryRowContext(ctx,
		`SELECT position, city_code, city_name FROM worklog_clockin
		 WHERE member_id = ? AND clock_date = ? ORDER BY seq LIMIT 1`, memberID, date,
	).Scan(&position, &cityCode, &cityName)
	if err == nil {
		params.Position = position.String
		params.CityCode = cityCode.String
		params.CityName = cityName.String
	}
	return params
}

// ensureCostCenter 模板「成本分配」（id=4）兜底：商旅默认带出则归一后沿用并回报当前值；
// 空值时用本成员最近一次本地成本中心回填。返回 {code, name} 或空（无来源可填）。
func (m *Module) ensureCostCenter(ctx context.Context, tpl map[string]any, memberID int64) (code, name string) {
	comp := findComp(tpl, "4")
	if comp == nil {
		return "", ""
	}
	var v map[string]any
	if s, _ := comp["value"].(string); s != "" {
		_ = json.Unmarshal([]byte(s), &v)
	}
	if v == nil {
		v = map[string]any{}
	}
	// 编码已带出：选项码缺失/误写时归一（商旅自动带出常只给名称+编码不给选中码，原样透传保存不生效）
	if cc := fmt.Sprintf("%v", v["costCenterCode"]); cc != "" && cc != "<nil>" {
		NormalizeCostAllocComp(comp, v)
		name, _ := v["costCenterName"].(string)
		return cc, name
	}
	var ccCode, ccName sql.NullString
	err := m.DB.QueryRowContext(ctx,
		`SELECT cost_center_code, cost_center_name FROM worklog_fee
		 WHERE member_id = ? AND cost_center_code <> '' ORDER BY fee_date DESC LIMIT 1`, memberID,
	).Scan(&ccCode, &ccName)
	if err != nil {
		return "", ""
	}
	NormalizeCostAllocComp(comp, map[string]any{
		"costCenterName": ccName.String,
		"value":          "",
		"costCenterCode": ccCode.String,
	})
	return ccCode.String, ccName.String
}

// feeCostAlloc 弹层展示口径：组件 id=4 选项按 optionsJsonObject 的 value 码反查文案；模板无值回退本地摘要。
func feeCostAlloc(tpl map[string]any, local map[string]any) map[string]any {
	comp := findComp(tpl, "4")
	if comp != nil {
		if s, _ := comp["value"].(string); s != "" {
			var v map[string]any
			if json.Unmarshal([]byte(s), &v) == nil {
				code := fmt.Sprintf("%v", v["costCenterCode"])
				optCode := fmt.Sprintf("%v", v["value"])
				if code != "" && code != "<nil>" || (optCode != "" && optCode != "<nil>") {
					label := CostAllocOpts[optCode]
					if opts, _ := comp["optionsJsonObject"].([]any); opts != nil {
						for _, o := range opts {
							om, _ := o.(map[string]any)
							if om != nil && fmt.Sprintf("%v", om["value"]) == optCode {
								label, _ = om["label"].(string)
								break
							}
						}
					}
					name, _ := v["costCenterName"].(string)
					if code == "<nil>" {
						code = ""
					}
					return map[string]any{
						"label":          label,
						"costCenterName": name,
						"costCenterCode": code,
					}
				}
			}
		}
	}
	if local != nil {
		if code, _ := local["cost_center_code"].(string); code != "" {
			name, _ := local["cost_center_name"].(string)
			return map[string]any{
				"label":          "成本中心",
				"costCenterName": name,
				"costCenterCode": code,
			}
		}
	}
	return nil
}

// feeGet GET /fee?member_id=&date=：费用弹层数据源（本地摘要 + 商旅实时模板原文 + 成本分配现状）。
func (m *Module) feeGet(w http.ResponseWriter, r *http.Request) {
	memberID := rawNumQuery(r, "member_id")
	date := r.URL.Query().Get("date")
	if date == "" {
		date = TodayCN()
	}
	acc := m.requireMemberAccount(w, r, memberID, date)
	if acc == nil {
		return
	}
	ctx := r.Context()
	local := map[string]any{}
	var food, transit float64
	var ccCode, ccName, extra sql.NullString
	err := m.DB.QueryRowContext(ctx,
		`SELECT food_fee, transit_fee, cost_center_code, cost_center_name, extra FROM worklog_fee
		 WHERE member_id = ? AND fee_date = ?`, memberID, date,
	).Scan(&food, &transit, &ccCode, &ccName, &extra)
	if err == nil {
		local = map[string]any{
			"food_fee": food, "transit_fee": transit,
			"cost_center_code": ccCode.String, "cost_center_name": ccName.String,
			"extra": extra.String,
		}
	}
	out := map[string]any{
		"date": date,
		"fee":  m.feeSummary(ctx, memberID, date),
	}
	if err == nil {
		out["local"] = local
	} else {
		out["local"] = nil
	}
	// 商旅实时模板（成本分配选项/上传图片组件现状都在模板里）
	if m.API == nil {
		writeOK(w, out)
		return
	}
	d, derr := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, date), devOpt(acc))
	if derr != nil {
		fail(w, http.StatusBadRequest, codeFeeTpl, "获取商旅费用模板失败，请重试")
		return
	}
	tpl := ExtractClockTemplate(d)
	if tpl == nil {
		// Node 口径：模板取不到统一 40038（风控拒答/通道异常都归此码，前端引导重试）
		fail(w, http.StatusBadRequest, codeFeeTpl, "获取商旅费用模板失败，请重试")
		return
	}
	out["clockTemplate"] = tpl
	out["costAlloc"] = feeCostAlloc(tpl, local)
	writeOK(w, out)
}

// devOpt 账号设备口径（缺省同 Node devOpt）。
func devOpt(acc *Account) CallOpts {
	return CallOpts{
		DeviceType:    acc.DeviceType,
		SystemVersion: acc.SystemVersion,
		Mobile:        acc.Mobile,
	}
}

// feePost POST /fee：保存费用（本地优先 + 商旅完整模板 read-modify-write 走 slapp）。
// body: {member_id, date, foodFee|food_fee, transitFee|transit_fee, overrides?}；
// overrides 为组件级透传 {组件id: value字符串}，原样进 saveFeeInfoNew。
func (m *Module) feePost(w http.ResponseWriter, r *http.Request) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	memberID := rawNum(body["member_id"])
	date := rawString(body["date"])
	if date == "" {
		date = TodayCN()
	}
	acc := m.requireMemberAccount(w, r, memberID, date)
	if acc == nil {
		return
	}
	ctx := r.Context()
	food := num(rawString(body["foodFee"]))
	if rawString(body["foodFee"]) == "" {
		food = num(rawString(body["food_fee"]))
	}
	transit := num(rawString(body["transitFee"]))
	if rawString(body["transitFee"]) == "" {
		transit = num(rawString(body["transit_fee"]))
	}
	var ccCode, ccName string
	if m.API != nil {
		d, derr := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, date), devOpt(acc))
		tpl := ExtractClockTemplate(d)
		if derr != nil || tpl == nil {
			fail(w, http.StatusBadRequest, codeFeeTpl, "获取商旅费用模板失败，请重试")
			return
		}
		// 成本分配（id=4）兜底：模板未带出时用本成员最近成本中心回填（勿用 overrides 同串覆盖，value/data 不对称）
		ccCode, ccName = m.ensureCostCenter(ctx, tpl, memberID)
		overrides := rawStringMap(body["overrides"])
		overrides["10"] = SubsidyJSON(food, transit)
		d2, rerr := m.API.SaveFeeInfoNew(ctx, acc.Token, date, tpl, overrides, devOpt(acc))
		if rerr != nil {
			writeErr(w, rerr)
			return
		}
		if d2 == nil || num(d2["statusCode"]) != 200 {
			msg := msgOr(d2, "商旅费用保存失败，请重试")
			fail(w, http.StatusBadRequest, codeFeeSave, msg)
			return
		}
		m.AddSyncLog(ctx, acc.TeamID, memberID, date, "daily", "fee", "ok", "")
	} else {
		m.AddSyncLog(ctx, acc.TeamID, memberID, date, "daily", "fee", "fail", "商旅协议服务未接入")
	}
	// 双写本地费用摘要（成本中心随本次生效值一并回写；未取到则保留旧值）
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO worklog_fee (team_id, member_id, fee_date, food_fee, transit_fee, cost_center_code, cost_center_name, synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
		 ON DUPLICATE KEY UPDATE food_fee = VALUES(food_fee), transit_fee = VALUES(transit_fee),
		   cost_center_code = IF(VALUES(cost_center_code) <> '', VALUES(cost_center_code), cost_center_code),
		   cost_center_name = IF(VALUES(cost_center_name) <> '', VALUES(cost_center_name), cost_center_name),
		   synced_at = NOW()`,
		acc.TeamID, memberID, date, food, transit, ccCode, ccName)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"fee": m.feeSummary(ctx, memberID, date)}, "保存成功")
}

// photoResync POST /photos/{id}/resync：商旅照片重新同步（任务化：登记即回 opId，串行后台执行，
// 端侧凭 /op/status、/sync/active 收尾；失败含成员与原因，sgcc_synced 随结果落库）。
func (m *Module) photoResync(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	photoID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var entryID, entryTeamID int64
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT p.entry_id, e.team_id FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 WHERE p.id = ? AND e.team_id = ?`, photoID, team.ID).Scan(&entryID, &entryTeamID)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "照片不存在")
		return
	}
	if m.Ops.ActivePhotoOp(entryTeamID, entryID) {
		fail(w, http.StatusConflict, codeRateLimit, "该卡片有商旅同步任务进行中，请稍后再试")
		return
	}
	op := m.Ops.New("resync", entryTeamID, entryID, photoID)
	if op == nil {
		fail(w, http.StatusConflict, codeRateLimit, "该卡片有商旅同步任务进行中，请稍后再试")
		return
	}
	opID := op.ID
	m.Ops.enqueueRun(func() {
		m.Ops.Run(opID, func(*PhotoOp) error {
			err := m.syncPhotoToSgcc(context.Background(), photoID, nil, func(done, total int) {
				if total > 0 {
					m.Ops.SetProgress(opID, done*100/total, "running")
					m.Ops.SetCounts(opID, done, total)
				}
			})
			if err != nil {
				if name := failedNameOf(err); name != "" {
					m.Ops.SetFailedName(opID, name)
				}
			}
			return err
		})
	})
	writeOK(w, map[string]any{"opId": opID}, "已发起重新同步")
}

// opStatus GET /op/status?op_id=：照片任务状态。
// opView 任务视图双端键名并存：Node /op/status、/sync/active（status/done/total/error/failedName/
// photoId/photoUrl、entryId）与网页端 PhotoOp（step/progress/failed、entry_id）。
func opView(op *PhotoOp) map[string]any {
	status := "running"
	switch op.Step {
	case "done":
		status = "ok"
	case "failed":
		status = "fail"
	}
	photoID := op.PhotoID
	if op.PhotoIDOut != 0 {
		photoID = op.PhotoIDOut
	}
	return map[string]any{
		"opId":       op.ID,
		"kind":       op.Kind,
		"teamId":     op.TeamID,
		"team_id":    op.TeamID,
		"entryId":    op.EntryID,
		"entry_id":   op.EntryID,
		"photoId":    photoID,
		"photo_id":   op.PhotoID,
		"status":     status,
		"done":       op.Done,
		"total":      op.Total,
		"progress":   op.Progress,
		"step":       op.Step,
		"failed":     op.Failed,
		"error":      op.Error,
		"failedName": op.FailedName,
		"photoUrl":   op.PhotoURL,
	}
}

// opStatus GET /op/status?op_id=：单任务状态轮询（仅本班组）。
// Node 口径：任务不存在/已过期/非本班按 200 空完成态回（端侧据此收尾），不落 404。
func (m *Module) opStatus(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	opID, _ := strconv.ParseInt(r.URL.Query().Get("op_id"), 10, 64)
	op, found := m.Ops.Status(opID)
	if !found || op.TeamID != team.ID {
		writeOK(w, map[string]any{
			"status": "ok", "done": 0, "total": 0,
			"progress": 100, "step": "done", "failed": false, "error": "", "failedName": "",
			"photoId": nil, "photoUrl": "",
		})
		return
	}
	writeOK(w, opView(op))
}

// SyncJob 同步进度任务（kind=batch/card/daily）。
type SyncJob struct {
	ID       int64  `json:"id"`
	TeamID   int64  `json:"team_id"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Errors   int    `json:"errors"` // 网页端旧键（= failed）
	Failed   int    `json:"failed"`
	FailMsg  string `json:"failMsg"`
	Kind     string `json:"kind"`
	Date     string `json:"date"`
	Finished bool   `json:"finished"`
	At       time.Time
}

// syncFailText 失败提示友好口径：网络/代理类报文归一（原始报文落核查记录 detail，端侧 toast 用本口径）。
func syncFailText(msg string) string {
	m := msg
	for _, pat := range []string{
		"ECONNREFUSED", "ECONNRESET", "ETIMEDOUT", "ENOTFOUND", "EAI_AGAIN",
		"EHOSTUNREACH", "socket hang up",
	} {
		if strings.Contains(m, pat) {
			return "网络/代理异常，无法连接商旅平台"
		}
	}
	low := strings.ToLower(m)
	if strings.Contains(low, "time out") || strings.Contains(low, "timeout") {
		return "网络/代理异常，无法连接商旅平台"
	}
	return truncateStr(m, 60)
}

// SyncJobRegistry 进度任务注册表（进程内存；重启清零仅作进度展示，同 Node syncJobs）。
type SyncJobRegistry struct {
	mu   sync.Mutex
	seq  int64
	jobs map[int64]*SyncJob
}

// NewSyncJobRegistry 构造注册表。
func NewSyncJobRegistry() *SyncJobRegistry {
	return &SyncJobRegistry{jobs: map[int64]*SyncJob{}}
}

// New 登记任务（total<=0 = 无可同步成员即刻完成；顺手清理 30min 前的过期任务）。
func (reg *SyncJobRegistry) New(teamID int64, total int, kind, date string) int64 {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := time.Now()
	for id, j := range reg.jobs {
		if now.Sub(j.At) > 30*time.Minute {
			delete(reg.jobs, id)
		}
	}
	reg.seq++
	if kind == "" {
		kind = "batch"
	}
	reg.jobs[reg.seq] = &SyncJob{
		ID: reg.seq, TeamID: teamID, Total: total, Kind: kind, Date: date,
		Finished: total <= 0, At: now,
	}
	return reg.seq
}

// Step 完成一步（errMsg 非空计失败，留存首条友好提示）。
func (reg *SyncJobRegistry) Step(id int64, errMsg string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if job, ok := reg.jobs[id]; ok {
		job.Done++
		if errMsg != "" {
			job.Failed++
			job.Errors = job.Failed
			if job.FailMsg == "" {
				job.FailMsg = syncFailText(errMsg)
			}
		}
		job.At = time.Now()
	}
}

// Abort 循环级异常中止：按未跑步数计失败（端侧收尾提示失败而非「同步完成」）。
func (reg *SyncJobRegistry) Abort(id int64, errMsg string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if job, ok := reg.jobs[id]; ok {
		if left := job.Total - job.Done; left > job.Failed {
			job.Failed = left
			job.Errors = job.Failed
		}
		if job.Failed < 1 {
			job.Failed, job.Errors = 1, 1
		}
		if job.FailMsg == "" {
			job.FailMsg = syncFailText(errMsg)
		}
		job.At = time.Now()
	}
}

// Finish 收尾。
func (reg *SyncJobRegistry) Finish(id int64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if job, ok := reg.jobs[id]; ok {
		job.Finished = true
		job.At = time.Now()
	}
}

// Get 查询任务。
func (reg *SyncJobRegistry) Get(id int64) (*SyncJob, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	job, ok := reg.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *job
	return &cp, true
}

// Active 返回未完成任务。
func (reg *SyncJobRegistry) Active() []*SyncJob {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	out := make([]*SyncJob, 0)
	for _, job := range reg.jobs {
		if !job.Finished {
			cp := *job
			out = append(out, &cp)
		}
	}
	return out
}

// ActiveByKind 本班组进行中（未完成）的同类任务（取最新；Node activeJob 同口径）。
func (reg *SyncJobRegistry) ActiveByKind(teamID int64, kind string) *SyncJob {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var hit *SyncJob
	for _, job := range reg.jobs {
		if job.TeamID == teamID && job.Kind == kind && !job.Finished &&
			(hit == nil || job.At.After(hit.At)) {
			cp := *job
			hit = &cp
		}
	}
	return hit
}

// RecentFinished 近 within 内完成的同类任务（端侧收尾区分 全部成功 / 部分失败 / 整体失败）。
func (reg *SyncJobRegistry) RecentFinished(teamID int64, kind string, within time.Duration) *SyncJob {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	now := time.Now()
	var hit *SyncJob
	for _, job := range reg.jobs {
		if job.TeamID == teamID && job.Kind == kind && job.Finished &&
			now.Sub(job.At) <= within &&
			(hit == nil || job.At.After(hit.At)) {
			cp := *job
			hit = &cp
		}
	}
	return hit
}

// syncPull POST /sync/pull：手动从商旅拉取。三模式并存：
//
//	{entry_id}     卡片级（仅本卡用车人，日期以卡片 log_date 为准；所有 work-log 用户可发起）；
//	{member_id}    逐成员（网页端口径：单人单日）；
//	{date}/{from,to} 区段批量（仅超管 / 本班班组管理员；无参 = 当日；区段最多跨 62 天）。
//
// 区段/卡片逐日串行异步执行，一律以商旅为准覆盖本地；结果见 GET /sync/progress、/sync/logs。
func (m *Module) syncPull(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	ctx := r.Context()
	entryID := rawNum(body["entry_id"])
	memberID := rawNum(body["member_id"])

	// ---- 卡片级：仅本卡用车人（日期以卡片为准，from/to 忽略）----
	var onlyMemberIDs []int64
	from, to := "", ""
	if entryID > 0 {
		var logDate string
		if err := m.DB.QueryRowContext(ctx,
			`SELECT DATE_FORMAT(log_date, '%Y-%m-%d') FROM worklog_entry WHERE id = ? AND team_id = ?`,
			entryID, team.ID).Scan(&logDate); err != nil {
			fail(w, http.StatusNotFound, codeMissing, "日志不存在")
			return
		}
		rows, err := m.DB.QueryContext(ctx,
			`SELECT member_id FROM worklog_entry_member WHERE entry_id = ?`, entryID)
		if err != nil {
			writeErr(w, err)
			return
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				onlyMemberIDs = append(onlyMemberIDs, id)
			}
		}
		rows.Close()
		if len(onlyMemberIDs) == 0 {
			fail(w, http.StatusBadRequest, 40042, "该卡片暂无用车人，无需同步")
			return
		}
		from, to = logDate, logDate
	} else if memberID > 0 {
		// ---- 逐成员（网页端口径）：单人单日 ----
		date := rawString(body["date"])
		if date == "" {
			date = TodayCN()
		}
		acc := m.requireMemberAccount(w, r, memberID, date)
		if acc == nil {
			return
		}
		jobID := jobs.New(team.ID, 1, "card", date)
		go func() {
			defer jobs.Finish(jobID)
			if err := m.SyncOne(context.Background(), acc, date, "daily", team.ID); err != nil {
				jobs.Step(jobID, err.Error())
				return
			}
			jobs.Step(jobID, "")
		}()
		writeOK(w, map[string]any{"jobId": jobID, "total": 1}, "已发起同步")
		return
	} else {
		// ---- 区段批量（无参 = 当日）：仅超管 / 本班班组管理员（Node 口径 40304）----
		if ident.Role != "admin" &&
			!(ident.Role == "team_admin" && ident.TeamID == team.ID) {
			fail(w, http.StatusForbidden, 40304, "仅管理员可执行此操作")
			return
		}
		from = rawString(body["from"])
		if from == "" {
			from = rawString(body["date"])
		}
		if from == "" {
			from = TodayCN()
		}
		to = rawString(body["to"])
		if to == "" {
			to = from
		}
	}
	if !validDate(from) || !validDate(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeParam, "日期区段不正确")
		return
	}
	days := daysBetween(from, to) + 1
	if days > 62 {
		fail(w, http.StatusBadRequest, codeParam, "日期区段最多跨 62 天")
		return
	}
	// 区段批量门禁：区段内卡上成员的人级绑定账号任一登录过期即不允许发起（卡片级不设门禁）
	if entryID == 0 {
		rows, err := m.DB.QueryContext(ctx,
			`SELECT DISTINCT m.name
			 FROM worklog_entry e
			 JOIN worklog_entry_member em ON em.entry_id = e.id
			 JOIN worklog_member m ON m.id = em.member_id
			 JOIN worklog_sgcc_account a ON (a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)) AND a.token_status = 0
			 WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?`, team.ID, from, to)
		if err != nil {
			writeErr(w, err)
			return
		}
		expired := make([]string, 0)
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				expired = append(expired, name)
			}
		}
		rows.Close()
		if len(expired) > 0 {
			fail(w, http.StatusConflict, codeBoundOther,
				"以下成员商旅登录已过期，请先重新绑定后再发起批量同步："+strings.Join(expired, "、"))
			return
		}
	}
	// 同班组同时只允许一个批量任务（卡片级不在此限）
	if entryID == 0 && jobs.ActiveByKind(team.ID, "batch") != nil {
		fail(w, http.StatusConflict, 40909, "本班组已有批量从商旅同步进行中，请等待完成后再发起")
		return
	}
	// 进度登记：总单元 = 区段内「日 × 卡上绑定成员」数（卡片级 = 本卡绑定用车人数）
	total := m.syncTotal(ctx, team.ID, from, to, onlyMemberIDs)
	kind := "batch"
	if entryID > 0 {
		kind = "card"
	}
	jobID := jobs.New(team.ID, total, kind, from)
	go func() {
		defer jobs.Finish(jobID)
		for i := 0; i < days; i++ {
			d := addDays(from, i)
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						jobs.Abort(jobID, fmt.Sprintf("%v", rec))
					}
				}()
				m.syncTeamDay(context.Background(), team.ID, d, "daily", onlyMemberIDs, func(errMsg string) {
					jobs.Step(jobID, errMsg)
				})
			}()
		}
	}()
	msg := "已发起从商旅拉取（" + mdText(from) + "）"
	switch {
	case entryID > 0:
		msg = "已发起从商旅拉取（" + mdText(from) + "，仅本卡用车人）"
	case from != to:
		msg = "已发起从商旅拉取（" + mdText(from) + " 至 " + mdText(to) + "）"
	}
	writeOK(w, map[string]any{"jobId": jobID, "total": total}, msg)
}

// validDate YYYY-MM-DD 严格校验（Node DATE_RE 同口径）。
func validDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// daysBetween 区段天数差（UTC 日界步进，避免本地时区影响；Node DAY_MS 口径）。
func daysBetween(from, to string) int {
	f, err1 := time.ParseInLocation("2006-01-02", from, time.UTC)
	t, err2 := time.ParseInLocation("2006-01-02", to, time.UTC)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(t.Sub(f).Hours() / 24)
}

// addDays 起始日 + n 天（UTC 步进）。
func addDays(from string, n int) string {
	f, err := time.ParseInLocation("2006-01-02", from, time.UTC)
	if err != nil {
		return from
	}
	return f.AddDate(0, 0, n).Format("2006-01-02")
}

// mdText 响应文案口径：M月D日（不补零）。
func mdText(s string) string {
	if len(s) < 10 {
		return s
	}
	mo, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	return strconv.Itoa(mo) + "月" + strconv.Itoa(d) + "日"
}

// syncTotal 进度总单元：区段内「日 × 卡上绑定成员」数（onlyMemberIDs 非空 = 卡片级绑定用车人数）。
func (m *Module) syncTotal(ctx context.Context, teamID int64, from, to string, onlyMemberIDs []int64) int {
	var n int
	if len(onlyMemberIDs) > 0 {
		ph := placeholders(len(onlyMemberIDs))
		args := make([]any, 0, len(onlyMemberIDs))
		for _, id := range onlyMemberIDs {
			args = append(args, id)
		}
		err := m.DB.QueryRowContext(ctx,
			`SELECT COUNT(DISTINCT m.id) FROM worklog_member m
			 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
			 WHERE m.id IN (`+ph+`)`, args...).Scan(&n)
		if err != nil {
			return 0
		}
		return n
	}
	err := m.DB.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT m.id, e.log_date)
		 FROM worklog_entry e
		 JOIN worklog_entry_member em ON em.entry_id = e.id
		 JOIN worklog_member m ON m.id = em.member_id
		 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
		 WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?`, teamID, from, to).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

// syncTeamDay 整班单日对账（卡片驱动：当日卡上有人即同步——人已调班也经人级解析找到其账号；
// onlyMemberIDs 非空仅同步这些成员）。人级解析直连优先、user_id 兜底，按卡上成员去重；
// 成员间 sleepInterval 防风控；每处理完一名成员回调一次（成败均计一步，失败带原因）。
func (m *Module) syncTeamDay(ctx context.Context, teamID int64, date, scope string, onlyMemberIDs []int64, onStep func(errMsg string)) {
	var rows *sql.Rows
	var err error
	if len(onlyMemberIDs) > 0 {
		ph := placeholders(len(onlyMemberIDs))
		args := make([]any, 0, len(onlyMemberIDs)+1)
		args = append(args, teamID)
		for _, id := range onlyMemberIDs {
			args = append(args, id)
		}
		rows, err = m.DB.QueryContext(ctx,
			`SELECT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token, a.device_type, a.system_version,
			        a.token_status, m.id
			 FROM worklog_member m
			 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
			 WHERE m.team_id = ? AND m.id IN (`+ph+`)
			 ORDER BY (a.member_id = m.id) DESC`, args...)
	} else {
		rows, err = m.DB.QueryContext(ctx,
			`SELECT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token, a.device_type, a.system_version,
			        a.token_status, m.id
			 FROM worklog_entry e
			 JOIN worklog_entry_member em ON em.entry_id = e.id
			 JOIN worklog_member m ON m.id = em.member_id
			 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
			 WHERE e.team_id = ? AND e.log_date = ?
			 ORDER BY (a.member_id = m.id) DESC`, teamID, date)
	}
	if err != nil {
		if onStep != nil {
			onStep(err.Error())
		}
		return
	}
	type row struct {
		acc    Account
		cardID int64
	}
	list := make([]row, 0)
	for rows.Next() {
		var it row
		var team, userID, memberID sql.NullInt64
		if rows.Scan(&it.acc.ID, &team, &userID, &memberID, &it.acc.Mobile, &it.acc.Token,
			&it.acc.DeviceType, &it.acc.SystemVersion, &it.acc.TokenStatus, &it.cardID) == nil {
			it.acc.TeamID, it.acc.UserID = team.Int64, userID.Int64
			if memberID.Valid {
				it.acc.MemberID = memberID.Int64
			}
			it.acc.CardMemberID = it.cardID
			list = append(list, it)
		}
	}
	rows.Close()
	seen := map[int64]bool{}
	for _, it := range list {
		if seen[it.cardID] {
			continue // 人级解析理论可命中多行（直连+兜底），按卡上成员去重
		}
		seen[it.cardID] = true
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					if onStep != nil {
						onStep(fmt.Sprintf("%v", rec))
					}
				}
			}()
			if err := m.SyncOne(ctx, &it.acc, date, scope, teamID); err != nil {
				if onStep != nil {
					onStep(err.Error())
				}
			} else if onStep != nil {
				onStep("")
			}
		}()
		m.sleepInterval(ctx)
	}
}

// syncProgress GET /sync/progress?job_id=：手动拉取进度轮询（仅本班组任务可查）。
// Node 口径：任务不存在 / 已过期 / 非本班按 200 空完成态回（端侧据此收尾）。
// failed/failMsg：已失败成员步数与首条失败提示（友好口径）。
func (m *Module) syncProgress(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	jobID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("job_id")), 10, 64)
	job, found := jobs.Get(jobID)
	if !found || job.TeamID != team.ID {
		writeOK(w, map[string]any{
			"total": 0, "done": 0, "finished": true, "failed": 0, "errors": 0, "failMsg": "",
		})
		return
	}
	writeOK(w, job)
}

// syncActive GET /sync/active：本班组同步进行态。
// batch = 区段批量拉取（出工日志全局锁）；daily = 每日定时核查（仅当日本班参与卡片锁定）；
// ops = 进行中的照片单操作任务；batchDone = 近 2 分钟内完成的批量任务终态。
// jobs 为网页端旧键（进行中任务列表），保留兼容。
func (m *Module) syncActive(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	b := jobs.ActiveByKind(team.ID, "batch")
	d := jobs.ActiveByKind(team.ID, "daily")
	rb := jobs.RecentFinished(team.ID, "batch", 2*time.Minute)
	out := map[string]any{
		"running": b != nil,
		"total":   0, "done": 0, "failed": 0,
		"batchDone": nil,
		"daily":     map[string]any{"running": false},
		"jobs":      jobs.Active(),
	}
	if b != nil {
		out["total"], out["done"], out["failed"] = b.Total, b.Done, b.Failed
	}
	if rb != nil {
		out["batchDone"] = map[string]any{
			"total": rb.Total, "done": rb.Done, "failed": rb.Failed, "failMsg": rb.FailMsg,
		}
	}
	if d != nil && d.Date == TodayCN() { // 仅当日核查参与卡片锁定
		out["daily"] = map[string]any{"running": true, "total": d.Total, "done": d.Done}
	}
	ops := make([]map[string]any, 0)
	for _, op := range m.Ops.GetActive() {
		if op.TeamID == team.ID {
			ops = append(ops, opView(op))
		}
	}
	out["ops"] = ops
	writeOK(w, out)
}

// syncLogs GET /sync/logs：核查流水。
// 过滤口径并存：?month=YYYY-MM（按月、近 100 条）、?from=&to=（按区段、近 200 条）、
// 既有 ?date=&member_id=&scope=&type=&result=（网页端查询，近 200 条）。
func (m *Module) syncLogs(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	q := r.URL.Query()
	where := []string{"l.team_id = ?"}
	args := []any{team.ID}
	limit := 200
	from, to := strings.TrimSpace(q.Get("from")), strings.TrimSpace(q.Get("to"))
	month := strings.TrimSpace(q.Get("month"))
	switch {
	case validDate(from) && validDate(to) && from <= to:
		where = append(where, "l.sync_date BETWEEN ? AND ?")
		args = append(args, from, to)
		limit = 200
	case len(month) == 7 && month[4] == '-':
		where = append(where, "DATE_FORMAT(l.sync_date, '%Y-%m') = ?")
		args = append(args, month)
		limit = 100
	}
	if v := q.Get("date"); v != "" {
		where = append(where, "DATE_FORMAT(l.sync_date, '%Y-%m-%d') = ?")
		args = append(args, v)
	}
	if v := q.Get("member_id"); v != "" {
		where = append(where, "l.member_id = ?")
		args = append(args, v)
	}
	if v := q.Get("scope"); v != "" {
		where = append(where, "l.scope = ?")
		args = append(args, v)
	}
	if v := q.Get("type"); v != "" {
		where = append(where, "l.type = ?")
		args = append(args, v)
	}
	if v := q.Get("result"); v != "" {
		where = append(where, "l.result = ?")
		args = append(args, v)
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT l.id, l.team_id, COALESCE(l.member_id, 0), COALESCE(m.name, ''),
		        DATE_FORMAT(l.sync_date, '%Y-%m-%d'), l.scope, l.type, l.result, l.detail,
		        COALESCE(DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i'), '')
		 FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
		 WHERE `+strings.Join(where, " AND ")+` ORDER BY l.id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := make([]SyncLogRow, 0)
	for rows.Next() {
		var row SyncLogRow
		if err := rows.Scan(&row.ID, &row.TeamID, &row.MemberID, &row.MemberName,
			&row.SyncDate, &row.Scope, &row.Type, &row.Result, &row.Detail, &row.CreatedAt); err == nil {
			list = append(list, row)
		}
	}
	writeOK(w, map[string]any{"list": list})
}

// syncLogsClear DELETE /sync/logs：清理核查流水（管理端）。
func (m *Module) syncLogsClear(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if !requireDictAdmin(w, ident, team) {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM worklog_sync_log WHERE team_id = ?`, team.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// jobs 全局进度注册表（模块级共享，供 sync/pull 与每日核查共用）。
var jobs = NewSyncJobRegistry()
