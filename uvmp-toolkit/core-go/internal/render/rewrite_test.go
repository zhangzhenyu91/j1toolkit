package render

import (
	"strings"
	"testing"
	"time"
)

func TestBuildPopupRewriteJS(t *testing.T) {
	oldDT := time.Date(2026, 10, 28, 11, 59, 58, 0, time.Local)
	newDT := time.Date(2026, 10, 28, 12, 0, 10, 0, time.Local)
	js := BuildPopupRewriteJS(oldDT, newDT, 3600, 3612)

	for _, want := range []string{
		"var oldC = [2026,10,28,11,59,58]",
		"newC = [2026,10,28,12,0,10]",
		"oldDur = 3600",
		"newDur = 3612",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("生成脚本缺少 %q", want)
		}
	}
	// 占位符必须全部替换干净
	for _, ph := range []string{"__OLDC__", "__NEWC__", "__OLDDUR__", "__NEWDUR__"} {
		if strings.Contains(js, ph) {
			t.Fatalf("占位符 %s 未替换", ph)
		}
	}
	// 关键改写逻辑保留（按值匹配、±90s 容差、小数小时）
	for _, want := range []string{"timeRe", "durRe", "Math.abs(sec - oldDur) > 90", "toFixed(dec)"} {
		if !strings.Contains(js, want) {
			t.Fatalf("改写脚本缺少关键逻辑 %q", want)
		}
	}
}

func TestBuildPopupRewriteJSDurations(t *testing.T) {
	dt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	js := BuildPopupRewriteJS(dt, dt, 0, 90)
	if !strings.Contains(js, "oldDur = 0") || !strings.Contains(js, "newDur = 90") {
		t.Fatalf("时长注入不对: %s", js)
	}
}
