package treehazard

// 手动新增 / 编辑回归（fake/httptest，不连真实库）：字段归一（日期、pole 由区段解析、vertical_num 重算）、
// 校验失败不落库、权限门控（普通用户 403 / 他班班管 403）、不能越班（UPDATE 带 team_id）、
// 锚点语义（不动 crane 幂等锚点与 synced_at，新增 source='manual'、不写删除墓碑）。

import (
	"database/sql/driver"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// manualBody 手动表单常规入参（发现侧 + 处理侧）。
func manualBody() map[string]any {
	return map[string]any{
		"voltage_level": "110",
		"line_name":     "甲线",
		"section":       "3#-4#",
		"position_note": "路边",
		"tree_species":  "杨树",
		"tree_count":    "2",
		"dist_small":    "5",
		"dist_big":      "6",
		"diameter":      "20",
		"horizontal":    "3",
		"vertical":      "超高2.5",
		"finder_name":   "张三",
		"found_at":      "2026.10.20",
		"handler_name":  "李四",
		"handle_note":   "削枝",
		"handled_at":    "2026-10-21",
	}
}

// TestRecordManualCreate 手动新增：source='manual' + 日期归一 + pole 由区段解析 + vertical_num 重算。
func TestRecordManualCreate(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})

	rec := env.do(t, "POST", "/api/v1/treehazard/records", manualBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["id"]) != "100" {
		t.Fatalf("应返回新 id=100，实际 %v", data)
	}
	if env.fake.execCount("INSERT INTO treehazard_record") != 1 {
		t.Fatalf("应插入 1 行，实际 %v", env.fake.allQueries())
	}
	ex, ok := env.fake.lastExec("INSERT INTO treehazard_record")
	if !ok {
		t.Fatalf("未找到插入语句")
	}
	q := ex.query
	if !strings.Contains(q, "'manual'") {
		t.Fatalf("source 应固定 'manual'：%s", q)
	}
	if strings.Contains(q, "synced_at") || strings.Contains(q, "crane_session_id") || strings.Contains(q, "crane_record_id") {
		t.Fatalf("新增不应触碰 crane 锚点 / synced_at：%s", q)
	}
	// args：team_id + 20 业务列 + created_by
	if len(ex.args) != 22 {
		t.Fatalf("参数应 22 个，实际 %d：%v", len(ex.args), ex.args)
	}
	if fmt.Sprint(ex.args[0]) != "1" {
		t.Fatalf("team_id 应 1，实际 %v", ex.args[0])
	}
	if fmt.Sprint(ex.args[4]) != "3" || fmt.Sprint(ex.args[5]) != "4" {
		t.Fatalf("pole 应由区段解析为 3/4，实际 %v %v", ex.args[4], ex.args[5])
	}
	if fmt.Sprint(ex.args[13]) != "超高2.5" {
		t.Fatalf("垂直原文应原样，实际 %v", ex.args[13])
	}
	if fmt.Sprint(ex.args[14]) != "2.5" {
		t.Fatalf("vertical_num 应重算 2.5，实际 %v", ex.args[14])
	}
	if fmt.Sprint(ex.args[16]) != "2026-10-20" {
		t.Fatalf("发现时间应归一 2026-10-20，实际 %v", ex.args[16])
	}
	if fmt.Sprint(ex.args[20]) != "handled" {
		t.Fatalf("有处理时间应 status=handled，实际 %v", ex.args[20])
	}
	if fmt.Sprint(ex.args[21]) != "2" {
		t.Fatalf("created_by 应为当前班管 id=2，实际 %v", ex.args[21])
	}
	if env.fake.execCount("treehazard_deleted_id") != 0 {
		t.Fatalf("新增不应写删除墓碑，实际 %v", env.fake.allQueries())
	}
}

// TestRecordManualCreateUnhandled 新增无处理时间：清空处理三列并回退未处理。
func TestRecordManualCreateUnhandled(t *testing.T) {
	env := newTestServer(t, identAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	body := manualBody()
	body["handled_at"] = ""
	body["handler_name"] = "李四"
	body["handle_note"] = "削枝"

	rec := env.do(t, "POST", "/api/v1/treehazard/records", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	ex, ok := env.fake.lastExec("INSERT INTO treehazard_record")
	if !ok {
		t.Fatalf("未找到插入语句")
	}
	if fmt.Sprint(ex.args[17]) != "" || fmt.Sprint(ex.args[18]) != "" || ex.args[19] != nil {
		t.Fatalf("handled_at 空应清空处理三列，实际 %v %v %v", ex.args[17], ex.args[18], ex.args[19])
	}
	if fmt.Sprint(ex.args[20]) != "unhandled" {
		t.Fatalf("应 status=unhandled，实际 %v", ex.args[20])
	}
}

// TestRecordManualValidation 校验失败 40001 且不落库：缺线路名 / 日期不可识别 / 非法 JSON。
func TestRecordManualValidation(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})

	rec := env.do(t, "POST", "/api/v1/treehazard/records", map[string]any{"section": "1#-2#"})
	if env := decodeEnvelope(t, rec); env.Code != codeParam {
		t.Fatalf("缺线路名应 40001，实际 %d", env.Code)
	}

	body := manualBody()
	body["found_at"] = "2026年10月20日"
	rec = env.do(t, "POST", "/api/v1/treehazard/records", body)
	if env := decodeEnvelope(t, rec); env.Code != codeParam {
		t.Fatalf("发现时间不可识别应 40001，实际 %d", env.Code)
	}

	body = manualBody()
	body["handled_at"] = "abc"
	rec = env.do(t, "PUT", "/api/v1/treehazard/records/5", body)
	if env := decodeEnvelope(t, rec); env.Code != codeParam {
		t.Fatalf("处理时间不可识别应 40001，实际 %d", env.Code)
	}

	if n := env.fake.execCount("INSERT INTO treehazard_record"); n != 0 {
		t.Fatalf("校验失败不应插入，实际 %d 条：%v", n, env.fake.allQueries())
	}
	if n := env.fake.execCount("UPDATE treehazard_record"); n != 0 {
		t.Fatalf("校验失败不应更新，实际 %d 条：%v", n, env.fake.allQueries())
	}
}

// TestRecordManualPermission 权限门控：普通用户 40304；他班班管 40304（requireManagerNow 口径）。
func TestRecordManualPermission(t *testing.T) {
	envUser := newTestServer(t, identUser)
	envUser.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	if rec := envUser.do(t, "POST", "/api/v1/treehazard/records", manualBody()); rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户新增应 403，实际 %d", rec.Code)
	}
	if rec := envUser.do(t, "PUT", "/api/v1/treehazard/records/5", manualBody()); rec.Code != http.StatusForbidden {
		t.Fatalf("普通用户编辑应 403，实际 %d", rec.Code)
	}

	envOther := newTestServer(t, identOtherTM)
	envOther.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	if rec := envOther.do(t, "POST", "/api/v1/treehazard/records", manualBody()); rec.Code != http.StatusForbidden {
		t.Fatalf("他班班管新增应 403，实际 %d", rec.Code)
	}
	if rec := envOther.do(t, "PUT", "/api/v1/treehazard/records/5", manualBody()); rec.Code != http.StatusForbidden {
		t.Fatalf("他班班管编辑应 403，实际 %d", rec.Code)
	}
	if n := envUser.fake.execCount("INSERT INTO treehazard_record") + envOther.fake.execCount("INSERT INTO treehazard_record"); n != 0 {
		t.Fatalf("无权限不应落库，实际 %d", n)
	}
}

// TestRecordManualUpdate 手动编辑：UPDATE 带 team_id 不能越班、不动 crane 锚点与 synced_at、
// vertical 重算 vertical_num、handled_at 空回退未处理。
func TestRecordManualUpdate(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(5)}})

	body := manualBody()
	body["vertical"] = "3-4"
	body["handled_at"] = ""
	rec := env.do(t, "PUT", "/api/v1/treehazard/records/5", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("编辑应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := dataMap(t, decodeEnvelope(t, rec))
	if fmt.Sprint(data["id"]) != "5" {
		t.Fatalf("应返回 id=5，实际 %v", data)
	}
	ex, ok := env.fake.lastExec("UPDATE treehazard_record SET")
	if !ok {
		t.Fatalf("未找到更新语句")
	}
	q := ex.query
	if !strings.Contains(q, "WHERE id = ? AND team_id = ?") {
		t.Fatalf("更新应带 id+team_id 防越班：%s", q)
	}
	if strings.Contains(q, "synced_at") || strings.Contains(q, "crane_session_id") || strings.Contains(q, "crane_record_id") {
		t.Fatalf("编辑不应触碰 crane 锚点 / synced_at：%s", q)
	}
	if fmt.Sprint(ex.args[len(ex.args)-2]) != "5" || fmt.Sprint(ex.args[len(ex.args)-1]) != "1" {
		t.Fatalf("末两位参数应为 id=5 / team_id=1，实际 %v", ex.args)
	}
	if fmt.Sprint(ex.args[13]) != "3" {
		t.Fatalf("vertical_num 应取首个数字 3，实际 %v", ex.args[13])
	}
	if fmt.Sprint(ex.args[19]) != "unhandled" {
		t.Fatalf("handled_at 空应 status=unhandled，实际 %v", ex.args[19])
	}
}

// TestRecordManualUpdateNotFound 越班 / 不存在：按 id+team_id 定位不到 → 404，不更新。
func TestRecordManualUpdateNotFound(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	// 无命中行（他班记录同样走此路径）。
	env.fake.on("SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{})

	rec := env.do(t, "PUT", "/api/v1/treehazard/records/9", manualBody())
	if env := decodeEnvelope(t, rec); env.Code != codeNotFound {
		t.Fatalf("定位不到应 40400，实际 %d", env.Code)
	}
	if n := env.fake.execCount("UPDATE treehazard_record"); n != 0 {
		t.Fatalf("定位不到不应更新，实际 %d 条：%v", n, env.fake.allQueries())
	}
}

// TestRecordManualSectionLocationSplit 手动录入区段与位置分离：区段只留杆塔区段（字母杆号保留），
// 尾部位置说明并入 position_note（此处入参未带 position_note，取区段拆出值）。
func TestRecordManualSectionLocationSplit(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	body := manualBody()
	body["section"] = "18A#-19#位于××附近"
	body["position_note"] = ""

	rec := env.do(t, "POST", "/api/v1/treehazard/records", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	ex, ok := env.fake.lastExec("INSERT INTO treehazard_record")
	if !ok {
		t.Fatalf("未找到插入语句")
	}
	if fmt.Sprint(ex.args[3]) != "18A#-19#" || ex.args[4] != nil || ex.args[5] != nil {
		t.Fatalf("区段应为 18A#-19# 且锚点留空，实际 %v %v %v", ex.args[3], ex.args[4], ex.args[5])
	}
	if fmt.Sprint(ex.args[6]) != "位于××附近" {
		t.Fatalf("位置说明应拆入 position_note，实际 %v", ex.args[6])
	}
}
