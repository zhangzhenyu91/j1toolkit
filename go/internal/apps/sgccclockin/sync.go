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
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
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
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO worklog_clockin (team_id, member_id, clock_date, seq, detail_id, clock_time,
			   position, longitude, latitude, city_code, city_name, work_hours, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE detail_id = VALUES(detail_id), clock_time = VALUES(clock_time),
			   position = VALUES(position), longitude = VALUES(longitude), latitude = VALUES(latitude),
			   city_code = VALUES(city_code), city_name = VALUES(city_name), work_hours = VALUES(work_hours),
			   source = VALUES(source)`,
			teamID, memberID, date, seq, rec.DetailID, nullIfEmpty(rec.ClockTime),
			rec.Position, rec.Lng, rec.Lat, rec.CityCode, rec.CityName, rec.WorkHours, rec.Source); err != nil {
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

// ParseDayClockins 解析 dayNew 应答中的打卡数组（data.list 或 data）。
func ParseDayClockins(decoded map[string]any) []ClockinRecordLocal {
	if decoded == nil {
		return nil
	}
	data, _ := decoded["data"].([]any)
	if data == nil {
		if d, ok := decoded["data"].(map[string]any); ok {
			data, _ = d["list"].([]any)
		}
	}
	out := make([]ClockinRecordLocal, 0, len(data))
	for _, it := range data {
		row, ok := it.(map[string]any)
		if !ok {
			continue
		}
		rec := ClockinRecordLocal{
			DetailID:  fmt.Sprintf("%v", pick(row, "detailId", "id")),
			ClockTime: FmtCnDateTime(pick(row, "clockInTime", "clockTime")),
			Position:  str(pick(row, "position", "address")),
			Lng:       str(pick(row, "longitude", "lng")),
			Lat:       str(pick(row, "latitude", "lat")),
			CityCode:  str(pick(row, "cityCode")),
			CityName:  str(pick(row, "cityName")),
			WorkHours: str(pick(row, "workHours")),
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

func str(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
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

// SyncOne 单成员某日对账（登录态 + 打卡；scope=daily/remind）：登录态探测 + dayNew 打卡对账回写。
func (m *Module) SyncOne(ctx context.Context, acc *Account, date, scope string, teamID int64) error {
	if !m.probeAuth(ctx, acc) {
		m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "auth", "fail", "登录态失效")
		return fmt.Errorf("登录态失效")
	}
	m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "auth", "ok", "")
	if m.API == nil {
		return nil
	}
	d, err := m.API.DayNew(ctx, acc.Token, date, CallOpts{
		Token: acc.Token, Mobile: acc.Mobile,
		DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
	})
	if err != nil {
		m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "clockin", "fail", truncateStr(err.Error(), 200))
		return err
	}
	if d == nil {
		m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "clockin", "fail", "平台应答不可解")
		return fmt.Errorf("平台应答不可解")
	}
	remote := ParseDayClockins(d)
	// 过滤本成员的记录（dayNew 为本人全量；代打卡场景按 member 名下）
	for i := range remote {
		remote[i].Source = 1
	}
	before := m.clockinSummary(ctx, acc.MemberID, date)
	if err := m.RefreshClockins(ctx, teamID, acc.MemberID, date, remote); err != nil {
		m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "clockin", "fail", truncateStr(err.Error(), 200))
		return err
	}
	after := m.clockinSummary(ctx, acc.MemberID, date)
	result := "ok"
	detail := ""
	if fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after) {
		result = "diff"
		detail = "打卡流水已按商旅对账回写"
	}
	m.AddSyncLog(ctx, teamID, acc.MemberID, date, scope, "clockin", result, detail)
	return nil
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

// dayGet GET /day?member_id=&date=：当日打卡/费用/照片摘要。
func (m *Module) dayGet(w http.ResponseWriter, r *http.Request) {
	memberID := rawNumQuery(r, "member_id")
	date := r.URL.Query().Get("date")
	if date == "" {
		date = TodayCN()
	}
	acc := m.requireMemberAccount(w, r, memberID, date)
	if acc == nil {
		return
	}
	writeOK(w, map[string]any{
		"date":    date,
		"clockin": m.clockinSummary(r.Context(), memberID, date),
		"fee":     m.feeSummary(r.Context(), memberID, date),
	})
}

func rawNumQuery(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
	return n
}

// clockinPost POST /clockin：打卡（seq 1 首次 / 2 结束 / update 更新）。
func (m *Module) clockinPost(w http.ResponseWriter, r *http.Request) {
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
	seq := int(num(rawString(body["seq"])))
	if seq != 1 && seq != 2 && seq != 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	payload := map[string]any{
		"seq":       seq,
		"position":  rawString(body["position"]),
		"longitude": rawString(body["longitude"]),
		"latitude":  rawString(body["latitude"]),
		"cityCode":  rawString(body["city_code"]),
		"cityName":  rawString(body["city_name"]),
		"detailId":  rawString(body["detail_id"]),
		"date":      date,
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
		return
	}
	_, err = m.API.MarkClockin(r.Context(), acc.Token, payload, CallOpts{
		Token: acc.Token, Mobile: acc.Mobile,
		DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	// 打卡成功后用 dayNew 全量刷新该日打卡流水
	if d, err := m.API.DayNew(r.Context(), acc.Token, date, CallOpts{
		Token: acc.Token, Mobile: acc.Mobile,
		DeviceType: acc.DeviceType, SystemVersion: acc.SystemVersion,
	}); err == nil && d != nil {
		_ = m.RefreshClockins(r.Context(), acc.TeamID, memberID, date, ParseDayClockins(d))
	}
	writeOK(w, map[string]any{"clockin": m.clockinSummary(r.Context(), memberID, date)})
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
	if m.API != nil {
		d, derr := m.API.GetFeeInfoNew(ctx, acc.Token, m.feeTplParams(ctx, memberID, date), devOpt(acc))
		if derr == nil {
			if tpl := ExtractClockTemplate(d); tpl != nil {
				out["clockTemplate"] = tpl
				out["costAlloc"] = feeCostAlloc(tpl, local)
			}
		}
	}
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
func (m *Module) opStatus(w http.ResponseWriter, r *http.Request) {
	_, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	opID, _ := strconv.ParseInt(r.URL.Query().Get("op_id"), 10, 64)
	op, found := m.Ops.Status(opID)
	if !found {
		fail(w, http.StatusNotFound, codeMissing, "任务不存在")
		return
	}
	writeOK(w, op)
}

// SyncJob 同步进度任务（kind=daily/pull）。
type SyncJob struct {
	ID       int64  `json:"id"`
	TeamID   int64  `json:"team_id"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Errors   int    `json:"errors"`
	Kind     string `json:"kind"`
	Date     string `json:"date"`
	Finished bool   `json:"finished"`
}

// SyncJobRegistry 进度任务注册表。
type SyncJobRegistry struct {
	mu   sync.Mutex
	seq  int64
	jobs map[int64]*SyncJob
}

// NewSyncJobRegistry 构造注册表。
func NewSyncJobRegistry() *SyncJobRegistry {
	return &SyncJobRegistry{jobs: map[int64]*SyncJob{}}
}

// New 登记任务。
func (reg *SyncJobRegistry) New(teamID int64, total int, kind, date string) int64 {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.seq++
	reg.jobs[reg.seq] = &SyncJob{ID: reg.seq, TeamID: teamID, Total: total, Kind: kind, Date: date}
	return reg.seq
}

// Step 完成一步（errMsg 非空计失败）。
func (reg *SyncJobRegistry) Step(id int64, errMsg string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if job, ok := reg.jobs[id]; ok {
		job.Done++
		if errMsg != "" {
			job.Errors++
		}
	}
}

// Finish 收尾。
func (reg *SyncJobRegistry) Finish(id int64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if job, ok := reg.jobs[id]; ok {
		job.Finished = true
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

// syncPull POST /sync/pull {member_id?, date}：手动从商旅拉取（单成员或本班当日卡片成员）。
func (m *Module) syncPull(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	date := rawString(body["date"])
	if date == "" {
		date = TodayCN()
	}
	memberID := rawNum(body["member_id"])
	acc := m.requireMemberAccount(w, r, memberID, date)
	if acc == nil {
		return
	}
	jobID := jobs.New(team.ID, 1, "pull", date)
	go func() {
		defer jobs.Finish(jobID)
		if err := m.SyncOne(context.Background(), acc, date, "daily", team.ID); err != nil {
			jobs.Step(jobID, err.Error())
			return
		}
		jobs.Step(jobID, "")
	}()
	writeOK(w, map[string]any{"jobId": jobID}, "已发起同步")
}

// syncProgress GET /sync/progress?job_id=：同步任务进度。
func (m *Module) syncProgress(w http.ResponseWriter, r *http.Request) {
	_, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	jobID, _ := strconv.ParseInt(r.URL.Query().Get("job_id"), 10, 64)
	job, found := jobs.Get(jobID)
	if !found {
		fail(w, http.StatusNotFound, codeMissing, "任务不存在")
		return
	}
	writeOK(w, job)
}

// syncActive GET /sync/active：进行中的同步任务与照片任务。
func (m *Module) syncActive(w http.ResponseWriter, r *http.Request) {
	_, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	writeOK(w, map[string]any{
		"jobs": jobs.Active(),
		"ops":  m.Ops.GetActive(),
	})
}

// syncLogs GET /sync/logs?date=&member_id=&scope=&type=&result=：核查流水。
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
		        DATE_FORMAT(l.sync_date, '%Y-%m-%d'), l.scope, l.type, l.result, l.detail
		 FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
		 WHERE `+strings.Join(where, " AND ")+` ORDER BY l.id DESC LIMIT 200`, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := make([]SyncLogRow, 0)
	for rows.Next() {
		var row SyncLogRow
		if err := rows.Scan(&row.ID, &row.TeamID, &row.MemberID, &row.MemberName,
			&row.SyncDate, &row.Scope, &row.Type, &row.Result, &row.Detail); err == nil {
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
