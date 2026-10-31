package adminapi

// 班组管理 SQL fixture 测试：全量列表 / 新增 / 修改（改名级联钩子、状态归一）/
// 删除（数据引用拒绝 + 派车开关级联清 + 安全日检查钩子）。

import (
	"database/sql/driver"
	"net/http"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// registerZeroCounts 预置班组删除引用检查全部零计数（COUNT(*) 恒返回一行）。
func registerZeroCounts(fake *fakeDB) {
	for _, table := range []string{
		"sys_user",
		"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member", "worklog_tower",
		"quiz_bank",
		"treehazard_record", "treehazard_staging_batch", "treehazard_conflict", "treehazard_deleted_id",
		"crossing_record", "crossing_staging_batch", "crossing_conflict", "crossing_deleted_id",
		"team_line", "wa_group",
		"worklog_sgcc_account", "worklog_clockin", "worklog_fee", "worklog_sync_log",
	} {
		fake.on("COUNT(*) FROM "+table+" WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
	}
}

// TestListTeams 全量列表含停用班组、成员数与排序字段。
func TestListTeams(t *testing.T) {
	fake, _, srv := newTestEnv(t, adminIdent())
	fake.on("FROM sys_team t",
		[]string{"id", "name", "wxid", "kvm_group_name", "sort", "status", "created_at", "user_count"},
		[][]driver.Value{
			{int64(1), "检修一班", "wx-g1", "kvm-1", 1, 1, "2026-10-27 09:00:00", int64(3)},
			{int64(2), "运维二班", "", "", 2, 0, "2026-09-01 10:00:00", int64(0)},
		})
	status, env := do(t, srv, "GET", "/api/v1/admin/teams/full", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	wantCode(t, env, 0)
	list := env["data"].(map[string]any)["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	row := list[0].(map[string]any)
	if row["wxid"] != "wx-g1" || row["kvm_group_name"] != "kvm-1" || row["status"].(float64) != 1 {
		t.Fatalf("row = %v", row)
	}
	if list[1].(map[string]any)["status"].(float64) != 0 {
		t.Fatalf("停用班组应保留: %v", list[1])
	}
}

// TestCreateTeam 新增：名称必填 / wxid 超长 / 重名 40902 / 成功（sort 取最大 +1）。
func TestCreateTeam(t *testing.T) {
	t.Run("名称必填", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "POST", "/api/v1/admin/teams", map[string]any{"name": "  "})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNameEmpty))
	})
	t.Run("wxid 超长", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "POST", "/api/v1/admin/teams",
			map[string]any{"name": "三班", "wxid": string(make([]byte, 129))})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeWXIDTooLong))
	})
	t.Run("重名 40902", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("COALESCE(MAX(sort)", []string{"maxSort"}, [][]driver.Value{{5}})
		fake.onErr("INSERT INTO sys_team", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		status, env := do(t, srv, "POST", "/api/v1/admin/teams", map[string]any{"name": "检修一班"})
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamDup))
		wantMessage(t, env, "班组「检修一班」已存在")
	})
	t.Run("创建成功", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("COALESCE(MAX(sort)", []string{"maxSort"}, [][]driver.Value{{5}})
		status, env := do(t, srv, "POST", "/api/v1/admin/teams",
			map[string]any{"name": "三班", "wxid": "wx-g3"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已创建")
		ex, ok := fake.lastExec("INSERT INTO sys_team")
		if !ok {
			t.Fatal("未发出 INSERT")
		}
		if ex.args[0] != driver.Value("三班") || ex.args[2] != driver.Value(int64(6)) {
			t.Fatalf("INSERT 参数 = %v, want 三班/6", ex.args)
		}
	})
}

// TestUpdateTeam 修改：40405 / 状态 "0" 归一 / 改名触发级联钩子 / 重名 40902 文案。
func TestUpdateTeam(t *testing.T) {
	t.Run("班组不存在", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/404", map[string]any{"name": "x"})
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamMissing))
	})
	t.Run("状态字符串 0 归一", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/1", map[string]any{"status": "0"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		ex, _ := fake.lastExec("UPDATE sys_team SET")
		found := false
		for _, a := range ex.args {
			if a == driver.Value(int64(0)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("UPDATE 参数应含 0: %v", ex.args)
		}
	})
	t.Run("改名级联钩子", func(t *testing.T) {
		fake, mod, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"旧班名"}})
		renames := [][2]string{}
		mod.OnTeamRenamed = func(oldName, newName string) { renames = append(renames, [2]string{oldName, newName}) }
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/1", map[string]any{"name": "新班名"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		if len(renames) != 1 || renames[0] != [2]string{"旧班名", "新班名"} {
			t.Fatalf("renames = %v", renames)
		}
	})
	t.Run("未改名不触发钩子", func(t *testing.T) {
		fake, mod, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
		renames := 0
		mod.OnTeamRenamed = func(_, _ string) { renames++ }
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/1", map[string]any{"name": "检修一班"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		if renames != 0 {
			t.Fatalf("未改名不应触发钩子: %d", renames)
		}
	})
	t.Run("重名 40902 文案用提交名", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"旧班名"}})
		fake.onErr("UPDATE sys_team SET", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/1", map[string]any{"name": "检修一班"})
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantMessage(t, env, "班组「检修一班」已存在")
	})
	t.Run("没有需要修改的内容", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/teams/1", map[string]any{})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoChange))
	})
}

// TestDeleteTeam 删除：员工/出工日志/题库/商旅/安全日引用拒绝；成功清派车开关并删班组。
func TestDeleteTeam(t *testing.T) {
	teamRow := func(fake *fakeDB) {
		fake.on("SELECT name FROM sys_team WHERE id", []string{"name"}, [][]driver.Value{{"检修一班"}})
	}

	t.Run("仍有员工", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		fake.on("COUNT(*) FROM sys_user WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(2)}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamInUse))
		wantMessage(t, env, "该班组下仍有 2 名员工，请改为停用")
	})

	t.Run("已有出工日志数据", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		fake.on("COUNT(*) FROM sys_user WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		fake.on("COUNT(*) FROM worklog_entry WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(4)}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamInUse))
		wantMessage(t, env, "该班组已有出工日志数据，请改为停用")
	})

	t.Run("已有题库数据", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		fake.on("COUNT(*) FROM sys_user WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		for _, table := range []string{"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member", "worklog_tower"} {
			fake.on("COUNT(*) FROM "+table+" WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		}
		fake.on("COUNT(*) FROM quiz_bank WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(1)}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantMessage(t, env, "该班组已有题库数据，请改为停用")
	})

	t.Run("已有商旅打卡数据", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		fake.on("COUNT(*) FROM sys_user WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		for _, table := range []string{"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member", "worklog_tower", "quiz_bank",
			"treehazard_record", "treehazard_staging_batch", "treehazard_conflict", "treehazard_deleted_id",
			"crossing_record", "crossing_staging_batch", "crossing_conflict", "crossing_deleted_id",
			"team_line", "wa_group"} {
			fake.on("COUNT(*) FROM "+table+" WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		}
		fake.on("COUNT(*) FROM worklog_sgcc_account WHERE team_id", []string{"cnt"}, [][]driver.Value{{int64(1)}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantMessage(t, env, "该班组已有商旅打卡数据，请改为停用")
	})

	t.Run("已有安全日活动记录", func(t *testing.T) {
		fake, mod, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		registerZeroCounts(fake)
		mod.HasTeamSafedayRecords = func(name string) bool { return name == "检修一班" }
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusConflict {
			t.Fatalf("status = %d", status)
		}
		wantMessage(t, env, "该班组已有安全日活动记录，请改为停用")
	})

	t.Run("删除成功", func(t *testing.T) {
		fake, mod, srv := newTestEnv(t, adminIdent())
		teamRow(fake)
		registerZeroCounts(fake)
		mod.HasTeamSafedayRecords = func(string) bool { return false }
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/1", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已删除")
		if fake.execCount("DELETE FROM worklog_dispatch_sync_team WHERE team_id") != 1 {
			t.Fatal("应级联清除派车单同步开关行")
		}
		if fake.execCount("DELETE FROM sys_team WHERE id") != 1 {
			t.Fatal("未删除班组行")
		}
	})

	t.Run("班组不存在", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "DELETE", "/api/v1/admin/teams/404", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamMissing))
	})
}
