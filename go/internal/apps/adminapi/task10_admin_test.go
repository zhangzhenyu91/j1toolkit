package adminapi

import (
	"database/sql/driver"
	"net/http"
	"strings"
	"testing"
)

// MySQL 对同值 UPDATE 返回 affected=0；此时用户其实存在，不能误判 404。
func TestUpdateUserSameValuesNotNotFound(t *testing.T) {
	fake, _, srv := newTestEnv(t, adminIdent())
	fake.onExecExact("UPDATE sys_user SET", 0, 0)
	fake.on("SELECT id FROM sys_user WHERE id", []string{"id"}, [][]driver.Value{{int64(5)}})
	status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{"nickname": "同名"})
	if status != http.StatusOK {
		t.Fatalf("同值保存应成功而不是 404：status=%d env=%v", status, env)
	}
	wantCode(t, env, 0)
}

// 删班组引用检查必须覆盖树障/交跨/运维线路/微信应答监听群。
func TestDeleteTeamBlocksNewBusinessReferences(t *testing.T) {
	cases := []struct {
		table string
		want  string
	}{
		{"treehazard_record", "树障"},
		{"crossing_record", "交跨"},
		{"team_line", "运维线路"},
		{"wa_group", "微信"},
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			fake, mod, srv := newTestEnv(t, adminIdent())
			fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
			registerZeroCounts(fake)
			mod.HasTeamSafedayRecords = func(string) bool { return false }
			fake.on("COUNT(*) FROM "+tc.table+" WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(1)}})
			status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
			if status != http.StatusConflict {
				t.Fatalf("status=%d env=%v", status, env)
			}
			wantCode(t, env, float64(codeTeamInUse))
			msg, _ := env["message"].(string)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("提示应包含 %q，实际 %q", tc.want, msg)
			}
			if fake.execCount("DELETE FROM sys_team WHERE id") != 0 {
				t.Fatal("有引用时不得删除班组")
			}
		})
	}
}

// 任一引用命中时必须先返回 409，不得提前删除同步开关或班组。
func TestDeleteTeamChecksBeforeAnyWrite(t *testing.T) {
	fake, mod, srv := newTestEnv(t, adminIdent())
	fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
	registerZeroCounts(fake)
	mod.HasTeamSafedayRecords = func(string) bool { return false }
	// 最后一个引用检查命中：验证前面的检查也不会触发任何 DELETE。
	fake.on("COUNT(*) FROM worklog_sgcc_account WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(1)}})
	status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
	if status != http.StatusConflict {
		t.Fatalf("status=%d env=%v", status, env)
	}
	for _, q := range []string{
		"DELETE FROM worklog_dispatch_sync_team", "DELETE FROM treehazard_team_setting",
		"DELETE FROM treehazard_sync_log", "DELETE FROM crossing_sync_log", "DELETE FROM sys_team WHERE id",
	} {
		if n := fake.execCount(q); n != 0 {
			t.Fatalf("409 时不得执行 %q，实际 %d 次", q, n)
		}
	}
	if len(fake.eventList()) != 0 {
		t.Fatalf("409 时不应开启事务：%v", fake.eventList())
	}
}

// 成功删除：开关/班组级配置/同步日志与班组行在同一事务内清理。
func TestDeleteTeamCleansInOneTransaction(t *testing.T) {
	fake, mod, srv := newTestEnv(t, adminIdent())
	fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
	registerZeroCounts(fake)
	mod.HasTeamSafedayRecords = func(string) bool { return false }
	status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d env=%v", status, env)
	}
	want := []string{
		"DELETE FROM worklog_dispatch_sync_team WHERE team_id",
		"DELETE FROM treehazard_team_setting WHERE team_id",
		"DELETE FROM treehazard_sync_log WHERE team_id",
		"DELETE FROM crossing_sync_log WHERE team_id",
		"DELETE FROM sys_team WHERE id",
	}
	for _, q := range want {
		if n := fake.execCount(q); n != 1 {
			t.Fatalf("应执行一次 %q，实际 %d", q, n)
		}
	}
	events := fake.eventList()
	if len(events) != 2 || events[0] != "begin" || events[1] != "commit" {
		t.Fatalf("删除应在同一事务内 begin/commit，实际 %v", events)
	}
}
