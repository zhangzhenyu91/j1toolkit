package callme

// 模型提示识别与匹配（照搬 callme.js：extractModelHint / matchModelByHint / normModelKey）：
// query 中「（请）使用/调用/换成/切换到/切换成 + 模型名」候选，仅含 ASCII 字母/数字的候选才进匹配；
// 按空格切 1–3 词前缀段、先长后短与模型 id/name 规范化双向包含匹配；多命中取规范化名最长者。

import (
	"regexp"
	"strings"
)

var modelHintRe = regexp.MustCompile(`(?:请)?(?:使用|调用|换成|切换到|切换成)\s*([A-Za-z0-9一-龥][A-Za-z0-9.\-_ 一-龥]{0,29})`)

var hasASCIINum = regexp.MustCompile(`[A-Za-z0-9]`)

// ExtractModelHint 抽取模型提示候选；无候选（或纯中文提问）返回空串。
func ExtractModelHint(query string) string {
	m := modelHintRe.FindStringSubmatch(query)
	if m == nil || !hasASCIINum.MatchString(m[1]) {
		return ""
	}
	return m[1]
}

// NormModelKey 模型名规范化：小写并去掉空格/点/横线/下划线。
func NormModelKey(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer(".", "", "-", "", "_", "", " ", "", "\t", "").Replace(s)
}

// ModelHit 命中的模型（含最高思考强度档）。
type ModelHit struct {
	ID              string
	Name            string
	ReasoningEffort string // 空 = 不传
}

// MatchModelByHint 候选词与模型表匹配；无命中返回 nil。
func MatchModelByHint(hint string, models []Model) *ModelHit {
	words := strings.Fields(hint)
	for n := min(len(words), 3); n >= 1; n-- {
		seg := NormModelKey(strings.Join(words[:n], " "))
		if seg == "" {
			continue
		}
		var hits []Model
		for _, m := range models {
			keys := []string{NormModelKey(m.ID), NormModelKey(m.NameOf())}
			for _, k := range keys {
				if k != "" && (strings.Contains(k, seg) || strings.Contains(seg, k)) {
					hits = append(hits, m)
					break
				}
			}
		}
		if len(hits) == 0 {
			continue
		}
		best := hits[0]
		bestLen := len(NormModelKey(best.NameOf()))
		if bestLen == 0 {
			bestLen = len(NormModelKey(best.ID))
		}
		for _, h := range hits[1:] {
			l := len(NormModelKey(h.NameOf()))
			if l == 0 {
				l = len(NormModelKey(h.ID))
			}
			if l > bestLen {
				best, bestLen = h, l
			}
		}
		effort := ""
		if levels := best.ThinkingLevels(); len(levels) > 0 {
			effort = levels[len(levels)-1]
		}
		return &ModelHit{ID: best.ID, Name: best.NameOf(), ReasoningEffort: effort}
	}
	return nil
}

// KnowledgeQAModels 仅 KnowledgeQA 模型可被指定/列出。
func KnowledgeQAModels(models []Model) []Model {
	var out []Model
	for _, m := range models {
		if m.Type == "KnowledgeQA" {
			out = append(out, m)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
