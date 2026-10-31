// Task 5 round1 回归：删卡先事务后删对象、DB 失败保留对象、备注占位 TTL 回收、
// 保存附件时回收过期占位、改名查询防脏 JSON、对象删除失败可追踪。
package worklog

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

// TestLogDeleteDBFailureKeepsObjects 删卡事务失败时不得先删 COS 对象（避免行还在对象已无）。
func TestLogDeleteDBFailureKeepsObjects(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("SELECT e.team_id, e.cross_team, COALESCE(e.remark_files, '')",
		[]string{"team_id", "cross_team", "remark_files"},
		[][]driver.Value{{int64(1), int64(0), "[]"}})
	env.fake.on("SELECT cos_key FROM worklog_photo WHERE entry_id = ?", []string{"cos_key"},
		[][]driver.Value{{"worklog/a/1-1.jpg"}})
	env.fake.on("SELECT cos_key FROM worklog_remark_file WHERE entry_id = ?", []string{"cos_key"}, nil)
	env.fake.onErr("DELETE FROM worklog_entry WHERE id = ?", errors.New("db down"))
	env.objects.items["worklog/a/1-1.jpg"] = []byte("JPEG")

	rec := env.do(t, "DELETE", "/api/v1/worklog/logs/7", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeInternal && envr.Code != codeParam {
		t.Fatalf("DB 失败应返回错误，实际 %d %s", envr.Code, envr.Message)
	}
	if _, ok := env.objects.items["worklog/a/1-1.jpg"]; !ok {
		t.Fatalf("事务失败不得删除对象: %v", env.objects.deletes)
	}
}

// TestLogDeleteCommitBeforeObjectDelete 事务提交成功后才删对象；对象删除失败仍返回成功并记日志。
func TestLogDeleteCommitBeforeObjectDelete(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("SELECT e.team_id, e.cross_team, COALESCE(e.remark_files, '')",
		[]string{"team_id", "cross_team", "remark_files"},
		[][]driver.Value{{int64(1), int64(0), "[]"}})
	env.fake.on("SELECT cos_key FROM worklog_photo WHERE entry_id = ?", []string{"cos_key"},
		[][]driver.Value{{"worklog/a/1-1.jpg"}})
	env.fake.on("SELECT cos_key FROM worklog_remark_file WHERE entry_id = ?", []string{"cos_key"}, nil)
	env.objects.items["worklog/a/1-1.jpg"] = []byte("JPEG")
	env.objects.deleteErr = errors.New("COS 删除超时")
	var deletedBeforeCommit bool
	env.objects.onDelete = func(string) {
		committed := false
		for _, e := range env.fake.eventLog() {
			if e == "commit" {
				committed = true
			}
		}
		if !committed {
			deletedBeforeCommit = true
		}
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	rec := env.do(t, "DELETE", "/api/v1/worklog/logs/7", nil)
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("对象删除失败不影响删卡成功，实际 %d %s", envr.Code, envr.Message)
	}
	if deletedBeforeCommit {
		t.Fatal("对象删除必须在事务提交之后")
	}
	if !strings.Contains(buf.String(), "删除") || !strings.Contains(buf.String(), "COS 删除超时") {
		t.Fatalf("对象删除失败必须记日志: %q", buf.String())
	}
}

// TestRemarkPlaceholderSweep 过期且未被 remark_files 引用的登记行清理对象与记录；
// 已绑定（被引用）的登记行保留。
func TestRemarkPlaceholderSweep(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("UPDATE worklog_photo SET verify_status", []string{"v"}, nil)
	env.fake.on("FROM worklog_remark_file r", []string{"id", "cos_key"},
		[][]driver.Value{{int64(9), "worklog/remark/检修一班/2026.08.18/7-9.docx"}})
	env.objects.items["worklog/remark/检修一班/2026.08.18/7-9.docx"] = []byte("X")
	env.objects.items["worklog/remark/检修一班/2026.08.18/7-8.docx"] = []byte("Y")

	if err := env.module.sweepPendingPhotos(context.Background()); err != nil {
		t.Fatalf("sweeper: %v", err)
	}
	// 回收 SQL 必须按“过期 + 未被 remark_files 引用”筛选，已绑定行不进入结果集。
	q, ok := env.fake.lastQuery("FROM worklog_remark_file r")
	if !ok || !strings.Contains(q.query, "NOT JSON_CONTAINS") || !strings.Contains(q.query, "created_at <") {
		t.Fatalf("占位回收 SQL 应同时校验过期与未引用: %v", q)
	}
	if env.fake.execCount("DELETE FROM worklog_remark_file WHERE id = ?") != 1 {
		t.Fatalf("应删除 1 条过期未引用登记，实际 %d", env.fake.execCount("DELETE FROM worklog_remark_file"))
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/7-9.docx"]; ok {
		t.Fatal("过期未引用占位的对象应删除")
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/7-8.docx"]; !ok {
		t.Fatal("已绑定/未过期对象不得删除")
	}
}

// TestRemarkSaveReclaimsExpiredPlaceholder 保存附件时顺带回收本卡过期未引用占位。
func TestRemarkSaveReclaimsExpiredPlaceholder(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	r := logUpdateRow(1, 0, 0, "")
	env.fake.on(r.match, r.cols, r.rows)
	env.fake.on("COALESCE(remark, '')", []string{"remark", "remark_files"},
		[][]driver.Value{{"", "[]"}})
	env.fake.on("FROM worklog_remark_file r", []string{"id", "cos_key"},
		[][]driver.Value{{int64(11), "worklog/remark/检修一班/2026.08.18/7-11.docx"}})
	env.fake.on("FROM worklog_remark_file", []string{"cos_key"}, nil)
	env.objects.items["worklog/remark/检修一班/2026.08.18/7-11.docx"] = []byte("Z")

	rec := env.do(t, "PUT", "/api/v1/worklog/logs/7", map[string]any{
		"patrol_content": "巡视", "remark_files": []any{},
	})
	if envr := decodeEnv(t, rec); envr.Code != codeOK {
		t.Fatalf("保存应成功，实际 %d %s", envr.Code, envr.Message)
	}
	if env.fake.execCount("DELETE FROM worklog_remark_file WHERE id = ?") != 1 {
		t.Fatalf("保存时应回收过期占位，实际 %d", env.fake.execCount("DELETE FROM worklog_remark_file"))
	}
	if _, ok := env.objects.items["worklog/remark/检修一班/2026.08.18/7-11.docx"]; ok {
		t.Fatal("回收的占位对象应删除")
	}
}

// TestMemberRenameGuardsInvalidJSON 改名级联查询用 JSON_VALID 防老脏数据令 JSON_CONTAINS 报错。
func TestMemberRenameGuardsInvalidJSON(t *testing.T) {
	env := newTestServer(t, identTM).withTeam()
	env.fake.on("SELECT p.id, p.members FROM worklog_photo p", []string{"id", "members"}, nil)
	if err := cascadeMemberRename(context.Background(), env.module.DB, 5, "旧名", "新名"); err != nil {
		t.Fatalf("级联失败: %v", err)
	}
	q, ok := env.fake.lastQuery("JSON_CONTAINS")
	if !ok {
		t.Fatal("未执行改名级联查询")
	}
	if !strings.Contains(q.query, "JSON_VALID") {
		t.Fatalf("级联查询必须加 JSON_VALID 防脏数据: %s", q.query)
	}
}
