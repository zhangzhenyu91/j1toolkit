package crossing

// 交跨记录线路归一回归（fake/httptest，不连真实库）：
// 写入手动新增按 team_line + team_line_link 别名归一，读取列表按线路目录归一后再分组。

import (
	"database/sql/driver"
	"testing"

	"shade/internal/auth"
)

// TestRecordCreateCanonicalizesLinkedLine 手动新增：关联后的原线路写法统一为规范名。
func TestRecordCreateCanonicalizesLinkedLine(t *testing.T) {
	cases := []string{"峰义12线", "峰义Ⅰ线", "峰义1线"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			db, fdb := openFake(t)
			teamStub(fdb, 5, "检修一班")
			fdb.onRowsAlways("FROM team_line WHERE team_id", []string{"voltage_level", "line_name"},
				[]driver.Value{"110", "峰义ⅠⅡ线"})
			fdb.onRowsAlways("FROM team_line_link WHERE team_id = ?", []string{"alias_name", "line_name"},
				[]driver.Value{"峰义Ⅰ线", "峰义ⅠⅡ线"},
				[]driver.Value{"峰义1线", "峰义ⅠⅡ线"},
			)
			m := New(db, fakeCfg{})
			h := newTestRouter(m, auth.Identity{ID: 9, Role: "team_admin", TeamID: 5})
			rec := doJSON(t, h, "POST", "/crossing/records", "t",
				`{"line_name": "`+raw+`"}`)
			if rec.Code != 200 {
				t.Fatalf("手动新增应 200，实际 %d body=%s", rec.Code, rec.Body.String())
			}
			inserts := fdb.callsWith("INSERT INTO crossing_record")
			if len(inserts) != 1 {
				t.Fatalf("应插入 1 次，实际 %d", len(inserts))
			}
			// 顺序：team_id, voltage_level, line_name, ...
			args := inserts[0].args
			if args[2] != driver.Value("峰义ⅠⅡ线") {
				t.Fatalf("line_name 应归一为 峰义ⅠⅡ线，实际 %v", args[2])
			}
			if args[1] != driver.Value("110") {
				t.Fatalf("电压留空应按规范线路补 110，实际 %v", args[1])
			}
		})
	}
}

// TestRecordListCanonicalizesAliasIntoTeamLine 读取分组：记录原始写法归一后进入运维线路组，不进 unmatched。
func TestRecordListCanonicalizesAliasIntoTeamLine(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM team_line WHERE team_id", []string{"voltage_level", "line_name"},
		[]driver.Value{"110", "峰义Ⅰ线"})
	fdb.onRowsAlways("FROM team_line_link WHERE team_id = ?", []string{"alias_name", "line_name"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", recordColsOf(),
		recordRow(1, 5, "110", "峰义1线", "2#-3#", 2, 3),
	)
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/records", "t", "")
	if rec.Code != 200 {
		t.Fatalf("记录列表应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	data := decodeEnvelope(t, rec)["data"].(map[string]any)
	lines := data["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("应只有 1 个运维线路组，实际 %d", len(lines))
	}
	group := lines[0].(map[string]any)
	if group["line_name"] != "峰义Ⅰ线" {
		t.Fatalf("组名应为 峰义Ⅰ线，实际 %v", group["line_name"])
	}
	recs := group["records"].([]any)
	if len(recs) != 1 {
		t.Fatalf("该组应含 1 条记录，实际 %d", len(recs))
	}
	if got := recs[0].(map[string]any)["line_name"]; got != "峰义Ⅰ线" {
		t.Fatalf("记录条线路名应归一为 峰义Ⅰ线，实际 %v", got)
	}
	unmatched := data["unmatched"].([]any)
	if len(unmatched) != 0 {
		t.Fatalf("不应有未匹配记录，实际 %v", unmatched)
	}
}
