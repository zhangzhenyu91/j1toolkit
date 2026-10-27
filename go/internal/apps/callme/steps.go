package callme

// 步骤树标题/摘要（精简版 utils/agentsteps.js）：工具节点展示就绪的 pendingTitle / doneTitle /
// toolSummary 与 buildSteps（agent_steps → 步骤树 + 统计）。完整工具名映射按需扩展。

import (
	"fmt"
	"strings"
)

// toolLabel 工具名 → 展示名。
func toolLabel(name string) string {
	switch name {
	case "web_search":
		return "联网搜索"
	case "knowledge_search", "retrieve_knowledge":
		return "知识检索"
	case "calculator":
		return "计算"
	default:
		if name == "" {
			return "工具调用"
		}
		return name
	}
}

// PendingTitle 调用中标题。
func PendingTitle(name string) string { return "正在调用「" + toolLabel(name) + "」" }

// DoneTitle 完成态标题（失败前缀「调用失败」）。
func DoneTitle(name string, args map[string]any, data map[string]any, success bool) string {
	if !success {
		return "调用失败「" + toolLabel(name) + "」"
	}
	if q := extractQuery(name, args, data); q != "" {
		return toolLabel(name) + "：" + truncateRunes(q, 30)
	}
	return "已完成「" + toolLabel(name) + "」"
}

// ToolSummary 完成摘要（命中数 / 结果片段）。
func ToolSummary(name string, data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"summary", "result_summary"} {
		if v := StringOf(data, key); v != "" {
			return truncateRunes(v, 120)
		}
	}
	for _, key := range []string{"count", "total", "hit_count"} {
		if v := StringOf(data, key); v != "" {
			return "命中 " + v + " 条"
		}
	}
	if r := StringOf(data, "result"); r != "" {
		return truncateRunes(r, 120)
	}
	return ""
}

func extractQuery(name string, args map[string]any, data map[string]any) string {
	for _, m := range []map[string]any{args, data} {
		if m == nil {
			continue
		}
		for _, key := range []string{"query", "keyword", "q", "search_query", "question"} {
			if v := StringOf(m, key); v != "" {
				return v
			}
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// StepMeta 步骤树统计。
type StepMeta struct {
	ToolCalls int `json:"toolCalls"`
	// DurationMs 由调用方并入（agent_duration_ms）。
}

// Step 步骤树节点（精简：type=title/thinking/tool，展示就绪）。
type Step struct {
	Type    string `json:"type"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content,omitempty"`
	Success *bool  `json:"success,omitempty"`
}

// BuildSteps agent_steps 数组 → 步骤树 + 统计（精简实现：合并思考文本、工具调用按序展开）。
func BuildSteps(agentSteps []map[string]any) ([]Step, StepMeta) {
	var steps []Step
	meta := StepMeta{}
	var thinking []string
	for _, s := range agentSteps {
		typ := StringOf(s, "type")
		switch typ {
		case "thinking", "thought":
			if c := StringOf(s, "content"); c != "" {
				thinking = append(thinking, c)
			}
		case "tool_call", "tool", "tool_result":
			meta.ToolCalls++
			name := StringOf(s, "tool_name")
			if name == "" {
				name = StringOf(s, "name")
			}
			success := StringOf(s, "success") != "false"
			title := DoneTitle(name, s, s, success)
			st := Step{Type: "tool", Title: title, Success: &success}
			if sum := ToolSummary(name, s); sum != "" {
				st.Content = sum
			}
			steps = append(steps, st)
		default:
			if c := StringOf(s, "content"); c != "" {
				steps = append(steps, Step{Type: "text", Content: c})
			}
		}
	}
	if len(thinking) > 0 {
		steps = append([]Step{{Type: "thinking", Content: strings.Join(thinking, "\n")}}, steps...)
	}
	return steps, meta
}

// FormatDuration 毫秒 → 展示串（如 1.2s / 350ms）。
func FormatDuration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
