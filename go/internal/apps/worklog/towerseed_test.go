package worklog

import (
	"context"
	"database/sql/driver"
	"testing"

	"shade/internal/assetpath"
)

func TestSeedDefaultTowers(t *testing.T) {
	if assetpath.ResolveFile("worklog/tower-coords.json") == "" {
		t.Skip("未找到杆塔坐标素材")
	}
	db, fake := openFake(t)
	defer db.Close()
	fake.on("COUNT(*) FROM worklog_tower", []string{"count"}, [][]driver.Value{{int64(0)}})
	fake.on("FROM sys_team WHERE status = 1", []string{"id"}, [][]driver.Value{{int64(7)}})
	m := New(db)
	n, err := m.SeedDefaultTowers(context.Background())
	if err != nil || n == 0 {
		t.Fatalf("杆塔种子迁移失败: n=%d err=%v", n, err)
	}
	total := 0
	for _, call := range fake.execs {
		total += len(call.args)
	}
	call, ok := fake.lastExec("INSERT INTO worklog_tower")
	if !ok || total != n*7 || call.args[0] != int64(7) {
		t.Fatalf("种子写入参数不符: n=%d args=%d team=%v", n, total, call.args[0])
	}
	// 用独立实例验证非空不覆盖。
	db2, fake2 := openFake(t)
	defer db2.Close()
	fake2.on("COUNT(*) FROM worklog_tower", []string{"count"}, [][]driver.Value{{int64(n)}})
	m2 := New(db2)
	again, err := m2.SeedDefaultTowers(context.Background())
	if err != nil || again != 0 || fake2.execCount("INSERT INTO worklog_tower") != 0 {
		t.Fatalf("已有杆塔数据不应覆盖: n=%d err=%v", again, err)
	}
}
