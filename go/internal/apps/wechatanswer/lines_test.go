// 线路名归一与 team_line 匹配测试（移植 lines.js 口径）。
package wechatanswer

import (
	"context"
	"testing"
)

func TestNormalizeLineName(t *testing.T) {
	cases := map[string]string{
		"110kV吕汾1线":   "吕汾Ⅰ线",
		"吕汾1":         "吕汾Ⅰ",
		"吕汾II线":       "吕汾Ⅱ线",
		"35kV 吕汾III线": "吕汾Ⅲ线",
		" 吕汾 2 线 ":    "吕汾Ⅱ线",
		"吕汾一回":        "吕汾一回",
	}
	for raw, want := range cases {
		if got := NormalizeLineName(raw); got != want {
			t.Errorf("NormalizeLineName(%q)=%q，期望 %q", raw, got, want)
		}
	}
}

func TestMatchLine(t *testing.T) {
	lines := []TeamLine{
		{Voltage: "110", Name: "吕汾Ⅰ线"},
		{Voltage: "110", Name: "吕汾Ⅱ线"},
		{Voltage: "35", Name: "城东线"},
	}
	if m := MatchLine("110kV吕汾1线", lines); m.Status != "ok" || m.Name != "吕汾Ⅰ线" || m.Voltage != "110" {
		t.Fatalf("精确匹配失败: %+v", m)
	}
	if m := MatchLine("城东", lines); m.Status != "ok" || m.Name != "城东线" {
		t.Fatalf("补「线」匹配失败: %+v", m)
	}
	if m := MatchLine("吕汾", lines); m.Status != "ambiguous" || len(m.Candidates) != 2 {
		t.Fatalf("多候选应 ambiguous: %+v", m)
	}
	if m := MatchLine("不存在线", lines); m.Status != "unmatched" {
		t.Fatalf("未命中应 unmatched: %+v", m)
	}
}

func TestResolveLineVoltageFromTeamLine(t *testing.T) {
	m := New(nil, nil)
	m.Lines = staticLines{lines: []TeamLine{{Voltage: "110", Name: "吕汾Ⅰ线"}}}
	match := m.resolveLine(nil, 1, "吕汾1线")
	if match.Status != "ok" || match.Voltage != "110" || match.Name != "吕汾Ⅰ线" {
		t.Fatalf("resolveLine 失败: %+v", match)
	}
}

// staticLines 静态线路表。
type staticLines struct {
	lines []TeamLine
}

func (s staticLines) TeamLines(_ context.Context, _ int64) ([]TeamLine, error) {
	return s.lines, nil
}
