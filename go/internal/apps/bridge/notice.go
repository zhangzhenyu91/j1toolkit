// 站内通知与微信外发真实适配：站内写 sys_notice，微信直调 WeChatPadPro（逐接收方单发，群消息真实 @）。
// treehazard.SyncNotifier / crossing.Notifier 走 RoleNotifier（超管 + 本班班管，异常才外发班组群）；
// worklog.Notifier 走 WorklogNotifier（照片人工审核：站内通知班管 + 群消息 @班管）。
// 外发失败不阻断主流程（站内通知已写入即可，微信失败仅记日志/返回）。
package bridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"shade/internal/apps/wechatanswer"
)

// NoticePusher 站内通知写入最小面（可注入；SQLNoticeWriter 为真实现）。
type NoticePusher interface {
	// PushNotice 写入一条站内通知；targets 为角色数组（可空）、userIds 为按人投放（可空），
	// 两者至少其一非空才有接收人。
	PushNotice(ctx context.Context, targets []string, userIds []int64, title, content string) error
}

// SQLNoticeWriter NoticePusher 真实现（sys_notice 表，与 server/src/notice/index.js push 同口径）。
type SQLNoticeWriter struct{ DB *sql.DB }

// PushNotice 写入站内通知（created_by 留空=系统）。
func (w SQLNoticeWriter) PushNotice(ctx context.Context, targets []string, userIds []int64, title, content string) error {
	if w.DB == nil {
		return errors.New("通知写入未接入（DB 未注入）")
	}
	roles := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for _, r := range targets {
		if r != "" && !seen[r] {
			seen[r] = true
			roles = append(roles, r)
		}
	}
	ids := make([]int64, 0, len(userIds))
	seenID := map[int64]bool{}
	for _, id := range userIds {
		if id > 0 && !seenID[id] {
			seenID[id] = true
			ids = append(ids, id)
		}
	}
	if len(roles) == 0 && len(ids) == 0 {
		return nil // 无接收人：不写空通知
	}
	rolesJSON := "[]"
	if len(roles) > 0 {
		rolesJSON = "[" + joinQuoted(roles) + "]"
	}
	var idsArg any // JSON 数组字面量或 NULL（无按人投放）
	if len(ids) > 0 {
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, fmt.Sprintf("%d", id))
		}
		idsArg = "[" + strings.Join(parts, ",") + "]"
	}
	_, err := w.DB.ExecContext(ctx,
		`INSERT INTO sys_notice (title, content, targets, user_ids, created_by) VALUES (?, ?, ?, ?, NULL)`,
		title, content, rolesJSON, idsArg)
	return err
}

// joinQuoted JSON 字符串数组字面量元素（引号内转义）。
func joinQuoted(items []string) string {
	parts := make([]string, 0, len(items))
	for _, s := range items {
		parts = append(parts, fmt.Sprintf("%q", s))
	}
	return strings.Join(parts, ",")
}

// Mention 微信群 @ 对象（wxid + 昵称）。
type Mention struct {
	Wxid     string
	Nickname string
}

// WxidResolver 微信接收方解析（SQLWxidResolver 真实现；测试注入 fake）。
type WxidResolver interface {
	// Wxids 解析接收方 wxid（用户个人 + 班组群，限启用且已设 wxid，去重保序）。
	Wxids(ctx context.Context, userIds, teamIds []int64) ([]string, error)
	// Mentions 解析被@人（限正常且 wxid/昵称均已设置，按 wxid 去重）。
	Mentions(ctx context.Context, atUserIds []int64) ([]Mention, error)
}

// SQLWxidResolver WxidResolver 真实现（sys_user / sys_team，与 wxpush.js resolveWxids/resolveMentions 同口径）。
type SQLWxidResolver struct{ DB *sql.DB }

// Wxids 解析接收方 wxid。
func (r SQLWxidResolver) Wxids(ctx context.Context, userIds, teamIds []int64) ([]string, error) {
	if r.DB == nil {
		return nil, errors.New("微信外发未接入（DB 未注入）")
	}
	var out []string
	seen := map[string]bool{}
	add := func(wxid string) {
		if wxid != "" && !seen[wxid] {
			seen[wxid] = true
			out = append(out, wxid)
		}
	}
	if len(userIds) > 0 {
		rows, err := r.DB.QueryContext(ctx,
			`SELECT wxid FROM sys_user WHERE id IN (`+idPlaceholders(userIds)+`) AND status = 1 AND wxid != ''`,
			idArgs(userIds)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var w string
			if rows.Scan(&w) == nil {
				add(w)
			}
		}
		rows.Close()
	}
	if len(teamIds) > 0 {
		rows, err := r.DB.QueryContext(ctx,
			`SELECT wxid FROM sys_team WHERE id IN (`+idPlaceholders(teamIds)+`) AND status = 1 AND wxid != ''`,
			idArgs(teamIds)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var w string
			if rows.Scan(&w) == nil {
				add(w)
			}
		}
		rows.Close()
	}
	return out, nil
}

// Mentions 解析被@人。
func (r SQLWxidResolver) Mentions(ctx context.Context, atUserIds []int64) ([]Mention, error) {
	if r.DB == nil {
		return nil, errors.New("微信外发未接入（DB 未注入）")
	}
	if len(atUserIds) == 0 {
		return nil, nil
	}
	rows, err := r.DB.QueryContext(ctx,
		`SELECT wxid, nickname FROM sys_user WHERE id IN (`+idPlaceholders(atUserIds)+`)
		 AND status = 1 AND wxid != '' AND nickname != ''`, idArgs(atUserIds)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mention
	seen := map[string]bool{}
	for rows.Next() {
		var m Mention
		if rows.Scan(&m.Wxid, &m.Nickname) == nil && !seen[m.Wxid] {
			seen[m.Wxid] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// idPlaceholders IN 子句占位符。
func idPlaceholders(ids []int64) string {
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
}

// idArgs IN 子句参数。
func idArgs(ids []int64) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

// WxPusher 微信外发最小面（PadProWxPusher 真实现；nil 表示未配置微信外发，站内通知照常）。
type WxPusher interface {
	// Send 逐接收方发送文本；返回成功条数（失败不抛到业务主流程，聚合 error 供记日志）。
	Send(ctx context.Context, userIds, teamIds []int64, text string, atUserIds []int64) (int, error)
}

// PadProWxPusher 直接调用 WeChatPadPro /v1/Msg/SendTxt；群消息用 At 实现真实 @。
type PadProWxPusher struct {
	Client   *wechatanswer.PadProClient
	Resolver WxidResolver
}

// NewPadProWxPusher 构造微信外发（client 与智能应答共用连接配置；resolver 通常 SQLWxidResolver{DB}）。
func NewPadProWxPusher(client *wechatanswer.PadProClient, resolver WxidResolver) *PadProWxPusher {
	return &PadProWxPusher{Client: client, Resolver: resolver}
}

// Send 逐接收方发送（解析 wxid → 群 @ 前缀与 At 列表 → 逐个调用）。
func (p *PadProWxPusher) Send(ctx context.Context, userIds, teamIds []int64, text string, atUserIds []int64) (int, error) {
	if p == nil || p.Client == nil || p.Resolver == nil {
		return 0, errors.New("微信外发未接入")
	}
	wxids, err := p.Resolver.Wxids(ctx, userIds, teamIds)
	if err != nil {
		return 0, err
	}
	if len(wxids) == 0 {
		return 0, errors.New("接收方均未设置 wxid")
	}
	mentions, err := p.Resolver.Mentions(ctx, atUserIds)
	if err != nil {
		return 0, err
	}
	atPrefix := ""
	ns := make([]string, 0, len(mentions))
	for _, m := range mentions {
		atPrefix += "@" + m.Nickname + " "
		ns = append(ns, m.Wxid)
	}
	atStr := strings.Join(ns, ",")
	sent := 0
	var errs []string
	for _, wxid := range wxids {
		body := text
		at := ""
		if strings.Contains(wxid, "@chatroom") {
			if atPrefix != "" {
				body = atPrefix + "\n" + text
			}
			at = atStr
		}
		if err := p.Client.SendTextAt(ctx, wxid, body, at); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", wxid, err))
			continue
		}
		sent++
	}
	if len(errs) > 0 {
		return sent, errors.New(strings.Join(errs, "；"))
	}
	return sent, nil
}

// ---- treehazard.SyncNotifier / crossing.Notifier ----

// RoleNotifier 树障／交跨通知：站内投放超管 + 本班班管；成功仅站内，异常才发班组群。
type RoleNotifier struct {
	Notice NoticePusher
	// Recipients 按班组解析站内接收人（超管 + 本班班管）；nil 时兼容旧角色投放。
	Recipients func(ctx context.Context, teamIDs []int64) ([]int64, error)
	// Wx 微信外发（可 nil：仅站内通知）。
	Wx WxPusher
	// WxTeams 班组 id 供给（可 nil：按超管通知且不外发）；tree/crossing 装配时绑定同步班组。
	WxTeams func(ctx context.Context) []int64
}

// stagingNoticeTitles 直推暂存通知标题（仅站内，不发微信群）。
var stagingNoticeTitles = map[string]bool{
	"树障记录进暂存区": true,
	"交跨记录进暂存区": true,
}

// Push 写站内通知；标题含失败／异常时才追加微信外发，避免成功日报刷屏。
func (n *RoleNotifier) Push(ctx context.Context, targets []string, title, content string) error {
	if n == nil || n.Notice == nil {
		return errors.New("站内通知未接入")
	}
	var teams []int64
	if n.WxTeams != nil {
		teams = n.WxTeams(ctx)
	}
	if n.Recipients != nil {
		users, err := n.Recipients(ctx, teams)
		if err != nil {
			return err
		}
		if err := n.Notice.PushNotice(ctx, nil, users, title, content); err != nil {
			return err
		}
	} else if err := n.Notice.PushNotice(ctx, targets, nil, title, content); err != nil {
		return err
	}
	abnormal := strings.Contains(title, "失败") || strings.Contains(title, "异常")
	if abnormal && n.Wx != nil && len(teams) > 0 && !stagingNoticeTitles[title] {
		_, _ = n.Wx.Send(ctx, nil, teams, title+"\n"+content, nil)
	}
	return nil
}

// ---- worklog.Notifier ----

// ReviewInfo 照片人工审核通知所需上下文（同 photoverify.js notifyManualReview 查询列）。
type ReviewInfo struct {
	TeamID  int64
	LogDate string
	PlateNo string
	Members []string
}

// ReviewSource 照片上下文查询（SQLReviewSource 真实现；测试注入 fake）。
type ReviewSource interface {
	// PhotoReviewInfo 照片上下文；照片已删除返回 (nil, nil)。
	PhotoReviewInfo(ctx context.Context, photoID int64) (*ReviewInfo, error)
	// TeamAdminIDs 本班启用班组管理员 id。
	TeamAdminIDs(ctx context.Context, teamID int64) ([]int64, error)
}

// SQLReviewSource ReviewSource 真实现（worklog_photo / worklog_entry / worklog_vehicle / sys_user）。
type SQLReviewSource struct{ DB *sql.DB }

// PhotoReviewInfo 照片上下文查询（photoverify.js notifyManualReview 同口径）。
func (s SQLReviewSource) PhotoReviewInfo(ctx context.Context, photoID int64) (*ReviewInfo, error) {
	if s.DB == nil {
		return nil, errors.New("出工日志库未接入")
	}
	var info ReviewInfo
	var membersJSON sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT e.team_id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(v.plate_no, ''), COALESCE(p.members, '')
		 FROM worklog_photo p
		 JOIN worklog_entry e ON e.id = p.entry_id
		 LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
		 WHERE p.id = ?`, photoID).Scan(&info.TeamID, &info.LogDate, &info.PlateNo, &membersJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info.Members = parseNameList(membersJSON.String)
	return &info, nil
}

// TeamAdminIDs 本班启用班组管理员。
func (s SQLReviewSource) TeamAdminIDs(ctx context.Context, teamID int64) ([]int64, error) {
	if s.DB == nil {
		return nil, errors.New("出工日志库未接入")
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id = ? AND status = 1`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// parseNameList members JSON 数组字符串 → 名单（非数组/空归空）。
func parseNameList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// 容错解析 ["a","b"]；失败按整体单名处理不抛错。
	raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	var out []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.Trim(strings.TrimSpace(part), `"'`)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// reviewMaxRetry 重试上限（与 worklog.maxVerifyRetry / photoverify.js MAX_RETRY 同值，通知文案用）。
const reviewMaxRetry = 3

// WorklogNotifier worklog.Notifier 真实现：水印照片需人工审核 → 站内通知本班班组管理员
// + 班组微信群消息 @班管（photoverify.js notifyManualReview 同口径；通知失败返回 error，
// 调用方 worklog 照片流水线忽略不阻断）。
type WorklogNotifier struct {
	Src    ReviewSource
	Notice NoticePusher
	// Wx 微信外发（可 nil：仅站内通知）。
	Wx WxPusher
}

// NotifyManualReview 组装并发送人工审核通知。
func (n *WorklogNotifier) NotifyManualReview(ctx context.Context, photoID int64) error {
	if n == nil || n.Src == nil || n.Notice == nil {
		return errors.New("人工审核通知未接入")
	}
	info, err := n.Src.PhotoReviewInfo(ctx, photoID)
	if err != nil {
		return err
	}
	if info == nil {
		return nil // 照片已删除
	}
	admins, err := n.Src.TeamAdminIDs(ctx, info.TeamID)
	if err != nil {
		return err
	}
	names := strings.Join(info.Members, "、")
	plate := info.PlateNo
	if plate == "" {
		plate = "未出车"
	}
	memberSeg := ""
	if names != "" {
		memberSeg = "（" + names + "）"
	}
	title := "水印照片需人工审核"
	content := fmt.Sprintf("%s %s 卡片%s的水印照片经 %d 次重试仍未识别出施工内容，已标记为验证失败。请在出工日志中找到当日该车辆卡片，点击照片「验证失败」手动修正施工内容。",
		info.LogDate, plate, memberSeg, reviewMaxRetry)
	if err := n.Notice.PushNotice(ctx, nil, admins, title, content); err != nil {
		return err
	}
	if n.Wx != nil {
		_, _ = n.Wx.Send(ctx, nil, []int64{info.TeamID}, title+"\n"+content, admins)
	}
	return nil
}

// ---- 装配工厂（main 一行注入）----

// teamIDByName 班组名 → id（RoleNotifier 外发班组群用；未找到返回空）。
func teamIDByName(ctx context.Context, db *sql.DB, name string) []int64 {
	name = strings.TrimSpace(name)
	if db == nil || name == "" {
		return nil
	}
	var id int64
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM sys_team WHERE name = ? AND status = 1`, name).Scan(&id); err != nil {
		return nil
	}
	return []int64{id}
}

// roleRecipientIDs 站内接收人：全部超管 + 指定班组启用班管；空班组仅超管。
func roleRecipientIDs(ctx context.Context, db *sql.DB, teamIDs []int64) ([]int64, error) {
	if db == nil {
		return nil, errors.New("站内通知未接入（DB 未注入）")
	}
	query := `SELECT id FROM sys_user WHERE status = 1 AND (role = 'admin'`
	args := []any{}
	if len(teamIDs) > 0 {
		query += ` OR (role = 'team_admin' AND team_id IN (` + idPlaceholders(teamIDs) + `))`
		args = append(args, idArgs(teamIDs)...)
	}
	query += `) ORDER BY id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NewRoleNotifier 构造树障／交跨通知（超管 + 本班班管；异常才外发配置班组群）。
func NewRoleNotifier(db *sql.DB, cfg ConfigGet, wxTeamCfgKey string) *RoleNotifier {
	n := &RoleNotifier{Notice: SQLNoticeWriter{DB: db}}
	n.Recipients = func(ctx context.Context, teamIDs []int64) ([]int64, error) {
		return roleRecipientIDs(ctx, db, teamIDs)
	}
	if cfg != nil {
		n.Wx = NewPadProWxPusher(NewPadPro(cfg), SQLWxidResolver{DB: db})
		n.WxTeams = func(ctx context.Context) []int64 {
			return teamIDByName(ctx, db, tokenOf(ctx, cfg, wxTeamCfgKey))
		}
	}
	return n
}

// NewWorklogNotifier 构造水印照片人工审核通知（站内班管 + 班组群 @班管）。
func NewWorklogNotifier(db *sql.DB, cfg ConfigGet) *WorklogNotifier {
	n := &WorklogNotifier{
		Src:    SQLReviewSource{DB: db},
		Notice: SQLNoticeWriter{DB: db},
	}
	if cfg != nil {
		n.Wx = NewPadProWxPusher(NewPadPro(cfg), SQLWxidResolver{DB: db})
	}
	return n
}

// 编译期契约。
var (
	_ NoticePusher = SQLNoticeWriter{}
	_ WxidResolver = SQLWxidResolver{}
	_ WxPusher     = (*PadProWxPusher)(nil)
)
