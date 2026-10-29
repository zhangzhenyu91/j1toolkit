package worklog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"strings"
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
	// 占位符数须与参数数一致（7 列：team_id + 5 坐标列 + sort），否则真实 MySQL 报列数不匹配
	for _, exec := range fake.execs {
		if !strings.Contains(exec.query, "INSERT INTO worklog_tower") {
			continue
		}
		if strings.Count(exec.query, "?") != len(exec.args) {
			t.Fatalf("INSERT 占位符数应与参数数一致: %d vs %d", strings.Count(exec.query, "?"), len(exec.args))
		}
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

// TestHandleTowersEnvelope GET /towers 响应形状须为 { rows: [...] }（server/src/worklog/towers.js 契约），
// 行 = [电压等级, 线路名称, 杆塔号, 数字经度, 数字纬度]；回归：裸数组会让小程序/网页端取 r.rows 落空报「加载失败」。
func TestHandleTowersEnvelope(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_tower",
		[]string{"voltage_level", "line_name", "tower_no", "lng", "lat"},
		[][]driver.Value{{"110", "临牵线", "12", "111.500000", "36.200000"}})
	rec := env.do(t, http.MethodGet, "/api/v1/worklog/towers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("杆塔查询应 200: %s", rec.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Rows [][]any `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v %s", err, rec.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("杆塔查询应成功: %s", rec.Body.String())
	}
	if len(body.Data.Rows) != 1 || len(body.Data.Rows[0]) != 5 {
		t.Fatalf("应返回 1 行 5 列杆塔坐标: %s", rec.Body.String())
	}
	row := body.Data.Rows[0]
	if row[0] != "110" || row[1] != "临牵线" || row[2] != "12" {
		t.Fatalf("杆塔行内容不符: %v", row)
	}
	lng, lok := row[3].(float64)
	lat, aok := row[4].(float64)
	if !lok || !aok || lng != 111.5 || lat != 36.2 {
		t.Fatalf("经纬度应为数字: %v", row)
	}
}

// TestHandleTowersNoTeam 无班组回 { rows: [] }（与有班组同形状，前端按空数据提示）。
func TestHandleTowersNoTeam(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.onErr("FROM sys_team", sql.ErrNoRows)
	rec := env.do(t, http.MethodGet, "/api/v1/worklog/towers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("无班组应 200 空态: %s", rec.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Rows [][]any `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v %s", err, rec.Body.String())
	}
	if body.Code != 0 || body.Data.Rows == nil || len(body.Data.Rows) != 0 {
		t.Fatalf("无班组应空 rows: %s", rec.Body.String())
	}
}
