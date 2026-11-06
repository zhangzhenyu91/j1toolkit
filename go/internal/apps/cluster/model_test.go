package cluster

import (
	"encoding/json"
	"testing"
)

func TestParseNodesFile(t *testing.T) {
	raw, _ := json.Marshal(NodesFile{
		Version:   1,
		ClusterID: "team-a",
		Nodes: []Node{
			{
				NodeID: "node-1", Name: "一号机", Role: "worker", TeamID: 3,
				Online: true, Port: 17890, HomeHub: "hub-1",
				SyncTables: map[string]SyncTable{
					AppTreeSync: {TeamID: 3, Configured: true, Exists: true},
				},
			},
			{NodeID: ""},
		},
	})
	file, err := ParseNodesFile(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(file.Nodes) != 1 || file.Nodes[0].NodeID != "node-1" {
		t.Fatalf("空 node_id 应被丢弃，got %+v", file.Nodes)
	}
	if file.Nodes[0].SyncTables[AppTreeSync].TeamID != 3 {
		t.Fatalf("sync_tables 未保留")
	}
}

func TestParseNodesFileRejects(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		[]byte("{"),
		[]byte(`{"version":2,"nodes":[]}`),
	} {
		if _, err := ParseNodesFile(raw); err == nil {
			t.Fatalf("应拒绝: %s", raw)
		}
	}
}

func TestNodeReady(t *testing.T) {
	node := Node{
		NodeID: "node-1", TeamID: 3, Online: true,
		SyncTables: map[string]SyncTable{
			AppTreeSync: {TeamID: 3, Configured: true, Exists: true},
		},
	}
	if ready, reason := node.Ready(AppTreeSync, 3); !ready {
		t.Fatalf("应就绪: %s", reason)
	}
	if ready, _ := node.Ready(AppCrossingSync, 3); ready {
		t.Fatalf("未配置的应用不应就绪")
	}
	if ready, _ := node.Ready(AppTreeSync, 9); ready {
		t.Fatalf("班组不一致不应就绪")
	}
	node.Online = false
	if ready, _ := node.Ready(AppTreeSync, 3); ready {
		t.Fatalf("离线不应就绪")
	}
}

func TestHubRef(t *testing.T) {
	ref := Hub{ID: "hub-1", Ddns: "dev.example", Mac: "aa", KVMUser: "admin"}.HubRef()
	if ref.ID != "hub-1" || ref.Ddns != "dev.example" || ref.User != "admin" {
		t.Fatalf("HubRef 映射错误: %+v", ref)
	}
}
