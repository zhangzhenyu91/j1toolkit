package worklog

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// 老库缺列：补齐 team_id/备注/派车单号/照片识别/商旅列，回填默认班组并升级联合唯一键。
func TestEnsureSchemaMigratesLegacyWorklog(t *testing.T) {
	db, f := openFake(t)
	f.on("SELECT id FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	m := New(db)
	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	for _, table := range []string{"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member"} {
		if n := f.execCount("ALTER TABLE " + table + " ADD COLUMN team_id"); n != 1 {
			t.Fatalf("%s 应补 team_id 列，实际 %d 次", table, n)
		}
		if n := f.execCount("UPDATE " + table + " SET team_id = ? WHERE team_id IS NULL"); n != 1 {
			t.Fatalf("%s 应按默认班组回填 team_id，实际 %d 次", table, n)
		}
	}
	for _, want := range []string{
		"ALTER TABLE worklog_entry ADD COLUMN dispatch_order_no",
		"ALTER TABLE worklog_entry ADD COLUMN remark ",
		"ALTER TABLE worklog_entry ADD COLUMN remark_files ",
		"ALTER TABLE worklog_entry ADD COLUMN cross_team ",
		"ALTER TABLE worklog_member ADD COLUMN user_id",
		"ALTER TABLE worklog_destination ADD COLUMN out_of_city",
		"ALTER TABLE worklog_destination ADD COLUMN stationed",
		"ALTER TABLE worklog_photo ADD COLUMN shot_time",
		"ALTER TABLE worklog_photo ADD COLUMN weather",
		"ALTER TABLE worklog_photo ADD COLUMN location",
		"ALTER TABLE worklog_photo ADD COLUMN date_ok",
		"ALTER TABLE worklog_photo ADD COLUMN dest_ok",
		"ALTER TABLE worklog_photo ADD COLUMN is_watermark",
		"ALTER TABLE worklog_photo ADD COLUMN md5",
		"ALTER TABLE worklog_photo ADD COLUMN source",
		"ALTER TABLE worklog_photo ADD COLUMN sgcc_img_id",
		"ALTER TABLE worklog_photo ADD COLUMN sgcc_synced",
	} {
		if n := f.execCount(want); n != 1 {
			t.Fatalf("应执行 %q，实际 %d 次", want, n)
		}
	}
	for _, uk := range []string{"uk_team_plate", "uk_team_dest", "uk_team_member"} {
		if n := f.execCount("ADD UNIQUE KEY " + uk); n != 1 {
			t.Fatalf("应建立 %s，实际 %d 次", uk, n)
		}
	}
	if n := f.execCount("ALTER TABLE worklog_entry ADD KEY idx_team"); n != 1 {
		t.Fatalf("worklog_entry 应补 idx_team 索引，实际 %d 次", n)
	}
}

// 新库/已升级库：列与联合唯一键都已存在时不得重复 ALTER。
func TestEnsureSchemaSkipsExistingLegacyObjects(t *testing.T) {
	db, f := openFake(t)
	f.on("SELECT id FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	for _, table := range worklogTeamTables {
		f.on("TABLE_NAME = '"+table+"' AND COLUMN_NAME = 'team_id'", []string{"COLUMN_NAME"}, [][]driver.Value{{"team_id"}})
	}
	for _, col := range worklogCompatColumns {
		probe := "TABLE_NAME = '" + col.Table + "' AND COLUMN_NAME = '" + col.Column + "'"
		f.on(probe, []string{"COLUMN_NAME"}, [][]driver.Value{{col.Column}})
	}
	for _, idx := range []struct{ table, name string }{
		{"worklog_entry", "idx_team"},
		{"worklog_photo", "idx_verify_created"},
	} {
		f.on("TABLE_NAME = '"+idx.table+"' AND INDEX_NAME = '"+idx.name+"'", []string{"INDEX_NAME"}, [][]driver.Value{{idx.name}})
	}
	for _, uk := range []struct{ table, field, name string }{
		{"worklog_vehicle", "plate_no", "uk_team_plate"},
		{"worklog_destination", "name", "uk_team_dest"},
		{"worklog_member", "name", "uk_team_member"},
	} {
		f.on("TABLE_NAME = '"+uk.table+"' AND NON_UNIQUE = 0", []string{"INDEX_NAME", "COLUMN_NAME"},
			[][]driver.Value{{uk.name, "team_id"}, {uk.name, uk.field}})
	}
	m := New(db)
	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if n := f.execCount("ALTER TABLE"); n != 0 {
		t.Fatalf("已存在列/索引时不应 ALTER，实际 %d 次：%v", n, f.allQueries())
	}
	if n := f.execCount("DROP INDEX"); n != 0 {
		t.Fatalf("已有联合唯一键时不应删除索引，实际 %d 次", n)
	}
}

// 旧单列唯一索引：先删旧唯一再建 (team_id, 名称) 联合唯一。
func TestEnsureSchemaUpgradesSingleColumnUniqueIndexes(t *testing.T) {
	db, f := openFake(t)
	f.on("SELECT id FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	for _, col := range worklogCompatColumns {
		probe := "TABLE_NAME = '" + col.Table + "' AND COLUMN_NAME = '" + col.Column + "'"
		f.on(probe, []string{"COLUMN_NAME"}, [][]driver.Value{{col.Column}})
	}
	f.on("TABLE_NAME = 'worklog_vehicle' AND INDEX_NAME = 'idx_team'", []string{"INDEX_NAME"}, [][]driver.Value{{"idx_team"}})
	for _, uk := range []struct{ table, field, oldIndex, newIndex string }{
		{"worklog_vehicle", "plate_no", "plate_no", "uk_team_plate"},
		{"worklog_destination", "name", "name", "uk_team_dest"},
		{"worklog_member", "name", "name", "uk_team_member"},
	} {
		f.on("TABLE_NAME = '"+uk.table+"' AND NON_UNIQUE = 0", []string{"INDEX_NAME", "COLUMN_NAME"},
			[][]driver.Value{{uk.oldIndex, uk.field}})
	}
	m := New(db)
	if err := m.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	for _, uk := range []struct{ table, oldIndex, newIndex string }{
		{"worklog_vehicle", "plate_no", "uk_team_plate"},
		{"worklog_destination", "name", "uk_team_dest"},
		{"worklog_member", "name", "uk_team_member"},
	} {
		if n := f.execCount("ALTER TABLE " + uk.table + " DROP INDEX `" + uk.oldIndex + "`"); n != 1 {
			t.Fatalf("%s 应删除旧唯一索引 %s，实际 %d 次", uk.table, uk.oldIndex, n)
		}
		if n := f.execCount("ALTER TABLE " + uk.table + " ADD UNIQUE KEY " + uk.newIndex); n != 1 {
			t.Fatalf("%s 应建立 %s，实际 %d 次", uk.table, uk.newIndex, n)
		}
	}
}

// 迁移元数据查询失败必须终止启动，不能带着未知旧结构继续跑。
func TestEnsureSchemaFailsClosedOnCompatProbeError(t *testing.T) {
	db, f := openFake(t)
	f.onErr("COLUMN_NAME = 'team_id'", errors.New("information_schema unavailable"))
	m := New(db)
	err := m.EnsureSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "information_schema unavailable") {
		t.Fatalf("列探测失败应终止 EnsureSchema，实际 %v", err)
	}
}

// 升级联合唯一必须先建新键再删旧全局唯一，避免中途失败留下无约束窗口。
func TestEnsureSchemaAddsNewUniqueBeforeDroppingOld(t *testing.T) {
	db, f := openFake(t)
	f.on("SELECT id FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	for _, table := range worklogTeamTables {
		f.on("TABLE_NAME = '"+table+"' AND COLUMN_NAME = 'team_id'", []string{"COLUMN_NAME"}, [][]driver.Value{{"team_id"}})
	}
	for _, col := range worklogCompatColumns {
		probe := "TABLE_NAME = '" + col.Table + "' AND COLUMN_NAME = '" + col.Column + "'"
		f.on(probe, []string{"COLUMN_NAME"}, [][]driver.Value{{col.Column}})
	}
	for _, uk := range []struct{ table, field, oldIndex string }{
		{"worklog_vehicle", "plate_no", "plate_no"},
		{"worklog_destination", "name", "name"},
		{"worklog_member", "name", "name"},
	} {
		f.on("TABLE_NAME = '"+uk.table+"' AND NON_UNIQUE = 0", []string{"INDEX_NAME", "COLUMN_NAME"},
			[][]driver.Value{{uk.oldIndex, uk.field}})
	}
	if err := New(db).EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	firstExec := func(sub string) int {
		for i, e := range f.execs {
			if strings.Contains(e.query, sub) {
				return i
			}
		}
		return -1
	}
	for _, uk := range []struct{ table, newKey, oldIndex string }{
		{"worklog_vehicle", "uk_team_plate", "plate_no"},
		{"worklog_destination", "uk_team_dest", "name"},
		{"worklog_member", "uk_team_member", "name"},
	} {
		add := firstExec("ALTER TABLE " + uk.table + " ADD UNIQUE KEY " + uk.newKey)
		drop := firstExec("ALTER TABLE " + uk.table + " DROP INDEX `" + uk.oldIndex + "`")
		if add < 0 || drop < 0 || add > drop {
			t.Fatalf("%s 应先 ADD(%d) 再 DROP(%d)，实际 execs=%v", uk.table, add, drop, f.allQueries())
		}
	}
}

// 班组内重复数据导致联合唯一失败时，错误必须可操作（指出表/字段并给出排查 SQL）。
func TestEnsureSchemaDuplicateDataActionableError(t *testing.T) {
	db, f := openFake(t)
	f.on("SELECT id FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	for _, table := range worklogTeamTables {
		f.on("TABLE_NAME = '"+table+"' AND COLUMN_NAME = 'team_id'", []string{"COLUMN_NAME"}, [][]driver.Value{{"team_id"}})
	}
	for _, col := range worklogCompatColumns {
		probe := "TABLE_NAME = '" + col.Table + "' AND COLUMN_NAME = '" + col.Column + "'"
		f.on(probe, []string{"COLUMN_NAME"}, [][]driver.Value{{col.Column}})
	}
	for _, uk := range []struct{ table, field, oldIndex string }{
		{"worklog_vehicle", "plate_no", "plate_no"},
		{"worklog_destination", "name", "name"},
		{"worklog_member", "name", "name"},
	} {
		f.on("TABLE_NAME = '"+uk.table+"' AND NON_UNIQUE = 0", []string{"INDEX_NAME", "COLUMN_NAME"},
			[][]driver.Value{{uk.oldIndex, uk.field}})
	}
	f.onErr("ADD UNIQUE KEY uk_team_member", errors.New("Error 1062: Duplicate entry '张三' for key 'uk_team_member'"))
	err := New(db).EnsureSchema(context.Background())
	if err == nil {
		t.Fatal("重复数据应使迁移失败")
	}
	for _, want := range []string{"worklog_member", "重复", "COUNT(*)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误应包含 %q，实际 %v", want, err)
		}
	}
}
