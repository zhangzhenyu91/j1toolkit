package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"shade/internal/apps/usbcmd"
)

// ErrNodesFileMissing 盘根没有 cluster_nodes.json（Hub 尚未写入/未启用集群）。
var ErrNodesFileMissing = errors.New("U 盘上没有集群节点表（Hub 可能未启用集群）")

// Reader 只读 U 盘文件的最小面（tree/crossing UsbLink 均满足）。
type Reader interface {
	DownloadFile(ctx context.Context, name string) ([]byte, error)
}

// Store 集群节点/班组绑定/Hub 数据访问。
type Store struct {
	DB *sql.DB
}

// NewStore 构造数据访问层。
func NewStore(db *sql.DB) *Store {
	return &Store{DB: db}
}

const schemaDDL = `
CREATE TABLE IF NOT EXISTS intranet_hub (
  hub_id VARCHAR(64) NOT NULL PRIMARY KEY,
  name VARCHAR(128) NOT NULL DEFAULT '',
  ddns VARCHAR(255) NOT NULL DEFAULT '',
  mac VARCHAR(64) NOT NULL DEFAULT '',
  kvm_user VARCHAR(128) NOT NULL DEFAULT '',
  enabled TINYINT NOT NULL DEFAULT 1,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='内网 Hub/GLKVM 设备注册';

CREATE TABLE IF NOT EXISTS cluster_node (
  node_id VARCHAR(64) NOT NULL PRIMARY KEY,
  name VARCHAR(128) NOT NULL DEFAULT '',
  role VARCHAR(16) NOT NULL DEFAULT '',
  team_id BIGINT NOT NULL DEFAULT 0,
  team_name VARCHAR(128) NOT NULL DEFAULT '',
  platform VARCHAR(32) NOT NULL DEFAULT '',
  arch VARCHAR(32) NOT NULL DEFAULT '',
  version VARCHAR(64) NOT NULL DEFAULT '',
  port INT NOT NULL DEFAULT 0,
  ip VARCHAR(64) NOT NULL DEFAULT '',
  online TINYINT NOT NULL DEFAULT 0,
  last_seen VARCHAR(64) NOT NULL DEFAULT '',
  home_hub VARCHAR(64) NOT NULL DEFAULT '',
  sync_tables JSON NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_team (team_id),
  KEY idx_hub (home_hub)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='内网集群节点快照缓存';

CREATE TABLE IF NOT EXISTS team_sync_binding (
  team_id BIGINT NOT NULL,
  app VARCHAR(32) NOT NULL,
  node_id VARCHAR(64) NOT NULL,
  enabled TINYINT NOT NULL DEFAULT 1,
  updated_by BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (team_id, app),
  KEY idx_node (node_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='班组同步节点绑定';

CREATE TABLE IF NOT EXISTS team_sync_setting (
  team_id BIGINT NOT NULL PRIMARY KEY,
  night_enabled TINYINT NOT NULL DEFAULT 0,
  updated_by BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='班组同步夜间开关';
`

// EnsureSchema 建集群相关表（幂等）。
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("数据库未接入")
	}
	// MySQL 驱动默认未开 multiStatements；逐条执行，避免整段失败。
	for _, ddl := range strings.Split(schemaDDL, ";") {
		ddl = strings.TrimSpace(ddl)
		if ddl == "" {
			continue
		}
		if _, err := s.DB.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

// fileHomeHub 从节点表推断写入它的 Hub；客户端 nodes_file 会给所有节点写 home_hub。
func fileHomeHub(file NodesFile) string {
	for _, node := range file.Nodes {
		if node.HomeHub != "" {
			return node.HomeHub
		}
	}
	return ""
}

// UpsertNodes 用指定 Hub 的快照更新节点缓存：仅该 Hub 名下未出现的节点置离线，
// 其他 Hub 的节点保持各自最后一次快照状态（多 Hub/跨班组共用时不能互相踢下线）。
func (s *Store) UpsertNodes(ctx context.Context, hubID string, file NodesFile) error {
	if s == nil || s.DB == nil {
		return errors.New("数据库未接入")
	}
	if hubID == "" {
		hubID = fileHomeHub(file)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	reset := `UPDATE cluster_node SET online = 0`
	resetArgs := []any{}
	if hubID != "" {
		reset += ` WHERE home_hub = ?`
		resetArgs = append(resetArgs, hubID)
	}
	if _, err := tx.ExecContext(ctx, reset, resetArgs...); err != nil {
		return err
	}
	for _, node := range file.Nodes {
		tables, _ := json.Marshal(node.SyncTables)
		online := 0
		if node.Online {
			online = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cluster_node
			   (node_id, name, role, team_id, team_name, platform, arch, version,
			    port, ip, online, last_seen, home_hub, sync_tables, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())
			 ON DUPLICATE KEY UPDATE
			   name=VALUES(name), role=VALUES(role), team_id=VALUES(team_id),
			   team_name=VALUES(team_name), platform=VALUES(platform), arch=VALUES(arch),
			   version=VALUES(version), port=VALUES(port), ip=VALUES(ip),
			   online=VALUES(online), last_seen=VALUES(last_seen),
			   home_hub=VALUES(home_hub), sync_tables=VALUES(sync_tables), updated_at=NOW()`,
			node.NodeID, node.Name, node.Role, node.TeamID, node.TeamName,
			node.Platform, node.Arch, node.Version, node.Port, node.IP,
			online, node.LastSeen, node.HomeHub, string(tables)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const nodeColumns = `node_id, name, role, team_id, team_name, platform, arch, version,
	port, ip, online, last_seen, home_hub, sync_tables`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNode(row rowScanner) (Node, error) {
	var node Node
	var online int
	var tables []byte
	if err := row.Scan(
		&node.NodeID, &node.Name, &node.Role, &node.TeamID, &node.TeamName,
		&node.Platform, &node.Arch, &node.Version, &node.Port, &node.IP,
		&online, &node.LastSeen, &node.HomeHub, &tables,
	); err != nil {
		return Node{}, err
	}
	node.Online = online != 0
	node.SyncTables = map[string]SyncTable{}
	if len(tables) > 0 {
		_ = json.Unmarshal(tables, &node.SyncTables)
	}
	return node, nil
}

// ListNodes 全部缓存节点（在线优先）。
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM cluster_node ORDER BY online DESC, name, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// ListTeamNodes 某班组的缓存节点。
func (s *Store) ListTeamNodes(ctx context.Context, teamID int64) ([]Node, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM cluster_node WHERE team_id = ? ORDER BY online DESC, name, node_id`,
		teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// GetNode 按 id 读节点；不存在返回 nil, nil。
func (s *Store) GetNode(ctx context.Context, nodeID string) (*Node, error) {
	row := s.DB.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM cluster_node WHERE node_id = ?`, nodeID)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// ResolveTeamSync 解析班组某应用的绑定节点与 Hub 设备。
func (s *Store) ResolveTeamSync(ctx context.Context, teamID int64, app string) (Resolved, error) {
	var nodeID string
	err := s.DB.QueryRowContext(ctx,
		`SELECT node_id FROM team_sync_binding
		 WHERE team_id = ? AND app = ? AND enabled = 1`, teamID, app).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolved{}, ErrNoBinding
	}
	if err != nil {
		return Resolved{}, err
	}
	var teamName string
	if err := s.DB.QueryRowContext(ctx,
		`SELECT name FROM sys_team WHERE id = ? AND status = 1`, teamID).Scan(&teamName); err != nil {
		return Resolved{}, fmt.Errorf("班组不存在或已停用: %w", err)
	}
	node, err := s.GetNode(ctx, nodeID)
	if err != nil {
		return Resolved{}, err
	}
	if node == nil {
		return Resolved{}, ErrNodeMissing
	}
	if node.TeamID != teamID {
		return Resolved{}, ErrTeamMismatch
	}
	hub := Hub{ID: node.HomeHub}
	if node.HomeHub != "" {
		var found Hub
		var enabled int
		err := s.DB.QueryRowContext(ctx,
			`SELECT hub_id, name, ddns, mac, kvm_user, enabled
			 FROM intranet_hub WHERE hub_id = ?`, node.HomeHub).
			Scan(&found.ID, &found.Name, &found.Ddns, &found.Mac, &found.KVMUser, &enabled)
		if err == nil && enabled != 0 {
			found.Enabled = true
			hub = found
		}
	}
	return Resolved{
		TeamID: teamID, TeamName: teamName, App: app, Node: *node, Hub: hub,
	}, nil
}

// Bind 保存班组同步节点；nodeID 为空表示解绑。
func (s *Store) Bind(ctx context.Context, teamID int64, app, nodeID string, userID int64) error {
	if _, ok := AppNames[app]; !ok {
		return fmt.Errorf("不支持的同步应用: %s", app)
	}
	var status int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT status FROM sys_team WHERE id = ?`, teamID).Scan(&status); err != nil {
		return fmt.Errorf("班组不存在: %w", err)
	}
	if status != 1 {
		return errors.New("班组已停用")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		_, err := s.DB.ExecContext(ctx,
			`DELETE FROM team_sync_binding WHERE team_id = ? AND app = ?`, teamID, app)
		return err
	}
	node, err := s.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if node == nil {
		return ErrNodeMissing
	}
	if node.TeamID != teamID {
		return ErrTeamMismatch
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO team_sync_binding (team_id, app, node_id, enabled, updated_by, updated_at)
		 VALUES (?, ?, ?, 1, ?, NOW())
		 ON DUPLICATE KEY UPDATE node_id=VALUES(node_id), enabled=1,
		   updated_by=VALUES(updated_by), updated_at=NOW()`,
		teamID, app, nodeID, userID)
	return err
}

// ListBindings 某班组已启用的绑定（app → node_id）。
func (s *Store) ListBindings(ctx context.Context, teamID int64) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT app, node_id FROM team_sync_binding WHERE team_id = ? AND enabled = 1`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var app, nodeID string
		if err := rows.Scan(&app, &nodeID); err != nil {
			return nil, err
		}
		out[app] = nodeID
	}
	return out, rows.Err()
}

// NightEnabled 班组夜间同步开关。
func (s *Store) NightEnabled(ctx context.Context, teamID int64) (bool, error) {
	var enabled int
	err := s.DB.QueryRowContext(ctx,
		`SELECT night_enabled FROM team_sync_setting WHERE team_id = ?`, teamID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled != 0, nil
}

// SetNightEnabled 保存班组夜间同步开关。
func (s *Store) SetNightEnabled(ctx context.Context, teamID int64, enabled bool, userID int64) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO team_sync_setting (team_id, night_enabled, updated_by, updated_at)
		 VALUES (?, ?, ?, NOW())
		 ON DUPLICATE KEY UPDATE night_enabled=VALUES(night_enabled),
		   updated_by=VALUES(updated_by), updated_at=NOW()`,
		teamID, value, userID)
	return err
}

// NightTeam 夜间同步的班组。
type NightTeam struct {
	ID   int64
	Name string
}

// ListNightTeams 返回开启夜间同步且该应用有绑定的班组。
func (s *Store) ListNightTeams(ctx context.Context, app string) ([]NightTeam, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.id, t.name FROM team_sync_setting s
		 JOIN sys_team t ON t.id = s.team_id AND t.status = 1
		 WHERE s.night_enabled = 1
		   AND EXISTS (SELECT 1 FROM team_sync_binding b
		               WHERE b.team_id = s.team_id AND b.app = ? AND b.enabled = 1)
		 ORDER BY t.sort, t.id`, app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NightTeam{}
	for rows.Next() {
		var team NightTeam
		if err := rows.Scan(&team.ID, &team.Name); err != nil {
			return nil, err
		}
		out = append(out, team)
	}
	return out, rows.Err()
}

// HasBindings 该应用是否已有班组绑定（用于判断是否仍走旧单设备模式）。
func (s *Store) HasBindings(ctx context.Context, app string) (bool, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_sync_binding WHERE app = ? AND enabled = 1`, app).
		Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// CacheUpdatedAt 节点缓存最近更新时间（展示“上次读取”）。
func (s *Store) CacheUpdatedAt(ctx context.Context) (string, error) {
	var value sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT DATE_FORMAT(MAX(updated_at), '%Y-%m-%d %H:%i:%s') FROM cluster_node`).Scan(&value)
	if err != nil {
		return "", err
	}
	return value.String, nil
}

// ListHubs Hub 设备注册列表。
func (s *Store) ListHubs(ctx context.Context) ([]Hub, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT hub_id, name, ddns, mac, kvm_user, enabled FROM intranet_hub ORDER BY hub_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hub{}
	for rows.Next() {
		var hub Hub
		var enabled int
		if err := rows.Scan(&hub.ID, &hub.Name, &hub.Ddns, &hub.Mac, &hub.KVMUser, &enabled); err != nil {
			return nil, err
		}
		hub.Enabled = enabled != 0
		out = append(out, hub)
	}
	return out, rows.Err()
}

// UpsertHub 新增/更新 Hub 设备。
func (s *Store) UpsertHub(ctx context.Context, hub Hub) error {
	enabled := 0
	if hub.Enabled {
		enabled = 1
	}
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO intranet_hub (hub_id, name, ddns, mac, kvm_user, enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NOW())
		 ON DUPLICATE KEY UPDATE name=VALUES(name), ddns=VALUES(ddns), mac=VALUES(mac),
		   kvm_user=VALUES(kvm_user), enabled=VALUES(enabled), updated_at=NOW()`,
		hub.ID, hub.Name, hub.Ddns, hub.Mac, hub.KVMUser, enabled)
	return err
}

// ReadNodes 从 U 盘读取 cluster_nodes.json，并按 Hub 范围更新缓存。
func (s *Store) ReadNodes(ctx context.Context, reader Reader, hubID string) (NodesFile, error) {
	if reader == nil {
		return NodesFile{}, errors.New("U 盘链路未接入")
	}
	raw, err := reader.DownloadFile(ctx, NodesFileName)
	if err != nil {
		return NodesFile{}, err
	}
	if raw == nil {
		return NodesFile{}, ErrNodesFileMissing
	}
	file, err := ParseNodesFile(raw)
	if err != nil {
		return NodesFile{}, err
	}
	if err := s.UpsertNodes(ctx, hubID, file); err != nil {
		return NodesFile{}, err
	}
	return file, nil
}

// HubRef 把 Hub 注册转成桥接层 context 值；未注册时保留 ID 走旧配置回退。
func (h Hub) HubRef() usbcmd.HubRef {
	return usbcmd.HubRef{ID: h.ID, Ddns: h.Ddns, Mac: h.Mac, User: h.KVMUser}
}
