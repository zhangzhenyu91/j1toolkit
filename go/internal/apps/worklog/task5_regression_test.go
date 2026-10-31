// Task 5 回归：历史卡片只对新加入的成员/车辆/目的地要求启用；成员改名级联历史照片人名；
// 备注附件 cos_key 必须服务端登记且属于本卡（保存与删除均校验）；容量占位原子化。
package worklog

import (
	"database/sql/driver"
	"strings"
	"testing"
)

// logUpdatePreRules 注册 PUT /logs/{id} 通用前置查询规则（日期查重、照片人名、成员重写等）。
func logUpdatePreRules(e *testEnv, currentMembers ...int64) {
	rows := make([][]driver.Value, 0, len(currentMembers))
	for _, id := range currentMembers {
		rows = append(rows, []driver.Value{id})
	}
	e.fake.on("SELECT member_id FROM worklog_entry_member WHERE entry_id = ?", []string{"member_id"}, rows)
	e.fake.on("SELECT m.name FROM worklog_entry_member em", []string{"name"}, nil)
	e.fake.on("SELECT members FROM worklog_photo", []string{"members"}, nil)
}

// TestLogUpdateKeepsHistoricalDisabledRefs 存量卡保存时，库内已有的停用成员/车辆/目的地仍可保留；
// 只有新加入项才要求启用。
func TestLogUpdateKeepsHistoricalDisabledRefs(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	r := logUpdateRow(0, 3, 4, "")
	env.fake.on(r.match, r.cols, r.rows)
	logUpdatePreRules(env, 5)
	env.fake.on("SELECT status FROM worklog_vehicle WHERE id = ?", []string{"status"}, [][]driver.Value{{int64(0)}})
	env.fake.on("SELECT status FROM worklog_destination WHERE id = ?", []string{"status"}, [][]driver.Value{{int64(0)}})
	env.fake.on("SELECT id, sort, status FROM worklog_member", []string{"id", "sort", "status"},
		[][]driver.Value{{int64(5), int64(1), int64(0)}})

	rec := env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 3, "destination_id": 4, "member_ids": []any{5}, "patrol_content": "补记",
	})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("存量停用引用应可保存，实际 %d %s", envr.Code, envr.Message)
	}
	if env.fake.execCount("UPDATE worklog_entry SET patrol_content") != 1 {
		t.Fatalf("卡片更新未执行: %v", env.fake.allQueries())
	}

	// 新加入的停用成员仍必须拒绝
	env2 := newTestServer(t, identUser).withTeam()
	env2.fake.on(r.match, r.cols, r.rows)
	logUpdatePreRules(env2)
	env2.fake.on("SELECT id, sort FROM worklog_member", []string{"id", "sort"}, nil)
	rec = env2.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"vehicle_id": 3, "destination_id": 4, "member_ids": []any{6},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeMember {
		t.Fatalf("新加入停用成员应 %d，实际 %d %s", codeMember, envr.Code, envr.Message)
	}
}

// TestMemberRenameCascadesPhotoNames 成员改名在同一事务内级联 worklog_photo.members 历史人名。
func TestMemberRenameCascadesPhotoNames(t *testing.T) {
	env := newTestServer(t, identTM).withTeam()
	env.fake.on("SELECT id FROM worklog_member WHERE id = ? AND team_id = ?", []string{"id"},
		[][]driver.Value{{int64(5)}})
	env.fake.on("SELECT name FROM worklog_member WHERE id = ? AND team_id = ?", []string{"name"},
		[][]driver.Value{{"旧名"}})
	env.fake.on("SELECT p.id, p.members FROM worklog_photo p", []string{"id", "members"},
		[][]driver.Value{{int64(11), `["旧名","赵登"]`}})

	rec := env.do(t, "PUT", "/api/v1/worklog/admin/members/5", map[string]any{"name": "新名"})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("改名应成功，实际 %d %s", envr.Code, envr.Message)
	}
	upd, ok := env.fake.lastExec("UPDATE worklog_photo SET members = ?")
	if !ok {
		t.Fatalf("改名必须级联历史照片人名")
	}
	if len(upd.args) < 2 || !strings.Contains(upd.args[0].(string), "新名") ||
		strings.Contains(upd.args[0].(string), "旧名") {
		t.Fatalf("级联人名不符: %v", upd.args)
	}
	events := env.fake.eventLog()
	committed := false
	for _, e := range events {
		if e == "commit" {
			committed = true
		}
	}
	if !committed {
		t.Fatalf("改名级联应在事务内提交，事件 %v", events)
	}
}

// TestRemarkSaveRejectsUnregisteredKey 备注附件 cos_key 未在服务端登记（或属于他卡）时保存必须拒绝，
// 不得写入库或据此删除对象。
func TestRemarkSaveRejectsUnregisteredKey(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	r := logUpdateRow(1, 0, 0, "")
	env.fake.on(r.match, r.cols, r.rows)
	env.fake.on("COALESCE(remark, '')", []string{"remark", "remark_files"},
		[][]driver.Value{{"", "[]"}})
	env.fake.on("FROM worklog_remark_file", []string{"cos_key"}, nil)
	env.objects.items["worklog/remark/检修一班/2026.08.18/9-1.docx"] = []byte("X")

	rec := env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"patrol_content": "补记",
		"remark_files": []any{map[string]any{
			"name": "报告.docx", "url": "https://cos.test/x.docx",
			"cos_key": "worklog/remark/检修一班/2026.08.18/9-1.docx", "type": "doc", "size": 1,
		}},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeFileBad {
		t.Fatalf("未登记/他卡附件应 %d，实际 %d %s", codeFileBad, envr.Code, envr.Message)
	}
	if env.fake.execCount("UPDATE worklog_entry SET remark") != 0 {
		t.Fatal("校验失败不得写入备注")
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/9-1.docx"]; !ok {
		t.Fatal("校验失败不得删除对象")
	}
}

// TestLogDeleteOnlyDeletesRegisteredRemark 删卡只清理本卡已登记附件，库内历史脏 key 不得删他卡对象。
func TestLogDeleteOnlyDeletesRegisteredRemark(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	legit := `{"name":"本卡.pdf","url":"https://cos.test/legit.pdf","cos_key":"worklog/remark/检修一班/2026.08.18/7-1.pdf","type":"doc","size":1}`
	foreign := `{"name":"他卡.pdf","url":"https://cos.test/foreign.pdf","cos_key":"worklog/remark/检修一班/2026.08.18/9-1.pdf","type":"doc","size":1}`
	env.fake.on("SELECT e.team_id, e.cross_team, COALESCE(e.remark_files, '')",
		[]string{"team_id", "cross_team", "remark_files"},
		[][]driver.Value{{int64(1), int64(0), "[" + legit + "," + foreign + "]"}})
	env.fake.on("SELECT cos_key FROM worklog_photo WHERE entry_id = ?", []string{"cos_key"}, nil)
	env.fake.on("SELECT cos_key FROM worklog_remark_file WHERE entry_id = ?", []string{"cos_key"},
		[][]driver.Value{{"worklog/remark/检修一班/2026.08.18/7-1.pdf"}})
	env.objects.items["worklog/remark/检修一班/2026.08.18/7-1.pdf"] = []byte("A")
	env.objects.items["worklog/remark/检修一班/2026.08.18/9-1.pdf"] = []byte("B")

	rec := env.do(t, "DELETE", "/api/v1/worklog/logs/7", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("删卡应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/7-1.pdf"]; ok {
		t.Fatal("本卡已登记附件对象应删除")
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/9-1.pdf"]; !ok {
		t.Fatal("他卡附件对象不得因本卡删除而丢失")
	}
	if env.fake.execCount("DELETE FROM worklog_remark_file WHERE entry_id = ?") != 1 {
		t.Fatal("删卡应清理本卡附件登记")
	}
}

// TestRemarkUploadRegistersKey 备注上传必须先登记（entry_id + cos_key），后续保存/删除据此证明归属。
func TestRemarkUploadRegistersKey(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkEntry("[]")
	body, ct := multipartBody(t, "file", "临时文件.tmp", []byte("DOC-DATA"), map[string]string{"name": "现场记录.docx"})
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/logs/7/remark-files", body, ct)
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("上传应成功，实际 %d %s", envr.Code, envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	key, _ := data["cos_key"].(string)
	ins, ok := env.fake.lastExec("INSERT INTO worklog_remark_file")
	if !ok {
		t.Fatalf("上传必须登记附件，实际 %v", env.fake.allQueries())
	}
	if len(ins.args) < 2 || ins.args[0] != driver.Value(int64(7)) || ins.args[1] != driver.Value(key) {
		t.Fatalf("登记参数不符: %v", ins.args)
	}
}

// TestRemarkUploadCapCountsRegistrations 上限按“已登记 ∪ 卡内已有”原子计算，占位已满即拒绝。
func TestRemarkUploadCapCountsRegistrations(t *testing.T) {
	env := newTestServer(t, identUser).withTeam().withRemarkEntry("[]")
	rows := make([][]driver.Value, 0, 9)
	for i := 0; i < 9; i++ {
		rows = append(rows, []driver.Value{"k-" + string(rune('a'+i))})
	}
	env.fake.on("SELECT cos_key FROM worklog_remark_file WHERE entry_id = ?", []string{"cos_key"}, rows)
	body, ct := multipartBody(t, "file", "a.jpg", []byte("J"), map[string]string{"name": "a.jpg"})
	rec := env.doMultipart(t, "POST", "/api/v1/worklog/logs/7/remark-files", body, ct)
	if envr := decodeEnv(t, rec); envr.Code != codeFileCount {
		t.Fatalf("登记占位已满应 %d，实际 %d %s", codeFileCount, envr.Code, envr.Message)
	}
	if env.fake.execCount("INSERT INTO worklog_remark_file") != 0 {
		t.Fatal("满额不得登记新附件")
	}
}

// TestPhotoUploadSerializesMemberSlot 每人一张水印照片的占位需在事务内加行锁并复查。
func TestPhotoUploadSerializesMemberSlot(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	r := photoEntryRule()
	env.fake.on(r.match, r.cols, r.rows)
	env.fake.on("FROM worklog_photo", []string{"id", "members"}, nil)
	env.fake.on(photoMemberRule().match, photoMemberRule().cols, photoMemberRule().rows)

	rec := uploadPhotoReq(t, env, "/api/v1/worklog/logs/1/photos", tinyPNG(t), map[string]string{
		"members": `["赵登"]`,
	})
	envr := decodeEnv(t, rec)
	if envr.Code != codeOK {
		t.Fatalf("上传应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if _, ok := env.fake.lastExec("FOR UPDATE"); !ok {
		t.Fatalf("上传必须在事务内锁定卡片行后复查名额，实际查询 %v", env.fake.allQueries())
	}
	events := env.fake.eventLog()
	if len(events) < 2 || events[0] != "begin" || events[len(events)-1] != "commit" {
		t.Fatalf("名额检查与写入应处于同一事务，事件 %v", events)
	}
}
