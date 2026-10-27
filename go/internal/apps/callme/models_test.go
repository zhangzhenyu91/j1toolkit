package callme

// 模型提示识别与匹配测试（extractModelHint / matchModelByHint 口径）。

import "testing"

func TestExtractModelHint(t *testing.T) {
	// 候选捕获跟随 JS 正则（可含空格与中文）；是否模型提示由 hasASCII 判定，
	// 具体模型名由 MatchModelByHint 按 1–3 词前缀段收缩匹配。
	cases := []struct {
		query string
		want  string
	}{
		{"请使用 deepseek-v3 帮我查一下", "deepseek-v3 帮我查一下"},
		{"换成Qwen3分析这份报告", "Qwen3分析这份报告"},
		{"如何使用万用表测量电阻", ""}, // 纯中文候选不视为模型提示
		{"今天天气怎么样", ""},     // 无候选
		{"切换到 GLM-4.5 之后总结全文", "GLM-4.5 之后总结全文"},
	}
	for _, c := range cases {
		if got := ExtractModelHint(c.query); got != c.want {
			t.Fatalf("ExtractModelHint(%q)=%q, want %q", c.query, got, c.want)
		}
	}
}

func TestMatchModelByHint(t *testing.T) {
	models := []Model{
		{ID: "deepseek-v3", Name: "DeepSeek V3", Type: "KnowledgeQA"},
		{ID: "qwen3-235b", DisplayName: "Qwen3 235B", Type: "KnowledgeQA",
			Capabilities: map[string]any{"thinking_levels": []any{"low", "high"}}},
		{ID: "qwen3-mini", DisplayName: "Qwen3 Mini", Type: "KnowledgeQA"},
		{ID: "vision-x", Name: "Vision X", Type: "Vision"}, // 非 KnowledgeQA 由调用方过滤
	}
	models = KnowledgeQAModels(models)

	// 真实 query 捕获的候选（含中文尾段）也应命中 deepseek-v3。
	hit := MatchModelByHint(ExtractModelHint("请使用 deepseek-v3 帮我查一下"), models)
	if hit == nil || hit.ID != "deepseek-v3" {
		t.Fatalf("deepseek-v3 未命中: %+v", hit)
	}

	// 「qwen3」前缀命中两个 → 取规范化名最长者（qwen3-235b 的 display_name 更长）。
	hit = MatchModelByHint("qwen3", models)
	if hit == nil || hit.ID != "qwen3-235b" {
		t.Fatalf("qwen3 命中错误: %+v", hit)
	}
	if hit.ReasoningEffort != "high" {
		t.Fatalf("思考强度应取最高档 high, got %q", hit.ReasoningEffort)
	}

	if hit := MatchModelByHint("不存在的模型", models); hit != nil {
		t.Fatalf("不该命中: %+v", hit)
	}
}

func TestNormModelKey(t *testing.T) {
	if NormModelKey("Deep-Seek_V3.1 ") != "deepseekv31" {
		t.Fatalf("NormModelKey 归一化错误: %q", NormModelKey("Deep-Seek_V3.1 "))
	}
}
