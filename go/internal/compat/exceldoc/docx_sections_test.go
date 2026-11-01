package exceldoc

import (
	"strings"
	"testing"
)

// TestHeadingAndSectionBlocks 动态章节渲染基础件：标题段落加粗转义、章节块按序拼接、
// 空正文保留一个空段落。
func TestHeadingAndSectionBlocks(t *testing.T) {
	heading := HeadingParagraph(`一、标题 <&>"'`)
	if !strings.Contains(heading, "<w:b/>") {
		t.Fatalf("标题应加粗: %s", heading)
	}
	if !strings.Contains(heading, `一、标题 &lt;&amp;&gt;&quot;&apos;`) {
		t.Fatalf("标题应转义: %s", heading)
	}
	if strings.Contains(heading, "<&>") {
		t.Fatalf("标题不应保留未转义字符: %s", heading)
	}
	for _, want := range []string{`w:before="157"`, `w:after="157"`, `w:beforeLines="50"`, `w:afterLines="50"`, `w:line="360"`, `<w:sz w:val="21"/>`} {
		if !strings.Contains(heading, want) {
			t.Fatalf("标题样式应与原安全日底稿一致（%s）: %s", want, heading)
		}
	}

	block := SectionBlock("章节甲", "第一行\r\n第二行 <b>")
	if !strings.Contains(block, "章节甲") {
		t.Fatalf("章节块应包含标题: %s", block)
	}
	if !strings.Contains(block, "第一行") || !strings.Contains(block, "第二行 &lt;b&gt;") {
		t.Fatalf("章节块应包含转义后的正文: %s", block)
	}
	if c := strings.Count(block, "<w:p>"); c != 3 {
		t.Fatalf("章节块应为标题段 + 两行正文段（3 段），got %d: %s", c, block)
	}

	empty := SectionBlock("空章节", "  ")
	if !strings.Contains(empty, "空章节") {
		t.Fatalf("空章节仍应保留标题: %s", empty)
	}
	if c := strings.Count(empty, "<w:p>"); c != 2 {
		t.Fatalf("空章节应为标题段 + 空正文段（2 段），got %d: %s", c, empty)
	}

	blocks := SectionBlocks([]SectionBlockData{
		{Title: "甲", Text: "A"},
		{Title: "乙", Text: "B"},
	})
	ia := strings.Index(blocks, "甲")
	ib := strings.Index(blocks, "乙")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("章节块顺序不符: %s", blocks)
	}
}

// TestHasExclusiveRawPlaceholder 独占占位符识别：整段仅有 {@章节区}（即使被 Word 拆 run）
// 返回 true；正文混排返回 false；整个文档无该占位符返回 false。
func TestHasExclusiveRawPlaceholder(t *testing.T) {
	single := minimalDocx(para(`{@章节区}`))
	if !HasExclusiveRawPlaceholder(string(mustDocumentXML(t, single)), "章节区") {
		t.Fatalf("独占占位符应识别")
	}
	split := minimalDocx(`<w:p><w:r><w:t>{@</w:t></w:r><w:r><w:t>章节区}</w:t></w:r></w:p>`)
	if !HasExclusiveRawPlaceholder(string(mustDocumentXML(t, split)), "章节区") {
		t.Fatalf("拆 run 的独占占位符应识别")
	}
	mixed := minimalDocx(`<w:p><w:r><w:t>前置 {@章节区} 后置</w:t></w:r></w:p>`)
	if HasExclusiveRawPlaceholder(string(mustDocumentXML(t, mixed)), "章节区") {
		t.Fatalf("混排占位符不应视为独占")
	}
	if HasExclusiveRawPlaceholder(string(mustDocumentXML(t, minimalDocx(para("无占位符")))), "章节区") {
		t.Fatalf("无占位符不应返回 true")
	}
}

// TestRenderDocumentDynamicSections 动态模板：独占 {@章节区} 整段替换为章节块，
// 不残留占位符文本，标题与正文按序出现。
func TestRenderDocumentDynamicSections(t *testing.T) {
	doc := minimalDocx(para("安全日活动") + para(`{@章节区}`) + para("记录人：{记录人}"))
	parts, err := ReadDocx(doc)
	if err != nil {
		t.Fatalf("读 docx: %v", err)
	}
	raw := map[string]string{"章节区": SectionBlocks([]SectionBlockData{
		{Title: "一、经过", Text: "经过正文"},
		{Title: "二、原因", Text: "原因正文"},
	})}
	out, err := RenderDocument(string(parts[documentXMLPath]), map[string]string{"记录人": "王五"}, raw)
	if err != nil {
		t.Fatalf("渲染: %v", err)
	}
	if strings.Contains(out, "{@章节区}") {
		t.Fatalf("占位符应被替换: %s", out)
	}
	i1 := strings.Index(out, "一、经过")
	i2 := strings.Index(out, "二、原因")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("章节顺序不符: %s", out)
	}
	if !strings.Contains(out, "经过正文") || !strings.Contains(out, "原因正文") || !strings.Contains(out, "王五") {
		t.Fatalf("正文/字段未注入: %s", out)
	}
}

// TestReplaceParagraphSequence 连续段落区域替换：按可见文本匹配，兼容拆 run，
// 区域外表头与记录人保持不变；缺失或重复区域必须报错，避免静默生成残缺文档。
func TestReplaceParagraphSequence(t *testing.T) {
	xml := string(mustDocumentXML(t, minimalDocx(
		para("活动主题：{学习内容}")+
			para("一、旧标题")+
			`<w:p><w:r><w:t>{@</w:t></w:r><w:r><w:t>旧正文}</w:t></w:r></w:p>`+
			para("记录人：{记录人}"),
	)))
	out, err := ReplaceParagraphSequence(xml, []string{"一、旧标题", "{@旧正文}"}, SectionBlocks([]SectionBlockData{
		{Title: "一、自定义经过", Text: "经过正文"},
		{Title: "二、自定义原因", Text: "原因正文"},
	}))
	if err != nil {
		t.Fatalf("替换连续段落: %v", err)
	}
	for _, want := range []string{"活动主题：{学习内容}", "记录人：{记录人}", "一、自定义经过", "经过正文", "二、自定义原因", "原因正文"} {
		if !strings.Contains(out, want) {
			t.Fatalf("替换后缺少 %q: %s", want, out)
		}
	}
	for _, unwanted := range []string{"一、旧标题", "{@旧正文}"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("旧区域不应残留 %q: %s", unwanted, out)
		}
	}
	if _, err := ReplaceParagraphSequence(xml, []string{"不存在"}, "X"); err == nil {
		t.Fatalf("缺失区域应报错")
	}
	duplicate := xml + xml
	if _, err := ReplaceParagraphSequence(duplicate, []string{"一、旧标题", "{@旧正文}"}, "X"); err == nil {
		t.Fatalf("重复区域应报错")
	}
}

// mustDocumentXML 取 docx 的 word/document.xml。
func mustDocumentXML(t *testing.T, data []byte) []byte {
	t.Helper()
	parts, err := ReadDocx(data)
	if err != nil {
		t.Fatalf("读 docx: %v", err)
	}
	xml, ok := parts[documentXMLPath]
	if !ok {
		t.Fatalf("缺 document.xml")
	}
	return xml
}
