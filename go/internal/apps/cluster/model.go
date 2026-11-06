// Package cluster 内网节点集群的服务端数据面：节点快照缓存、Hub 设备注册、
// 班组↔同步节点绑定与班组夜间开关。节点本身由客户端集群服务维护，本包只读
// 盘根 cluster_nodes.json 并落库，不做网络直连。
package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 盘根节点表文件名（Hub 写入，服务端每次 U 盘会话读取）。
const NodesFileName = "cluster_nodes.json"

// 同步应用标识（与客户端 app.json / RPC app_id 一致）。
const (
	AppTreeSync     = "tree_sync"
	AppCrossingSync = "crossing_sync"
)

var AppNames = map[string]string{
	AppTreeSync:     "树障统计表",
	AppCrossingSync: "交跨统计表",
}

var (
	ErrNoBinding    = errors.New("该班组未绑定同步节点")
	ErrNodeMissing  = errors.New("绑定节点不在集群节点表中")
	ErrTeamMismatch = errors.New("节点归属班组与绑定班组不一致")
)

// SyncTable 节点本地某应用表配置/文件就绪情况。
type SyncTable struct {
	TeamID     int64 `json:"team_id"`
	Configured bool  `json:"configured"`
	Exists     bool  `json:"exists"`
}

// Node 集群节点（来自客户端 cluster_nodes.json）。
type Node struct {
	NodeID     string               `json:"node_id"`
	Name       string               `json:"name"`
	Role       string               `json:"role"`
	TeamID     int64                `json:"team_id"`
	TeamName   string               `json:"team_name"`
	Platform   string               `json:"platform"`
	Arch       string               `json:"arch"`
	Version    string               `json:"version"`
	Port       int                  `json:"port"`
	IP         string               `json:"ip"`
	Online     bool                 `json:"online"`
	LastSeen   string               `json:"last_seen"`
	HomeHub    string               `json:"home_hub"`
	SyncTables map[string]SyncTable `json:"sync_tables"`
}

// NodesFile Hub 写盘根的节点快照。
type NodesFile struct {
	Version   int    `json:"version"`
	ClusterID string `json:"cluster_id"`
	UpdatedAt string `json:"updated_at"`
	Nodes     []Node `json:"nodes"`
}

// Hub GLKVM 设备注册（home_hub 对应节点快照里的 hub 节点 id）。
type Hub struct {
	ID      string `json:"hub_id"`
	Name    string `json:"name"`
	Ddns    string `json:"ddns"`
	Mac     string `json:"mac"`
	KVMUser string `json:"kvm_user"`
	Enabled bool   `json:"enabled"`
}

// Binding 班组同步节点绑定。
type Binding struct {
	TeamID int64  `json:"team_id"`
	App    string `json:"app"`
	NodeID string `json:"node_id"`
}

// Resolved 一轮班组同步解析结果。
type Resolved struct {
	TeamID   int64
	TeamName string
	App      string
	Node     Node
	Hub      Hub
}

// ParseNodesFile 解析并归一客户端节点快照；版本/空节点表按错误处理。
func ParseNodesFile(raw []byte) (NodesFile, error) {
	var file NodesFile
	if len(raw) == 0 {
		return file, errors.New("节点表为空")
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return file, fmt.Errorf("节点表 JSON 解析失败: %w", err)
	}
	if file.Version != 1 {
		return file, fmt.Errorf("不支持的节点表版本: %d", file.Version)
	}
	out := make([]Node, 0, len(file.Nodes))
	for _, node := range file.Nodes {
		node.NodeID = strings.TrimSpace(node.NodeID)
		if node.NodeID == "" || len(node.NodeID) > 64 {
			continue
		}
		node.Name = strings.TrimSpace(node.Name)
		node.TeamName = strings.TrimSpace(node.TeamName)
		node.HomeHub = strings.TrimSpace(node.HomeHub)
		if node.SyncTables == nil {
			node.SyncTables = map[string]SyncTable{}
		}
		if node.Port < 0 {
			node.Port = 0
		}
		out = append(out, node)
	}
	file.Nodes = out
	return file, nil
}

// Ready 节点某应用是否可用于该班组同步。
func (n Node) Ready(app string, teamID int64) (bool, string) {
	if !n.Online {
		return false, "节点离线"
	}
	if n.TeamID != teamID {
		return false, "节点归属班组不一致"
	}
	table, ok := n.SyncTables[app]
	if !ok || !table.Configured {
		return false, "节点未配置该应用的内网表路径"
	}
	if !table.Exists {
		return false, "节点内网表文件不存在"
	}
	return true, ""
}
