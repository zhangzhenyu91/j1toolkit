package quiz

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

type countingAnalyzer struct {
	calls int
	text  string
}

func (a *countingAnalyzer) Configured() bool { return true }
func (a *countingAnalyzer) Analyze(context.Context, QuestionForAI) (string, error) {
	a.calls++
	return a.text, nil
}

// AI 回写必须以 pending + 快照为 CAS：只有分析文本仍与入队时一致才允许写回。
func TestAnalyzerWritebackUsesPendingCAS(t *testing.T) {
	db, fake := openFake(t)
	fake.on("SELECT type, content, options, answer", []string{"type", "content", "options", "answer", "analysis", "analysis_status"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A", "旧解析", "failed"}})
	an := &countingAnalyzer{text: "AI 解析"}
	q := newAnalysisQueue(db, an, nil)
	q.processOne(context.Background(), 7)
	if an.calls != 1 {
		t.Fatalf("应调用一次 AI 解析，实际 %d", an.calls)
	}
	pending, ok := fake.lastExec("SET analysis_status = 'pending'")
	if !ok || !strings.Contains(pending.query, "analysis_status = ?") || !strings.Contains(pending.query, "COALESCE(analysis") {
		t.Fatalf("置 pending 必须带状态/快照 CAS：%+v", pending)
	}
	done, ok := fake.lastExec("analysis_status = 'done'")
	if !ok {
		t.Fatal("应写回解析结果")
	}
	if !strings.Contains(done.query, "analysis_status = 'pending'") && !strings.Contains(done.query, "analysis_status = ?") {
		t.Fatalf("回写必须限定 pending：%s", done.query)
	}
	if !strings.Contains(done.query, "COALESCE(analysis") {
		t.Fatalf("回写必须带分析快照 CAS：%s", done.query)
	}
	if len(done.args) < 3 || done.args[len(done.args)-1] != driver.Value("旧解析") {
		t.Fatalf("CAS 快照参数不符：%v", done.args)
	}
}

// 人工已改解析/已终态的题不得被队列覆盖。
func TestAnalyzerSkipsAlreadyEditedQuestion(t *testing.T) {
	db, fake := openFake(t)
	fake.on("SELECT type, content, options, answer", []string{"type", "content", "options", "answer", "analysis", "analysis_status"},
		[][]driver.Value{{"single", "题干", `["甲","乙"]`, "A", "人工解析", "done"}})
	an := &countingAnalyzer{text: "AI 解析"}
	q := newAnalysisQueue(db, an, nil)
	q.processOne(context.Background(), 8)
	if an.calls != 0 {
		t.Fatalf("已人工修改/已终态的题不应再调用 AI，实际 %d 次", an.calls)
	}
	if n := fake.execCount("analysis_status = 'pending'"); n != 0 {
		t.Fatalf("不应把人工解析题改为 pending，实际 %d 次", n)
	}
	if n := fake.execCount("analysis_status = 'done'"); n != 0 {
		t.Fatalf("不得覆盖人工解析，实际写入 %d 次", n)
	}
}
