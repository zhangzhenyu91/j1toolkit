package crossing

// 交跨统计表内网 U 盘同步（与 server/src/crossing/sync.js 同口径，编排层可注入）：
// 同步对象：CROSSING_SYNC_TEAM 指定班组的全量记录 ↔ 被控机导出的「{班组名}交跨统计表.xlsx」
// （O 列记录ID 为行级比对锚点）。轮次：准备（snapshot 指令 + 挂载 U 盘）→ 等指令窗口 → 取件
// （下载内网表 → diff 比对回写：updates 覆盖 / news 插入 source='intranet' / 双边都改 → 冲突留档；
// 裁决 keep=server 的旧值不回灌防反转）→ 视差异清盘收尾或下发最新表 + replace 指令 → 同步日志。
// 设备链路经 UsbLink 注入（真实现由主装配对接 GLKVM fileshare；nil/未配置明确报错，不假成功）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"shade/internal/apps/usbcmd"
)

// ErrSyncNotConfigured 内网 U 盘同步未配置/链路未接入（明确返回，不假成功）。
var ErrSyncNotConfigured = errors.New("未配置同步设备（CROSSING_SYNC_DEVICE_DDNS）")

// ErrSyncBusy 已有同步轮次进行中（手动/排程/空闲共用核心 Run 的互斥锁）。
var ErrSyncBusy = errors.New("交跨统计表同步中，请稍后再试")

// UsbLink 内网 U 盘同步设备链路（可注入；对应 GLKVM fileshare 的 mount/download/list/delete/push）。
type UsbLink interface {
	// MountToHost 挂载（共享）U 盘至被控机端。
	MountToHost(ctx context.Context) error
	// DownloadFile 下载 U 盘内文件；文件不存在返回 (nil, nil)（区别于链路故障报错）。
	DownloadFile(ctx context.Context, name string) ([]byte, error)
	// ListFiles 列 U 盘根目录文件名。
	ListFiles(ctx context.Context) ([]string, error)
	// DeleteFiles 删除 U 盘内文件（names 一次删多个）。
	DeleteFiles(ctx context.Context, names []string) error
	// PushFile 推送文件到 U 盘。
	PushFile(ctx context.Context, name string, data []byte) error
}

// 同步指令文件与命名常量（与 sync.js 一致）。
const (
	syncCmdFile = "crossing_sync_cmd.json"
)

// IntranetSyncCounts 轮次计数（供摘要与 /sync/status 展示）。
type IntranetSyncCounts struct {
	Updates     int  `json:"updates"`
	News        int  `json:"news"`
	Conflicts   int  `json:"conflicts"`
	Skips       int  `json:"skips"`
	Adjudicated int  `json:"adjudicated"`
	Missing     int  `json:"missing"`
	Pushed      bool `json:"pushed"`
	NoFile      bool `json:"no_file"`
}

// IntranetSyncResult 一轮同步结果。
type IntranetSyncResult struct {
	OK      bool               `json:"ok"`
	Message string             `json:"message"`
	Counts  IntranetSyncCounts `json:"counts"`
}

// newSyncCycleID 同步轮次标识：yyyyMMdd-HHmm-xxxx（北京时间 + 4 位随机）。
func newSyncCycleID(now time.Time) string {
	cn := now.UTC().Add(8 * time.Hour)
	return fmt.Sprintf("%s-%04x", cn.Format("20060102-1504"), cn.UnixNano()&0xffff)
}

// syncCmdPayload 指令文件内容（统一协议；保留 cycle_id/kind/file 旧字段供滚动升级）。
func syncCmdPayload(cycle, kind, fileName string, now time.Time) []byte {
	return usbcmd.Payload("crossing_sync", cycle, kind, fileName, now, time.Hour)
}

// syncRecordMap 服务端记录 → diff 比对 map（13 业务列 + 同步点时间戳）。
func syncRecordMap(r Record) map[string]any {
	inspectAt := ""
	if r.InspectAt != nil {
		inspectAt = *r.InspectAt
	}
	m := map[string]any{
		"id": r.ID, "voltage_level": r.VoltageLevel, "line_name": r.LineName, "section": r.Section,
		"crossing_type": r.CrossingType, "dist_small": r.DistSmall, "dist_big": r.DistBig,
		"cross_horizontal": r.CrossHorizontal, "cross_vertical": r.CrossVertical,
		"cross_angle": r.CrossAngle, "has_hazard": r.HasHazard, "finder_name": r.FinderName,
		"inspect_at": inspectAt, "remark": r.Remark,
	}
	if r.SyncedAt != nil {
		m["synced_at"] = r.SyncedAt.Format("2006-01-02 15:04:05")
	} else {
		m["synced_at"] = ""
	}
	m["updated_at"] = r.UpdatedAt.Format("2006-01-02 15:04:05")
	return m
}

// applyIntranetDiff 差异应用：updates 内网值覆盖 13 业务列（+synced_at，WHERE 带 team_id 防越班）；
// news 以 source='intranet' 插入（crane 双 NULL，pole 从区段文本解析，synced_at=NOW）；
// conflicts 落 crossing_conflict 留档（人工二选一解决，本轮不改记录）。
func (m *Module) applyIntranetDiff(ctx context.Context, teamID int64, result DiffResult) error {
	for _, u := range result.Updates {
		f := u.Fields
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, crossing_type = ?,
			   dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
			   finder_name = ?, inspect_at = ?, remark = ?, synced_at = NOW()
			 WHERE id = ? AND team_id = ?`,
			f["voltage_level"], f["line_name"], f["section"], f["crossing_type"],
			f["dist_small"], f["dist_big"], f["cross_horizontal"], f["cross_vertical"], f["cross_angle"], f["has_hazard"],
			f["finder_name"], f["inspect_at"], f["remark"], u.ID, teamID); err != nil {
			return err
		}
	}
	for _, row := range result.News {
		f := intranetFields(row)
		poleFrom, poleTo := parseSectionPoles(normStr(row["section"]))
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
			   dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
			   finder_name, inspect_at, remark, source, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'intranet', NOW())`,
			teamID, f["voltage_level"], f["line_name"], f["section"], poleFrom, poleTo, f["crossing_type"],
			f["dist_small"], f["dist_big"], f["cross_horizontal"], f["cross_vertical"], f["cross_angle"], f["has_hazard"],
			f["finder_name"], f["inspect_at"], f["remark"]); err != nil {
			return err
		}
	}
	for _, c := range result.Conflicts {
		serverSnap, _ := json.Marshal(c.Server)
		intranetSnap, _ := json.Marshal(c.Intranet)
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO crossing_conflict (team_id, record_id, server_snapshot, intranet_snapshot) VALUES (?, ?, ?, ?)`,
			teamID, c.ID, string(serverSnap), string(intranetSnap)); err != nil {
			return err
		}
	}
	return nil
}

// decideIntranetAction 下发判定（纯函数）：吸收了内网改动、有已裁决驳回待收敛的行，
// 或服务端存在「同步点之后又改过 / 从未同步」的记录 → 需下发最新表。
func decideIntranetAction(result DiffResult, serverRecords []map[string]any) (needPush bool, reason string) {
	if absorbed := len(result.Updates) + len(result.News); absorbed > 0 {
		return true, fmt.Sprintf("吸收内网改动 %d 条", absorbed)
	}
	if result.Adjudicated > 0 {
		return true, fmt.Sprintf("裁决待收敛 %d 条", result.Adjudicated)
	}
	if result.Missing > 0 {
		return true, fmt.Sprintf("内网缺行 %d 条", result.Missing)
	}
	changed := 0
	for _, r := range serverRecords {
		syncedAt := diffParseDateMillis(r["synced_at"])
		if syncedAt == 0 {
			changed++
			continue
		}
		if updatedAt := diffParseDateMillis(r["updated_at"]); updatedAt > syncedAt {
			changed++
		}
	}
	if changed > 0 {
		return true, fmt.Sprintf("服务端变更 %d 条", changed)
	}
	return false, "无差异"
}

// markSyncedAfterPush 下发后落同步点：全量记录 synced_at=NOW()；pending 冲突行除外（保人工裁决语义）。
func (m *Module) markSyncedAfterPush(ctx context.Context, teamID int64) error {
	_, err := m.DB.ExecContext(ctx,
		`UPDATE crossing_record SET synced_at = NOW()
		 WHERE team_id = ? AND id NOT IN (
		   SELECT record_id FROM crossing_conflict WHERE team_id = ? AND status = 'pending' AND record_id IS NOT NULL)`,
		teamID, teamID)
	return err
}

// cleanupUsb 取件收尾清扫：删 U 盘根目录全部交跨表残留（任意「*交跨统计表.xlsx」与指令文件）；best-effort。
func cleanupUsb(ctx context.Context, link UsbLink) error {
	files, err := link.ListFiles(ctx)
	if err != nil {
		return err
	}
	victims := make([]string, 0)
	for _, n := range files {
		if strings.HasSuffix(n, "交跨统计表.xlsx") || n == syncCmdFile {
			victims = append(victims, n)
		}
	}
	if len(victims) == 0 {
		return nil
	}
	return link.DeleteFiles(ctx, victims)
}

// pushLatest 下发最新表：按线路分表（split=true，team_line 顺序 + 未匹配线路排尾）→
// push 表文件 + replace 指令 → 重挂回共享态，返回 replace 指令 id 供调用方等待结果。
func (m *Module) pushLatest(ctx context.Context, link UsbLink, teamID int64, teamName string, records []Record) (string, error) {
	fileName := exportFileName(teamName, "")
	lineRows, err := fetchLineOrder(m.DB, teamID)
	if err != nil {
		return "", fmt.Errorf("读取线路顺序失败: %w", err)
	}
	lineOrder := make(map[string]int, len(lineRows))
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	buf, err := buildWorkbook(teamName, records, true, lineOrder)
	if err != nil {
		return "", err
	}
	replaceID := usbcmd.NewID(time.Now())
	if err := link.PushFile(ctx, fileName, buf); err != nil {
		return "", err
	}
	if err := link.PushFile(ctx, syncCmdFile, syncCmdPayload(replaceID, "replace", fileName, time.Now())); err != nil {
		return "", err
	}
	if err := link.MountToHost(ctx); err != nil {
		return "", err
	}
	return replaceID, nil
}

// fetchIntranetPhase 取件与比对回写：下载内网表 → diff（带已裁决驳回快照防反转）→ 应用 →
// 收尾清盘 → 视差异决定收尾或下发最新表（收尾顺序统一「先清扫、后下发」）。
func (m *Module) fetchIntranetPhase(ctx context.Context, link UsbLink, teamID int64, teamName string) (IntranetSyncCounts, string, error) {
	counts := IntranetSyncCounts{}
	fileName := exportFileName(teamName, "")
	records, err := m.loadTeamRecords(ctx, teamID)
	if err != nil {
		return counts, "", err
	}
	serverRecords := make([]map[string]any, 0, len(records))
	for _, r := range records {
		serverRecords = append(serverRecords, syncRecordMap(r))
	}
	// 墓碑是“服务端已删”的唯一依据：查询失败必须终止，不能按无墓碑继续回灌已删记录。
	deletedIDs := make([]int64, 0)
	drows, err := m.DB.QueryContext(ctx, `SELECT id FROM crossing_deleted_id WHERE team_id = ?`, teamID)
	if err != nil {
		return counts, "", fmt.Errorf("读取删除墓碑失败: %w", err)
	}
	for drows.Next() {
		var id int64
		if err := drows.Scan(&id); err != nil {
			drows.Close()
			return counts, "", fmt.Errorf("读取删除墓碑失败: %w", err)
		}
		deletedIDs = append(deletedIDs, id)
	}
	if err := drows.Err(); err != nil {
		drows.Close()
		return counts, "", fmt.Errorf("读取删除墓碑失败: %w", err)
	}
	drows.Close()

	buf, err := link.DownloadFile(ctx, fileName)
	if err != nil {
		return counts, "", err
	}
	if buf == nil {
		// 内网无表：清残留后直接下发最新表（含空表），内网以服务端为准起步
		_ = cleanupUsb(ctx, link)
		replaceID, err := m.pushLatest(ctx, link, teamID, teamName, records)
		if err != nil {
			return counts, "", err
		}
		counts.Pushed, counts.NoFile = true, true
		return counts, replaceID, nil
	}
	rows, err := ParseIntranetSheet(buf)
	if err != nil {
		return counts, "", err
	}
	m.canonicalizeIntranetRows(ctx, teamID, rows)
	// 已裁决驳回（keep=server）的冲突快照：裁决后内网旧表若未收敛，驳回旧值不回灌防反转裁决。
	// 已裁决驳回快照查询失败同样终止：否则可能把已裁决的旧值重新回灌、反转人工裁决。
	adjudicated := make([]Adjudicated, 0)
	aRows, err := m.DB.QueryContext(ctx,
		`SELECT record_id, intranet_snapshot FROM crossing_conflict
		 WHERE team_id = ? AND status = 'resolved' AND resolution = 'server' AND record_id IS NOT NULL`, teamID)
	if err != nil {
		return counts, "", fmt.Errorf("读取已裁决冲突失败: %w", err)
	}
	for aRows.Next() {
		var rid int64
		var snap string
		if err := aRows.Scan(&rid, &snap); err != nil {
			aRows.Close()
			return counts, "", fmt.Errorf("读取已裁决冲突失败: %w", err)
		}
		if parsed := parseSnapshot(snap); len(parsed) > 0 {
			adjudicated = append(adjudicated, Adjudicated{RecordID: rid, Intranet: parsed})
		}
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return counts, "", fmt.Errorf("读取已裁决冲突失败: %w", err)
	}
	aRows.Close()
	result := DiffRecords(serverRecords, rows, deletedIDs, adjudicated)
	if err := m.applyIntranetDiff(ctx, teamID, result); err != nil {
		return counts, "", err
	}
	needPush, _ := decideIntranetAction(result, serverRecords)
	counts = IntranetSyncCounts{
		Updates: len(result.Updates), News: len(result.News), Conflicts: len(result.Conflicts),
		Skips: result.Skips, Adjudicated: result.Adjudicated, Missing: result.Missing,
	}
	if err := cleanupUsb(ctx, link); err != nil {
		// 收尾清扫 best-effort：失败仅记日志不阻断同步（下一轮收尾再清，同 Node cleanupUsb 口径）
		log.Printf("[交跨同步] U 盘残留清扫失败（继续收尾）：%v\n", err)
	}
	if !needPush {
		return counts, "", link.MountToHost(ctx)
	}
	fresh, err := m.loadTeamRecords(ctx, teamID) // 回写后的最新全量
	if err != nil {
		return counts, "", err
	}
	replaceID, err := m.pushLatest(ctx, link, teamID, teamName, fresh)
	if err != nil {
		return counts, "", err
	}
	counts.Pushed = true
	return counts, replaceID, nil
}

// buildIntranetSummary 结果摘要：'新增n 更新n 冲突n [裁决收敛n ]已下发' / '… 无差异'；失败 '失败：原因'。
func buildIntranetSummary(counts IntranetSyncCounts, err error) string {
	if err != nil {
		return "失败：" + err.Error()
	}
	parts := []string{fmt.Sprintf("新增%d", counts.News), fmt.Sprintf("更新%d", counts.Updates), fmt.Sprintf("冲突%d", counts.Conflicts)}
	if counts.Adjudicated > 0 {
		parts = append(parts, fmt.Sprintf("裁决收敛%d", counts.Adjudicated))
	}
	if counts.Missing > 0 {
		parts = append(parts, fmt.Sprintf("缺行%d", counts.Missing))
	}
	if counts.Pushed {
		if counts.NoFile {
			parts = append(parts, "内网无表·已下发")
		} else {
			parts = append(parts, "已下发")
		}
	} else {
		parts = append(parts, "无差异")
	}
	return strings.Join(parts, " ")
}

// syncLogAndNotify 写同步日志 + 结果通知（超管 + 本班班组管理员；通知依赖 Notifier，nil 跳过）。
func (m *Module) syncLogAndNotify(ctx context.Context, teamID int64, teamName, kind string, ok bool, text string) {
	_, _ = m.DB.ExecContext(ctx,
		`INSERT INTO crossing_sync_log (team_id, kind, result) VALUES (?, ?, ?)`, teamID, kind, text)
	if m.Notifier == nil {
		return
	}
	title := fmt.Sprintf("交跨统计表同步完成（%s）", teamName)
	if !ok {
		title = "交跨统计表同步失败"
	}
	kindName := "手动"
	if kind != "manual" {
		kindName = kind
	}
	_ = m.Notifier.Push(ctx, []string{"admin", "team_admin"}, title, fmt.Sprintf("方式：%s\n%s", kindName, text))
}

// beginSync 抢占同步重入锁；已在运行返回 false。
func (m *Module) beginSync() bool {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.syncRunning {
		return false
	}
	m.syncRunning = true
	return true
}

// endSync 释放同步重入锁。
func (m *Module) endSync() {
	m.syncMu.Lock()
	m.syncRunning = false
	m.syncMu.Unlock()
}

// RunIntranetCycle 完整同步轮次（手动/空闲/夜间共用核心入口）：锁下沉到本方法，
// 手动入口与排程并发时后到者立即返回 ErrSyncBusy，绝不重叠操作 U 盘。
func (m *Module) RunIntranetCycle(ctx context.Context, kind string) (IntranetSyncResult, error) {
	if !m.beginSync() {
		return IntranetSyncResult{OK: false, Message: "失败：" + ErrSyncBusy.Error()}, ErrSyncBusy
	}
	defer m.endSync()
	return m.runIntranetCycle(ctx, kind)
}

// runIntranetCycle 已持锁的同步实现：准备（snapshot 指令 + 挂载）→ 等结果 →
// 取件比对回写/下发 → 等 replace 结果 → 日志通知。未配置/链路未接入返回 ErrSyncNotConfigured（明确返回而非假成功）。
func (m *Module) runIntranetCycle(ctx context.Context, kind string) (IntranetSyncResult, error) {
	if kind == "" {
		kind = "manual"
	}
	// 全局 U 盘会话锁：树障/交跨/派车共用，所有轮次阻塞排队；放在核心入口而非
	// RunIntranetCycle，保证 HTTP 手动入口也经过同一把锁（手动本身是异步 goroutine）。
	if err := usbcmd.DefaultSession.Acquire(ctx); err != nil {
		return IntranetSyncResult{OK: false, Message: "失败：" + err.Error()}, err
	}
	defer usbcmd.DefaultSession.Release()
	if m.Usb == nil {
		return IntranetSyncResult{OK: false, Message: "同步链路未接入（UsbLink 未注入）"},
			fmt.Errorf("%w：同步链路未接入", ErrSyncNotConfigured)
	}
	if m.wkCfg(ctx, "CROSSING_SYNC_DEVICE_DDNS") == "" {
		return IntranetSyncResult{OK: false, Message: ErrSyncNotConfigured.Error()}, ErrSyncNotConfigured
	}
	teamName := m.wkCfg(ctx, "CROSSING_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var teamID int64
	var matchedName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE name = ? AND status = 1`, teamName).Scan(&teamID, &matchedName); err != nil {
		e := fmt.Errorf("未找到同步班组「%s」（未建或已停用）", teamName)
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	cycle := newSyncCycleID(time.Now())
	fileName := exportFileName(matchedName, "")
	if err := m.Usb.PushFile(ctx, syncCmdFile, syncCmdPayload(cycle, "snapshot", fileName, time.Now())); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if err := m.Usb.MountToHost(ctx); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	// 等 snapshot 结果：客户端先写结果再删指令，服务端按 id 读结果判定是否真的导出成功。
	policy := usbcmd.DefaultPolicy
	if m.SyncCmdPolicy != nil {
		policy = *m.SyncCmdPolicy
	}
	res, err := usbcmd.Await(ctx, m.Usb, "crossing_sync", cycle, policy)
	if err != nil {
		e := fmt.Errorf("等待被控机执行结果失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if !res.OK {
		e := fmt.Errorf("被控机导出失败：%s", res.Summary())
		m.syncLogAndNotify(ctx, teamID, matchedName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	counts, replaceID, err := m.fetchIntranetPhase(ctx, m.Usb, teamID, matchedName)
	var replaceErr error
	if err == nil && replaceID != "" {
		rres, rerr := usbcmd.Await(ctx, m.Usb, "crossing_sync", replaceID, policy)
		if rerr != nil {
			replaceErr = fmt.Errorf("回写结果未取回：%w", rerr)
		} else if !rres.OK {
			replaceErr = fmt.Errorf("回写失败：%s", rres.Summary())
		} else if merr := m.markSyncedAfterPush(ctx, teamID); merr != nil {
			replaceErr = fmt.Errorf("落同步点失败：%w", merr)
		}
	}
	text := buildIntranetSummary(counts, err)
	if replaceErr != nil {
		text += "；" + replaceErr.Error()
	}
	finalErr := err
	if finalErr == nil {
		finalErr = replaceErr
	}
	m.syncLogAndNotify(ctx, teamID, matchedName, kind, finalErr == nil, text)
	if finalErr != nil {
		return IntranetSyncResult{OK: false, Message: text, Counts: counts}, finalErr
	}
	return IntranetSyncResult{OK: true, Message: text, Counts: counts}, nil
}

// IntranetSyncStatus 同步状态（enabled：已配置设备且链路已接入；running：轮次进行中；
// last：本班最近一条 sync_log；nextNight：下次夜间取件时间（北京时间 ISO），未启用为 nil）。
func (m *Module) IntranetSyncStatus(ctx context.Context) map[string]any {
	enabled := m.wkCfg(ctx, "CROSSING_SYNC_DEVICE_DDNS") != "" && m.Usb != nil
	m.syncMu.Lock()
	running := m.syncRunning
	m.syncMu.Unlock()
	out := map[string]any{"enabled": enabled, "running": running, "last": nil, "nextNight": nil}
	if !enabled {
		return out
	}
	teamName := m.wkCfg(ctx, "CROSSING_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var kind, result, createdAt string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT l.kind, l.result, DATE_FORMAT(l.created_at, '%Y-%m-%d %H:%i:%s') FROM crossing_sync_log l
		 JOIN sys_team t ON t.id = l.team_id WHERE t.name = ? ORDER BY l.id DESC LIMIT 1`, teamName).
		Scan(&kind, &result, &createdAt); err == nil {
		out["last"] = map[string]any{"kind": kind, "result": result, "created_at": createdAt}
	}
	fetchTime := m.wkCfg(ctx, "CROSSING_SYNC_FETCH_TIME")
	if fetchTime == "" {
		fetchTime = "00:00"
	}
	hh, mm := 0, 0
	fmt.Sscanf(fetchTime, "%d:%d", &hh, &mm)
	cn := time.Now().UTC().Add(8 * time.Hour)
	next := time.Date(cn.Year(), cn.Month(), cn.Day(), hh, mm, 0, 0, time.UTC)
	if !next.After(cn) {
		next = next.Add(24 * time.Hour)
	}
	out["nextNight"] = next.Format("2006-01-02T15:04:05+08:00")
	return out
}

// syncPushNow POST /sync/push-now：手动触发一轮完整同步（异步执行：含指令窗口等待，
// 不宜挂 HTTP 等待；结果走通知与 /sync/status）。未配置明确 50301，不假成功。
func (m *Module) syncPushNow(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enterAdmin(w, r, 0); !ok {
		return
	}
	ctx := r.Context()
	if m.wkCfg(ctx, "CROSSING_SYNC_DEVICE_DDNS") == "" {
		fail(w, http.StatusServiceUnavailable, codeNoToken, "未配置同步设备")
		return
	}
	if !m.beginSync() {
		fail(w, http.StatusLocked, 42301, "同步进行中，请稍后再试")
		return
	}
	go func() {
		defer m.endSync()
		// 已持锁走核心实现；排程侧走 RunIntranetCycle 自行抢锁，两者共用同一把锁。
		_, _ = m.runIntranetCycle(context.Background(), "manual") // 内部已全 catch + 通知
	}()
	writeOK(w, map[string]any{"started": true}, "同步已开始")
}

// syncStatus GET /sync/status：{enabled, running, last, nextNight}（登录即可，供工作台展示）。
func (m *Module) syncStatus(w http.ResponseWriter, r *http.Request) {
	writeOK(w, m.IntranetSyncStatus(r.Context()))
}
