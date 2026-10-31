package httpapi

// 系统表建表/老库补列与账号自举测试：fake DB（accesslog_test.go 的轻量 database/sql 假驱动）
// 覆盖四表 DDL、只补缺列升级、sys_app 种子只补缺（不批量改名/不覆盖既有授权）、
// 管理员授权与 ADMIN_PASSWORD 缺失告警、last-admin 约束不被启动破坏；不连接真实数据库。

import (
	"bytes"
	"context"
	"database/sql/driver"
	"log"
	"os"
	"strings"
	"testing"
)

// callsWith 取 query 含 substr 的全部调用（exec 与 query 同记录，按文本过滤）。
func callsWith(f *fakeDB, substr string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if strings.Contains(c.query, substr) {
			out = append(out, c)
		}
	}
	return out
}

// columnProbe 列探测规则文本（EnsureSystemSchema 内联表名/列名，可按子串路由）。
func columnProbe(table, column string) string {
	return "TABLE_NAME = '" + table + "' AND COLUMN_NAME = '" + column + "'"
}

func TestEnsureSystemSchemaCreatesTablesAndUpgradesMissingColumns(t *testing.T) {
	db, f := newFakeDB(t)
	if err := EnsureSystemSchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureSystemSchema: %v", err)
	}
	for _, table := range []string{"sys_team", "sys_user", "sys_app", "sys_user_app"} {
		if n := len(callsWith(f, "CREATE TABLE IF NOT EXISTS "+table+" (")); n != 1 {
			t.Fatalf("建表 %s 执行 %d 次，应为 1", table, n)
		}
	}
	// 老库全缺 → 逐列补 ALTER
	for _, col := range systemLegacyColumns {
		if n := len(callsWith(f, col.DDL)); n != 1 {
			t.Fatalf("补列 %s.%s 执行 %d 次，应为 1", col.Table, col.Column, n)
		}
	}
}

func TestEnsureSystemSchemaKeepsExistingColumns(t *testing.T) {
	db, f := newFakeDB(t)
	for _, col := range systemLegacyColumns {
		f.onRows(columnProbe(col.Table, col.Column), []string{"COLUMN_NAME"}, []driver.Value{col.Column})
	}
	if err := EnsureSystemSchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureSystemSchema: %v", err)
	}
	if n := len(callsWith(f, "ALTER TABLE")); n != 0 {
		t.Fatalf("列已存在时不应 ALTER，实际 %d 次", n)
	}
}

func TestEnsureSystemSchemaUpgradesOnlyMissingColumns(t *testing.T) {
	db, f := newFakeDB(t)
	// 仅 sys_user.role 已存在
	f.onRows(columnProbe("sys_user", "role"), []string{"COLUMN_NAME"}, []driver.Value{"role"})
	if err := EnsureSystemSchema(context.Background(), db); err != nil {
		t.Fatalf("EnsureSystemSchema: %v", err)
	}
	if n := len(callsWith(f, "ALTER TABLE sys_user ADD COLUMN role")); n != 0 {
		t.Fatalf("已存在的 role 列不应重复补，实际 %d 次", n)
	}
	if n := len(callsWith(f, "ALTER TABLE")); n != len(systemLegacyColumns)-1 {
		t.Fatalf("缺列应补 %d 条 ALTER，实际 %d", len(systemLegacyColumns)-1, n)
	}
}

func TestSystemLegacyColumnsCoverRequired(t *testing.T) {
	required := map[string]bool{
		"sys_user.unionid":        false,
		"sys_user.team_id":        false,
		"sys_user.role":           false,
		"sys_user.wxid":           false,
		"sys_user.status":         false,
		"sys_team.wxid":           false,
		"sys_team.kvm_group_name": false,
		"sys_team.status":         false,
		"sys_app.terminal":        false,
		"sys_app.status":          false,
	}
	for _, col := range systemLegacyColumns {
		key := col.Table + "." + col.Column
		if _, ok := required[key]; ok {
			required[key] = true
		}
	}
	for key, seen := range required {
		if !seen {
			t.Fatalf("老库补列清单缺少 %s", key)
		}
	}
}

func TestSeedAppExtrasOnlyInsertsMissing(t *testing.T) {
	db, f := newFakeDB(t)
	if err := SeedAppExtras(context.Background(), db); err != nil {
		t.Fatalf("SeedAppExtras: %v", err)
	}
	seeds := callsWith(f, "INTO sys_app (app_key")
	if len(seeds) != 2 {
		t.Fatalf("种子语句应为 2 条（原有五件套 + 新应用），实际 %d", len(seeds))
	}
	for _, c := range seeds {
		if !strings.Contains(c.query, "INSERT IGNORE") {
			t.Fatalf("种子必须 INSERT IGNORE 只补缺（不覆盖既有行）：%s", c.query)
		}
		if strings.Contains(c.query, "ON DUPLICATE KEY") {
			t.Fatalf("种子不得批量改名/覆盖既有行：%s", c.query)
		}
	}
	extra := callsWith(f, seedAppExtrasSQL)
	if len(extra) != 1 {
		t.Fatalf("新应用种子执行 %d 次", len(extra))
	}
	for _, want := range []string{
		"('wechat-answer', '微信智能应答', 'chat', '/wechat-answer', 'pc', 12, 1)",
		"('app-log', '应用日志', 'file-logs', '/app-logs', 'pc', 13, 1)",
	} {
		if !strings.Contains(extra[0].query, want) {
			t.Fatalf("新应用种子缺少 %s", want)
		}
	}
	apps := callsWith(f, seedAppsSQL)
	if len(apps) != 1 {
		t.Fatalf("原有应用种子执行 %d 次", len(apps))
	}
	for _, want := range []string{"'call-me'", "'safe-day'", "'kvm'", "'file-transfer'", "'wm-add'"} {
		if !strings.Contains(apps[0].query, want) {
			t.Fatalf("原有应用种子缺少 %s", want)
		}
	}
}

func TestBootstrapAccountsSeedsEmptyLibrary(t *testing.T) {
	db, f := newFakeDB(t)
	f.onRows("SELECT COUNT(*) FROM sys_team", []string{"cnt"}, []driver.Value{int64(0)})
	if err := BootstrapAccounts(context.Background(), db, "boss", "secret123"); err != nil {
		t.Fatalf("BootstrapAccounts: %v", err)
	}
	// 1. 空库班组种子
	teams := callsWith(f, "INSERT INTO sys_team (name, sort) VALUES (?, 1), (?, 2)")
	if len(teams) != 1 {
		t.Fatalf("班组种子执行 %d 次", len(teams))
	}
	// 2. 创建管理员（bcrypt 哈希入参）
	users := callsWith(f, "INSERT INTO sys_user (username, password_hash")
	if len(users) != 1 {
		t.Fatalf("创建管理员执行 %d 次", len(users))
	}
	if users[0].args[0] != driver.Value("boss") {
		t.Fatalf("管理员用户名 %v", users[0].args[0])
	}
	if hash, _ := users[0].args[1].(string); !strings.HasPrefix(hash, "$2") {
		t.Fatalf("密码未按 bcrypt 存储: %q", hash)
	}
	// 3. 角色兜底（只提升为 admin）
	if n := len(callsWith(f, "UPDATE sys_user SET role = 'admin' WHERE username = ?")); n != 1 {
		t.Fatalf("角色兜底执行 %d 次", n)
	}
	// 4. 老班名并入 sys_team（只补缺不改名）
	if n := len(callsWith(f, "INSERT IGNORE INTO sys_team (name, sort)")); n != 1 {
		t.Fatalf("老班名并入执行 %d 次", n)
	}
	// 5. team 文本回填 team_id
	if n := len(callsWith(f, "SET u.team_id = t.id")); n != 1 {
		t.Fatalf("team_id 回填执行 %d 次", n)
	}
	// 6. 管理员默认授权五件套 + 五个业务应用（INSERT IGNORE 不覆盖既有授权）
	grant := callsWith(f, "INSERT IGNORE INTO sys_user_app (user_id, app_id)")
	if len(grant) != 10 {
		t.Fatalf("管理员授权应 10 条，实际 %d", len(grant))
	}
	wantKeys := []string{"call-me", "safe-day", "kvm", "file-transfer", "wm-add",
		"netdisk", "work-log", "tree-hazard", "crossing", "team-data"}
	for i, c := range grant {
		if c.args[1] != driver.Value(wantKeys[i]) {
			t.Fatalf("授权顺序 %d 为 %v，应为 %s", i, c.args[1], wantKeys[i])
		}
	}
}

func TestBootstrapAccountsPreservesUsersAndApps(t *testing.T) {
	db, f := newFakeDB(t)
	f.onRows("SELECT COUNT(*) FROM sys_team", []string{"cnt"}, []driver.Value{int64(2)})
	f.onRows("SELECT id FROM sys_user WHERE username = ?", []string{"id"}, []driver.Value{int64(9)})
	if err := BootstrapAccounts(context.Background(), db, "boss", "secret123"); err != nil {
		t.Fatalf("BootstrapAccounts: %v", err)
	}
	// 既有管理员：不重建、不重置密码
	if n := len(callsWith(f, "INSERT INTO sys_user (username")); n != 0 {
		t.Fatalf("既有管理员不应重建，INSERT %d 次", n)
	}
	// 非空班组表：不写班组种子
	if n := len(callsWith(f, "INSERT INTO sys_team (name, sort) VALUES (?, 1), (?, 2)")); n != 0 {
		t.Fatalf("非空班组表不应写种子，INSERT %d 次", n)
	}
	// last-admin 约束不被启动破坏：不删账号、不降级任何角色
	if n := len(callsWith(f, "DELETE FROM sys_user")); n != 0 {
		t.Fatalf("启动不得删除账号，DELETE %d 次", n)
	}
	for _, c := range callsWith(f, "UPDATE sys_user SET role") {
		if !strings.Contains(c.query, "role = 'admin'") {
			t.Fatalf("启动不得降级角色：%s", c.query)
		}
	}
	// 授权仍按既有管理员 id 补缺（INSERT IGNORE 不覆盖既有授权）
	grant := callsWith(f, "INSERT IGNORE INTO sys_user_app (user_id, app_id)")
	if len(grant) != 10 {
		t.Fatalf("管理员授权应 10 条，实际 %d", len(grant))
	}
	for _, c := range grant {
		if c.args[0] != driver.Value(int64(9)) {
			t.Fatalf("授权对象 %v，应为既有管理员 9", c.args[0])
		}
	}
}

func TestBootstrapAccountsWarnsWhenAdminPasswordMissing(t *testing.T) {
	db, f := newFakeDB(t)
	f.onRows("SELECT COUNT(*) FROM sys_team", []string{"cnt"}, []driver.Value{int64(2)})
	f.onRows("SELECT id FROM sys_user WHERE username = ?", []string{"id"}, []driver.Value{int64(9)})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if err := BootstrapAccounts(context.Background(), db, "boss", ""); err != nil {
		t.Fatalf("BootstrapAccounts: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"************************************************************", "[安全警告]", "ADMIN_PASSWORD", "Admin@123"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少醒目告警片段 %q，实际输出：%s", want, out)
		}
	}
}
