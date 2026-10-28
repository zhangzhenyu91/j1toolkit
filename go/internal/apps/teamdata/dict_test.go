package teamdata

// 班组字典 CRUD / 引用拦截 / 互斥标记 / 成员排序测试（fake DB 校验 SQL 与业务码）。

import (
	"database/sql/driver"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"shade/internal/auth"
)

// driverRow 简写：fake 行值。
type driverRow = driver.Value

func admin() auth.Identity { return auth.Identity{ID: 1, Role: "admin", TeamID: 2} }

// 车牌列表：SQL 形态与响应结构。
func TestVehicleList(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	fake.on("FROM worklog_vehicle WHERE team_id = ?", []string{"id", "name", "sort", "status"},
		[][]driverRow{{int64(7), "京A001", int64(1), int64(1)}})
	status, body := do(t, srv, http.MethodGet, "/api/v1/teamdata/vehicles", nil)
	if status != http.StatusOK {
		t.Fatalf("列表应 200，got %d %v", status, body)
	}
	data := body["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("应 1 条，got %v", list)
	}
	item := list[0].(map[string]any)
	if item["name"] != "京A001" || item["id"] != float64(7) {
		t.Fatalf("列表行不符: %v", item)
	}
	if _, ok := fake.lastQuery("ORDER BY sort, id"); !ok {
		t.Fatalf("应按 sort,id 排序查询")
	}
}

// 成员列表带 user_id、目的地列表带市外/驻地标记。
func TestDictListExtraColumns(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	fake.on("FROM worklog_member WHERE team_id = ?", []string{"id", "name", "sort", "status", "user_id"},
		[][]driverRow{{int64(1), "张三", int64(1), int64(1), int64(9)}, {int64(2), "李四", int64(2), int64(1), nil}})
	_, body := do(t, srv, http.MethodGet, "/api/v1/teamdata/members", nil)
	list := body["data"].(map[string]any)["list"].([]any)
	item := list[0].(map[string]any)
	if item["user_id"] != float64(9) {
		t.Fatalf("成员应带 user_id: %v", item)
	}
	if list[1].(map[string]any)["user_id"] != nil {
		t.Fatalf("未绑定账号 user_id 应为 null: %v", list[1])
	}

	fake.on("FROM worklog_destination WHERE team_id = ?", []string{"id", "name", "sort", "status", "out_of_city", "stationed"},
		[][]driverRow{{int64(2), "变电站", int64(1), int64(1), int64(1), int64(0)}})
	_, body = do(t, srv, http.MethodGet, "/api/v1/teamdata/destinations", nil)
	item = body["data"].(map[string]any)["list"].([]any)[0].(map[string]any)
	if item["out_of_city"] != float64(1) || item["stationed"] != float64(0) {
		t.Fatalf("目的地应带互斥标记: %v", item)
	}
}

// 新增：空名 40012；重名（ER_DUP_ENTRY）40900；正常返回 id 且 sort 排尾。
func TestDictCreate(t *testing.T) {
	fake, srv := newTestEnv(t, admin())

	status, body := do(t, srv, http.MethodPost, "/api/v1/teamdata/vehicles", map[string]any{"name": "  "})
	if status != http.StatusBadRequest || codeOf(body) != codeInput {
		t.Fatalf("空名应 40012，got %d %v", status, body)
	}

	fake.on("SELECT COALESCE(MAX(sort), 0) FROM worklog_vehicle", []string{"maxSort"},
		[][]driverRow{{int64(5)}})
	fake.onErr("INSERT INTO worklog_vehicle", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	status, body = do(t, srv, http.MethodPost, "/api/v1/teamdata/vehicles", map[string]any{"name": "京A002"})
	if status != http.StatusConflict || codeOf(body) != codeDup {
		t.Fatalf("重名应 40900，got %d %v", status, body)
	}

	// 换成功路径：注册 insert 规则
	fake2, srv2 := newTestEnv(t, admin())
	fake2.on("SELECT COALESCE(MAX(sort), 0) FROM worklog_vehicle", []string{"maxSort"},
		[][]driverRow{{int64(5)}})
	fake2.onExec("INSERT INTO worklog_vehicle", 1, 66)
	status, body = do(t, srv2, http.MethodPost, "/api/v1/teamdata/vehicles", map[string]any{"name": "京A002"})
	if status != http.StatusOK {
		t.Fatalf("新增应 200，got %d %v", status, body)
	}
	if body["data"].(map[string]any)["id"] != float64(66) {
		t.Fatalf("应回传 insertId: %v", body)
	}
	ex, ok := fake2.lastExec("INSERT INTO worklog_vehicle")
	if !ok {
		t.Fatalf("应有 INSERT")
	}
	// team_id, name, sort=max+1
	if len(ex.args) != 3 || ex.args[1] != "京A002" || ex.args[2] != int64(6) {
		t.Fatalf("INSERT 参数不符: %v", ex.args)
	}
}

// 目的地互斥标记：同置 1 → 40012；置市外自动清驻地。
func TestDestinationFlagExclusion(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	fake.on("FROM worklog_destination WHERE id = ?", []string{"id"}, [][]driverRow{{int64(5)}})

	status, body := do(t, srv, http.MethodPut, "/api/v1/teamdata/destinations/5",
		map[string]any{"out_of_city": 1, "stationed": 1})
	if status != http.StatusBadRequest || codeOf(body) != codeInput {
		t.Fatalf("同置 1 应 40012，got %d %v", status, body)
	}

	status, body = do(t, srv, http.MethodPut, "/api/v1/teamdata/destinations/5",
		map[string]any{"out_of_city": 1})
	if status != http.StatusOK {
		t.Fatalf("标记市外应 200，got %d %v", status, body)
	}
	ex, ok := fake.lastExec("UPDATE worklog_destination SET out_of_city = ?")
	if !ok {
		t.Fatalf("应有标记更新")
	}
	// out_of_city=1、stationed=0 写在 SQL 里、id=5
	if !containsAll(ex.query, "stationed = 0") || len(ex.args) != 2 || ex.args[0] != int64(1) || ex.args[1] != int64(5) {
		t.Fatalf("互斥标记参数不符: query=%q args=%v", ex.query, ex.args)
	}

	// 布尔入参按 JS Number(true)=1 口径。
	status, body = do(t, srv, http.MethodPut, "/api/v1/teamdata/destinations/5",
		map[string]any{"stationed": true})
	if status != http.StatusOK {
		t.Fatalf("布尔标记驻地应 200，got %d %v", status, body)
	}
	ex, ok = fake.lastExec("UPDATE worklog_destination SET stationed = ?")
	if !ok || ex.args[0] != int64(1) {
		t.Fatalf("布尔 true 应写 1: %+v", ex)
	}
}

// 删除：被日志引用 → 40901（带条数）；无引用 → 删除。
func TestDictDeleteInUse(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	fake.on("SELECT COUNT(*) FROM worklog_entry WHERE vehicle_id = ?", []string{"cnt"},
		[][]driverRow{{int64(3)}})
	status, body := do(t, srv, http.MethodDelete, "/api/v1/teamdata/vehicles/7", nil)
	if status != http.StatusConflict || codeOf(body) != codeInUse {
		t.Fatalf("被引用应 40901，got %d %v", status, body)
	}
	msg, _ := body["message"].(string)
	if !containsAll(msg, "3", "改为停用") {
		t.Fatalf("提示应含引用条数与改停用指引: %q", msg)
	}
	if fake.execCount("DELETE FROM worklog_vehicle") != 0 {
		t.Fatalf("被引用不应执行删除")
	}

	fake.on("SELECT COUNT(*) FROM worklog_entry WHERE vehicle_id = ?", []string{"cnt"},
		[][]driverRow{{int64(0)}})
	fake.onExec("DELETE FROM worklog_vehicle", 1, 0)
	status, body = do(t, srv, http.MethodDelete, "/api/v1/teamdata/vehicles/7", nil)
	if status != http.StatusOK {
		t.Fatalf("无引用应 200，got %d %v", status, body)
	}
}

// 成员排序：上移换位并整体重写 sort 为 1..N；顶端上移保持不变。
func TestMemberMove(t *testing.T) {
	fake, srv := newTestEnv(t, admin())
	fake.on("SELECT id FROM worklog_member WHERE team_id = ? ORDER BY sort, id", []string{"id"},
		[][]driverRow{{int64(10)}, {int64(20)}, {int64(30)}})

	status, body := do(t, srv, http.MethodPut, "/api/v1/teamdata/members/20/move",
		map[string]any{"direction": "up"})
	if status != http.StatusOK {
		t.Fatalf("上移应 200，got %d %v", status, body)
	}
	execs := fake.allExecs()
	updates := []fakeExec{}
	for _, e := range execs {
		if containsAll(e.query, "UPDATE worklog_member SET sort") {
			updates = append(updates, e)
		}
	}
	if len(updates) != 3 {
		t.Fatalf("应重写 3 行 sort，got %d", len(updates))
	}
	// 20 上移到首位：sort(1,20) sort(2,10) sort(3,30)
	if updates[0].args[0] != int64(1) || updates[0].args[1] != int64(20) ||
		updates[1].args[0] != int64(2) || updates[1].args[1] != int64(10) {
		t.Fatalf("换位后 sort 顺序不符: %v %v", updates[0].args, updates[1].args)
	}

	// 顶端上移：次序不变
	fake2, srv2 := newTestEnv(t, admin())
	fake2.on("SELECT id FROM worklog_member WHERE team_id = ? ORDER BY sort, id", []string{"id"},
		[][]driverRow{{int64(10)}, {int64(20)}})
	status, body = do(t, srv2, http.MethodPut, "/api/v1/teamdata/members/10/move",
		map[string]any{"direction": "up"})
	if status != http.StatusOK {
		t.Fatalf("顶端上移应 200，got %d %v", status, body)
	}
	if fake2.execCount("UPDATE worklog_member SET sort") != 0 {
		t.Fatalf("顶端上移不应重写 sort")
	}
}

// 小工具：driver.Value 转文本 / 子串全含判定。
func toString(v any) string { return fmt.Sprintf("%v", v) }

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
