package worklog

import (
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestAdminCanUpdateOtherTeamEntry(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("FROM worklog_entry e WHERE e.id = ?", []string{
		"team_id", "vehicle_id", "destination_id", "cross_team", "dispatch_order_no", "log_date",
	}, [][]driver.Value{{int64(2), int64(1), int64(2), int64(0), "PC-1", "2026-10-30"}})
	env.fake.on("FROM worklog_entry_member", []string{"member_id"}, nil)
	env.fake.on("FROM worklog_vehicle", []string{"status"}, [][]driver.Value{{int64(1)}})
	rec := env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 2, "destination_id": 2, "member_ids": []int64{},
	})
	if got := decodeEnv(t, rec); got.Code != codeOK {
		t.Fatalf("超管修改他班卡片应允许，实际 %d %s", got.Code, got.Message)
	}
	q, ok := env.fake.lastQuery("FROM worklog_entry e WHERE e.id = ?")
	if !ok || !strings.Contains(q.query, "? = ?") {
		t.Fatalf("超管按 ID 操作不应再套班组可见性过滤，实际 SQL=%v", q)
	}
	upd, ok := env.fake.lastExec("UPDATE worklog_entry SET patrol_content = ?, vehicle_id = ?")
	if !ok || len(upd.args) < 2 || upd.args[1] != int64(2) {
		t.Fatalf("超管跨班修改应落库 vehicle_id=2，实际 %v", upd.args)
	}
}

func TestAdminCanDeleteOtherTeamPhoto(t *testing.T) {
	env := newTestServer(t, identAdmin).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"cos_key", "entry_id", "team_id"},
		[][]driver.Value{{"team-2/photo.jpg", int64(7), int64(2)}})
	rec := env.do(t, "DELETE", "/api/v1/worklog/photos/9", nil)
	if got := decodeEnv(t, rec); got.Code != codeOK {
		t.Fatalf("超管删除他班照片应允许，实际 %d %s", got.Code, got.Message)
	}
	q, ok := env.fake.lastQuery("FROM worklog_photo p JOIN worklog_entry e")
	if !ok || !strings.Contains(q.query, "? = ?") {
		t.Fatalf("超管删除他班照片不应再套班组可见性过滤，实际 SQL=%v", q)
	}
}

func TestUserCannotUpdateOtherTeamEntry(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.onErr("FROM worklog_entry e WHERE e.id = ?", sql.ErrNoRows)
	rec := env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 1, "destination_id": 2, "member_ids": []int64{},
	})
	if got := decodeEnv(t, rec); got.Code != codeMissing {
		t.Fatalf("普通成员修改他班卡片仍应 404，实际 %d %s", got.Code, got.Message)
	}
	q, ok := env.fake.lastQuery("FROM worklog_entry e WHERE e.id = ?")
	if !ok || strings.Contains(q.query, "? = ?") || !strings.Contains(q.query, "cross_team = 1") {
		t.Fatalf("普通成员仍须保留班组与跨班可见性限制，实际 SQL=%v", q)
	}
}
