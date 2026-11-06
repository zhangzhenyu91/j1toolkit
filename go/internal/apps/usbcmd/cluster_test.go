package usbcmd

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPayloadForTarget(t *testing.T) {
	raw := PayloadForTarget("tree_sync", "20261106-1200-abcd", "snapshot",
		"表.xlsx", map[string]any{"team_id": 3}, "node-1", "team-a",
		time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), 10*time.Minute)
	var cmd Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cmd.Target == nil || cmd.Target.NodeID != "node-1" {
		t.Fatalf("target 未写入: %+v", cmd.Target)
	}
	if cmd.Cluster != "team-a" || cmd.Params["team_id"] != float64(3) {
		t.Fatalf("cluster/params 未写入: %+v", cmd)
	}
	if cmd.ExpiresAt == "" {
		t.Fatalf("ttl 未写入")
	}
}

func TestParseResultRelayFields(t *testing.T) {
	raw := []byte(`{"version":1,"id":"x","app":"tree_sync","ok":true,
		"target":"node-1","node":"node-1","relayed_by":"hub-1"}`)
	res, err := ParseResult(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Target != "node-1" || res.Node != "node-1" || res.RelayedBy != "hub-1" {
		t.Fatalf("转发字段未保留: %+v", res)
	}
}

func TestSessionPoolSeparatesKeys(t *testing.T) {
	pool := NewSessionPool()
	ctx := context.Background()
	if err := pool.Acquire(ctx, "hub-a"); err != nil {
		t.Fatalf("抢占失败: %v", err)
	}
	if pool.TryAcquire("hub-a") {
		t.Fatalf("同一 Hub 不应重复抢占")
	}
	if !pool.TryAcquire("hub-b") {
		t.Fatalf("不同 Hub 应可并行")
	}
	pool.Release("hub-b")
	pool.Release("hub-a")
	if !pool.TryAcquire("hub-a") {
		t.Fatalf("释放后应可再抢占")
	}
	pool.Release("hub-a")
}

func TestHubContext(t *testing.T) {
	ctx := WithHub(context.Background(), HubRef{ID: "hub-1", Ddns: "dev"})
	hub, ok := HubFrom(ctx)
	if !ok || hub.ID != "hub-1" || hub.Ddns != "dev" {
		t.Fatalf("Hub context 丢失: %+v %v", hub, ok)
	}
	if _, ok := HubFrom(context.Background()); ok {
		t.Fatalf("无 Hub 时应返回 false")
	}
}
