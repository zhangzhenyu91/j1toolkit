package teamdata

// 运维线路关联接口测试（fake DB，不接真实 MySQL/Redis）：
// preview 自动推导统一名称；关联后建统一线路、登记别名、删除原线路、改写树障/交跨历史名。

import (
	"database/sql/driver"
	"net/http"
	"testing"

	"shade/internal/auth"
)

// oplineCols 按 id 读取线路的行列（与 loadOplinesByIDs 的 SELECT 一致）。
var oplineCols = []string{"id", "voltage_level", "line_name", "sort"}

// registerLinkRules 注册关联流程所需的查询规则。
// 注意 fake find 为「最后注册优先、按 SQL 子串匹配」：loadOplinesByIDs 的语句同时包含
// 「id IN」与「ORDER BY sort, id」，必须先注册 ORDER BY 规则（供 linecatalog.Load），
// 再注册 id IN 规则，才能让按 id 取线路命中两行。
func registerLinkRules(fake *fakeDB) {
	// linecatalog.Load：关联后本班只剩统一线路（供 ResolveExact 解析历史记录名）。
	fake.on("ORDER BY sort, id", []string{"voltage_level", "line_name"},
		[][]driver.Value{{"110", "峰义ⅠⅡ线"}})
	// 按 id 取选中线路（sort/id 序）。
	fake.on("id IN", oplineCols, [][]driver.Value{
		{int64(1), "110", "峰义Ⅰ线", int64(1)},
		{int64(2), "110", "峰义Ⅱ线", int64(2)},
	})
	// 统一线路尚不存在。
	fake.on("WHERE team_id = ? AND line_name = ?", oplineCols, nil)
	// fake 不落库：别名需在 Load 中可见，关联后的历史名才能解析到统一线路。
	fake.on("FROM team_line_link WHERE team_id = ?", []string{"alias_name", "line_name"},
		[][]driver.Value{
			{"峰义Ⅰ线", "峰义ⅠⅡ线"},
			{"峰义Ⅱ线", "峰义ⅠⅡ线"},
		})
	// 树障历史记录：原线路名、数字写法与无关线路。
	fake.on("FROM treehazard_record WHERE team_id = ?", []string{"line_name"},
		[][]driver.Value{{"峰义Ⅰ线"}, {"峰义1线"}, {"其他线"}})
	// 交跨历史记录：另一条原线路名。
	fake.on("FROM crossing_record WHERE team_id = ?", []string{"line_name"},
		[][]driver.Value{{"峰义Ⅱ线"}})
}

// POST /oplines/link/preview：自动推导统一名称与公共电压。
func TestOplineLinkPreview(t *testing.T) {
	fake, srv := newTestEnv(t, auth.Identity{ID: 1, Role: "admin", TeamID: 2})
	fake.on("id IN", oplineCols, [][]driver.Value{
		{int64(1), "110", "峰义Ⅰ线", int64(1)},
		{int64(2), "110", "峰义Ⅱ线", int64(2)},
	})

	status, env := do(t, srv, http.MethodPost, "/api/v1/teamdata/oplines/link/preview",
		map[string]any{"ids": []int{1, 2}})
	if status != http.StatusOK {
		t.Fatalf("预览应 200，got %d %v", status, env)
	}
	data, _ := env["data"].(map[string]any)
	if data["line_name"] != "峰义ⅠⅡ线" {
		t.Fatalf("建议统一名称应为 峰义ⅠⅡ线，got %v", data["line_name"])
	}
	if data["voltage_level"] != "110" {
		t.Fatalf("公共电压应为 110，got %v", data["voltage_level"])
	}
}

// POST /oplines/link：关联两条同塔线路并统一历史记录。
func TestOplineLink(t *testing.T) {
	fake, srv := newTestEnv(t, auth.Identity{ID: 1, Role: "admin", TeamID: 2})
	registerLinkRules(fake)
	fake.onExec("INSERT INTO team_line (", 1, 100)

	status, env := do(t, srv, http.MethodPost, "/api/v1/teamdata/oplines/link",
		map[string]any{"ids": []int{1, 2}})
	if status != http.StatusOK {
		t.Fatalf("关联应 200，got %d %v", status, env)
	}
	data, _ := env["data"].(map[string]any)
	if data["line_name"] != "峰义ⅠⅡ线" {
		t.Fatalf("统一线路名应为 峰义ⅠⅡ线，got %v", data["line_name"])
	}
	if id, _ := data["line_id"].(float64); id != 100 {
		t.Fatalf("统一线路应为新建线路 100，got %v", data["line_id"])
	}
	if n, _ := data["records_updated"].(float64); n <= 0 {
		t.Fatalf("应改写历史记录（records_updated > 0），got %v", data["records_updated"])
	}

	// 建统一线路、登记两条别名、删除被并入的原线路。
	if got := fake.execCount("INSERT INTO team_line ("); got != 1 {
		t.Fatalf("应执行 1 次 team_line 插入，got %d，execs=%v", got, fake.allExecs())
	}
	if got := fake.execCount("INSERT INTO team_line_link"); got != 2 {
		t.Fatalf("应为两条原线路各登记一条别名，got %d", got)
	}
	if got := fake.execCount("DELETE FROM team_line WHERE"); got != 1 {
		t.Fatalf("应删除被并入的原线路，got %d", got)
	}
	// 树障/交跨历史记录按统一线路改名。
	if got := fake.execCount("UPDATE treehazard_record"); got == 0 {
		t.Fatalf("应改写树障记录线路名，execs=%v", fake.allExecs())
	}
	if got := fake.execCount("UPDATE crossing_record"); got == 0 {
		t.Fatalf("应交跨记录线路名，execs=%v", fake.allExecs())
	}

	// 新建线路的写入参数：本班、公共电压、统一名称、最前排序。
	ins, ok := fake.lastExec("INSERT INTO team_line (")
	if !ok {
		t.Fatalf("应有 team_line 插入")
	}
	if len(ins.args) != 4 || ins.args[0] != int64(2) || ins.args[1] != "110" ||
		ins.args[2] != "峰义ⅠⅡ线" || ins.args[3] != int64(1) {
		t.Fatalf("team_line 插入参数不符，got %v", ins.args)
	}
	// 别名登记参数：峰义Ⅰ线 / 峰义Ⅱ线 → 峰义ⅠⅡ线。
	link, ok := fake.lastExec("INSERT INTO team_line_link")
	if !ok {
		t.Fatalf("应有 team_line_link 插入")
	}
	if len(link.args) != 3 || link.args[0] != int64(2) || link.args[1] != "峰义Ⅱ线" ||
		link.args[2] != "峰义ⅠⅡ线" {
		t.Fatalf("team_line_link 插入参数不符，got %v", link.args)
	}
}
