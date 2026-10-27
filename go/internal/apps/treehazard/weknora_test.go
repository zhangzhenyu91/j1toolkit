package treehazard

// WeKnora 知识库同步测试：假 WeKnoraClient / 假状态存储注入，不连真实服务。

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeWeKnora WeKnora API 假实现（记录调用并按脚本应答）。
type fakeWeKnora struct {
	uploaded    []string // 上传 fileName 序列
	deleted     []string
	batchCalls  [][]string
	listItems   []map[string]any
	uploadSeq   int
	conflictFor string // 该 fileName 首次上传返回 409（带已有 id）
	existingID  string
}

func (f *fakeWeKnora) ListKnowledge(context.Context, string, map[string]string) ([]map[string]any, error) {
	return f.listItems, nil
}

func (f *fakeWeKnora) UploadKnowledgeFile(_ context.Context, _ string, p WeKnoraUpload) (string, error) {
	f.uploaded = append(f.uploaded, p.FileName)
	if f.conflictFor != "" && p.FileName == f.conflictFor && f.uploadSeq == 0 {
		f.uploadSeq++
		return "", &WeKnoraDuplicateError{IDs: []string{f.existingID}}
	}
	f.uploadSeq++
	return fmt.Sprintf("k%d", f.uploadSeq), nil
}

func (f *fakeWeKnora) DeleteKnowledge(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeWeKnora) BatchDeleteKnowledge(_ context.Context, _ string, ids []string) error {
	f.batchCalls = append(f.batchCalls, ids)
	return nil
}

// fakeWeKnoraState 内存状态存储。
type fakeWeKnoraState struct{ st WeKnoraState }

func (f *fakeWeKnoraState) Load(context.Context) (WeKnoraState, error) {
	if f.st.Lines == nil {
		return WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}, nil
	}
	return f.st, nil
}

func (f *fakeWeKnoraState) Save(_ context.Context, st WeKnoraState) error {
	f.st = st
	return nil
}

// weknoraRecordRow 一条 treehazard_record 行（29 列，对齐 recordCols）。
func weknoraRecordRow(id int64, line string) []driver.Value {
	now := driver.Value(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	return []driver.Value{
		id, int64(1), "10", line, "3#-4#", nil, nil, "", "杨树", "3", "5", "5", "20", "2", "4.5", nil,
		"张三", "2026-10-01", "", "", nil, "unhandled", "crane", nil, nil, nil, nil, now, now,
	}
}

// weknoraEnv 构造同步测试模块（同步班组已建；records 预置行）。
func weknoraEnv(t *testing.T, records [][]driver.Value) (*Module, *fakeDB, *fakeWeKnora, *fakeWeKnoraState) {
	t.Helper()
	db, fake := openFake(t)
	fake.on(teamRule().match, teamRule().cols, teamRule().rows)
	fake.on("FROM treehazard_record WHERE team_id = ?", []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "position_note",
		"tree_species", "tree_count", "dist_small", "dist_big", "diameter", "horizontal", "vertical",
		"vertical_num", "finder_name", "found_at", "handler_name", "handle_note", "handled_at",
		"status", "source", "crane_session_id", "crane_record_id", "synced_at", "created_by",
		"created_at", "updated_at",
	}, records)
	m := New(db)
	m.Cfg = memConfig{
		"WEKNORA_TREEHAZARD_KB_ID": "kb-1",
		"WEKNORA_API_KEY":          "key-1",
		"TREEHAZARD_SYNC_TEAM":     "检修一班",
	}
	client := &fakeWeKnora{}
	state := &fakeWeKnoraState{st: WeKnoraState{Version: 2, Lines: map[string]WeKnoraLineState{}}}
	m.WeKnora = client
	m.WeKnoraState = state
	return m, fake, client, state
}

func TestRunWeKnoraSyncNotConfigured(t *testing.T) {
	db, _ := openFake(t)
	m := New(db)
	m.Cfg = memConfig{} // 未配置 kb id / api key
	_, err := m.RunWeKnoraSync(context.Background())
	if !errors.Is(err, ErrWeKnoraNotConfigured) {
		t.Fatalf("未配置应返回 ErrWeKnoraNotConfigured，got %v", err)
	}
}

func TestRunWeKnoraSyncUploadAndOrphanCleanup(t *testing.T) {
	m, _, client, state := weknoraEnv(t, [][]driver.Value{
		weknoraRecordRow(1, "甲线"),
		weknoraRecordRow(2, ""), // 空线路名归「未分组线路」
	})
	client.listItems = []map[string]any{
		{"id": "old-1", "file_name": "树木台账/废弃表.xlsx"}, // 孤儿 → 批量删
		{"id": "k1", "file_name": "树木台账/甲线.xlsx"},     // 本轮产物 → 保留
	}
	res, err := m.RunWeKnoraSync(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || res.Created != 2 || res.Updated != 0 || res.Skipped != 0 {
		t.Fatalf("首轮应新建 2 条线路表，got %+v", res)
	}
	if len(client.uploaded) != 2 {
		t.Fatalf("应上传 2 张线路表，got %v", client.uploaded)
	}
	if len(client.batchCalls) != 1 || len(client.batchCalls[0]) != 1 || client.batchCalls[0][0] != "old-1" {
		t.Fatalf("应批量清理孤儿 old-1，got %v", client.batchCalls)
	}
	// 第二轮：哈希未变 → 全部跳过，不再上传。
	res2, err := m.RunWeKnoraSync(context.Background())
	if err != nil {
		t.Fatalf("第二轮同步失败: %v", err)
	}
	if !res2.OK || res2.Skipped != 2 || len(client.uploaded) != 2 {
		t.Fatalf("第二轮应全部跳过不再上传，got %+v uploads=%v", res2, client.uploaded)
	}
	if len(state.st.Lines) != 2 {
		t.Fatalf("状态应记录 2 条线路，got %d", len(state.st.Lines))
	}
}

func TestRunWeKnoraSyncConflictRetry(t *testing.T) {
	m, _, client, _ := weknoraEnv(t, [][]driver.Value{weknoraRecordRow(1, "甲线")})
	client.conflictFor = "树木台账/甲线.xlsx"
	client.existingID = "dup-9"
	res, err := m.RunWeKnoraSync(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !res.OK || res.Deleted != 1 {
		t.Fatalf("409 兜底应删旧件 1 并成功，got %+v", res)
	}
	deletedOK := false
	for _, id := range client.deleted {
		if id == "dup-9" {
			deletedOK = true
		}
	}
	if !deletedOK {
		t.Fatalf("应删除同名旧件 dup-9，got %v", client.deleted)
	}
}

func TestRunWeKnoraSyncRemovedLineCleanup(t *testing.T) {
	m, _, _, state := weknoraEnv(t, nil)
	state.st.Lines = map[string]WeKnoraLineState{
		"旧线路": {Hash: "h", KnowledgeID: "gone-1", FileName: "旧线路.xlsx"},
	}
	res, err := m.RunWeKnoraSync(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("已下线路应删条目 1，got %+v", res)
	}
	if _, ok := state.st.Lines["旧线路"]; ok {
		t.Fatalf("已下线路状态项应清除")
	}
}
