package treehazard

// 手动录入 / 编辑的线路归一回归（fake/httptest，不连真实库）：
// 已关联的统一线路（team_line + team_line_link 别名）在写入前归一为规范名，
// 后续录入「峰义Ⅰ线 / 峰义1线 / 峰义12线」等写法统一落为「峰义ⅠⅡ线」。

import (
	"database/sql/driver"
	"fmt"
	"net/http"
	"testing"
)

// lineCatalogStub 注册本班线路目录：统一线路 + 被并入的原线路名别名。
func lineCatalogStub(env *testEnv) {
	env.fake.on("FROM sys_team", []string{"id", "name"}, [][]driver.Value{{int64(1), "检修一班"}})
	env.fake.on("FROM team_line WHERE team_id = ? ORDER BY sort, id",
		[]string{"voltage_level", "line_name"}, [][]driver.Value{{"110", "峰义ⅠⅡ线"}})
	env.fake.on("FROM team_line_link WHERE team_id = ? ORDER BY id",
		[]string{"alias_name", "line_name"}, [][]driver.Value{
			{"峰义Ⅰ线", "峰义ⅠⅡ线"},
			{"峰义1线", "峰义ⅠⅡ线"},
		})
}

// TestRecordManualCreateLineAliasCanonical 手动新增：关联后的原线路写法统一为规范名。
func TestRecordManualCreateLineAliasCanonical(t *testing.T) {
	cases := []string{"峰义1线", "峰义Ⅰ线", "峰义12线"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			env := newTestServer(t, identTeamAdmin)
			lineCatalogStub(env)

			body := manualBody()
			body["line_name"] = raw
			rec := env.do(t, "POST", "/api/v1/treehazard/records", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
			}
			ex, ok := env.fake.lastExec("INSERT INTO treehazard_record")
			if !ok {
				t.Fatalf("未找到插入语句")
			}
			// args：team_id, voltage_level, line_name, ...
			if fmt.Sprint(ex.args[2]) != "峰义ⅠⅡ线" {
				t.Fatalf("line_name 应归一为 峰义ⅠⅡ线，实际 %v", ex.args[2])
			}
		})
	}
}

// TestRecordManualUpdateLineAliasCanonical 手动编辑：原始写法归一后再覆盖 line_name。
func TestRecordManualUpdateLineAliasCanonical(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	lineCatalogStub(env)
	env.fake.on("SELECT id FROM treehazard_record WHERE id = ? AND team_id = ?",
		[]string{"id"}, [][]driver.Value{{int64(5)}})

	body := manualBody()
	body["line_name"] = "峰义1线"
	rec := env.do(t, "PUT", "/api/v1/treehazard/records/5", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("编辑应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	ex, ok := env.fake.lastExec("UPDATE treehazard_record SET")
	if !ok {
		t.Fatalf("未找到更新语句")
	}
	// args：voltage_level, line_name, ...
	if fmt.Sprint(ex.args[1]) != "峰义ⅠⅡ线" {
		t.Fatalf("line_name 应归一为 峰义ⅠⅡ线，实际 %v", ex.args[1])
	}
}

// TestRecordManualCreateLineAliasFillsVoltage 手动新增：电压留空时按规范线路补电压。
func TestRecordManualCreateLineAliasFillsVoltage(t *testing.T) {
	env := newTestServer(t, identTeamAdmin)
	lineCatalogStub(env)

	body := manualBody()
	body["line_name"] = "峰义1线"
	body["voltage_level"] = ""
	rec := env.do(t, "POST", "/api/v1/treehazard/records", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	ex, ok := env.fake.lastExec("INSERT INTO treehazard_record")
	if !ok {
		t.Fatalf("未找到插入语句")
	}
	if fmt.Sprint(ex.args[1]) != "110" || fmt.Sprint(ex.args[2]) != "峰义ⅠⅡ线" {
		t.Fatalf("应补电压 110 且归一线路名，实际 %v / %v", ex.args[1], ex.args[2])
	}
}
