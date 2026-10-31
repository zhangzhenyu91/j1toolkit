package worklog

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// 月份过滤必须用日期范围（可走索引），不得对 log_date 套 DATE_FORMAT。
func TestMonthQueryUsesDateRange(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_entry e", []string{
		"id", "team_id", "log_date", "patrol_content", "remark", "remark_files", "cross_team",
		"vehicle_id", "plate_no", "destination_id", "destination_name", "out_of_city",
		"stationed", "dispatch_order_no", "created_by", "created_at",
	}, nil)
	rec := env.do(t, "GET", "/api/v1/worklog/day-status?month=2026-08", nil)
	if decodeEnv(t, rec).Code != codeOK {
		t.Fatalf("day-status 应成功: %s", rec.Body.String())
	}
	q, ok := env.fake.lastQuery("FROM worklog_entry e")
	if !ok {
		t.Fatal("未执行卡片查询")
	}
	if strings.Contains(q.query, "DATE_FORMAT(e.log_date, '%Y-%m')") {
		t.Fatalf("月份过滤不应使用 DATE_FORMAT：%s", q.query)
	}
	if !strings.Contains(q.query, "e.log_date >= ? AND e.log_date < ?") {
		t.Fatalf("月份过滤应为日期范围：%s", q.query)
	}
	if len(q.args) < 2 || q.args[0] != driver.Value("2026-08-01") || q.args[1] != driver.Value("2026-09-01") {
		t.Fatalf("范围参数应为月初到次月初，实际 %v", q.args)
	}
}

// 更新卡片时成员姓名应一次批量查询，不得逐 id 查询。
func TestMemberNamesLoadedInOneQuery(t *testing.T) {
	db, fake := openFake(t)
	fake.on("FROM worklog_member WHERE id IN", []string{"id", "name"},
		[][]driver.Value{{int64(10), "赵登"}, {int64(11), "郑海楠"}})
	m := New(db)
	names, err := m.memberNamesByID(context.Background(), []int64{10, 11})
	if err != nil || names[10] != "赵登" || names[11] != "郑海楠" {
		t.Fatalf("批量成员姓名不符: %v %v", names, err)
	}
	n := 0
	for _, q := range fake.queries {
		if strings.Contains(q.query, "FROM worklog_member WHERE id") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("成员姓名应一次批量查询，实际 %d 次", n)
	}
}

// 中间件已解析班组时，handler 入口不得再次查 sys_team/权限。
func TestEnterReusesResolvedTeam(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	rec := env.do(t, "GET", "/api/v1/worklog/meta", nil)
	if decodeEnv(t, rec).Code != codeOK {
		t.Fatalf("meta 应成功: %s", rec.Body.String())
	}
	n := 0
	for _, q := range env.fake.queries {
		if strings.Contains(q.query, "FROM sys_team") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("班组只应在中间件解析一次，实际查询 %d 次", n)
	}
}
