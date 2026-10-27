package adminapi

// 应用启停/排序与按人应用授权 SQL fixture 测试。

import (
	"database/sql/driver"
	"net/http"
	"testing"
)

// TestListApps 全部应用按 sort,id 输出（含停用）。
func TestListApps(t *testing.T) {
	fake, _, srv := newTestEnv(t, adminIdent())
	fake.on("FROM sys_app ORDER BY sort, id",
		[]string{"id", "app_key", "name", "icon", "path", "terminal", "sort", "status"},
		[][]driver.Value{
			{int64(1), "call-me", "Call Me", "robot", "/p", "both", 1, 1},
			{int64(2), "safe-day", "安全日活动记录", "file-safety", "/s", "both", 2, 0},
		})
	status, env := do(t, srv, "GET", "/api/v1/admin/apps", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	wantCode(t, env, 0)
	list := env["data"].(map[string]any)["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	if list[1].(map[string]any)["app_key"] != "safe-day" {
		t.Fatalf("row = %v", list[1])
	}
}

// TestUpdateApp 启停/排序：应用不存在 40406、无改动 40014、成功按字段更新。
func TestUpdateApp(t *testing.T) {
	t.Run("应用不存在", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/apps/404", map[string]any{"status": 0})
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeAppMissing))
	})
	t.Run("没有需要修改的内容", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_app WHERE id", []string{"id"}, [][]driver.Value{{int64(1)}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/apps/1", map[string]any{})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoChange))
	})
	t.Run("启停与排序", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_app WHERE id", []string{"id"}, [][]driver.Value{{int64(1)}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/apps/1", map[string]any{"status": 0, "sort": 7})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已保存")
		ex, ok := fake.lastExec("UPDATE sys_app SET")
		if !ok {
			t.Fatal("未发出 UPDATE")
		}
		hasStatus, hasSort := false, false
		for _, a := range ex.args {
			if a == driver.Value(int64(0)) {
				hasStatus = true
			}
			if a == driver.Value(int64(7)) {
				hasSort = true
			}
		}
		if !hasStatus || !hasSort {
			t.Fatalf("UPDATE 参数 = %v", ex.args)
		}
	})
}

// TestUserApps 按人授权：读取 / 全量替换（过滤无效 id、事务删除+插入）/ 校验失败。
func TestUserApps(t *testing.T) {
	t.Run("读取授权", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_user WHERE id", []string{"id"}, [][]driver.Value{{int64(5)}})
		fake.on("FROM sys_user_app WHERE user_id", []string{"app_id"},
			[][]driver.Value{{int64(1)}, {int64(3)}})
		status, env := do(t, srv, "GET", "/api/v1/admin/users/5/apps", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, 0)
		ids := env["data"].(map[string]any)["app_ids"].([]any)
		if len(ids) != 2 || ids[0].(float64) != 1 || ids[1].(float64) != 3 {
			t.Fatalf("app_ids = %v", ids)
		}
	})
	t.Run("用户不存在", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "GET", "/api/v1/admin/users/404/apps", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeUserMissing))
	})
	t.Run("app_ids 非数组", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5/apps", map[string]any{"app_ids": "1,2"})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeAppIDsBad))
	})
	t.Run("全量替换并过滤无效 id", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_user WHERE id", []string{"id"}, [][]driver.Value{{int64(5)}})
		fake.on("FROM sys_app WHERE id IN", []string{"id"}, [][]driver.Value{{int64(1)}, {int64(2)}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5/apps",
			map[string]any{"app_ids": []any{1, 2, 99, "x", 3.5, 1}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已保存")
		ids := env["data"].(map[string]any)["app_ids"].([]any)
		if len(ids) != 2 || ids[0].(float64) != 1 || ids[1].(float64) != 2 {
			t.Fatalf("过滤后 app_ids = %v", ids)
		}
		if fake.execCount("DELETE FROM sys_user_app WHERE user_id") != 1 {
			t.Fatal("应先清空原授权")
		}
		ex, ok := fake.lastExec("INSERT IGNORE INTO sys_user_app")
		if !ok {
			t.Fatal("未批量插入授权")
		}
		if len(ex.args) != 4 {
			t.Fatalf("INSERT 参数 = %v", ex.args)
		}
	})
	t.Run("清空授权", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_user WHERE id", []string{"id"}, [][]driver.Value{{int64(5)}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5/apps", map[string]any{"app_ids": []any{}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		if fake.execCount("DELETE FROM sys_user_app WHERE user_id") != 1 {
			t.Fatal("应清空原授权")
		}
		if fake.execCount("INSERT IGNORE INTO sys_user_app") != 0 {
			t.Fatal("空授权不应插入")
		}
	})
}
