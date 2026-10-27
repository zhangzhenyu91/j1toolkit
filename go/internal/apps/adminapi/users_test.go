package adminapi

// 员工管理 SQL fixture 测试：列表筛选、新建校验、修改（自我保护/状态归一/班组管理员约束/
// 密码重置）、删除（不能删自己/最后一个超管）。

import (
	"context"
	"database/sql/driver"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestListUsers 列表查询：keyword 模糊 + team_id 筛选（0=未分组），行形状含班组名。
func TestListUsers(t *testing.T) {
	fake, _, srv := newTestEnv(t, adminIdent())
	fake.on("FROM sys_user u LEFT JOIN sys_team",
		[]string{"id", "username", "nickname", "wxid", "team_id", "team", "role", "status", "created_at"},
		[][]driver.Value{
			{int64(2), "alice", "Alice", "wx-a", nil, nil, "user", 1, "2026-10-27 09:00:00"},
			{int64(3), "bob", "Bob", "", int64(1), "检修一班", "team_admin", 0, "2026-10-26 08:00:00"},
		})

	status, env := do(t, srv, "GET", "/api/v1/admin/users?keyword=ali&team_id=0", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	wantCode(t, env, 0)
	data := env["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	first := list[0].(map[string]any)
	if first["team"] != nil || first["team_id"] != nil {
		t.Fatalf("未分组行 team/team_id 应为 null: %v", first)
	}
	second := list[1].(map[string]any)
	if second["team"] != "检修一班" || second["status"].(float64) != 0 {
		t.Fatalf("row = %v", second)
	}
	q, ok := fake.lastQuery("FROM sys_user u LEFT JOIN sys_team")
	if !ok {
		t.Fatal("未发出员工列表查询")
	}
	if len(q.args) != 2 || q.args[0] != driver.Value("%ali%") || q.args[1] != driver.Value("%ali%") {
		t.Fatalf("筛选参数 = %v", q.args)
	}
	if !strings.Contains(q.query, "u.team_id IS NULL") {
		t.Fatalf("team_id=0 应筛未分组: %s", q.query)
	}
}

// TestCreateUser 新建校验矩阵与成功路径（bcrypt 哈希落库 + 成员同步钩子）。
func TestCreateUser(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		code float64
	}{
		{"账号格式非法", map[string]any{"username": "a b", "password": "123456"}, codeBadUsername},
		{"账号为空", map[string]any{"username": "", "password": "123456"}, codeBadUsername},
		{"密码过短", map[string]any{"username": "alice", "password": "12345"}, codeBadPassword},
		{"角色非法", map[string]any{"username": "alice", "password": "123456", "role": "root"}, codeBadRole},
		{"班组管理员无班组", map[string]any{"username": "alice", "password": "123456", "role": "team_admin"}, codeTeamAdminNil},
		{"班组不存在或停用", map[string]any{"username": "alice", "password": "123456", "team_id": 9}, codeTeamInvalid},
		{"wxid 超长", map[string]any{"username": "alice", "password": "123456", "wxid": strings.Repeat("x", 129)}, codeWXIDTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, srv := newTestEnv(t, adminIdent())
			status, env := do(t, srv, "POST", "/api/v1/admin/users", tc.body)
			if status == http.StatusOK {
				t.Fatalf("校验 %s 不应成功", tc.name)
			}
			wantCode(t, env, tc.code)
		})
	}

	// 班组存在但已停用 → 40015
	t.Run("班组已停用", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT status FROM sys_team WHERE id", []string{"status"}, [][]driver.Value{{0}})
		status, env := do(t, srv, "POST", "/api/v1/admin/users",
			map[string]any{"username": "alice", "password": "123456", "team_id": 9})
		if status == http.StatusOK {
			t.Fatal("停用班组不应通过校验")
		}
		wantCode(t, env, float64(codeTeamInvalid))
	})

	// 账号重复 40901
	t.Run("账号已存在", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT id FROM sys_user WHERE username", []string{"id"}, [][]driver.Value{{int64(7)}})
		status, env := do(t, srv, "POST", "/api/v1/admin/users",
			map[string]any{"username": "alice", "password": "123456"})
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409", status)
		}
		wantCode(t, env, float64(codeUserDup))
	})

	// 成功：bcrypt 哈希落库、钩子触发
	t.Run("创建成功", func(t *testing.T) {
		fake, mod, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT status FROM sys_team WHERE id", []string{"status"}, [][]driver.Value{{1}})
		hookIDs := []int64{}
		mod.OnUserChanged = func(_ context.Context, userID int64) { hookIDs = append(hookIDs, userID) }

		status, env := do(t, srv, "POST", "/api/v1/admin/users",
			map[string]any{"username": "alice_2", "password": "secret77", "nickname": "Alice", "team_id": 3, "role": "team_admin"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已创建")
		id := env["data"].(map[string]any)["id"].(float64)
		if id != 100 {
			t.Fatalf("id = %v, want 100", id)
		}
		ex, ok := fake.lastExec("INSERT INTO sys_user")
		if !ok {
			t.Fatal("未发出 INSERT")
		}
		hash, _ := ex.args[1].(string)
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte("secret77")) != nil {
			t.Fatal("password_hash 不是该密码的 bcrypt 哈希")
		}
		if len(hookIDs) != 1 || hookIDs[0] != 100 {
			t.Fatalf("成员同步钩子 = %v, want [100]", hookIDs)
		}
	})
}

// TestUpdateUser 修改：自我保护、状态 "0" 归一、班组管理员约束、密码重置、缺改动、用户不存在。
func TestUpdateUser(t *testing.T) {
	// 自我保护：不能禁用或降级自己
	t.Run("不能禁用自己", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/1", map[string]any{"status": 0})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeSelfProtect))
	})
	t.Run("不能降级自己", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/1", map[string]any{"role": "user"})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeSelfProtect))
	})

	// 字符串 "0" 归一为禁用（不能被真值判定误当启用）
	t.Run("status 字符串 0 归一", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{"status": "0"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		ex, _ := fake.lastExec("UPDATE sys_user SET")
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

	// 班组管理员必须有班组（以更新后 team_id 为准）
	t.Run("清空班组后班组管理员拦截", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT role, team_id FROM sys_user WHERE id", []string{"role", "team_id"},
			[][]driver.Value{{"team_admin", int64(1)}})
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{"team_id": nil})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeTeamAdminNil))
	})

	// 重置密码：bcrypt 哈希落库
	t.Run("重置密码", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{"password": "newpass1"})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		ex, _ := fake.lastExec("UPDATE sys_user SET")
		hash, _ := ex.args[0].(string)
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte("newpass1")) != nil {
			t.Fatal("password_hash 不是新密码的 bcrypt 哈希")
		}
	})

	t.Run("密码过短", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{"password": "12345"})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeBadPassword))
	})

	t.Run("没有需要修改的内容", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/5", map[string]any{})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoChange))
	})

	t.Run("用户不存在", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.onExecExact("UPDATE sys_user SET", 0, 0)
		status, env := do(t, srv, "PUT", "/api/v1/admin/users/404", map[string]any{"nickname": "x"})
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeUserMissing))
	})
}

// TestDeleteUser 删除约束：不能删自己 / 不能删最后一个超管；成功清理授权与通知痕迹。
func TestDeleteUser(t *testing.T) {
	t.Run("不能删除自己", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT role FROM sys_user WHERE id", []string{"role"}, [][]driver.Value{{"admin"}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/users/1", nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeSelfProtect))
	})

	t.Run("不能删除最后一个超管", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT role FROM sys_user WHERE id", []string{"role"}, [][]driver.Value{{"admin"}})
		fake.on("COUNT(*) FROM sys_user WHERE role = 'admin'", []string{"cnt"}, [][]driver.Value{{int64(0)}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/users/2", nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeLastAdmin))
	})

	t.Run("删除成功并清理", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.on("SELECT role FROM sys_user WHERE id", []string{"role"}, [][]driver.Value{{"user"}})
		status, env := do(t, srv, "DELETE", "/api/v1/admin/users/5", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "已删除")
		if fake.execCount("DELETE FROM sys_user WHERE id") != 1 {
			t.Fatal("未删除用户行")
		}
		for _, q := range []string{
			"DELETE FROM sys_user_app WHERE user_id",
			"DELETE FROM sys_notice_read WHERE user_id",
			"DELETE FROM sys_notice_del WHERE user_id",
		} {
			if fake.execCount(q) != 1 {
				t.Fatalf("缺少清理语句: %s", q)
			}
		}
	})

	t.Run("用户不存在", func(t *testing.T) {
		_, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "DELETE", "/api/v1/admin/users/404", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeUserMissing))
	})
}
