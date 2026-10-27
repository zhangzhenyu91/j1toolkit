package sgccclockin

// 建表/列升级测试：幂等补列与绑定唯一键迁移；断言不改写、不删除业务历史数据行。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// TestEnsureSchemaAddMissingColumns 老库缺列 → 补列 ALTER；不动业务历史数据。
func TestEnsureSchemaAddMissingColumns(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	// information_schema 查无列（全部走补列分支）；user_id 可空查询同样无行 → 不 MODIFY；
	// 唯一键查询无 uk_team_user → 不迁移
	fdb.onRowsAlways("information_schema.COLUMNS", []string{"COLUMN_NAME"})
	fdb.onRowsAlways("information_schema.STATISTICS", []string{"INDEX_NAME"})

	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表应成功: %v", err)
	}
	alters := fdb.callsWith("ALTER TABLE worklog_photo ADD COLUMN")
	if len(alters) != 5 {
		t.Fatalf("worklog_photo 应补 5 列: %d", len(alters))
	}
	joined := ""
	for _, c := range alters {
		joined += c.query + "\n"
	}
	for _, col := range []string{"is_watermark", "source", "sgcc_img_id", "sgcc_synced", "md5"} {
		if !strings.Contains(joined, col) {
			t.Fatalf("缺列未补: %s", col)
		}
	}
	if len(fdb.callsWith("ALTER TABLE worklog_clockin ADD COLUMN")) != 2 {
		t.Fatalf("worklog_clockin 应补 2 列")
	}
	if len(fdb.callsWith("ALTER TABLE worklog_sgcc_account MODIFY")) != 0 {
		t.Fatalf("无 IS_NULLABLE=NO 证据不应 MODIFY")
	}
}

// TestEnsureSchemaNoHistoryRewrite 幂等升级不得改写/删除业务历史数据（照片/打卡/费用/核查流水）。
func TestEnsureSchemaNoHistoryRewrite(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	fdb.onRowsAlways("information_schema.COLUMNS", []string{"COLUMN_NAME"}, []driver.Value{"x"})
	fdb.onRowsAlways("information_schema.STATISTICS", []string{"INDEX_NAME"})

	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表应成功: %v", err)
	}
	for _, forbidden := range []string{
		"UPDATE worklog_photo", "DELETE FROM worklog_photo",
		"UPDATE worklog_clockin", "DELETE FROM worklog_clockin",
		"UPDATE worklog_fee", "DELETE FROM worklog_fee",
		"UPDATE worklog_sync_log", "DELETE FROM worklog_sync_log",
	} {
		if len(fdb.callsWith(forbidden)) != 0 {
			t.Fatalf("不应执行 %s", forbidden)
		}
	}
	if len(fdb.callsWith("ALTER TABLE")) != 0 {
		t.Fatalf("列已齐全不应 ALTER: %v", fdb.callsWith("ALTER TABLE"))
	}
}

// TestEnsureSchemaNullableUserID IS_NULLABLE=NO → 放宽 user_id 可空（管理员代绑）。
func TestEnsureSchemaNullableUserID(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	// 更具体匹配先注册：IS_NULLABLE 查询与 COLUMN_NAME 查询同表不同列
	fdb.onRowsAlways("IS_NULLABLE FROM information_schema.COLUMNS", []string{"IS_NULLABLE"}, []driver.Value{"NO"})
	fdb.onRowsAlways("COLUMN_NAME FROM information_schema.COLUMNS", []string{"COLUMN_NAME"}, []driver.Value{"x"})
	fdb.onRowsAlways("information_schema.STATISTICS", []string{"INDEX_NAME"})

	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表应成功: %v", err)
	}
	if len(fdb.callsWith("MODIFY user_id BIGINT UNSIGNED NULL")) != 1 {
		t.Fatalf("应放宽 user_id 可空一次")
	}
}

// TestEnsureSchemaKeyUpgrade 旧键在 → 绑定行去重 + 升级为 uk_user/uk_member（只动绑定配置行）。
func TestEnsureSchemaKeyUpgrade(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	fdb.onRowsAlways("COLUMN_NAME FROM information_schema.COLUMNS", []string{"COLUMN_NAME"}, []driver.Value{"x"})
	fdb.onRowsAlways("IS_NULLABLE FROM information_schema.COLUMNS", []string{"IS_NULLABLE"}, []driver.Value{"YES"})
	fdb.onRowsAlways("information_schema.STATISTICS", []string{"INDEX_NAME"}, []driver.Value{"uk_team_user"})
	// 两名同 user_id 跨班重复绑定行（id=1 与本人当前班组一致 → 保留；id=2 去重）
	fdb.onRowsAlways("FROM worklog_sgcc_account a LEFT JOIN sys_user", []string{"id", "user_id", "member_id", "team_id", "user_team_id"},
		[]driver.Value{int64(1), int64(7), int64(11), int64(1), int64(1)},
		[]driver.Value{int64(2), int64(7), int64(12), int64(2), int64(1)})

	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("升级应成功: %v", err)
	}
	dels := fdb.callsWith("DELETE FROM worklog_sgcc_account")
	if len(dels) != 1 || itoaArgs(dels[0].args) != "2" {
		t.Fatalf("应仅去重 id=2（保留与本人当前班组一致行）: %+v", dels)
	}
	swaps := fdb.callsWith("DROP INDEX uk_team_user")
	if len(swaps) != 1 || !strings.Contains(swaps[0].query, "ADD UNIQUE KEY uk_user") {
		t.Fatalf("应换唯一键: %+v", swaps)
	}
	// 去重只允许动绑定表
	if len(fdb.callsWith("DELETE FROM worklog_photo"))+len(fdb.callsWith("DELETE FROM worklog_clockin"))+
		len(fdb.callsWith("DELETE FROM worklog_fee")) != 0 {
		t.Fatalf("去重不得触碰业务历史数据")
	}
}
