// 每日核查与提醒排程：23:00 每日核查、11:00 开始打卡午间提醒、18:00 结束打卡傍晚提醒、
// 出口代理健康探测。定时器可注入（测试手动触发），提醒前先跑轻量同步（登录态 + 打卡对账），
// 同步失败成员转人工核查不自动提醒；失败处理与通知走 Notifier。
package sgccclockin

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"

	"shade/internal/config"
)

// Timer 可注入定时器（测试手动触发；nil 用系统 time.AfterFunc）。
type Timer interface {
	// AfterFunc 到点执行 fn；返回取消函数。
	AfterFunc(d time.Duration, fn func()) (cancel func())
}

// systemTimer 系统定时器。
type systemTimer struct{}

func (systemTimer) AfterFunc(d time.Duration, fn func()) func() {
	t := time.AfterFunc(d, fn)
	return func() { t.Stop() }
}

// Scheduler 提醒/核查排程（时间口径 UTC+8 固定偏移，同 utils/cndate.nextDailyRunUtc）。
// 生命周期：Start 启动（幂等，返回停止函数）→ 各排程到点执行后自排次日；排程时刻每次现读
// 运行时配置（SGCC_STARTCLOCK_REMIND_TIME / SGCC_ENDCLOCK_REMIND_TIME / SGCC_SYNC_TIME），
// 配置变更经 ChangeSubscriber 热重排（Reconfigure）；Stop 停止（幂等），停止后不再排新任务。
type Scheduler struct {
	Module      *Module
	Timer       Timer
	StartRemind string        // 开始打卡午间提醒缺省（Cfg 优先；默认 11:00）
	EndRemind   string        // 结束打卡傍晚提醒缺省（Cfg 优先；默认 18:00）
	DailyCheck  string        // 每日核查缺省（Cfg 优先；默认 23:00）
	ProxyProbe  time.Duration // 代理探测间隔缺省（Cfg 优先；默认 5 分钟）

	mu        sync.Mutex
	started   bool
	stopped   bool
	stopCh    chan struct{}
	cancels   map[string]func()
	proxyDown bool
}

// NewScheduler 构造排程器。
func NewScheduler(m *Module) *Scheduler {
	return &Scheduler{
		Module:      m,
		Timer:       systemTimer{},
		StartRemind: "11:00",
		EndRemind:   "18:00",
		DailyCheck:  "23:00",
		ProxyProbe:  5 * time.Minute,
	}
}

// nextDailyRun 计算下次触发时刻（UTC+8 的 hh:mm → 对应 UTC time）。
func nextDailyRun(now time.Time, hh, mm int) time.Time {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	next := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, cst)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next.UTC()
}

// parseHHMM 解析 "HH:MM"（非法回退默认）。
func parseHHMM(s string, defH, defM int) (int, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return defH, defM
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return defH, defM
	}
	return h, m
}

// cfgOr 读运行时配置（未接入/为空回退缺省）。
func (s *Scheduler) cfgOr(ctx context.Context, key, fallback string) string {
	if v := cfgValue(ctx, s.Module.Cfg, key); v != "" {
		return v
	}
	return fallback
}

// remindHHMM 排程时刻：Cfg 键优先，缺省字段回退，非法回退默认。
func (s *Scheduler) remindHHMM(ctx context.Context, key, fallback string, defH, defM int) (int, int) {
	return parseHHMM(s.cfgOr(ctx, key, fallback), defH, defM)
}

// probeInterval 代理探测间隔：Cfg 的 SGCC_PROXY_PROBE_INTERVAL_MS 优先（毫秒），缺省字段回退。
func (s *Scheduler) probeInterval(ctx context.Context) time.Duration {
	if v := cfgValue(ctx, s.Module.Cfg, CfgProxyProbeInterval); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	if s.ProxyProbe > 0 {
		return s.ProxyProbe
	}
	return 5 * time.Minute
}

// Start 启动排程（幂等可重入；返回停止函数）。
func (s *Scheduler) Start() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && !s.stopped {
		return s.Stop
	}
	s.started = true
	s.stopped = false
	s.stopCh = make(chan struct{})
	s.cancels = map[string]func(){}
	s.subscribeLocked()
	s.armTimersLocked()
	if s.Module.API != nil {
		go s.proxyLoop(s.stopCh)
	}
	return s.Stop
}

// Stop 停止排程（幂等）：取消全部定时器、停代理探测；停止后不再排新任务。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.stopCh != nil {
		close(s.stopCh)
	}
	for name, cancel := range s.cancels {
		cancel()
		delete(s.cancels, name)
	}
}

// Reconfigure 配置变更后热重排：取消全部待触发定时器并按当前配置重新排程（未启动/已停止为无操作）。
func (s *Scheduler) Reconfigure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopped {
		return
	}
	s.armTimersLocked()
}

// subscribeLocked 配置变更订阅：排程时刻键变更即热重排（Store 不支持订阅则仅每次排程现读配置）。
func (s *Scheduler) subscribeLocked() {
	sub, ok := s.Module.Cfg.(ChangeSubscriber)
	if !ok {
		return
	}
	sub.OnChange(func(ev config.ChangeEvent) {
		switch ev.Key {
		case CfgSyncTime, CfgStartRemindTime, CfgEndRemindTime:
			s.Reconfigure()
		}
	})
}

// armTimersLocked 排齐三项定时（午间提醒/傍晚提醒/每日核查）；调用方持 s.mu。
func (s *Scheduler) armTimersLocked() {
	s.armSlotLocked("start",
		func(ctx context.Context) (int, int) {
			return s.remindHHMM(ctx, CfgStartRemindTime, s.StartRemind, 11, 0)
		},
		func(ctx context.Context) {
			// 先轻量同步（登录态+打卡对账），失败成员剔除并转人工核查
			exclude := s.BeforeClockinRemind(ctx, TodayCN(), "开始打卡")
			if exclude != nil {
				s.RemindMissingStartClockin(ctx, exclude)
			}
		})
	s.armSlotLocked("end",
		func(ctx context.Context) (int, int) {
			return s.remindHHMM(ctx, CfgEndRemindTime, s.EndRemind, 18, 0)
		},
		func(ctx context.Context) {
			exclude := s.BeforeClockinRemind(ctx, TodayCN(), "结束打卡")
			if exclude != nil {
				s.RemindMissingEndClockin(ctx, exclude)
			}
		})
	s.armSlotLocked("daily",
		func(ctx context.Context) (int, int) {
			return s.remindHHMM(ctx, CfgSyncTime, s.DailyCheck, 23, 0)
		},
		func(ctx context.Context) {
			s.DailyCheckJob(ctx)
		})
}

// armSlotLocked 排一次触发：到点执行 body 后自排次日（Node setTimeout 自排程同口径）；
// 时刻每次现读配置，配置变更后下一轮排程生效（热重排走 Reconfigure）。调用方持 s.mu。
func (s *Scheduler) armSlotLocked(name string, hhmm func(ctx context.Context) (int, int), body func(ctx context.Context)) {
	if !s.started || s.stopped {
		return
	}
	if cancel, ok := s.cancels[name]; ok {
		cancel()
		delete(s.cancels, name)
	}
	ctx := context.Background()
	h, m := hhmm(ctx)
	delay := time.Until(nextDailyRun(time.Now(), h, m))
	t := s.Timer
	if t == nil {
		t = systemTimer{}
	}
	s.cancels[name] = t.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		delete(s.cancels, name)
		s.mu.Unlock()
		body(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.armSlotLocked(name, hhmm, body)
	})
}

// MissingEndRows 当日已打开始卡但未打结束卡的成员（跨全部班组；excludeIDs 剔除）。
func (s *Scheduler) MissingEndRows(ctx context.Context, date string, excludeIDs []int64) []MissingClockinRow {
	query := `SELECT c.team_id, c.member_id, m.name, COALESCE(MAX(a.user_id), 0),
	        DATE_FORMAT(MAX(CASE WHEN c.seq = 1 THEN c.clock_time END), '%H:%i')
	 FROM worklog_clockin c
	 JOIN worklog_member m ON m.id = c.member_id
	 LEFT JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
	 WHERE c.clock_date = ?`
	args := []any{date}
	if len(excludeIDs) > 0 {
		query += ` AND c.member_id NOT IN (` + placeholders(len(excludeIDs)) + ")"
		for _, id := range excludeIDs {
			args = append(args, id)
		}
	}
	query += ` GROUP BY c.team_id, c.member_id, m.name
	 HAVING MAX(CASE WHEN c.seq = 1 THEN 1 ELSE 0 END) = 1
	    AND MAX(CASE WHEN c.seq = 2 THEN 1 ELSE 0 END) = 0`
	return s.queryMissing(ctx, query, args)
}

// MissingStartRows 当日卡上已绑定商旅但未打开始卡（首次打卡）的成员。
func (s *Scheduler) MissingStartRows(ctx context.Context, date string, excludeIDs []int64) []MissingClockinRow {
	query := `SELECT MIN(e.team_id), m.id, m.name, COALESCE(MAX(a.user_id), 0), ''
	 FROM worklog_entry e
	 JOIN worklog_entry_member em ON em.entry_id = e.id
	 JOIN worklog_member m ON m.id = em.member_id
	 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
	 LEFT JOIN worklog_clockin c ON c.member_id = m.id AND c.clock_date = ? AND c.seq = 1
	 WHERE e.log_date = ? AND e.team_id IS NOT NULL AND c.id IS NULL`
	args := []any{date, date}
	if len(excludeIDs) > 0 {
		query += ` AND m.id NOT IN (` + placeholders(len(excludeIDs)) + ")"
		for _, id := range excludeIDs {
			args = append(args, id)
		}
	}
	query += ` GROUP BY m.id, m.name`
	return s.queryMissing(ctx, query, args)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Scheduler) queryMissing(ctx context.Context, query string, args []any) []MissingClockinRow {
	rows, err := s.Module.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]MissingClockinRow, 0)
	for rows.Next() {
		var r MissingClockinRow
		var teamID, userID sql.NullInt64
		var startHM sql.NullString
		if err := rows.Scan(&teamID, &r.MemberID, &r.Name, &userID, &startHM); err == nil {
			r.TeamID = teamID.Int64
			r.UserID = userID.Int64
			r.StartHM = startHM.String
			out = append(out, r)
		}
	}
	return out
}

// BeforeClockinRemind 提醒前轻量同步：对当日卡上绑定成员逐人做 登录态探测 + 打卡对账（scope=remind）。
// 返回剔除名单（同步失败成员）；整体失败返回 nil（本轮不自动核查）并通知人工核查。
func (s *Scheduler) BeforeClockinRemind(ctx context.Context, date, label string) []int64 {
	m := s.Module
	rows, err := m.DB.QueryContext(ctx,
		`SELECT DISTINCT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token,
		        a.device_type, a.system_version, a.token_status, m.id
		 FROM worklog_entry e
		 JOIN worklog_entry_member em ON em.entry_id = e.id
		 JOIN worklog_member m ON m.id = em.member_id
		 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
		 WHERE e.log_date = ? AND e.team_id IS NOT NULL`, date)
	if err != nil {
		if m.Notifier != nil {
			_ = m.Notifier.NotifyManualCheck(ctx, label, nil, label+"提醒前同步异常："+truncateStr(err.Error(), 200))
		}
		return nil
	}
	type accRow struct {
		acc    Account
		cardID int64
	}
	list := make([]accRow, 0)
	for rows.Next() {
		var a accRow
		var memberID, userID, teamID sql.NullInt64
		if err := rows.Scan(&a.acc.ID, &teamID, &userID, &memberID, &a.acc.Mobile, &a.acc.Token,
			&a.acc.DeviceType, &a.acc.SystemVersion, &a.acc.TokenStatus, &a.cardID); err == nil {
			a.acc.TeamID = teamID.Int64
			a.acc.UserID = userID.Int64
			if memberID.Valid {
				a.acc.MemberID = memberID.Int64
			}
			a.acc.CardMemberID = a.cardID
			list = append(list, a)
		}
	}
	rows.Close()
	failed := make([]int64, 0)
	failedRows := make([]MissingClockinRow, 0)
	for _, item := range list {
		acc := item.acc
		if err := m.SyncOneClockin(ctx, &acc, date, "remind", acc.TeamID); err != nil {
			failed = append(failed, item.cardID)
			failedRows = append(failedRows, MissingClockinRow{
				TeamID: acc.TeamID, MemberID: item.cardID, UserID: acc.UserID,
			})
		}
		m.sleepInterval(ctx)
	}
	if len(failed) > 0 && m.Notifier != nil {
		_ = m.Notifier.NotifyManualCheck(ctx, label, failedRows,
			label+"提醒前商旅同步失败（打卡状态未知），本次已跳过其自动打卡核查")
	}
	return failed
}

// sleepInterval 防风控间隔：Cfg 的 SGCC_SYNC_INTERVAL_MS 优先（默认 1500ms 同 Node），IntervalMs
// 为回退缺省；<=0 不等待（测试即时返回）。
func (m *Module) sleepInterval(ctx context.Context) {
	ms := m.IntervalMs
	if v := cfgValue(ctx, m.Cfg, CfgSyncIntervalMs); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			ms = n
		}
	}
	if ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

// RemindMissingEndClockin 结束打卡傍晚提醒（本人 + 班组群口径由 Notifier 合并）。
func (s *Scheduler) RemindMissingEndClockin(ctx context.Context, excludeIDs []int64) {
	rows := s.MissingEndRows(ctx, TodayCN(), excludeIDs)
	if len(rows) == 0 {
		return
	}
	if s.Module.Notifier != nil {
		_ = s.Module.Notifier.NotifyRemind(ctx, "end", rows)
	}
}

// RemindMissingStartClockin 开始打卡午间提醒。
func (s *Scheduler) RemindMissingStartClockin(ctx context.Context, excludeIDs []int64) {
	rows := s.MissingStartRows(ctx, TodayCN(), excludeIDs)
	if len(rows) == 0 {
		return
	}
	if s.Module.Notifier != nil {
		_ = s.Module.Notifier.NotifyRemind(ctx, "start", rows)
	}
}

// DailyCheckJob 每日核查任务体：卡片驱动逐班对账 + 其余账号登录态保鲜 + 缺结束卡检查 + 日报。
func (s *Scheduler) DailyCheckJob(ctx context.Context) {
	m := s.Module
	date := TodayCN()
	teamRows, err := m.DB.QueryContext(ctx,
		`SELECT DISTINCT team_id FROM (
		   SELECT team_id FROM worklog_sgcc_account WHERE team_id IS NOT NULL
		   UNION
		   SELECT e.team_id FROM worklog_entry e
		   JOIN worklog_entry_member em ON em.entry_id = e.id
		   JOIN worklog_member m ON m.id = em.member_id
		   JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
		   WHERE e.log_date = ? AND e.team_id IS NOT NULL
		 ) t`, date)
	if err != nil {
		return
	}
	teamIDs := make([]int64, 0)
	for teamRows.Next() {
		var id int64
		if err := teamRows.Scan(&id); err == nil {
			teamIDs = append(teamIDs, id)
		}
	}
	teamRows.Close()

	for _, teamID := range teamIDs {
		// 日报分界：仅统计当次核查新产生的记录
		var beforeID int64
		_ = m.DB.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), 0) FROM worklog_sync_log WHERE team_id = ?`, teamID).Scan(&beforeID)

		// 当日卡上绑定成员逐人对账（先取齐名单再登记任务：total 即名单数，total<=0 即刻完成）
		accRows, err := m.DB.QueryContext(ctx,
			`SELECT DISTINCT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token,
			        a.device_type, a.system_version, a.token_status
			 FROM worklog_entry e
			 JOIN worklog_entry_member em ON em.entry_id = e.id
			 JOIN worklog_member m ON m.id = em.member_id
			 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
			 WHERE e.team_id = ? AND e.log_date = ?`, teamID, date)
		list := make([]Account, 0)
		if err == nil {
			for accRows.Next() {
				var acc Account
				var memberID, userID, team sql.NullInt64
				if err := accRows.Scan(&acc.ID, &team, &userID, &memberID, &acc.Mobile, &acc.Token,
					&acc.DeviceType, &acc.SystemVersion, &acc.TokenStatus); err == nil {
					acc.TeamID = team.Int64
					acc.UserID = userID.Int64
					if memberID.Valid {
						acc.MemberID = memberID.Int64
					}
					list = append(list, acc)
				}
			}
			accRows.Close()
		}
		jobID := jobs.New(teamID, len(list), "daily", date)
		for i := range list {
			acc := &list[i]
			if err := m.SyncOne(ctx, acc, date, "daily", teamID); err != nil {
				jobs.Step(jobID, err.Error())
			} else {
				jobs.Step(jobID, "")
			}
			m.sleepInterval(ctx)
		}
		jobs.Finish(jobID)

		// 其余绑定账号登录态保鲜（1→0 跳变由 probeAuth 内自动通知）
		restRows, err := m.DB.QueryContext(ctx,
			`SELECT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token,
			        a.device_type, a.system_version, a.token_status
			 FROM worklog_sgcc_account a
			 WHERE a.team_id = ? AND a.member_id IS NOT NULL
			   AND a.member_id NOT IN (
			     SELECT DISTINCT m.id FROM worklog_entry e
			     JOIN worklog_entry_member em ON em.entry_id = e.id
			     JOIN worklog_member m ON m.id = em.member_id
			     JOIN worklog_sgcc_account a2 ON a2.member_id = m.id OR (m.user_id IS NOT NULL AND a2.user_id = m.user_id)
			     WHERE e.team_id = ? AND e.log_date = ?)`, teamID, teamID, date)
		if err == nil {
			for restRows.Next() {
				var acc Account
				var memberID, userID, team sql.NullInt64
				if err := restRows.Scan(&acc.ID, &team, &userID, &memberID, &acc.Mobile, &acc.Token,
					&acc.DeviceType, &acc.SystemVersion, &acc.TokenStatus); err == nil {
					acc.TeamID = team.Int64
					acc.UserID = userID.Int64
					if memberID.Valid {
						acc.MemberID = memberID.Int64
					}
					m.probeAuth(ctx, &acc)
					m.sleepInterval(ctx)
				}
			}
			restRows.Close()
		}

		// 每日核查附加检查：开始卡已打、结束卡未打 → 通知
		if m.Notifier != nil {
			rows := s.MissingEndRowsForTeam(ctx, teamID, date)
			if len(rows) > 0 {
				_ = m.Notifier.NotifyMissingEndClockin(ctx, teamID, date, rows)
			}
			// 日报（仅统计当次新记录）
			summary := m.syncSummary(ctx, teamID, date, beforeID)
			_ = m.Notifier.NotifyDailySyncResult(ctx, teamID, date, summary)
		}
	}
}

// MissingEndRowsForTeam 单班口径的缺结束卡成员（每日核查附加检查）。
func (s *Scheduler) MissingEndRowsForTeam(ctx context.Context, teamID int64, date string) []MissingClockinRow {
	rows := s.MissingEndRows(ctx, date, nil)
	out := make([]MissingClockinRow, 0, len(rows))
	for _, r := range rows {
		if r.TeamID == teamID {
			out = append(out, r)
		}
	}
	return out
}

// syncSummary 汇总当次核查新记录（id > afterID）。
func (m *Module) syncSummary(ctx context.Context, teamID int64, date string, afterID int64) SyncSummary {
	summary := SyncSummary{}
	_ = m.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&summary.TeamName)
	rows, err := m.DB.QueryContext(ctx,
		`SELECT l.type, l.result, l.detail, COALESCE(l.member_id, 0), COALESCE(m.name, '')
		 FROM worklog_sync_log l LEFT JOIN worklog_member m ON m.id = l.member_id
		 WHERE l.team_id = ? AND l.sync_date = ? AND l.id > ? ORDER BY l.id`,
		teamID, date, afterID)
	if err != nil {
		return summary
	}
	defer rows.Close()
	for rows.Next() {
		var row SyncLogRow
		if err := rows.Scan(&row.Type, &row.Result, &row.Detail, &row.MemberID, &row.MemberName); err == nil {
			switch row.Result {
			case "ok":
				summary.OK++
			case "diff":
				summary.Diff++
			case "fail":
				summary.Fail++
				summary.Fails = append(summary.Fails, row)
			}
		}
	}
	return summary
}

// proxyLoop 出口代理健康探测轮询（仅在 正常↔异常 跳变时通知超管；间隔每次现读配置，变更下一轮生效）。
func (s *Scheduler) proxyLoop(stopCh chan struct{}) {
	m := s.Module
	tick := func() {
		ok, skipped, errMsg := m.API.ProbeProxy()
		if skipped {
			return
		}
		s.mu.Lock()
		down := s.proxyDown
		if !ok && !down {
			s.proxyDown = true
		} else if ok && down {
			s.proxyDown = false
		}
		changed := down != s.proxyDown
		recovered := ok && down
		s.mu.Unlock()
		if changed && m.Notifier != nil {
			_ = m.Notifier.NotifyProxyState(context.Background(), errMsg, recovered)
		}
	}
	tick() // 启动即探一次
	for {
		select {
		case <-stopCh:
			return
		case <-time.After(s.probeInterval(context.Background())):
			tick()
		}
	}
}

// SelfCheck 启动通道自检（jsonm/slapp 双通道拉池）；失败通知超管。
func (s *Scheduler) SelfCheck(ctx context.Context) {
	if s.Module.API == nil {
		return
	}
	if err := s.Module.API.SelfCheck(ctx); err != nil {
		if s.Module.Notifier != nil {
			_ = s.Module.Notifier.NotifyProxyState(ctx, "商旅通道自检失败："+truncateStr(err.Error(), 100), false)
		}
	}
}
