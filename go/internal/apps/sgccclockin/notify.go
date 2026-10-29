// 通知实现（Notifier）：站内 + 微信双渠道，发送函数可注入（不内置密钥、不直连真实服务）；
// 微信侧签名与 wechatanswer.MessageSender.SendText 一致，main 可直接把 Sender 方法赋给 Wx 字段。
// 收件人与 wxid 从库解析（口径对照 server/src/sgccclockin/index.js 的 notify* 与 notice/wxpush.js）；
// 群内 @ 以「@昵称 」前缀折进正文（SendText 无 at 参数，通道侧不解析 at）；通知失败不抛断主流程。
package sgccclockin

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// InAppSendFunc 站内通知写入（userIDs 按人投放、roles 角色投放，两者至少其一非空）。
// main 可接 notice/sys_notice 写入实现；nil 时跳过站内渠道。
type InAppSendFunc func(ctx context.Context, userIDs []int64, roles []string, title, content string) error

// WxSendFunc 微信文本发送（target 为个人 wxid 或群 wxid 原文）。
type WxSendFunc func(ctx context.Context, target, text string) error

// WxAtSendFunc 微信群文本发送，at 为真实 @ 的 wxid 逗号分隔列表。
type WxAtSendFunc func(ctx context.Context, target, text, at string) error

// TextSender SendText 单方法接口（wechatanswer.MessageSender 天然满足）。
type TextSender interface {
	SendText(ctx context.Context, target, text string) error
}

// WxSendFrom 把 SendText 适配为 WxSendFunc（main 接 wechatanswer.Sender 用）。
func WxSendFrom(s TextSender) WxSendFunc {
	return func(ctx context.Context, target, text string) error {
		return s.SendText(ctx, target, text)
	}
}

// PushNotifier 商旅通知实现：收件人/wxid 从库解析，站内/微信发送函数注入；渠道函数 nil 时跳过该渠道。
type PushNotifier struct {
	DB *sql.DB
	// InApp 站内通知写入（nil 跳过站内）。
	InApp InAppSendFunc
	// Wx 微信文本发送（nil 跳过微信）。
	Wx WxSendFunc
	// WxAt 微信群真实 @ 发送（可选；nil 时回退 Wx 的文本 @ 前缀）。
	WxAt WxAtSendFunc
	// ProxyURL 出口代理地址取值（代理异常通知正文展示脱敏地址用；nil 时正文省略地址段）。
	ProxyURL func(ctx context.Context) string
}

// 编译期确认满足 Notifier。
var _ Notifier = (*PushNotifier)(nil)

// wxTarget 微信收件人（用户个人 wxid + 昵称）。
type wxTarget struct {
	Wxid     string
	Nickname string
}

// uniqIDs 去重保序（>0 才保留）。
func uniqIDs(ids ...int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// adminIDs 全部启用超管。
func (p *PushNotifier) adminIDs(ctx context.Context) []int64 {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT id FROM sys_user WHERE role = 'admin' AND status = 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// teamAdminIDs 指定班组的启用班组管理员（多班并集）。
func (p *PushNotifier) teamAdminIDs(ctx context.Context, teamIDs []int64) []int64 {
	if len(teamIDs) == 0 {
		return nil
	}
	args := make([]any, len(teamIDs))
	for i, id := range teamIDs {
		args[i] = id
	}
	rows, err := p.DB.QueryContext(ctx,
		`SELECT id FROM sys_user WHERE role = 'team_admin' AND team_id IN (`+placeholders(len(teamIDs))+`) AND status = 1`,
		args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// wxUsers 解析用户个人 wxid（限启用且已设 wxid 者；@ 提及用昵称）。
func (p *PushNotifier) wxUsers(ctx context.Context, userIDs []int64) map[int64]wxTarget {
	out := map[int64]wxTarget{}
	if len(userIDs) == 0 {
		return out
	}
	args := make([]any, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
	}
	rows, err := p.DB.QueryContext(ctx,
		`SELECT id, wxid, nickname FROM sys_user
		 WHERE id IN (`+placeholders(len(userIDs))+`) AND status = 1 AND wxid <> ''`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	wanted := map[int64]bool{}
	for _, id := range userIDs {
		wanted[id] = true
	}
	for rows.Next() {
		var id int64
		var t wxTarget
		if rows.Scan(&id, &t.Wxid, &t.Nickname) == nil && wanted[id] {
			out[id] = t
		}
	}
	return out
}

// teamWx 班组群 wxid（限启用且已设 wxid 者）。
func (p *PushNotifier) teamWx(ctx context.Context, teamID int64) string {
	if teamID <= 0 {
		return ""
	}
	var wxid string
	err := p.DB.QueryRowContext(ctx,
		`SELECT wxid FROM sys_team WHERE id = ? AND status = 1 AND wxid <> ''`, teamID).Scan(&wxid)
	if err != nil {
		return ""
	}
	return wxid
}

// inApp 站内渠道（注入为空或无收件人时跳过）。
func (p *PushNotifier) inApp(ctx context.Context, userIDs []int64, title, content string) error {
	if p.InApp == nil || len(userIDs) == 0 {
		return nil
	}
	return p.InApp(ctx, userIDs, nil, title, content)
}

// wxPersonal 微信个人渠道：逐收件人各发一条。
func (p *PushNotifier) wxPersonal(ctx context.Context, userIDs []int64, text string) error {
	if p.Wx == nil {
		return nil
	}
	var errs []error
	for _, t := range p.wxUsers(ctx, userIDs) {
		if err := p.Wx(ctx, t.Wxid, text); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// wxGroup 微信班组群渠道：合并一条发送；优先以 At 真实 @，兼容旧文本 @ 前缀。
func (p *PushNotifier) wxGroup(ctx context.Context, teamID int64, text string, atUserIDs []int64) error {
	if p.Wx == nil && p.WxAt == nil {
		return nil
	}
	target := p.teamWx(ctx, teamID)
	if target == "" {
		return nil
	}
	prefix := ""
	atIDs := []string{}
	for _, id := range atUserIDs {
		if t, ok := p.wxUsers(ctx, []int64{id})[id]; ok && t.Nickname != "" {
			prefix += "@" + t.Nickname + " "
			atIDs = append(atIDs, t.Wxid)
		}
	}
	if prefix != "" {
		text = prefix + "\n" + text
	}
	if p.WxAt != nil {
		return p.WxAt(ctx, target, text, strings.Join(atIDs, ","))
	}
	return p.Wx(ctx, target, text)
}

// maskProxyURL 出口代理地址脱敏（//user:pass@host → //***@host，同 Node wxpush/通知口径）。
func maskProxyURL(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	if at := strings.Index(rest, "@"); at >= 0 {
		return raw[:i+3] + "***@" + rest[at+1:]
	}
	return raw
}

// NotifyTokenExpired 登录过期：站内 + 微信投放本人与全部超管（微信另发所属班组群并@本人）。
func (p *PushNotifier) NotifyTokenExpired(ctx context.Context, account Account) error {
	name := ""
	wxUserID := account.UserID
	if account.MemberID > 0 {
		var memberUser sql.NullInt64
		if err := p.DB.QueryRowContext(ctx,
			`SELECT name, user_id FROM worklog_member WHERE id = ?`, account.MemberID,
		).Scan(&name, &memberUser); err == nil {
			if wxUserID <= 0 {
				wxUserID = memberUser.Int64
			}
		}
	}
	if name == "" && account.UserID > 0 {
		_ = p.DB.QueryRowContext(ctx,
			`SELECT nickname FROM sys_user WHERE id = ?`, account.UserID).Scan(&name)
	}
	var wxTeamID int64
	if wxUserID > 0 {
		_ = p.DB.QueryRowContext(ctx,
			`SELECT team_id FROM sys_user WHERE id = ?`, wxUserID).Scan(&wxTeamID)
	}
	userIDs := uniqIDs(append([]int64{wxUserID}, p.adminIDs(ctx)...)...)
	if len(userIDs) == 0 {
		return nil
	}
	title := "商旅登录已过期"
	content := "成员「" + orUnknown(name) + "」商旅登录已过期，打卡/费用/照片同步已暂停，请尽快在「我的 → 商旅打卡」重新登录。"
	errs := []error{p.inApp(ctx, userIDs, title, content)}
	text := title + "\n" + content
	if wxUserID > 0 {
		errs = append(errs, p.wxPersonal(ctx, []int64{wxUserID}, text))
		errs = append(errs, p.wxGroup(ctx, wxTeamID, text, []int64{wxUserID}))
	}
	return errors.Join(errs...)
}

// orUnknown 姓名缺省占位（同 Node「未知」）。
func orUnknown(name string) string {
	if strings.TrimSpace(name) == "" {
		return "未知"
	}
	return name
}

// NotifyMissingEndClockin 每日核查附加：开始卡已打、结束卡未打 → 站内+微信本人/班管/超管，
// 打卡班组群合并一条并@缺卡成员。
func (p *PushNotifier) NotifyMissingEndClockin(ctx context.Context, teamID int64, date string, rows []MissingClockinRow) error {
	if len(rows) == 0 {
		return nil
	}
	_ = date
	managers := uniqIDs(append(p.adminIDs(ctx), p.teamAdminIDs(ctx, []int64{teamID})...)...)
	title := "结束打卡未打"
	var errs []error
	atUsers := []int64{}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		content := "成员「" + r.Name + "」今日" + startClause(r.StartHM, "已打开始卡") +
			"，尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）。"
		errs = append(errs, p.inApp(ctx, uniqIDs(append([]int64{r.UserID}, managers...)...), title, content))
		if r.UserID > 0 {
			errs = append(errs, p.wxPersonal(ctx, []int64{r.UserID}, title+"\n"+content))
			atUsers = append(atUsers, r.UserID)
		}
		lines = append(lines, "· "+r.Name+endLineSuffix(r.StartHM))
	}
	groupText := title + "\n今日以下 " + strconv.Itoa(len(rows)) + " 名成员已打开始卡、尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）：\n" +
		strings.Join(lines, "\n")
	errs = append(errs, p.wxGroup(ctx, teamID, groupText, atUsers))
	return errors.Join(errs...)
}

// startClause 「{hh:mm} 已打开始卡」/「已打开始卡」（空值口径同 Node）。
func startClause(startHM, fallback string) string {
	if startHM == "" {
		return fallback
	}
	return startHM + " " + fallback
}

// endLineSuffix 群消息行尾：已打开始卡时刻展示（空值省略）。
func endLineSuffix(startHM string) string {
	if startHM == "" {
		return ""
	}
	return "（" + startHM + " 已打开始卡）"
}

// NotifyDailySyncResult 核查日报（每班一封，仅超管：成功仅站内，有差异或失败才发微信个人）。
func (p *PushNotifier) NotifyDailySyncResult(ctx context.Context, teamID int64, date string, summary SyncSummary) error {
	_ = teamID
	_ = date
	admins := p.adminIDs(ctx)
	if len(admins) == 0 {
		return nil
	}
	teamName := summary.TeamName
	if teamName == "" {
		teamName = "未知班组"
	}
	lines := []string{}
	if summary.OK+summary.Diff+summary.Fail == 0 {
		lines = append(lines, "今日无卡上绑定成员，未执行数据核查（仅做登录态探测）。")
	} else {
		lines = append(lines, "今日核查：一致 "+strconv.Itoa(summary.OK)+" / 回写 "+strconv.Itoa(summary.Diff)+" / 失败 "+strconv.Itoa(summary.Fail))
		if len(summary.Fails) > 0 {
			lines = append(lines, "", "失败明细：")
			typeNames := map[string]string{"clockin": "打卡", "fee": "费用", "photo": "照片", "auth": "登录态"}
			for _, f := range summary.Fails {
				name := f.MemberName
				if name == "" {
					name = "成员" + strconv.FormatInt(f.MemberID, 10)
				}
				tn := typeNames[f.Type]
				if tn == "" {
					tn = f.Type
				}
				lines = append(lines, "- "+name+"｜"+tn+"："+f.Detail)
			}
		}
	}
	title := "商旅每日核查完成（" + teamName + "）"
	content := strings.Join(lines, "\n")
	errs := []error{p.inApp(ctx, admins, title, content)}
	if summary.Diff > 0 || summary.Fail > 0 {
		errs = append(errs, p.wxPersonal(ctx, admins, title+"\n"+content))
	}
	return errors.Join(errs...)
}

// NotifyRemind 提醒消息（kind=start/end）：仅微信班组群，每班合并一条并@缺卡成员，不再个人私聊。
func (p *PushNotifier) NotifyRemind(ctx context.Context, kind string, rows []MissingClockinRow) error {
	if len(rows) == 0 {
		return nil
	}
	groups := map[int64][]MissingClockinRow{}
	for _, r := range rows {
		if r.TeamID > 0 {
			groups[r.TeamID] = append(groups[r.TeamID], r)
		}
	}
	var errs []error
	for teamID, list := range groups {
		lines := make([]string, 0, len(list))
		atUsers := []int64{}
		for _, r := range list {
			if kind == "start" {
				lines = append(lines, "· "+r.Name)
			} else {
				lines = append(lines, "· "+r.Name+endLineSuffix(r.StartHM))
			}
			if r.UserID > 0 {
				atUsers = append(atUsers, r.UserID)
			}
		}
		groupTitle := "结束打卡提醒"
		groupLead := "今日以下 " + strconv.Itoa(len(list)) + " 名成员已打开始卡、尚未打结束卡，请提醒本人尽快补打（仅当日可打卡）："
		if kind == "start" {
			groupTitle = "开始打卡提醒"
			groupLead = "今日以下 " + strconv.Itoa(len(list)) + " 名成员有出工卡片、尚未打开始卡（首次打卡），请提醒本人尽快打卡（仅当日可打卡）："
		}
		errs = append(errs, p.wxGroup(ctx, teamID, groupTitle+"\n"+groupLead+"\n"+strings.Join(lines, "\n"), atUsers))
	}
	return errors.Join(errs...)
}

// NotifyProxyState 代理异常/恢复（仅超管：站内 + 微信个人；正文含脱敏代理地址）。
func (p *PushNotifier) NotifyProxyState(ctx context.Context, errMsg string, recovered bool) error {
	admins := p.adminIDs(ctx)
	if len(admins) == 0 {
		return nil
	}
	masked := ""
	if p.ProxyURL != nil {
		masked = maskProxyURL(p.ProxyURL(ctx))
	}
	var title, content string
	if recovered {
		title = "商旅出口代理已恢复"
		content = "商旅 API 出口代理" + addrParen(masked) + "探测已恢复正常，打卡/费用/照片同步可正常进行。"
	} else {
		title = "商旅出口代理异常"
		content = "商旅 API 出口代理" + addrParen(masked) + "探测失败：" + truncateStr(errMsg, 200) +
			"。代理中断期间打卡/费用/照片同步将全员失败，请尽快检查代理服务与出口网络（SGCC_PROXY_URL）。"
	}
	errs := []error{p.inApp(ctx, admins, title, content)}
	errs = append(errs, p.wxPersonal(ctx, admins, title+"\n"+content))
	return errors.Join(errs...)
}

// addrParen 代理地址括注（无地址省略段落）。
func addrParen(masked string) string {
	if masked == "" {
		return "（未配置）"
	}
	return "（" + masked + "）"
}

// NotifyManualCheck 提醒前同步失败 → 人工核查通知（超管 + 涉及班管：站内 + 微信个人；
// 状态未知不发成员本人/班组群，避免误催）。
func (p *PushNotifier) NotifyManualCheck(ctx context.Context, label string, rows []MissingClockinRow, reason string) error {
	_ = label
	teamIDs := []int64{}
	for _, r := range rows {
		if r.TeamID > 0 {
			teamIDs = append(teamIDs, r.TeamID)
		}
	}
	recipients := uniqIDs(append(p.adminIDs(ctx), p.teamAdminIDs(ctx, uniqIDs(teamIDs...))...)...)
	if len(recipients) == 0 {
		return nil
	}
	lines := []string{}
	for _, r := range rows {
		name := r.Name
		if name == "" && r.MemberID > 0 {
			name = memberNameOf(ctx, p.DB, r.MemberID)
		}
		if name == "" {
			name = "成员" + strconv.FormatInt(r.MemberID, 10)
		}
		lines = append(lines, "· "+name)
	}
	content := reason
	if len(lines) > 0 {
		content += "\n" + strings.Join(lines, "\n")
	}
	title := "打卡核查需人工确认"
	errs := []error{p.inApp(ctx, recipients, title, content)}
	errs = append(errs, p.wxPersonal(ctx, recipients, title+"\n"+content))
	return errors.Join(errs...)
}
