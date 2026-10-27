package worklog

// 出工成员字典与账号同步测试：fake DB 覆盖单账号同步全分支（停用收敛/改名让位/调班迁移/
// 重名回退/认领/新建 + 商旅账号跟随）与启动只补缺（跳过已有/认领/新建/失败继续）；
// 不连接真实数据库，不调用任何外部服务。

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

// errDupEntry 模拟 MySQL ER_DUP_ENTRY（isDupEntry 按 "Duplicate entry" 判定）。
var errDupEntry = errors.New("Error 1062: Duplicate entry 'x' for key 'uk_team_member'")

// userRow5 构造 SyncMemberForUser 的 sys_user 结果行（id, username, nickname, team_id, status）。
func userRow5(id int64, nickname string, teamID driver.Value, status int64) []driver.Value {
	return []driver.Value{id, "u" + string(rune('0'+id%10)), nickname, teamID, status}
}

func TestSyncMemberForUserStopDisablesMembers(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	// 停用账号（status=0）：同步成员全部停用，不删
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(5, "张三", int64(2), 0)})
	if err := m.SyncMemberForUser(context.Background(), 5); err != nil {
		t.Fatalf("SyncMemberForUser: %v", err)
	}
	if n := f.execCount("UPDATE worklog_member SET status = 0 WHERE user_id = ?"); n != 1 {
		t.Fatalf("停用成员语句执行 %d 次，应为 1", n)
	}
	if n := f.execCount("INSERT INTO worklog_member"); n != 0 {
		t.Fatalf("停用路径不应新建成员，INSERT %d 次", n)
	}

	// 昵称为空：同样全部停用（独立 fake，避免同名规则首匹配干扰）
	db2, f2 := openFake(t)
	m2 := New(db2)
	f2.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(6, "", int64(2), 1)})
	if err := m2.SyncMemberForUser(context.Background(), 6); err != nil {
		t.Fatalf("SyncMemberForUser: %v", err)
	}
	if n := f2.execCount("UPDATE worklog_member SET status = 0 WHERE user_id = ?"); n != 1 {
		t.Fatalf("空昵称应停用成员，执行 %d 次", n)
	}
}

func TestSyncMemberForUserRenamesExistingAndSyncsSgcc(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(7, "张三", int64(2), 1)})
	f.on("SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(11)}})
	if err := m.SyncMemberForUser(context.Background(), 7); err != nil {
		t.Fatalf("SyncMemberForUser: %v", err)
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET name = ?"); ok {
		if len(e.args) != 2 || e.args[0] != driver.Value("张三") || e.args[1] != driver.Value(int64(11)) {
			t.Fatalf("改名语句参数 %v", e.args)
		}
	} else {
		t.Fatal("缺改名并启用语句")
	}
	if e, ok := f.lastExec("UPDATE worklog_sgcc_account SET team_id = ?"); ok {
		want := []driver.Value{int64(2), int64(11), int64(7), int64(11)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("商旅账号跟随参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺商旅账号跟随语句")
	}
}

func TestSyncMemberForUserRenameDupDisablesRow(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(7, "张三", int64(2), 1)})
	f.on("SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(11)}})
	f.onErr("UPDATE worklog_member SET name = ?", errDupEntry)
	if err := m.SyncMemberForUser(context.Background(), 7); err != nil {
		t.Fatalf("重名让位不应报错: %v", err)
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET status = 0 WHERE id = ?"); ok {
		if e.args[0] != driver.Value(int64(11)) {
			t.Fatalf("让位停用对象 %v，应为 11", e.args)
		}
	} else {
		t.Fatal("缺重名让位停用语句")
	}
	if n := f.execCount("UPDATE worklog_sgcc_account"); n != 0 {
		t.Fatalf("让位路径不应迁移商旅账号，执行 %d 次", n)
	}
}

func TestSyncMemberForUserMigratesOtherTeamRow(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(8, "李四", int64(3), 1)})
	f.on("SELECT id FROM worklog_member WHERE user_id = ? AND team_id <> ? ORDER BY id DESC",
		[]string{"id"}, [][]driver.Value{{int64(21)}})
	f.on("SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?",
		[]string{"maxSort"}, [][]driver.Value{{int64(5)}})
	if err := m.SyncMemberForUser(context.Background(), 8); err != nil {
		t.Fatalf("SyncMemberForUser: %v", err)
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET team_id = ?, name = ?, sort = ?, status = 1 WHERE id = ?"); ok {
		want := []driver.Value{int64(3), "李四", int64(6), int64(21)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("迁移参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺他班成员迁移语句")
	}
	if e, ok := f.lastExec("UPDATE worklog_sgcc_account SET team_id = ?"); ok {
		want := []driver.Value{int64(3), int64(21), int64(8), int64(21)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("商旅账号迁移参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺商旅账号迁移语句")
	}
}

func TestSyncMemberForUserMigrateDupClaimsSameName(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(8, "李四", int64(3), 1)})
	f.on("SELECT id FROM worklog_member WHERE user_id = ? AND team_id <> ? ORDER BY id DESC",
		[]string{"id"}, [][]driver.Value{{int64(21)}})
	f.on("SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?",
		[]string{"maxSort"}, [][]driver.Value{{int64(5)}})
	f.onErr("UPDATE worklog_member SET team_id = ?, name = ?, sort = ?, status = 1 WHERE id = ?", errDupEntry)
	f.on("FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL",
		[]string{"id"}, [][]driver.Value{{int64(31)}})
	if err := m.SyncMemberForUser(context.Background(), 8); err != nil {
		t.Fatalf("迁移撞名回退不应报错: %v", err)
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET status = 0 WHERE id = ?"); ok {
		if e.args[0] != driver.Value(int64(21)) {
			t.Fatalf("旧行让位对象 %v，应为 21", e.args)
		}
	} else {
		t.Fatal("缺旧行让位停用语句")
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET user_id = ?, status = 1 WHERE id = ?"); ok {
		want := []driver.Value{int64(8), int64(31)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("认领参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺同名认领语句")
	}
	if e, ok := f.lastExec("UPDATE worklog_sgcc_account SET team_id = ?"); ok {
		// 重名回退：member_id 改指新行 31，oldMemberId=21 一并归并
		want := []driver.Value{int64(3), int64(31), int64(8), int64(21)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("商旅账号归并参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺商旅账号归并语句")
	}
}

func TestSyncMemberForUserCreatesNew(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id, status FROM sys_user",
		[]string{"id", "username", "nickname", "team_id", "status"},
		[][]driver.Value{userRow5(9, "王五", int64(3), 1)})
	f.on("SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?",
		[]string{"maxSort"}, [][]driver.Value{{int64(2)}})
	if err := m.SyncMemberForUser(context.Background(), 9); err != nil {
		t.Fatalf("SyncMemberForUser: %v", err)
	}
	if e, ok := f.lastExec("INSERT INTO worklog_member (team_id, user_id, name, sort, status)"); ok {
		want := []driver.Value{int64(3), int64(9), "王五", int64(3)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("新建参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺新建成员语句")
	}
	if n := f.execCount("UPDATE worklog_sgcc_account"); n != 1 {
		t.Fatalf("新建后应跟随商旅账号，执行 %d 次", n)
	}
}

func TestSyncAllUserMembersSkipsExisting(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id FROM sys_user WHERE status = 1",
		[]string{"id", "username", "nickname", "team_id"},
		[][]driver.Value{{int64(1), "a", "甲", int64(2)}})
	f.on("SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(44)}})
	if err := m.SyncAllUserMembers(context.Background()); err != nil {
		t.Fatalf("SyncAllUserMembers: %v", err)
	}
	if n := f.execCount("INSERT INTO worklog_member"); n != 0 {
		t.Fatalf("已有同步成员行不应新建，INSERT %d 次", n)
	}
	if n := f.execCount("UPDATE worklog_member"); n != 0 {
		t.Fatalf("启动补缺不应改既有行启停/姓名，UPDATE %d 次", n)
	}
}

func TestSyncAllUserMembersClaimsSameNameRow(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id FROM sys_user WHERE status = 1",
		[]string{"id", "username", "nickname", "team_id"},
		[][]driver.Value{{int64(1), "a", "甲", int64(2)}})
	f.on("FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL",
		[]string{"id"}, [][]driver.Value{{int64(31)}})
	if err := m.SyncAllUserMembers(context.Background()); err != nil {
		t.Fatalf("SyncAllUserMembers: %v", err)
	}
	if e, ok := f.lastExec("UPDATE worklog_member SET user_id = ? WHERE id = ?"); ok {
		want := []driver.Value{int64(1), int64(31)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("认领参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺认领语句")
	}
	// 只补缺：认领不动 name/status
	if n := f.execCount("INSERT INTO worklog_member"); n != 0 {
		t.Fatalf("认领成功不应新建，INSERT %d 次", n)
	}
}

func TestSyncAllUserMembersCreatesAndContinuesAfterError(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id FROM sys_user WHERE status = 1",
		[]string{"id", "username", "nickname", "team_id"},
		[][]driver.Value{
			{int64(1), "a", "甲", int64(2)},
			{int64(2), "b", "乙", int64(2)},
		})
	f.on("SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?",
		[]string{"maxSort"}, [][]driver.Value{{int64(10)}})
	f.onErr("INSERT INTO worklog_member", errDupEntry)
	err := m.SyncAllUserMembers(context.Background())
	if err == nil {
		t.Fatal("应返回首个失败账号的错误（供启动告警）")
	}
	// 单账号失败继续：两个账号都尝试了新建
	if n := f.execCount("INSERT INTO worklog_member"); n != 2 {
		t.Fatalf("失败后应继续补缺后续账号，INSERT %d 次", n)
	}
}

func TestSyncAllUserMembersCreatesMissing(t *testing.T) {
	db, f := openFake(t)
	m := New(db)
	f.on("SELECT id, username, nickname, team_id FROM sys_user WHERE status = 1",
		[]string{"id", "username", "nickname", "team_id"},
		[][]driver.Value{
			{int64(1), "a", "甲", int64(2)},
			{int64(2), "b", "乙", int64(2)},
		})
	f.on("SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?",
		[]string{"maxSort"}, [][]driver.Value{{int64(10)}})
	if err := m.SyncAllUserMembers(context.Background()); err != nil {
		t.Fatalf("SyncAllUserMembers: %v", err)
	}
	if n := f.execCount("INSERT INTO worklog_member"); n != 2 {
		t.Fatalf("缺行账号应逐个新建，INSERT %d 次", n)
	}
	if e, ok := f.lastExec("INSERT INTO worklog_member (team_id, user_id, name, sort, status)"); ok {
		want := []driver.Value{int64(2), int64(2), "乙", int64(11)}
		for i := range want {
			if e.args[i] != want[i] {
				t.Fatalf("新建参数 %v，应为 %v", e.args, want)
			}
		}
	} else {
		t.Fatal("缺新建成员语句")
	}
}
