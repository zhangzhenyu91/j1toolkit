// docx.go —— Word 文档（docx）兼容处理：archive/zip 容器 + word/document.xml 文本级操作。
// 对齐 Node 侧口径（docxtemplater/pizzip）：
//   - {占位符} 纯文本替换，兼容占位符被 Word 拆散到多个 <w:t> run（模板实测存在拆 run）；
//     值内换行以 <w:br/> 落入文档；XML 特殊字符转义；
//   - {@占位符} 原生 XML 注入：占位符独占段落时整段替换为注入段落（安全日六段正文口径），
//     混排时在 run 内拆段注入；
//   - 分页合并：多份同模板 docx 以首份为底，后续 body 首段加 <w:pageBreakBefore/> 依次追加
//     （不用独立分页符段落，避免把新页首内容往下挤一行）；
//   - 费用表动态列：克隆 3 个样板行拼 w:tbl（列宽随人数动态，斜线表头格保留原文）。
package exceldoc

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// XML 文档主部件名。
const documentXMLPath = "word/document.xml"

// ---------- zip 容器 ----------

// ReadDocx 解开 docx（zip）为部件名→内容映射（含 document.xml）。
func ReadDocx(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("exceldoc: 打开 docx: %w", err)
	}
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = content
	}
	if _, ok := files[documentXMLPath]; !ok {
		return nil, errors.New("exceldoc: docx 缺少 word/document.xml")
	}
	return files, nil
}

// BuildDocx 把部件映射重新打包为 docx（DEFLATE，条目名按字典序保证产物稳定）。
func BuildDocx(files map[string][]byte) ([]byte, error) {
	if _, ok := files[documentXMLPath]; !ok {
		return nil, errors.New("exceldoc: 缺少 word/document.xml")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- 占位符渲染 ----------

// escapeXML 与 Node utils/file.js escapeXml 同口径（& < > " ' 全转义）。
func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

var (
	// 段落起止：<w:p> / <w:p ...>（不匹配 <w:pPr> 等同前缀元素）
	reParagraph = regexp.MustCompile(`<w:p(?:\s[^>]*)?>[\s\S]*?</w:p>`)
	// 文本 run：<w:t> 或 <w:t xml:space="preserve"> … </w:t>
	reTextRun = regexp.MustCompile(`<w:t(?:\s[^>]*)?>([\s\S]*?)</w:t>`)
	// 占位符：{xxx} 与 {@xxx}
	rePlaceholder = regexp.MustCompile(`\{(@?[^{}]+)\}`)
)

// textRun 记录段落内一个 <w:t> 元素的位置（相对段落 XML 串）。
type textRun struct {
	elemStart  int // <w:t 起点
	elemEnd    int // </w:t> 之后
	contentBeg int // 文本内容起点
	contentEnd int // 文本内容终点
	text       string
}

// parseRuns 提取段落内全部 <w:t> 元素位置与文本。
func parseRuns(p string) []textRun {
	var runs []textRun
	for _, m := range reTextRun.FindAllStringSubmatchIndex(p, -1) {
		runs = append(runs, textRun{
			elemStart:  m[0],
			elemEnd:    m[1],
			contentBeg: m[2],
			contentEnd: m[3],
			text:       p[m[2]:m[3]],
		})
	}
	return runs
}

// xmlText 还原 <w:t> 内已转义文本为纯文本（仅处理本包 escapeXML 的五种实体）。
func xmlText(s string) string {
	r := strings.NewReplacer(
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&apos;", "'",
		"&amp;", "&",
	)
	return r.Replace(s)
}

// renderFieldText 生成纯文本占位符的替换内容：转义 + 换行拆 <w:t>/<w:br/>。
// 放在 <w:t> 内容区内替换时，以闭合/重开 <w:t> 的方式插入 <w:br/>，产物保持良构。
func renderFieldText(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = escapeXML(lines[i])
	}
	return strings.Join(lines, `</w:t><w:br/><w:t xml:space="preserve">`)
}

// edit 描述对某个 run 内容区间 [beg,end) 的替换（区间相对段落串）。
type edit struct {
	beg, end int
	repl     string
}

// renderParagraph 处理单个段落 XML 的占位符：
//   - fields：{key} 纯文本替换（拆 run 兼容——占位符跨多个 <w:t> 时首个 run 写值、其余清空）；
//   - raw：{@key} 原生 XML 注入（独占段落→整段替换；混排→run 内拆段注入）。
//
// 占位符在「原始 <w:t> 文本拼接」上匹配：占位符不含 XML 特殊字符（纯中文/英文名），
// 实体不会拆开占位符；未命中的占位符原样保留。
func renderParagraph(p string, fields, raw map[string]string) (string, error) {
	runs := parseRuns(p)
	if len(runs) == 0 {
		return p, nil
	}
	var joined strings.Builder
	textOfs := make([]int, len(runs))
	for i, r := range runs {
		textOfs[i] = joined.Len()
		joined.WriteString(r.text)
	}
	text := joined.String()

	matches := rePlaceholder.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return p, nil
	}

	// 按 run 收集编辑区间（区间相对段落串）
	editsByRun := make([][]edit, len(runs))
	for _, m := range matches {
		name := text[m[2]:m[3]]
		rawKey := strings.HasPrefix(name, "@")
		key := strings.TrimPrefix(name, "@")

		var repl string
		var ok bool
		if rawKey {
			repl, ok = raw[key]
		} else {
			repl, ok = fields[key]
			if ok {
				repl = renderFieldText(repl)
			}
		}
		if !ok {
			continue // 未命中：原样保留
		}

		// 定位 [m[0],m[1]) 覆盖的 run 区间
		firstRun := -1
		lastRun := -1
		for i := range runs {
			start := textOfs[i]
			end := start + len(runs[i].text)
			if m[0] < end && m[1] > start {
				if firstRun < 0 {
					firstRun = i
				}
				lastRun = i
			}
		}
		if firstRun < 0 {
			continue
		}

		if rawKey {
			sole := strings.TrimSpace(xmlText(text)) == text[m[0]:m[1]]
			if sole {
				// 整段替换：单独标记（返回前统一处理）
				return replaceParagraphWith(p, repl), nil
			}
			// 混排：在命中起点处拆段注入（闭合 run/段落 → 注入 → 重开）
			repl = `</w:t></w:r></w:p>` + repl + `<w:p><w:r><w:t xml:space="preserve">`
		}

		for i := firstRun; i <= lastRun; i++ {
			start := textOfs[i]
			end := start + len(runs[i].text)
			cb := max(m[0], start) - start // run 内容内偏移
			ce := min(m[1], end) - start
			e := edit{beg: runs[i].contentBeg + cb, end: runs[i].contentBeg + ce}
			if i == firstRun {
				e.repl = repl
			} // 其余 run 命中区间清空
			editsByRun[i] = append(editsByRun[i], e)
		}
	}

	// 从后往前应用编辑，保持偏移有效
	var all []edit
	for _, es := range editsByRun {
		all = append(all, es...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].beg > all[j].beg })
	out := p
	for _, e := range all {
		out = out[:e.beg] + e.repl + out[e.end:]
	}
	return out, nil
}

// replaceParagraphWith 把整个段落（含包裹的 <w:r>）替换为注入的原生 XML。
func replaceParagraphWith(p, repl string) string {
	// 保留段前可能的紧邻空白由调用方上下文自带；此处整段替换
	return repl
}

// RenderDocument 对 document.xml 应用占位符渲染（文本替换 + 原生 XML 注入）。
// fields 为 {key} 纯文本值；raw 为 {@key} 段落 XML（多段以 <w:p>…</w:p> 直接拼接）。
func RenderDocument(xml string, fields, raw map[string]string) (string, error) {
	var errs []error
	out := reParagraph.ReplaceAllStringFunc(xml, func(p string) string {
		np, err := renderParagraph(p, fields, raw)
		if err != nil {
			errs = append(errs, err)
			return p
		}
		return np
	})
	if len(errs) > 0 {
		return "", errs[0]
	}
	return out, nil
}

// ---------- 原生 XML 段落注入（安全日六段正文口径） ----------

// 段落属性沿用模板占位符段落口径（行距 240 自动、左对齐、首行缩进两字符）：
// firstLineChars=200 为 Word 按字符缩进口径，firstLine=420（字号 21 半磅 × 2 字符）作回退。
const injectParaPr = `<w:pPr><w:spacing w:line="240" w:lineRule="auto"/>` +
	`<w:ind w:firstLine="420" w:firstLineChars="200"/><w:jc w:val="left"/></w:pPr>`

// run 属性沿用模板占位符 run 口径（hint=eastAsia 保证中文走中文字体）。
const injectRunPr = `<w:rPr><w:rFonts w:hint="eastAsia"/><w:lang w:val="en-US" w:eastAsia="zh-CN"/></w:rPr>`

// IndentedParagraphs 把多行文本转为多段 <w:p>（每段首行缩进两字符）。
// 段间空行保留为空段落；空值也给一个带缩进的空段落保持版式（与 Node toIndentedParagraphs 同口径）。
func IndentedParagraphs(text string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	lines := strings.Split(trimmed, "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(`<w:p>`)
		b.WriteString(injectParaPr)
		b.WriteString(`<w:r>`)
		b.WriteString(injectRunPr)
		b.WriteString(`<w:t xml:space="preserve">`)
		b.WriteString(escapeXML(line))
		b.WriteString(`</w:t></w:r></w:p>`)
	}
	return b.String()
}

// ---------- 分页合并（任务单一卡一页口径） ----------

var (
	reBody    = regexp.MustCompile(`<w:body>([\s\S]*)</w:body>`)
	reSectPr  = regexp.MustCompile(`<w:sectPr[\s\S]*?</w:sectPr>\s*$`)
	reSectAny = regexp.MustCompile(`<w:sectPr[\s\S]*?</w:sectPr>`)
)

// bodyContent 提取 document.xml 的 body 内容（去掉结尾 sectPr，页面设置以首份为准）。
func bodyContent(xml string) (string, error) {
	m := reBody.FindStringSubmatch(xml)
	if m == nil {
		return "", errors.New("exceldoc: 模板缺少 w:body")
	}
	return reSectPr.ReplaceAllString(m[1], ""), nil
}

// withPageBreakBefore 给 body 首段加 <w:pageBreakBefore/>（该段从新页页首开始；
// 已在新页页首时幂等不断页，且不占行高——不用独立分页符段落，避免把整页高的内容往下挤一行）。
func withPageBreakBefore(body string) string {
	pi := strings.Index(body, "<w:p")
	if pi == -1 {
		return body // 首元素非段落（如直接是表格），不加断页
	}
	gt := strings.Index(body[pi:], ">")
	if gt == -1 {
		return body
	}
	gt += pi
	openTag := body[pi : gt+1]
	rest := body[gt+1:]
	if strings.HasPrefix(rest, "<w:pPr>") {
		return body[:pi] + openTag + `<w:pPr><w:pageBreakBefore/>` + rest[len("<w:pPr>"):]
	}
	return body[:pi] + openTag + `<w:pPr><w:pageBreakBefore/></w:pPr>` + rest
}

// MergeDocx 合并多份同模板 docx：首份为底，其余 body 首段加 pageBreakBefore 后
// 依次追加到结尾 sectPr 之前（styles/fontTable/footer 沿用首份，body 自包含无新增关系）。
func MergeDocx(buffers [][]byte) ([]byte, error) {
	if len(buffers) == 0 {
		return nil, errors.New("exceldoc: 没有可合并的 docx")
	}
	base, err := ReadDocx(buffers[0])
	if err != nil {
		return nil, err
	}
	xml := string(base[documentXMLPath])

	var injection strings.Builder
	for _, buf := range buffers[1:] {
		files, err := ReadDocx(buf)
		if err != nil {
			return nil, err
		}
		body, err := bodyContent(string(files[documentXMLPath]))
		if err != nil {
			return nil, err
		}
		injection.WriteString(withPageBreakBefore(body))
	}
	if inj := injection.String(); inj != "" {
		loc := reSectAny.FindStringIndex(xml)
		if loc == nil {
			return nil, errors.New("exceldoc: 缺少结尾 sectPr，无法追加分页段落")
		}
		xml = xml[:loc[0]] + inj + xml[loc[0]:]
	}
	base[documentXMLPath] = []byte(xml)
	return BuildDocx(base)
}

// ---------- 费用表动态列辅助（出差费用汇总表口径） ----------

// 费用表列宽常量（dxa；2026-08 与管理员确认）：
// 人名 ≤7 时人名列统一 1631（首列 2220 固定）——7 人时表格右缘距页面右缘最接近 1600；
// 人名 >7 时人名列总宽保持 7 人时的 11417 dxa，均分（余数并入末列）。
const (
	FeeDateColW        = 2220  // 首列（日期列）宽
	feePageW           = 16838 // A4 横向页宽
	feeTableX          = 1600  // 表格距页面左缘（与模板 tblpX 一致）
	feeEdgeGap         = 1600  // 7 人时表格右缘距页面右缘的目标间距
	feeMaxFixedPersons = 7
	// FeePersonColW 7 人时人名列统一宽：1631 → 实际间距 1601
	FeePersonColW = (feePageW - feeTableX - feeEdgeGap - FeeDateColW) / feeMaxFixedPersons
	// feeTotalWMaxFixed 7 人（8 列）时的表格总宽：13637 dxa
	feeTotalWMaxFixed = FeeDateColW + FeePersonColW*feeMaxFixedPersons
	// FeeMultiplier 费用倍率：单元格文案 {费用}×{倍率}={计算值}；当前业务恒为 1
	FeeMultiplier = 1
)

// FeePerson 费用表列（范围内当过用车人的成员，按字典点亮顺序）。
type FeePerson struct {
	ID   int64
	Name string
}

// FeeEntry 费用表行（一张出车卡片一行）。
type FeeEntry struct {
	ID   int64
	Date string // 'YYYY-MM-DD'
}

// FeeTableData 费用表数据（无 DB 依赖，调用方从库取数后组包）。
type FeeTableData struct {
	Persons   []FeePerson              // 列（按成员字典点亮顺序）
	Entries   []FeeEntry               // 行（按 log_date、created_at、id 排序）
	MemberSet map[int64]map[int64]bool // entryID → 该卡用车人 memberID 集合
	FeeMap    map[string]float64       // "memberID|YYYY-MM-DD" → 伙食补助+交通费
}

// PersonColWidths 人名列宽：≤7 人统一 1631；>7 人保持 7 人总宽、均分（余数并入末列）。
func PersonColWidths(n int) []int {
	if n <= 0 {
		return nil
	}
	if n <= feeMaxFixedPersons {
		out := make([]int, n)
		for i := range out {
			out[i] = FeePersonColW
		}
		return out
	}
	share := feeTotalWMaxFixed - FeeDateColW
	base := share / n
	out := make([]int, n)
	for i := range out {
		out[i] = base
	}
	out[n-1] += share - base*n
	return out
}

// FillCell 克隆样板单元格并重写列宽与文本；text 传 nil 保留样板原文（斜线表头格/合计格），
// 传空串置空。同单元格可能有多个文本 run（Word 重存模板会把样板文案拆散）：
// 首个 run 写入新文案，其余 run 一律置空防残留。
func FillCell(tpl string, width int, text *string) string {
	reW := regexp.MustCompile(`(<w:tcW w:w=")\d+(")`)
	tc := reW.ReplaceAllString(tpl, fmt.Sprintf("${1}%d${2}", width))
	if text == nil {
		return tc
	}
	first := true
	reT := regexp.MustCompile(`<w:t(?:\s[^>]*)?>[\s\S]*?<\/w:t>`)
	return reT.ReplaceAllStringFunc(tc, func(old string) string {
		if first {
			first = false
			if *text == "" {
				return `<w:t></w:t>`
			}
			return `<w:t>` + escapeXML(*text) + `</w:t>`
		}
		return `<w:t></w:t>`
	})
}

// feeFormat 金额格式化：整数不带小数点（JS Number 同口径）。
func feeFormat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// RenderFeeTableXML 按数据重建费用汇总表 w:tbl（tblGrid + 表头行 + 数据行×N + 合计行）。
// xml 为模板 document.xml；模板表格须为 3 个样板行（表头/数据/合计）。
func RenderFeeTableXML(xml string, data FeeTableData) (string, error) {
	reTbl := regexp.MustCompile(`<w:tbl>[\s\S]*?<\/w:tbl>`)
	loc := reTbl.FindStringIndex(xml)
	if loc == nil {
		return "", errors.New("exceldoc: 模板缺少 w:tbl")
	}
	tbl := xml[loc[0]:loc[1]]

	reTblPr := regexp.MustCompile(`<w:tblPr>[\s\S]*?<\/w:tblPr>`)
	reTr := regexp.MustCompile(`<w:tr[ >][\s\S]*?<\/w:tr>`)
	reTrPrefix := regexp.MustCompile(`^<w:tr[^>]*>(?:<w:trPr>[\s\S]*?</w:trPr>)?`)
	reTc := regexp.MustCompile(`<w:tc>[\s\S]*?<\/w:tc>`)

	tblPr := reTblPr.FindString(tbl)
	trs := reTr.FindAllString(tbl, -1)
	if tblPr == "" || len(trs) != 3 {
		return "", errors.New("exceldoc: 费用汇总模板应为 3 个样板行（表头/数据/合计）")
	}
	headCells := reTc.FindAllString(trs[0], -1)
	rowCells := reTc.FindAllString(trs[1], -1)
	sumCells := reTc.FindAllString(trs[2], -1)
	if len(headCells) < 2 || len(rowCells) < 2 || len(sumCells) < 2 {
		return "", errors.New("exceldoc: 费用汇总样板行单元格不足")
	}

	persons := data.Persons
	widths := PersonColWidths(len(persons))
	totalW := FeeDateColW
	for _, w := range widths {
		totalW += w
	}
	reTblW := regexp.MustCompile(`(<w:tblW w:w=")\d+(")`)
	tblPrNew := reTblW.ReplaceAllString(tblPr, fmt.Sprintf("${1}%d${2}", totalW))

	var grid strings.Builder
	grid.WriteString(`<w:tblGrid>`)
	grid.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, FeeDateColW))
	for _, w := range widths {
		grid.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, w))
	}
	grid.WriteString(`</w:tblGrid>`)

	// 表头：首格为斜线单元格（只改列宽保留原文），其后一个用车人一列
	var head strings.Builder
	head.WriteString(reTrPrefix.FindString(trs[0]))
	head.WriteString(FillCell(headCells[0], FeeDateColW, nil))
	for i, p := range persons {
		name := p.Name
		head.WriteString(FillCell(headCells[1], widths[i], &name))
	}
	head.WriteString(`</w:tr>`)

	// 数据行：一卡一行，日期（M月D日）入第 1 列；用车人列填 {费用}×{倍率}={计算值}
	totals := make([]float64, len(persons))
	var rows strings.Builder
	for _, e := range data.Entries {
		parts := strings.Split(e.Date, "-")
		dateLabel := e.Date
		if len(parts) == 3 {
			m, _ := strconv.Atoi(parts[1])
			d, _ := strconv.Atoi(parts[2])
			dateLabel = fmt.Sprintf("%d月%d日", m, d)
		}
		inEntry := data.MemberSet[e.ID]
		var cells strings.Builder
		for i, p := range persons {
			if inEntry[p.ID] {
				fee, ok := data.FeeMap[fmt.Sprintf("%d|%s", p.ID, e.Date)]
				if ok {
					calc := float64(FeeMultiplier) * fee
					totals[i] += calc
					text := fmt.Sprintf("%s×%d=%s", feeFormat(fee), FeeMultiplier, feeFormat(calc))
					cells.WriteString(FillCell(rowCells[1], widths[i], &text))
				} else {
					// 防御：生成前核验已拦截（规则 f）
					empty := ""
					cells.WriteString(FillCell(rowCells[1], widths[i], &empty))
				}
			} else {
				empty := ""
				cells.WriteString(FillCell(rowCells[1], widths[i], &empty))
			}
		}
		rows.WriteString(reTrPrefix.FindString(trs[1]))
		rows.WriteString(FillCell(rowCells[0], FeeDateColW, &dateLabel))
		rows.WriteString(cells.String())
		rows.WriteString(`</w:tr>`)
	}

	// 合计行：第 1 列「合计」保留样板原文，各列填本列计算值之和
	var sum strings.Builder
	sum.WriteString(reTrPrefix.FindString(trs[2]))
	sum.WriteString(FillCell(sumCells[0], FeeDateColW, nil))
	for i := range persons {
		t := feeFormat(totals[i])
		sum.WriteString(FillCell(sumCells[1], widths[i], &t))
	}
	sum.WriteString(`</w:tr>`)

	newTbl := `<w:tbl>` + tblPrNew + grid.String() + head.String() + rows.String() + sum.String() + `</w:tbl>`
	return xml[:loc[0]] + newTbl + xml[loc[1]:], nil
}
