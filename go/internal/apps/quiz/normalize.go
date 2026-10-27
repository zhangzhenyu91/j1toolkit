// 题目字段归一与校验（对齐 server/src/quiz/index.js 的 normalizeType/normalizeAnswer/
// normalizeUserAnswer/validateQuestionPayload/parseOptions）。
package quiz

import (
	"encoding/json"
	"strings"
)

// letters 选项字母表。
var letters = []string{"A", "B", "C", "D", "E", "F"}

// parseOptions 选项 JSON 列防御解析（mysql2 可能返回字符串或已解析对象）。
func parseOptions(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return []string{}
	case string:
		if strings.TrimSpace(v) == "" {
			return []string{}
		}
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err != nil {
			return []string{}
		}
		if arr == nil {
			return []string{}
		}
		return arr
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}

// normalizeType 题型归一：单选/单选题/single → single，多选/多选题/multiple → multiple，
// 判断/判断题/judge → judge（trim、忽略大小写）；非法返回空串。
func normalizeType(v any) string {
	t := strings.ToLower(strings.TrimSpace(toString(v)))
	switch t {
	case "单选", "单选题", "single":
		return "single"
	case "多选", "多选题", "multiple":
		return "multiple"
	case "判断", "判断题", "judge":
		return "judge"
	}
	return ""
}

// toString 任意值转字符串（数字等按其 JSON 文本）。
func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		b, _ := json.Marshal(t)
		s := string(b)
		return strings.Trim(s, `"`)
	}
}

// answerResult 归一结果。
type answerResult struct {
	Answer string
	Error  string
}

// normalizeAnswer 答案归一与校验：single 单字母；multiple ≥2 字母且在选项范围内
// （去空格转大写、按字母序去重）；judge 对/正确/√/T/true/A → 'A'，错/错误/×/F/false/B → 'B'。
func normalizeAnswer(questionType string, raw any, optionCount int) answerResult {
	s := strings.TrimSpace(toString(raw))
	if questionType == "judge" {
		t := strings.ToLower(s)
		switch t {
		case "对", "正确", "√", "t", "true", "a":
			return answerResult{Answer: "A"}
		case "错", "错误", "×", "f", "false", "b":
			return answerResult{Answer: "B"}
		}
		return answerResult{Error: "判断题答案应为 对/错（或 A/B）"}
	}
	seen := map[rune]bool{}
	var uniq []rune
	for _, r := range strings.ToUpper(s) {
		if r < 'A' || r > 'Z' || seen[r] {
			continue
		}
		seen[r] = true
		uniq = append(uniq, r)
	}
	// 按字母序去重
	for i := 0; i < len(uniq); i++ {
		for j := i + 1; j < len(uniq); j++ {
			if uniq[j] < uniq[i] {
				uniq[i], uniq[j] = uniq[j], uniq[i]
			}
		}
	}
	if len(uniq) == 0 {
		return answerResult{Error: "答案不能为空"}
	}
	maxLetter := "A"
	if optionCount-1 >= 0 && optionCount-1 < len(letters) {
		maxLetter = letters[optionCount-1]
	} else if optionCount-1 >= len(letters) {
		maxLetter = letters[len(letters)-1]
	}
	for _, c := range uniq {
		if string(c) > maxLetter {
			return answerResult{Error: "答案超出选项范围：识别到 " + itoa(optionCount) + " 个选项（A~" + maxLetter + "），请检查该行选项是否填写完整"}
		}
	}
	answer := string(uniq)
	if questionType == "single" && len(uniq) != 1 {
		return answerResult{Error: "单选题答案应为 1 个字母"}
	}
	if questionType == "multiple" && len(uniq) < 2 {
		return answerResult{Error: "多选题答案应至少 2 个字母"}
	}
	return answerResult{Answer: answer}
}

// itoa 小工具。
func itoa(n int) string {
	return strings.TrimSpace(jsonNumber(n))
}

// jsonNumber 数字转文本（避免额外依赖）。
func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// normalizeUserAnswer 用户作答归一：转大写去非字母、去重、按字母序（多选集合相等即对）。
func normalizeUserAnswer(raw any) string {
	seen := map[rune]bool{}
	var uniq []rune
	for _, r := range strings.ToUpper(toString(raw)) {
		if r < 'A' || r > 'Z' || seen[r] {
			continue
		}
		seen[r] = true
		uniq = append(uniq, r)
	}
	for i := 0; i < len(uniq); i++ {
		for j := i + 1; j < len(uniq); j++ {
			if uniq[j] < uniq[i] {
				uniq[i], uniq[j] = uniq[j], uniq[i]
			}
		}
	}
	return string(uniq)
}

// questionPayload 管理接口题目字段（校验产物）。
type questionPayload struct {
	Type     string
	Content  string
	Options  []string
	Answer   string
	Analysis string
}

// validateQuestionPayload 管理接口题目字段校验；非法返回 Error 文案。
func validateQuestionPayload(body map[string]json.RawMessage) (questionPayload, string) {
	qt := normalizeType(rawString(body["type"]))
	if qt == "" {
		return questionPayload{}, "题型应为 single（单选）/ multiple（多选）/ judge（判断）"
	}
	content := strings.TrimSpace(rawString(body["content"]))
	if content == "" {
		return questionPayload{}, "题干不能为空"
	}
	var options []string
	if qt == "judge" {
		options = []string{"正确", "错误"} // 判断题无视选项，固定两项
	} else {
		if raw, has := body["options"]; has && rawHas(raw) {
			var arr []any
			if err := json.Unmarshal(raw, &arr); err == nil {
				for _, item := range arr {
					if s := strings.TrimSpace(toString(item)); s != "" {
						options = append(options, s)
					}
				}
			}
		}
		if len(options) < 2 {
			return questionPayload{}, "选项至少 2 个"
		}
		if len(options) > 6 {
			return questionPayload{}, "选项最多 6 个"
		}
	}
	a := normalizeAnswer(qt, rawString(body["answer"]), len(options))
	if a.Error != "" {
		return questionPayload{}, a.Error
	}
	return questionPayload{
		Type:     qt,
		Content:  content,
		Options:  options,
		Answer:   a.Answer,
		Analysis: strings.TrimSpace(rawString(body["analysis"])),
	}, ""
}
