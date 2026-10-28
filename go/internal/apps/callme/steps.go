package callme

// 步骤树标题/摘要（全量移植 server/src/utils/agentsteps.js）：实时 SSE 事件与历史消息
// （agent_steps）共用，产出「展示就绪」的步骤节点，小程序/Web 两端只做渲染、不含格式化逻辑。
// 文案口径对齐官方前端（AgentStreamDisplay.vue + zh-CN 语言包），与 Node 版逐字一致。

import (
	"fmt"
	"strings"
)

// toolLabels 工具名 → 展示名（进行中）：全量对齐官方 TOOL_NAME_KEYS；
// search_conversations / search_memory / write_skill_file / edit_skill_file 官方也无中文名（自补）。
var toolLabels = map[string]string{
	"knowledge_search":        "知识库检索",
	"search_knowledge":        "知识库检索",
	"grep_chunks":             "搜索关键词",
	"web_search":              "网络搜索",
	"web_fetch":               "网页抓取",
	"get_document_info":       "获取文档信息",
	"get_document_content":    "获取文档内容",
	"list_documents":          "浏览文档列表",
	"read_document":           "阅读文档",
	"get_related_documents":   "查找相关文档",
	"list_knowledge_chunks":   "查看知识分块",
	"wiki_search":             "搜索 Wiki",
	"wiki_read_page":          "阅读 Wiki 页面",
	"wiki_flag_issue":         "标记 Wiki 问题",
	"wiki_write_page":         "创建/覆盖 Wiki",
	"wiki_replace_text":       "局部替换 Wiki",
	"wiki_rename_page":        "重命名 Wiki",
	"wiki_delete_page":        "删除 Wiki",
	"wiki_read_issue":         "查看 Wiki 问题",
	"wiki_update_issue":       "更新 Wiki 问题",
	"wiki_read_source_doc":    "精读源文档",
	"todo_write":              "计划管理",
	"thinking":                "思考",
	"ask_user":                "向用户提问",
	"image_analysis":          "查看图片内容",
	"attachment_parsing":      "解析附件",
	"query_understand":        "理解问题",
	"query_knowledge_graph":   "知识图谱查询",
	"knowledge_graph_extract": "知识图谱抽取",
	"read_file":               "读取文件",
	"list_sandbox_files":      "列出沙箱文件",
	"read_sandbox_file":       "读取沙箱文件",
	"write_sandbox_file":      "写入沙箱文件",
	"edit_sandbox_file":       "编辑沙箱文件",
	"shell_exec":              "执行沙箱命令",
	"read_skill":              "读取技能",
	"execute_skill_script":    "执行技能脚本",
	"write_skill_file":        "写入技能文件",
	"edit_skill_file":         "编辑技能文件",
	"data_analysis":           "数据分析",
	"data_schema":             "数据结构",
	"database_query":          "数据库查询",
	"find_operations":         "查找操作",
	"run_operation":           "执行操作",
	"try_agent":               "试跑智能体",
	"search_conversations":    "回顾历史对话",
	"search_memory":           "搜索记忆",
	"open_page":               "打开页面",
	"read_page":               "查看当前页面",
	"select_tab":              "切换标签",
	"fill_page":               "填写页面",
	"search_weknora_docs":     "搜索产品文档",
	"read_weknora_doc":        "阅读产品文档",
	"local_browser":           "本机浏览器",
	"discover_mcp_tools":      "发现 MCP 工具",
	"call_mcp_tool":           "MCP 工具",
}

// toolDoneLabels 工具名 → 完成态描述（官方 toolStatus 文案；专属失败文案为「{描述}失败」）。
var toolDoneLabels = map[string]string{
	"knowledge_search":     "检索知识库",
	"search_knowledge":     "检索知识库",
	"grep_chunks":          "搜索关键词",
	"web_search":           "网络搜索",
	"get_document_info":    "获取文档信息",
	"list_documents":       "获取文档信息", // 官方 Web 端复用 getDocInfo 文案
	"read_document":        "查看文档",
	"get_document_content": "查看文档",
	"wiki_read_source_doc": "查看文档",
	"todo_write":           "更新任务列表",
	"thinking":             "完成思考",
	"image_analysis":       "已查看图片内容",
	"attachment_parsing":   "已解析附件",
	"query_understand":     "已完成问题理解",
}

// doneDirect 完成态直显中文名的工具（官方 getToolDescription 口径：成功直显、失败仍按「调用 {中文名} 失败」）。
var doneDirect = map[string]bool{
	"shell_exec":           true,
	"execute_skill_script": true,
	"read_skill":           true,
	"list_sandbox_files":   true,
	"read_file":            true,
	"read_sandbox_file":    true,
	"write_sandbox_file":   true,
	"edit_sandbox_file":    true,
	"wiki_search":          true,
	"wiki_read_page":       true,
}

// pendingTitles 进行中标题的特殊文案（官方 toolStatus 里带进行时的几个）。
var pendingTitles = map[string]string{
	"image_analysis":     "正在查看图片内容…",
	"attachment_parsing": "正在解析附件…",
	"query_understand":   "正在理解问题…",
	"shell_exec":         "正在执行沙箱命令…",
}

// queryTools 标题里允许附带「查询词」的搜索类工具。
var queryTools = map[string]bool{
	"knowledge_search": true,
	"search_knowledge": true,
	"wiki_search":      true,
	"web_search":       true,
	"grep_chunks":      true,
}

func toolLabel(name string) string {
	if v, ok := toolLabels[name]; ok {
		return v
	}
	if name == "" {
		return "工具"
	}
	return name
}

// num 数值取整容错（JS Number(v) 有限才用，否则 0）。
func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		var f float64
		if _, err := fmt.Sscan(t, &f); err == nil {
			return f
		}
	}
	return 0
}

// PendingTitle 调用中标题：正在调用 知识库检索…
func PendingTitle(name string) string {
	if v, ok := pendingTitles[name]; ok {
		return v
	}
	return "正在调用 " + toolLabel(name) + "…"
}

// extractQuery 从工具参数/结果提取查询词（官方：搜索类工具标题后缀「query」）。
func extractQuery(name string, args, data map[string]any) string {
	src := args
	if len(src) == 0 {
		src = data
	}
	if src == nil {
		return ""
	}
	if name == "grep_chunks" {
		var pats []string
		collect := func(v any) {
			arr, ok := v.([]any)
			if !ok {
				return
			}
			for _, it := range arr {
				if s, ok := it.(string); ok && s != "" {
					pats = append(pats, s)
				}
			}
		}
		if arr, ok := asAnySlice(src["queries"]); ok && len(arr) > 0 {
			collect(arr)
		} else if arr, ok := asAnySlice(src["patterns"]); ok && len(arr) > 0 {
			collect(arr)
		} else if s := StringOf(src, "query"); s != "" {
			pats = []string{s}
		} else if s := StringOf(src, "pattern"); s != "" {
			pats = []string{s}
		}
		if len(pats) == 0 {
			return ""
		}
		out := strings.Join(pats[:min(len(pats), 2)], "、")
		if len(pats) > 2 {
			out += fmt.Sprintf(" +%d", len(pats)-2)
		}
		return out
	}
	switch q := src["query"].(type) {
	case []any:
		var parts []string
		for _, it := range q {
			if s, ok := it.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "，")
	case string:
		return q
	}
	return ""
}

// DoneTitle 完成态标题：检索知识库：「带电」/ 搜索关键词：「带电」/ 调用 xxx 失败。
func DoneTitle(name string, args map[string]any, data map[string]any, success bool) string {
	direct := doneDirect[name]
	label, hasDone := toolDoneLabels[name]
	if !hasDone {
		if direct {
			label = toolLabel(name)
		} else {
			label = "调用 " + toolLabel(name)
		}
	}
	if !success {
		if !direct && hasDone {
			return label + "失败"
		}
		return "调用 " + toolLabel(name) + " 失败"
	}
	if queryTools[name] {
		if q := extractQuery(name, args, data); q != "" {
			return label + "：「" + q + "」"
		}
	}
	return label
}

// ToolSummary 结果摘要行（官方 agentStream.search 文案）：找到 30 个匹配片段，来自 24 个文档。
func ToolSummary(name string, data map[string]any) string {
	if data == nil {
		return ""
	}
	displayType := StringOf(data, "display_type")
	if name == "grep_chunks" || displayType == "grep_results" {
		chunks := int(num(data["total_matches"]))
		docs := int(num(data["document_count"]))
		if docs == 0 {
			docs = int(num(data["result_count"]))
		}
		if chunks == 0 {
			return "未找到匹配的内容"
		}
		return fmt.Sprintf("找到 %d 个匹配片段，来自 %d 个文档", chunks, docs)
	}
	if name == "web_search" {
		n := 0
		if arr, ok := asAnySlice(data["results"]); ok {
			n = len(arr)
		} else {
			n = int(num(data["count"]))
		}
		if n > 0 {
			return fmt.Sprintf("找到 %d 条网页", n)
		}
		return "未找到匹配的内容"
	}
	if name == "knowledge_search" || name == "search_knowledge" {
		n := 0
		if arr, ok := asAnySlice(data["results"]); ok {
			n = len(arr)
		} else {
			n = int(num(data["count"]))
		}
		if n == 0 {
			return "未找到匹配的内容"
		}
		kbCount := 0
		if kb, ok := data["kb_counts"].(map[string]any); ok {
			kbCount = len(kb)
		}
		if kbCount > 0 {
			return fmt.Sprintf("找到 %d 个结果，来自 %d 个文件", n, kbCount)
		}
		return fmt.Sprintf("找到 %d 个结果", n)
	}
	if displayType == "knowledge_chunks_list" {
		fetched := int(num(data["fetched_chunks"]))
		total := int(num(data["total_chunks"]))
		if total > 0 {
			return fmt.Sprintf("已加载 %d / %d 个分块", fetched, total)
		}
	}
	return ""
}

// Step 历史消息步骤树节点（展示就绪，端侧只渲染）：
// { kind:'thought', text } | { kind:'tool', name, status, title, summary, durationMs }。
type Step struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Title      string `json:"title,omitempty"`
	Summary    string `json:"summary,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// StepMeta 步骤树统计（steps_meta：rounds=agent_steps 轮数、tools=工具调用次数）。
type StepMeta struct {
	Rounds int `json:"rounds"`
	Tools  int `json:"tools"`
}

// BuildSteps 历史消息 agent_steps → 展示就绪步骤树 + 统计（同 utils/agentsteps.js buildSteps）。
func BuildSteps(agentSteps []map[string]any) ([]Step, StepMeta) {
	steps := []Step{}
	meta := StepMeta{}
	if agentSteps == nil {
		return steps, meta
	}
	for _, s := range agentSteps {
		if s == nil {
			continue
		}
		if v := s["reasoning_content"]; v != nil {
			steps = append(steps, Step{Kind: "thought", Text: fmt.Sprint(v)})
		}
		if v := s["thought"]; v != nil {
			steps = append(steps, Step{Kind: "thought", Text: fmt.Sprint(v)})
		}
		calls, ok := asAnySlice(s["tool_calls"])
		if !ok {
			continue
		}
		for _, raw := range calls {
			tc, _ := raw.(map[string]any)
			if tc == nil {
				continue
			}
			name := StringOf(tc, "name")
			result, _ := tc["result"].(map[string]any)
			success := result == nil || !isFalse(result["success"])
			var data map[string]any
			if result != nil {
				data, _ = result["data"].(map[string]any)
			}
			meta.Tools++
			var args map[string]any
			if a, ok := tc["args"].(map[string]any); ok {
				args = a
			}
			st := Step{
				Kind:       "tool",
				Name:       name,
				Status:     "done",
				Title:      DoneTitle(name, args, data, success),
				DurationMs: int64(num(tc["duration"])),
			}
			if !success {
				st.Status = "error"
				st.Summary = truncateRunes(StringOf(result, "error"), 120)
			} else {
				st.Summary = ToolSummary(name, data)
			}
			steps = append(steps, st)
		}
	}
	meta.Rounds = len(agentSteps)
	return steps, meta
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
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
