package appshell

import "testing"

func TestLogViewBatchesRenderingAndKeepsLatestLines(t *testing.T) {
	v := NewLogView()
	v.Append("第一行")
	v.Append("第二行")
	if !v.dirty {
		t.Fatal("连续追加后应等待合并刷新")
	}
	if len(v.rich.Segments) != 0 {
		t.Fatal("定时刷新前不应逐行重建富文本")
	}
	if got := len(v.Lines()); got != 2 {
		t.Fatalf("日志数据应立即可读，行数=%d", got)
	}
	v.flushPending()
	if len(v.rich.Segments) != 2 {
		t.Fatalf("合并刷新后应有 2 段，实际 %d", len(v.rich.Segments))
	}
	v.Append("第三行")
	v.SetText("替换")
	if v.dirty || v.flush != nil {
		t.Fatal("整体替换应取消待刷新任务")
	}
	if len(v.rich.Segments) != 1 || len(v.Lines()) != 1 {
		t.Fatal("整体替换应立即生效")
	}
}
