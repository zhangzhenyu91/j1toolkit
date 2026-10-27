package adminapi

// 站内通知已读 / 一键删已读 / 超管推送 / 删除（全局 vs 按人隐藏）SQL fixture 测试。

import (
	"database/sql/driver"
	"net/http"
	"testing"
)

// TestNoticeRead 标记已读：不可见 40403；可见写 sys_notice_read。
func TestNoticeRead(t *testing.T) {
	t.Run("通知不存在或不可见", func(t *testing.T) {
		_, _, srv := newTestEnv(t, userIdent())
		status, env := do(t, srv, "POST", "/api/v1/notice/9/read", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoticeMiss))
	})
	t.Run("标记已读", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, userIdent())
		fake.on("FROM sys_notice n WHERE n.id", []string{"id"}, [][]driver.Value{{int64(9)}})
		status, env := do(t, srv, "POST", "/api/v1/notice/9/read", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		ex, ok := fake.lastExec("INSERT IGNORE INTO sys_notice_read")
		if !ok {
			t.Fatal("未写已读记录")
		}
		if ex.args[0] != driver.Value(int64(9)) {
			t.Fatalf("notice_id 参数 = %v", ex.args)
		}
	})
}

// TestNoticeReadAll 一键已读（普通用户可调）。
func TestNoticeReadAll(t *testing.T) {
	fake, _, srv := newTestEnv(t, userIdent())
	status, env := do(t, srv, "POST", "/api/v1/notice/read-all", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, env = %v", status, env)
	}
	wantCode(t, env, 0)
	if fake.execCount("INSERT IGNORE INTO sys_notice_read") != 1 {
		t.Fatal("未发出一键已读语句")
	}
}

// TestNoticeDelRead 一键删已读：按人删除并回传条数。
func TestNoticeDelRead(t *testing.T) {
	fake, _, srv := newTestEnv(t, userIdent())
	fake.onExec("INSERT IGNORE INTO sys_notice_del", 3, 0)
	status, env := do(t, srv, "POST", "/api/v1/notice/del-read", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, env = %v", status, env)
	}
	wantCode(t, env, 0)
	wantMessage(t, env, "已删除 3 条已读通知")
	if env["data"].(map[string]any)["deleted"].(float64) != 3 {
		t.Fatalf("deleted = %v", env["data"])
	}
}

// TestNoticePush 超管推送：校验矩阵 + 成功（created_by=当前超管）。
func TestNoticePush(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		code float64
	}{
		{"标题内容为空", map[string]any{"title": "", "content": "c", "targets": []string{"user"}}, codeNoticeInput},
		{"标题超长", map[string]any{"title": string(make([]rune, 129)), "content": "c", "targets": []string{"user"}}, codeNoticeInput},
		{"对象非数组", map[string]any{"title": "t", "content": "c", "targets": "user"}, codeNoticeTarget},
		{"对象为空数组", map[string]any{"title": "t", "content": "c", "targets": []string{}}, codeNoticeTarget},
		{"对象非法角色", map[string]any{"title": "t", "content": "c", "targets": []string{"root"}}, codeNoticeTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, srv := newTestEnv(t, adminIdent())
			status, env := do(t, srv, "POST", "/api/v1/notice/push", tc.body)
			if status == http.StatusOK {
				t.Fatalf("校验 %s 不应成功", tc.name)
			}
			wantCode(t, env, tc.code)
		})
	}

	t.Run("推送成功", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "POST", "/api/v1/notice/push",
			map[string]any{"title": "停电通知", "content": "明天检修", "targets": []string{"admin", "user"}})
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "推送成功")
		if env["data"].(map[string]any)["id"].(float64) != 100 {
			t.Fatalf("id = %v", env["data"])
		}
		ex, ok := fake.lastExec("INSERT INTO sys_notice")
		if !ok {
			t.Fatal("未写通知")
		}
		if ex.args[0] != driver.Value("停电通知") || ex.args[3] != driver.Value(int64(1)) {
			t.Fatalf("INSERT 参数 = %v", ex.args)
		}
	})
}

// TestNoticeDelete 删除：超管全局删除（连带清痕迹）；普通用户按人隐藏；不可见 40403。
func TestNoticeDelete(t *testing.T) {
	t.Run("超管全局删除", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		status, env := do(t, srv, "DELETE", "/api/v1/notice/9", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		wantMessage(t, env, "删除成功")
		if fake.execCount("DELETE FROM sys_notice WHERE id") != 1 {
			t.Fatal("未删通知本体")
		}
		if fake.execCount("DELETE FROM sys_notice_read WHERE notice_id") != 1 ||
			fake.execCount("DELETE FROM sys_notice_del WHERE notice_id") != 1 {
			t.Fatal("未清理已读/按人删除痕迹")
		}
	})
	t.Run("超管删除不存在", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, adminIdent())
		fake.onExecExact("DELETE FROM sys_notice WHERE id", 0, 0)
		status, env := do(t, srv, "DELETE", "/api/v1/notice/404", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoticeMiss))
	})
	t.Run("普通用户按人隐藏", func(t *testing.T) {
		fake, _, srv := newTestEnv(t, userIdent())
		fake.on("FROM sys_notice n WHERE n.id", []string{"id"}, [][]driver.Value{{int64(9)}})
		status, env := do(t, srv, "DELETE", "/api/v1/notice/9", nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, env = %v", status, env)
		}
		wantCode(t, env, 0)
		ex, ok := fake.lastExec("INSERT IGNORE INTO sys_notice_del")
		if !ok {
			t.Fatal("未写按人删除记录")
		}
		if ex.args[0] != driver.Value(int64(9)) {
			t.Fatalf("notice_id 参数 = %v", ex.args)
		}
	})
	t.Run("普通用户不可见 40403", func(t *testing.T) {
		_, _, srv := newTestEnv(t, userIdent())
		status, env := do(t, srv, "DELETE", "/api/v1/notice/404", nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
		wantCode(t, env, float64(codeNoticeMiss))
	})
}
