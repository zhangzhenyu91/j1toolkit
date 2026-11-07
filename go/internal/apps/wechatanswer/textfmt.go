// 群内问答的文字回复格式化：微信消息不渲染 Markdown，这里把常见语法降级为可读纯文本
// （【标题】、• 列表、表格用 ｜ 分隔），并按行切分为多条消息，避免单条过长被折叠或截断。
package wechatanswer

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// wechatTextLimit 单条文本消息的建议长度（rune）；超长回答按行切分。
	wechatTextLimit = 2000
	// wechatTextMaxChunks 单次回答最多发送条数，超长时明确提示已截断，避免刷屏。
	wechatTextMaxChunks = 6
	// wechatTableCompactWidth 表格保持「a ｜ b」横排的显示宽度上限（CJK 计 2）；
	// 超过则转纵向「字段：值」，避免微信窄气泡自动折行后列关系错乱。
	wechatTableCompactWidth = 34
)

var (
	mdFenceRe      = regexp.MustCompile("^\\s*(```|~~~)")
	mdHeadingRe    = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdHRRe         = regexp.MustCompile(`^\s{0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$`)
	mdBulletRe     = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdOrderedRe    = regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)
	mdQuoteRe      = regexp.MustCompile(`^\s*>\s?(.*)$`)
	mdImageRe      = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdLinkRe       = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	mdAutolinkRe   = regexp.MustCompile(`<((?:https?|ftp)://[^>\s]+)>`)
	mdHTMLRe       = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	mdBRRe         = regexp.MustCompile(`(?i)<br\s*/?>`)
	mdInlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	// 只处理 ** / * 强调；下划线强调会误伤 SGCC_JWT_SECRET、snake_case 等标识符，保持原样。
	mdBoldRe   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdItalicRe = regexp.MustCompile(`\*([^*\n]+)\*`)
	mdStrikeRe = regexp.MustCompile(`~~([^~\n]+)~~`)
	mdEscapeRe = regexp.MustCompile(`\\([\\` + "`" + `*_{}\[\]()#+\-.!])`)
	mdHolderRe = regexp.MustCompile("\x00([CL])(\\d+)\x00")
)

// formatWeChatText 把 Markdown 回答降级为微信可读纯文本。
func formatWeChatText(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if mdFenceRe.MatchString(line) {
			inFence = !inFence
			out = append(out, "") // 代码块前后空行分隔
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if mdHRRe.MatchString(line) {
			out = append(out, "")
			continue
		}
		if m := mdHeadingRe.FindStringSubmatch(line); m != nil {
			if title := stripInline(strings.TrimSpace(m[2])); title != "" {
				out = append(out, "", "【"+title+"】", "")
			}
			continue
		}
		if m := mdQuoteRe.FindStringSubmatch(line); m != nil {
			out = append(out, stripInline(strings.TrimSpace(m[1])))
			continue
		}
		if isTableStart(lines, i) {
			rendered, next := formatTableBlock(lines, i)
			out = append(out, rendered...)
			i = next - 1
			continue
		}
		if isTableSeparator(trimmed) {
			continue
		}
		if strings.Contains(trimmed, "|") {
			if row := joinTableCells(splitTableCells(trimmed)); row != "" {
				out = append(out, row)
			}
			continue
		}
		if m := mdBulletRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"• "+stripInline(taskItem(strings.TrimSpace(m[2]))))
			continue
		}
		if m := mdOrderedRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+m[2]+". "+stripInline(strings.TrimSpace(m[3])))
			continue
		}
		out = append(out, stripInline(line))
	}
	// 段落之间最多保留一个空行。
	var b strings.Builder
	blank := 0
	for _, line := range out {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			b.WriteString("\n")
			continue
		}
		blank = 0
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// isTableSeparator Markdown 表格分隔行（|---|:---:|）：只有 | : - 与空白且含 -。
func isTableSeparator(line string) bool {
	if !strings.Contains(line, "|") || !strings.Contains(line, "-") {
		return false
	}
	for _, r := range line {
		switch r {
		case '|', ':', '-', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// isTableStart 标准 Markdown 表格起始：当前行含 |，下一行是分隔行。
func isTableStart(lines []string, i int) bool {
	return i+1 < len(lines) && strings.Contains(lines[i], "|") &&
		isTableSeparator(strings.TrimSpace(lines[i+1]))
}

// formatTableBlock 渲染整张表：窄表横排，宽表转纵向卡片；返回渲染行与下一条待处理行号。
func formatTableBlock(lines []string, start int) ([]string, int) {
	header := splitTableCells(lines[start])
	end := start + 2
	var rows [][]string
	for end < len(lines) {
		trimmed := strings.TrimSpace(lines[end])
		if trimmed == "" || !strings.Contains(trimmed, "|") {
			break
		}
		if isTableSeparator(trimmed) {
			end++
			continue
		}
		rows = append(rows, splitTableCells(lines[end]))
		end++
	}
	if tableCompact(header, rows) {
		out := make([]string, 0, len(rows)+1)
		out = append(out, joinTableCells(header))
		for _, row := range rows {
			out = append(out, joinTableCells(row))
		}
		return out, end
	}
	if len(rows) == 0 {
		out := make([]string, 0, len(header))
		for _, h := range header {
			if h != "" {
				out = append(out, "• "+h)
			}
		}
		return out, end
	}
	out := make([]string, 0, len(rows)*4)
	for n, row := range rows {
		title := ""
		if len(row) > 0 {
			title = strings.TrimSpace(row[0])
		}
		if title == "" {
			title = fmt.Sprintf("第%d行", n+1)
		} else if len(header) > 0 && header[0] != "" {
			title = header[0] + "：" + title
		}
		out = append(out, "", "【"+title+"】")
		for j := 1; j < len(row); j++ {
			value := strings.TrimSpace(row[j])
			if value == "" {
				continue
			}
			if j < len(header) && header[j] != "" {
				out = append(out, header[j]+"："+value)
			} else {
				out = append(out, value)
			}
		}
	}
	return out, end
}

// tableCompact 窄表可横排：表头与每一行都不超过微信可读宽度。
func tableCompact(header []string, rows [][]string) bool {
	if displayWidth(joinTableCells(header)) > wechatTableCompactWidth {
		return false
	}
	for _, row := range rows {
		if displayWidth(joinTableCells(row)) > wechatTableCompactWidth {
			return false
		}
	}
	return true
}

// splitTableCells 拆表格单元格（保护转义竖线，去掉尾部空列）。
func splitTableCells(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	s = strings.ReplaceAll(s, `\|`, "\x00")
	parts := strings.Split(s, "|")
	cells := make([]string, len(parts))
	for i := range parts {
		cells[i] = stripInline(strings.TrimSpace(strings.ReplaceAll(parts[i], "\x00", "|")))
	}
	for len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// joinTableCells 横排单元格（空尾列不产生多余分隔符）。
func joinTableCells(cells []string) string {
	for len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return strings.Join(cells, " ｜ ")
}

// displayWidth 粗略显示宽度：CJK/全角/Emoji 计 2，其余计 1。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWideRune(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isWideRune 宽字符区间（CJK/假名/谚文/全角/Emoji）。
func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE4F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF:
		return true
	}
	return false
}

// taskItem Markdown 任务列表转勾选框符号。
func taskItem(item string) string {
	lower := strings.ToLower(item)
	switch {
	case strings.HasPrefix(lower, "[ ] "):
		return "☐ " + strings.TrimSpace(item[4:])
	case strings.HasPrefix(lower, "[x] "):
		return "☑ " + strings.TrimSpace(item[4:])
	case lower == "[ ]":
		return "☐"
	case lower == "[x]":
		return "☑"
	}
	return item
}

// stripInline 行内 Markdown 降级：代码与链接先占位，避免内部符号被后续规则误删。
func stripInline(s string) string {
	if s == "" {
		return s
	}
	var codes, links []string
	s = mdInlineCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, mdInlineCodeRe.FindStringSubmatch(m)[1])
		return fmt.Sprintf("\x00C%d\x00", len(codes)-1)
	})
	s = mdImageRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdImageRe.FindStringSubmatch(m)
		links = append(links, imageLabel(stripEmphasis(strings.TrimSpace(sub[1])), sub[2]))
		return fmt.Sprintf("\x00L%d\x00", len(links)-1)
	})
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		links = append(links, linkLabel(stripEmphasis(strings.TrimSpace(sub[1])), sub[2]))
		return fmt.Sprintf("\x00L%d\x00", len(links)-1)
	})
	s = mdAutolinkRe.ReplaceAllStringFunc(s, func(m string) string {
		links = append(links, mdAutolinkRe.FindStringSubmatch(m)[1])
		return fmt.Sprintf("\x00L%d\x00", len(links)-1)
	})
	s = stripEmphasis(s)
	s = mdEscapeRe.ReplaceAllString(s, "$1")
	s = mdBRRe.ReplaceAllString(s, " ")
	s = mdHTMLRe.ReplaceAllString(s, "")
	s = mdHolderRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdHolderRe.FindStringSubmatch(m)
		i, _ := strconv.Atoi(sub[2])
		if sub[1] == "C" {
			if i >= 0 && i < len(codes) {
				return codes[i]
			}
		} else if i >= 0 && i < len(links) {
			return links[i]
		}
		return ""
	})
	return strings.TrimSpace(html.UnescapeString(s))
}

// stripEmphasis 去掉加粗/斜体/删除线标记，保留文字。
func stripEmphasis(s string) string {
	s = mdBoldRe.ReplaceAllString(s, "$1")
	s = mdStrikeRe.ReplaceAllString(s, "$1")
	s = mdItalicRe.ReplaceAllString(s, "$1")
	return s
}

// imageLabel 图片降级文案（微信无法在文字消息内联显示图片）。
func imageLabel(alt, url string) string {
	if alt == "" || alt == url {
		return "[图片] " + url
	}
	return "[图片] " + alt + "（" + url + "）"
}

// linkLabel 链接降级文案：保留可读文字与 URL，微信会自动识别链接。
func linkLabel(text, url string) string {
	if text == "" || text == url {
		return url
	}
	return text + "（" + url + "）"
}

// splitWeChatText 按行切分文本，单条不超过 limit 个 rune；超长单行硬切。
func splitWeChatText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if limit <= 0 {
		limit = wechatTextLimit
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var b strings.Builder
	n := 0
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			chunks = append(chunks, s)
		}
		b.Reset()
		n = 0
	}
	for _, line := range strings.Split(text, "\n") {
		lr := utf8.RuneCountInString(line)
		if lr > limit {
			if n > 0 {
				flush()
			}
			for _, r := range line {
				if n >= limit {
					flush()
				}
				b.WriteRune(r)
				n++
			}
			continue
		}
		if n > 0 && n+1+lr > limit {
			flush()
		}
		if n > 0 {
			b.WriteString("\n")
			n++
		}
		b.WriteString(line)
		n += lr
	}
	flush()
	return chunks
}

// capWeChatChunks 限制单次回答条数；超长部分明确告知截断，不静默丢内容。
func capWeChatChunks(chunks []string) []string {
	if len(chunks) <= wechatTextMaxChunks {
		return chunks
	}
	chunks = chunks[:wechatTextMaxChunks]
	chunks[len(chunks)-1] += "\n……（回答过长，已截断，完整回答可在 Call Me 对应会话查看）"
	return chunks
}
