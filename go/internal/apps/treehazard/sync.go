package treehazard

// 树障统计表内网 U 盘同步（与 server/src/treehazard/sync.js 同口径，编排层可注入）：
// 同步对象：TREEHAZARD_SYNC_TEAM 指定班组的全量记录 ↔ 被控机导出的「{班组名}树木统计表.xlsx」
// （Q 列记录ID 为行级比对锚点）。轮次：准备（snapshot 指令 + 挂载 U 盘）→ 等指令窗口 → 取件
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
	"sync"
	"time"

	"shade/internal/apps/cluster"
	"shade/internal/apps/usbcmd"
)

// ErrSyncNotConfigured 内网 U 盘同步未配置/链路未接入（明确返回，不假成功）。
var ErrSyncNotConfigured = errors.New("未配置同步设备（TREEHAZARD_SYNC_DEVICE_DDNS）")

// ErrSyncBusy 已有同步轮次进行中（手动/排程/空闲共用核心 Run 的互斥锁）。
var ErrSyncBusy = errors.New("树障统计表同步中，请稍后再试")

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

// SyncNotifier 同步结果站内通知（可注入；nil 跳过通知不影响同步）。
type SyncNotifier interface {
	Push(ctx context.Context, targets []string, title, content string) error
}

// 同步指令文件与命名常量（与 sync.js 一致）。
const (
	syncCmdFile        = "tree_sync_cmd.json"
	syncLockReason     = "树障统计表同步中（预计 10 分钟内恢复）"
	syncKindNameManual = "手动"
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

// syncCmdTTL 指令有效期：单次等待窗口 10 分钟 + 5 分钟余量。服务端取回一次即结束，
// 过期指令客户端会拒绝执行，避免服务端已判失败后仍回写（与服务端等待口径对齐）。
const syncCmdTTL = usbcmd.DefaultResultWait + 5*time.Minute

// syncCmdPayload 指令文件内容（统一协议；保留 cycle_id/kind/file 旧字段供滚动升级）。
func syncCmdPayload(cycle, kind, fileName string, route syncRoute, now time.Time) []byte {
	params := map[string]any{
		"team_id": route.teamID, "team_name": route.teamName,
	}
	return usbcmd.PayloadForTarget("tree_sync", cycle, kind, fileName, params,
		route.targetNode, route.clusterID, now, syncCmdTTL)
}

// syncRecordMap 服务端记录 → diff 比对 map（15 业务列 + status + 同步点时间戳）。
func syncRecordMap(r Record) map[string]any {
	m := map[string]any{
		"id": r.ID, "voltage_level": r.VoltageLevel, "line_name": r.LineName, "section": r.Section,
		"tree_species": r.TreeSpecies, "tree_count": r.TreeCount, "dist_small": r.DistSmall,
		"dist_big": r.DistBig, "diameter": r.Diameter, "horizontal": r.Horizontal, "vertical": r.Vertical,
		"vertical_num": r.VerticalNum, "finder_name": r.FinderName, "found_at": r.FoundAt,
		"handler_name": r.HandlerName, "handle_note": r.HandleNote, "handled_at": r.HandledAt,
		"status": r.Status,
	}
	if r.SyncedAt != nil {
		m["synced_at"] = r.SyncedAt.Format("2006-01-02 15:04:05")
	} else {
		m["synced_at"] = ""
	}
	m["updated_at"] = r.UpdatedAt.Format("2006-01-02 15:04:05")
	return m
}

// applyIntranetDiff 差异应用：
// updates 内网值覆盖 15 业务列（+vertical_num/status/synced_at，WHERE 带 team_id 防越班）；
// news 以 source='intranet' 插入（crane 双 NULL，pole 从区段文本解析，synced_at=NOW）；
// conflicts 落 treehazard_conflict 留档（人工二选一解决，本轮不改记录）。
func (m *Module) applyIntranetDiff(ctx context.Context, teamID int64, result DiffResult) error {
	for _, u := range result.Updates {
		f := u.Fields
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, tree_species = ?, tree_count = ?,
			   dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?, finder_name = ?, found_at = ?,
			   handler_name = ?, handle_note = ?, handled_at = ?, vertical_num = ?, status = ?, synced_at = NOW()
			 WHERE id = ? AND team_id = ?`,
			f["voltage_level"], f["line_name"], f["section"], f["tree_species"], f["tree_count"],
			f["dist_small"], f["dist_big"], f["diameter"], f["horizontal"], f["vertical"], f["finder_name"], f["found_at"],
			f["handler_name"], f["handle_note"], f["handled_at"], f["vertical_num"], f["status"], u.ID, teamID); err != nil {
			return err
		}
	}
	for _, row := range result.News {
		f := intranetFields(row)
		poleFrom, poleTo := parseSectionPoles(normStr(row["section"]))
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to,
			   tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
			   finder_name, found_at, handler_name, handle_note, handled_at, status, source, synced_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'intranet', NOW())`,
			teamID, f["voltage_level"], f["line_name"], f["section"], poleFrom, poleTo,
			f["tree_species"], f["tree_count"], f["dist_small"], f["dist_big"], f["diameter"], f["horizontal"],
			f["vertical"], f["vertical_num"], f["finder_name"], f["found_at"], f["handler_name"], f["handle_note"],
			f["handled_at"], f["status"]); err != nil {
			return err
		}
	}
	for _, c := range result.Conflicts {
		serverSnap, _ := json.Marshal(c.Server)
		intranetSnap, _ := json.Marshal(c.Intranet)
		if _, err := m.DB.ExecContext(ctx,
			`INSERT INTO treehazard_conflict (team_id, record_id, server_snapshot, intranet_snapshot) VALUES (?, ?, ?, ?)`,
			teamID, c.ID, string(serverSnap), string(intranetSnap)); err != nil {
			return err
		}
	}
	return nil
}

// decideIntranetAction 下发判定（纯函数）：吸收了内网改动（更新/新增）、有已裁决驳回待收敛的行，
// 或服务端存在「同步点之后又改过 / 从未同步」的记录 → 需下发最新表。
func decideIntranetAction(result DiffResult, serverRecords []map[string]any) (needPush bool, reason string) {
	if absorbed := len(result.Updates) + len(result.News); absorbed > 0 {
		return true, fmt.Sprintf("吸收内网改动 %d 条", absorbed)
	}
	if result.Adjudicated > 0 {
		return true, fmt.Sprintf("裁决待收敛 %d 条", result.Adjudicated)
	}
	// 内网表是完整快照；服务端有而内网缺的行必须下发完整表补齐，
	// 否则 87/111 这类缺表会被误判为「无差异」而永远不更新内网。
	if result.Missing > 0 {
		return true, fmt.Sprintf("内网缺行 %d 条", result.Missing)
	}
	changed := 0
	for _, r := range serverRecords {
		syncedAt := diffParseDateMillis(r["synced_at"])
		if syncedAt == 0 {
			changed++ // 从未同步
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

// markSyncedAfterPush 下发后落同步点：全量记录 synced_at=NOW()；pending 冲突行除外
// （保人工裁决语义——代价是冲突未决每轮仍判服务端变更触发下发，可接受）。
func (m *Module) markSyncedAfterPush(ctx context.Context, teamID int64) error {
	_, err := m.DB.ExecContext(ctx,
		`UPDATE treehazard_record SET synced_at = NOW()
		 WHERE team_id = ? AND id NOT IN (
		   SELECT record_id FROM treehazard_conflict WHERE team_id = ? AND status = 'pending' AND record_id IS NOT NULL)`,
		teamID, teamID)
	return err
}

// cleanupUsb 取件收尾清扫（同步完不保留本操作产物）：删 U 盘根目录全部树表残留
// （任意「*树木统计表.xlsx」与指令文件）；best-effort，失败仅返回错误不阻断同步。
func cleanupUsb(ctx context.Context, link UsbLink) error {
	files, err := link.ListFiles(ctx)
	if err != nil {
		return err
	}
	victims := make([]string, 0)
	for _, n := range files {
		if strings.HasSuffix(n, "树木统计表.xlsx") || n == syncCmdFile {
			victims = append(victims, n)
		}
	}
	if len(victims) == 0 {
		return nil
	}
	return link.DeleteFiles(ctx, victims)
}

// pushLatest 下发最新表：与手动导出单表同口径（排序 + 阈值标红）→ push 表文件 + replace 指令 →
// 重挂回共享态，返回 replace 指令 id 供调用方等待被控机结果。
func (m *Module) pushLatest(ctx context.Context, link UsbLink, teamID int64, teamName string, records []Record, route syncRoute) (string, error) {
	fileName := exportFileName(teamName, "")
	threshold, err := m.fetchThreshold(ctx, teamID)
	if err != nil {
		return "", fmt.Errorf("读取标红阈值失败: %w", err)
	}
	buf, err := buildWorkbookWithThreshold(teamName, records, false, nil, threshold)
	if err != nil {
		return "", err
	}
	replaceID := usbcmd.NewID(time.Now())
	if err := link.PushFile(ctx, fileName, buf); err != nil {
		return "", err
	}
	if err := link.PushFile(ctx, syncCmdFile, syncCmdPayload(replaceID, "replace", fileName, route, time.Now())); err != nil {
		return "", err
	}
	if err := link.MountToHost(ctx); err != nil {
		return "", err
	}
	return replaceID, nil
}

// fetchIntranetPhase 取件与比对回写：下载内网表 → diff（带已裁决驳回快照防反转）→ 应用 →
// 收尾清盘 → 视差异决定收尾或下发最新表（收尾顺序统一「先清扫、后下发」）。
func (m *Module) fetchIntranetPhase(ctx context.Context, link UsbLink, teamID int64, teamName string, route syncRoute) (IntranetSyncCounts, string, error) {
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
	drows, err := m.DB.QueryContext(ctx, `SELECT id FROM treehazard_deleted_id WHERE team_id = ?`, teamID)
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
		// 内网无表（首次同步/被控机未导出）：清残留后直接下发最新表（含空表），内网以服务端为准起步
		_ = cleanupUsb(ctx, link)
		replaceID, err := m.pushLatest(ctx, link, teamID, teamName, records, route)
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
	// 已裁决驳回（keep=server）的冲突快照：裁决后内网旧表若未收敛（行与快照全等），驳回旧值不回灌防反转裁决。
	// 已裁决驳回快照查询失败同样终止：否则可能把已裁决的旧值重新回灌、反转人工裁决。
	adjudicated := make([]Adjudicated, 0)
	aRows, err := m.DB.QueryContext(ctx,
		`SELECT record_id, intranet_snapshot FROM treehazard_conflict
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
	needPush, reason := decideIntranetAction(result, serverRecords)
	counts = IntranetSyncCounts{
		Updates: len(result.Updates), News: len(result.News), Conflicts: len(result.Conflicts),
		Skips: result.Skips, Adjudicated: result.Adjudicated, Missing: result.Missing,
	}
	if err := cleanupUsb(ctx, link); err != nil {
		// 收尾清扫 best-effort：失败仅记日志不阻断同步（下一轮收尾再清，同 Node cleanupUsb 口径）
		log.Printf("[树障同步] U 盘残留清扫失败（继续收尾）：%v\n", err)
	}
	if !needPush {
		// 无差异：消费完的表连同残留一并清盘后重挂回共享态收尾
		return counts, "", link.MountToHost(ctx)
	}
	fresh, err := m.loadTeamRecords(ctx, teamID) // 回写后的最新全量
	if err != nil {
		return counts, "", err
	}
	replaceID, err := m.pushLatest(ctx, link, teamID, teamName, fresh, route)
	if err != nil {
		return counts, "", err
	}
	counts.Pushed = true
	_ = reason
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

// syncLogAndNotify 写同步日志 + 结果通知（超管 + 本班班组管理员）；通知/日志失败仅记录不影响结果。
func (m *Module) syncLogAndNotify(ctx context.Context, teamID int64, teamName, kind string, ok bool, text string) {
	if _, err := m.DB.ExecContext(ctx,
		`INSERT INTO treehazard_sync_log (team_id, kind, result) VALUES (?, ?, ?)`, teamID, kind, text); err != nil {
		_ = err
	}
	if m.SyncNotifier == nil {
		return
	}
	title := fmt.Sprintf("树障统计表同步完成（%s）", teamName)
	if !ok {
		title = "树障统计表同步失败"
	}
	kindName := syncKindNameManual
	if kind != "manual" {
		kindName = kind
	}
	_ = m.SyncNotifier.Push(ctx, []string{"admin", "team_admin"}, title,
		fmt.Sprintf("方式：%s\n%s", kindName, text))
}

// beginSync 抢占某班组的同步重入锁；该班组已在运行返回 false。
func (m *Module) beginSync(teamID int64) bool {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.syncRunning == nil {
		m.syncRunning = map[int64]bool{}
	}
	if m.syncRunning[teamID] {
		return false
	}
	m.syncRunning[teamID] = true
	return true
}

// endSync 释放某班组的同步重入锁。
func (m *Module) endSync(teamID int64) {
	m.syncMu.Lock()
	delete(m.syncRunning, teamID)
	m.syncMu.Unlock()
}

// syncRoute 一轮同步的目标（legacy = 旧单设备，不填 target）。
type syncRoute struct {
	teamID     int64
	teamName   string
	targetNode string
	clusterID  string
	hubID      string
	hub        usbcmd.HubRef
	hasBinding bool
	nodeReady  bool
	nodeReason string
}

// legacyTeam 旧版单设备模式解析 TREEHAZARD_SYNC_TEAM（默认检修一班）。
func (m *Module) legacyTeam(ctx context.Context) (int64, string, error) {
	teamName := m.configValue(ctx, "TREEHAZARD_SYNC_TEAM")
	if teamName == "" {
		teamName = "检修一班"
	}
	var teamID int64
	var matchedName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE name = ? AND status = 1`, teamName).
		Scan(&teamID, &matchedName); err != nil {
		return 0, "", fmt.Errorf("未找到同步班组「%s」（未建或已停用）", teamName)
	}
	return teamID, matchedName, nil
}

// resolveRoute 解析班组绑定；无绑定返回旧单设备路由（不报错）。
func (m *Module) resolveRoute(ctx context.Context, teamID int64, teamName string) (syncRoute, error) {
	route := syncRoute{
		teamID: teamID, teamName: teamName,
		clusterID: m.configValue(ctx, "INTRANET_CLUSTER_ID"),
	}
	if m.Cluster == nil {
		return route, nil
	}
	resolved, err := m.Cluster.ResolveTeamSync(ctx, teamID, "tree_sync")
	if errors.Is(err, cluster.ErrNoBinding) {
		return route, nil
	}
	if err != nil {
		return route, err
	}
	route.hasBinding = true
	route.targetNode = resolved.Node.NodeID
	route.hubID = resolved.Hub.ID
	route.hub = resolved.Hub.HubRef()
	route.nodeReady, route.nodeReason = resolved.Node.Ready(cluster.AppTreeSync, teamID)
	return route, nil
}

// syncEnabled 该班组是否可同步（绑定节点或旧单设备配置）。
func (m *Module) syncEnabled(ctx context.Context, teamID int64) (bool, string) {
	if m.Usb == nil {
		return false, "同步链路未接入（UsbLink 未注入）"
	}
	if m.Cluster != nil {
		resolved, err := m.Cluster.ResolveTeamSync(ctx, teamID, "tree_sync")
		if err == nil {
			ready, reason := resolved.Node.Ready(cluster.AppTreeSync, teamID)
			if !ready {
				return false, reason
			}
			return true, ""
		}
		if !errors.Is(err, cluster.ErrNoBinding) {
			return false, err.Error()
		}
	}
	if m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS") != "" {
		return true, ""
	}
	return false, ErrSyncNotConfigured.Error()
}

// RunIntranetCycle 旧版入口（按 TREEHAZARD_SYNC_TEAM）：保留给测试/旧排程调用。
func (m *Module) RunIntranetCycle(ctx context.Context, kind string) (IntranetSyncResult, error) {
	if m.Usb == nil && m.Cluster == nil {
		return IntranetSyncResult{OK: false, Message: ErrSyncNotConfigured.Error()}, ErrSyncNotConfigured
	}
	if m.Cluster == nil && m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS") == "" {
		return IntranetSyncResult{OK: false, Message: ErrSyncNotConfigured.Error()}, ErrSyncNotConfigured
	}
	teamID, teamName, err := m.legacyTeam(ctx)
	if err != nil {
		return IntranetSyncResult{OK: false, Message: "失败：" + err.Error()}, err
	}
	return m.runIntranetCycleNamed(ctx, teamID, teamName, kind)
}

// RunIntranetCycleForTeam 指定班组的完整同步轮次（手动/夜间共用）。
func (m *Module) RunIntranetCycleForTeam(ctx context.Context, teamID int64, kind string) (IntranetSyncResult, error) {
	var teamName string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT name FROM sys_team WHERE id = ? AND status = 1`, teamID).Scan(&teamName); err != nil {
		return IntranetSyncResult{OK: false, Message: "未找到同步班组"}, err
	}
	return m.runIntranetCycleNamed(ctx, teamID, teamName, kind)
}

// runIntranetCycleNamed 已解析班组名的轮次入口（避免旧测试/调用重复查 id）。
func (m *Module) runIntranetCycleNamed(ctx context.Context, teamID int64, teamName, kind string) (IntranetSyncResult, error) {
	if !m.beginSync(teamID) {
		return IntranetSyncResult{OK: false, Message: "失败：" + ErrSyncBusy.Error()}, ErrSyncBusy
	}
	defer m.endSync(teamID)
	return m.runTeamCycle(ctx, teamID, teamName, kind)
}

// RunNightCycles 夜间同步：有绑定则按班组夜间开关并发执行；无任何绑定回退旧单设备。
func (m *Module) RunNightCycles(ctx context.Context, kind string) (IntranetSyncResult, error) {
	if m.Cluster == nil {
		return m.RunIntranetCycle(ctx, kind)
	}
	has, err := m.Cluster.HasBindings(ctx, "tree_sync")
	if err != nil {
		return IntranetSyncResult{OK: false, Message: "读取班组绑定失败: " + err.Error()}, err
	}
	if !has {
		return m.RunIntranetCycle(ctx, kind)
	}
	teams, err := m.Cluster.ListNightTeams(ctx, "tree_sync")
	if err != nil {
		return IntranetSyncResult{OK: false, Message: "读取夜间班组失败: " + err.Error()}, err
	}
	if len(teams) == 0 {
		return IntranetSyncResult{OK: true, Message: "夜间同步未启用或未绑定节点"}, nil
	}
	results := make([]IntranetSyncResult, len(teams))
	errs := make([]error, len(teams))
	var wg sync.WaitGroup
	for i, team := range teams {
		wg.Add(1)
		go func(i int, team cluster.NightTeam) {
			defer wg.Done()
			results[i], errs[i] = m.RunIntranetCycleForTeam(ctx, team.ID, kind)
		}(i, team)
	}
	wg.Wait()
	okCount, failCount := 0, 0
	parts := make([]string, 0, len(teams))
	var firstErr error
	for i, team := range teams {
		if errs[i] != nil {
			failCount++
			if firstErr == nil {
				firstErr = errs[i]
			}
			parts = append(parts, fmt.Sprintf("%s：失败（%s）", team.Name, results[i].Message))
			continue
		}
		okCount++
		parts = append(parts, fmt.Sprintf("%s：%s", team.Name, results[i].Message))
	}
	message := fmt.Sprintf("夜间同步 %d 个班组：成功 %d，失败 %d；%s",
		len(teams), okCount, failCount, strings.Join(parts, "；"))
	return IntranetSyncResult{OK: failCount == 0, Message: message}, firstErr
}

// runTeamCycle 已持班组锁的同步实现：准备（snapshot 指令 + 挂载）→ 单次窗口等结果 →
// 取件比对回写/下发 → 等 replace 结果 → 收尾复挂 U 盘 → 日志通知。
func (m *Module) runTeamCycle(ctx context.Context, teamID int64, teamName, kind string) (IntranetSyncResult, error) {
	if kind == "" {
		kind = "manual"
	}
	route, err := m.resolveRoute(ctx, teamID, teamName)
	if err != nil {
		e := fmt.Errorf("解析同步节点失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if route.hasBinding && !route.nodeReady {
		e := fmt.Errorf("同步节点未就绪：%s", route.nodeReason)
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	sessionKey := ""
	if route.hasBinding {
		ctx = usbcmd.WithHub(ctx, route.hub)
		sessionKey = route.hubID
		// 未注册 Hub 设备（无 ddns）时回退旧单设备，必须共用同一把 legacy 锁。
		if route.hub.Ddns == "" {
			sessionKey = ""
		}
	}
	if err := usbcmd.DefaultSessions.Acquire(ctx, sessionKey); err != nil {
		return IntranetSyncResult{OK: false, Message: "失败：" + err.Error()}, err
	}
	defer usbcmd.DefaultSessions.Release(sessionKey)
	if m.Usb == nil {
		return IntranetSyncResult{OK: false, Message: "同步链路未接入（UsbLink 未注入）"},
			fmt.Errorf("%w：同步链路未接入", ErrSyncNotConfigured)
	}
	if !route.hasBinding && m.configValue(ctx, "TREEHAZARD_SYNC_DEVICE_DDNS") == "" {
		return IntranetSyncResult{OK: false, Message: ErrSyncNotConfigured.Error()}, ErrSyncNotConfigured
	}
	cycle := newSyncCycleID(time.Now())
	fileName := exportFileName(teamName, "")
	// 本轮会操作 U 盘（push 会断开共享）：无论成功/超时，收尾都把盘挂回被控机。
	defer usbcmd.RestoreShare(ctx, m.Usb)
	// 准备：push snapshot 指令 → 挂载 U 盘至被控机端。
	if err := m.Usb.PushFile(ctx, syncCmdFile,
		syncCmdPayload(cycle, "snapshot", fileName, route, time.Now())); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if err := m.Usb.MountToHost(ctx); err != nil {
		e := fmt.Errorf("准备阶段失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	policy := usbcmd.Once(usbcmd.DefaultResultWait)
	if m.SyncCmdPolicy != nil {
		policy = *m.SyncCmdPolicy
	}
	res, err := usbcmd.Await(ctx, m.Usb, "tree_sync", cycle, policy)
	if err != nil {
		e := fmt.Errorf("等待被控机执行结果失败：%w", err)
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	if !res.OK {
		e := fmt.Errorf("被控机导出失败：%s", res.Summary())
		m.syncLogAndNotify(ctx, teamID, teamName, kind, false, buildIntranetSummary(IntranetSyncCounts{}, e))
		return IntranetSyncResult{OK: false, Message: "失败：" + e.Error()}, e
	}
	// 顺带读取 Hub 写的节点表，刷新服务端绑定页缓存（失败不影响本轮同步）。
	if m.Cluster != nil {
		if _, readErr := m.Cluster.ReadNodes(ctx, m.Usb, route.hubID); readErr != nil {
			log.Printf("[tree_sync] 读取集群节点表失败: %v", readErr)
		}
	}
	// 取件比对回写/下发。
	counts, replaceID, err := m.fetchIntranetPhase(ctx, m.Usb, teamID, teamName, route)
	var replaceErr error
	if err == nil && replaceID != "" {
		rres, rerr := usbcmd.Await(ctx, m.Usb, "tree_sync", replaceID, policy)
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
	m.syncLogAndNotify(ctx, teamID, teamName, kind, finalErr == nil, text)
	if finalErr != nil {
		return IntranetSyncResult{OK: false, Message: text, Counts: counts}, finalErr
	}
	return IntranetSyncResult{OK: true, Message: text, Counts: counts}, nil
}

// IntranetSyncStatus 指定班组同步状态；teamID<=0 走旧配置班组。
func (m *Module) IntranetSyncStatus(ctx context.Context, teamID int64) map[string]any {
	if teamID <= 0 {
		id, _, err := m.legacyTeam(ctx)
		if err != nil {
			return map[string]any{"enabled": false, "running": false, "last": nil, "nextNight": nil}
		}
		teamID = id
	}
	var teamName string
	_ = m.DB.QueryRowContext(ctx, `SELECT name FROM sys_team WHERE id = ?`, teamID).Scan(&teamName)
	enabled, reason := m.syncEnabled(ctx, teamID)
	m.syncMu.Lock()
	running := m.syncRunning[teamID]
	m.syncMu.Unlock()
	out := map[string]any{
		"enabled": enabled, "running": running, "last": nil, "nextNight": nil,
		"team": map[string]any{"id": teamID, "name": teamName}, "reason": reason,
	}
	if node := m.boundNode(ctx, teamID); node != nil {
		out["node"] = node
	}
	if !enabled {
		return out
	}
	var kind, result, createdAt string
	if err := m.DB.QueryRowContext(ctx,
		`SELECT kind, result, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')
		 FROM treehazard_sync_log WHERE team_id = ? ORDER BY id DESC LIMIT 1`, teamID).
		Scan(&kind, &result, &createdAt); err == nil {
		out["last"] = map[string]any{"kind": kind, "result": result, "created_at": createdAt}
	}
	fetchTime := m.configValue(ctx, "TREEHAZARD_SYNC_FETCH_TIME")
	if fetchTime == "" {
		fetchTime = "00:00"
	}
	hh, mm := 0, 0
	fmt.Sscanf(fetchTime, "%d:%d", &hh, &mm)
	now := time.Now()
	cn := now.UTC().Add(8 * time.Hour)
	next := time.Date(cn.Year(), cn.Month(), cn.Day(), hh, mm, 0, 0, time.UTC)
	if !next.After(cn) {
		next = next.Add(24 * time.Hour)
	}
	out["nextNight"] = next.Format("2006-01-02T15:04:05+08:00")
	return out
}

// boundNode 班组绑定节点展示；未绑定/解析失败返回 nil。
func (m *Module) boundNode(ctx context.Context, teamID int64) map[string]any {
	if m.Cluster == nil {
		return nil
	}
	resolved, err := m.Cluster.ResolveTeamSync(ctx, teamID, "tree_sync")
	if err != nil {
		return nil
	}
	ready, reason := resolved.Node.Ready("tree_sync", teamID)
	return map[string]any{
		"id": resolved.Node.NodeID, "name": resolved.Node.Name,
		"online": resolved.Node.Online, "ready": ready, "reason": reason,
	}
}

// handleSyncPushNow POST /sync/push-now：按当前班组触发一轮完整同步（异步）。
func (m *Module) handleSyncPushNow(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if enabled, reason := m.syncEnabled(ctx, team.ID); !enabled {
		writeFail(w, http.StatusServiceUnavailable, codeNotConfigured, reason, nil)
		return
	}
	if !m.beginSync(team.ID) {
		writeFail(w, http.StatusLocked, 42301, "同步进行中，请稍后再试", nil)
		return
	}
	go func() {
		defer m.endSync(team.ID)
		_, _ = m.runTeamCycle(context.Background(), team.ID, team.Name, "manual")
	}()
	writeOK(w, map[string]any{"started": true}, "同步已开始")
}

// handleSyncStatus GET /sync/status：按当前班组返回 {enabled,running,last,nextNight,node}。
func (m *Module) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	team, _ := teamFrom(r.Context())
	writeOK(w, m.IntranetSyncStatus(r.Context(), team.ID), "")
}
