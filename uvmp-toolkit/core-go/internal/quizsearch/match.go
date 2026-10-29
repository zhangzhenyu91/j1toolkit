// 题库搜题：OCR 文本与题库的模糊匹配（纯函数，移植自 desktop/electron/quizsearch/match.cjs）。
// 算法移植自嗖嗖搜题 TextSearchUtilities.cs 的「共同字符数」，并做两点改进：
//  1. 归一化：全角转半角后白名单过滤（仅留中日韩文字+字母+数字），OCR 误插的空格/标点不再干扰；
//  2. 阈值兜底：得分 = 命中字符数 / 题干去重字符数（题干覆盖率，抗框内杂字稀释），
//     低于阈值判「未匹配」，修复原软件「不中也硬显示最佳猜测」的误导。
package quizsearch

import (
	"sort"
	"strings"
)

// Question 题目（JSON 字段口径与 bank-store.cjs 的 banks/<id>.json 一致；
// bankName 仅在「全部启用库合并题目」时附加，不入库文件）。
type Question struct {
	BankName string   `json:"bankName,omitempty"`
	Type     string   `json:"type"`
	Content  string   `json:"content"`
	Options  []string `json:"options"`
	Answer   string   `json:"answer"`
	Analysis string   `json:"analysis"`
	Sort     int      `json:"sort"`
}

// IndexEntry 匹配索引条目：预存归一化题干与去重字符集。
type IndexEntry struct {
	Q     Question
	Norm  string
	Chars map[rune]struct{}
}

// Hit 匹配结果（对齐 match.cjs findBest 返回形状）。
// 未命中时 Question 为 nil；Near 为低于阈值时最接近的题目（供页面提示）。
type Hit struct {
	Question *Question
	Score    float64
	Common   int
	Norm     string
	Near     *Question
}

// MinCommon 硬下限：命中字符数低于 6 直接判未匹配（同 match.cjs）。
const MinCommon = 6

// DefaultThreshold 默认命中阈值（题干覆盖率）。
const DefaultThreshold = 0.6

// Normalize 文本归一化：全角 ASCII/数字转半角、剔除全角空格，
// 仅保留字母数字与 CJK 统一表意文字（含扩展 A 与兼容汉字），最后转大写。
func Normalize(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= 0xff01 && r <= 0xff5e {
			r -= 0xfee0 // 全角 ASCII/数字 → 半角
		}
		if r == 0x3000 {
			continue // 全角空格
		}
		if keepRune(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}

// keepRune 保留字符白名单：[0-9A-Za-z一-鿿㐀-䶿豈-﫿]。
func keepRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		return true
	case r >= 0x4e00 && r <= 0x9fff: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4dbf: // CJK 扩展 A
		return true
	case r >= 0xf900 && r <= 0xfaff: // CJK 兼容表意文字
		return true
	}
	return false
}

// charSet 字符串 → 去重字符集合。
func charSet(text string) map[rune]struct{} {
	set := make(map[rune]struct{}, len(text))
	for _, r := range text {
		set[r] = struct{}{}
	}
	return set
}

// BuildIndex 为一批题目建匹配索引（归一化后为空的题干跳过）。
func BuildIndex(questions []Question) []IndexEntry {
	index := make([]IndexEntry, 0, len(questions))
	for _, q := range questions {
		norm := Normalize(q.Content)
		if norm == "" {
			continue
		}
		index = append(index, IndexEntry{Q: q, Norm: norm, Chars: charSet(norm)})
	}
	return index
}

// FindBest 在索引中找最佳匹配（threshold 为命中阈值 0~1，默认 0.6）。
// OCR 归一化为空返回 nil；低于阈值或命中字符数不足返回 Question 为 nil 的 Hit。
func FindBest(index []IndexEntry, ocrText string, threshold float64) *Hit {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	norm := Normalize(ocrText)
	if norm == "" {
		return nil
	}
	ocrChars := charSet(norm)
	var best *IndexEntry
	bestCommon := 0
	for i := range index {
		item := &index[i]
		// 遍历较小集合，O(n)
		small, big := ocrChars, item.Chars
		if len(item.Chars) < len(ocrChars) {
			small, big = item.Chars, ocrChars
		}
		common := 0
		for ch := range small {
			if _, ok := big[ch]; ok {
				common++
			}
		}
		if common > bestCommon {
			bestCommon = common
			best = item
		}
	}
	if best == nil || bestCommon < MinCommon {
		return &Hit{Score: 0, Common: bestCommon, Norm: norm}
	}
	// 覆盖率口径：命中字符数 / 题干去重字符数。扫描框常同时框住选项/邻题/界面杂字，
	// 用题干覆盖率（而非原软件的原始共字数）可抗杂字稀释，且短题不会被长题挤掉。
	score := float64(bestCommon) / float64(len(best.Chars))
	q := best.Q
	if score < threshold {
		return &Hit{Score: score, Common: bestCommon, Norm: norm, Near: &q}
	}
	return &Hit{Question: &q, Score: score, Common: bestCommon, Norm: norm}
}

// sortedRunes 字符升序（normAnswer 用；JS 按 UTF-16 码元排序，ASCII/汉字场景等价）。
func sortedRunes(s string) string {
	rs := []rune(s)
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	return string(rs)
}
