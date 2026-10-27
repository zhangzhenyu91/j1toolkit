package exceldoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findAssetsDir 从测试目录向上查找 server/assets（读取现有模板但不得修改）。
// 找不到时跳过依赖模板的用例（独立目录运行不误报）。
func findAssetsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("取不到工作目录: %v", err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "server", "assets")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("未找到 server/assets（不在仓库内运行），跳过模板用例")
	return ""
}

// readTemplate 读只读模板文件。
func readTemplate(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findAssetsDir(t), rel))
	if err != nil {
		t.Fatalf("读模板 %s: %v", rel, err)
	}
	return data
}

// minimalDocx 生成仅含 word/document.xml 的最小 docx（分页合并等结构测试用）。
func minimalDocx(body string) []byte {
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body + `</w:body></w:document>`
	out, err := BuildDocx(map[string][]byte{documentXMLPath: []byte(xml)})
	if err != nil {
		panic(err)
	}
	return out
}

// para 构造含单 run 文本的段落。
func para(text string) string {
	return `<w:p><w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

// sectPr 结尾页面设置。
const testSectPr = `<w:sectPr><w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/></w:sectPr>`

// ---------- 拆 run 占位符 ----------

func TestPlaceholderSplitRun(t *testing.T) {
	// 合成：占位符被 Word 拆成 { / 名称 / } 三个 run
	xml := `<w:document><w:body>` +
		`<w:p><w:r><w:t>{</w:t></w:r><w:r><w:t>名称</w:t></w:r><w:r><w:t>}</w:t></w:r></w:p>` +
		testSectPr + `</w:body></w:document>`
	out, err := RenderDocument(xml, map[string]string{"名称": "A<B&\"C"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `A&lt;B&amp;&quot;C`) // 值转义落入首个 run
	mustNotContain(t, out, `{名称}`)
	// 拆散的剩余 run 文本必须清空，防残留
	if strings.Contains(out, `<w:t>名称</w:t>`) || strings.Contains(out, `<w:t>}</w:t>`) {
		t.Fatal("占位符拆 run 残留未清空")
	}
}

func TestPlaceholderPlainAndNewline(t *testing.T) {
	xml := `<w:document><w:body>` + para(`前置{值}后置`) + testSectPr + `</w:body></w:document>`
	out, err := RenderDocument(xml, map[string]string{"值": "甲\n乙"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `前置甲</w:t><w:br/><w:t xml:space="preserve">乙后置`)
	mustNotContain(t, out, `{值}`)
}

func TestPlaceholderMissKept(t *testing.T) {
	xml := `<w:document><w:body>` + para(`{不存在}`) + testSectPr + `</w:body></w:document>`
	out, err := RenderDocument(xml, map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `{不存在}`)
}

// ---------- 原生 XML 段落注入 ----------

func TestRawXMLSoleParagraph(t *testing.T) {
	xml := `<w:document><w:body>` + para(`标题`) + para(`{@段落}`) + testSectPr + `</w:body></w:document>`
	raw := IndentedParagraphs("第一段\n第二段")
	out, err := RenderDocument(xml, nil, map[string]string{"段落": raw})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, out, `{@段落}`)
	mustContain(t, out,
		`firstLineChars="200"`, // 首行缩进两字符
		`firstLine="420"`,      // 回退口径
		`第一段`, `第二段`,
	)
	// 注入为两个独立段落
	if c := strings.Count(out, `<w:ind w:firstLine="420"`); c != 2 {
		t.Fatalf("注入段落数 = %d，期望 2", c)
	}
}

func TestRawXMLInline(t *testing.T) {
	xml := `<w:document><w:body>` + para(`前{@x}后`) + testSectPr + `</w:body></w:document>`
	out, err := RenderDocument(xml, nil, map[string]string{"x": IndentedParagraphs("内容")})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, out, `{@x}`)
	mustContain(t, out, `内容`, `前`, `后`)
}

func TestIndentedParagraphsEscapes(t *testing.T) {
	out := IndentedParagraphs("a<b\n&c")
	mustContain(t, out, `a&lt;b`, `&amp;c`)
	if c := strings.Count(out, `<w:p>`); c != 2 {
		t.Fatalf("段落数 = %d，期望 2", c)
	}
}

// ---------- 真实模板渲染（只读） ----------

func TestRenderSafedayTemplate(t *testing.T) {
	files, err := ReadDocx(readTemplate(t, filepath.Join("safeday", "activity-record-template.docx")))
	if err != nil {
		t.Fatal(err)
	}
	xml := string(files[documentXMLPath])
	// 模板占位符确实被拆 run（{ / 名称 / } 分居三个 <w:t>）——拆 run 兼容的前提
	mustContain(t, xml, `{</w:t></w:r><w:r>`)
	mustContain(t, xml, `{@客观还原事故经过}`)

	fields := map[string]string{
		"班组名称":    "检修一班",
		"活动时间":    "2026年10月27日",
		"主持人":     "张三",
		"学习内容":    "安全规程",
		"本班组参加人员": "李四 王五",
		"上级参加人员":  "",
		"缺席人员":    "",
		"缺席人员原因":  "",
		"记录人":     "赵六",
	}
	raw := map[string]string{}
	for _, k := range []string{
		"客观还原事故经过", "分层剖析事故原因", "对照自查举一反三",
		"制定整改清单", "讨论防范措施", "定期督导检查防范措施落实情况和整改完成情况",
	} {
		raw[k] = IndentedParagraphs("【" + k + "】正文第一行\n第二行")
	}
	out, err := RenderDocument(xml, fields, raw)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `检修一班`, `张三`, `赵六`)
	// 全部占位符应已消化（模板全部 10 个纯文本键 + 6 个 {@} 键都给了值）
	mustNotContain(t, out,
		`{班组名称}`, `{活动时间}`, `{主持人}`, `{学习内容}`, `{本班组参加人员}`,
		`{上级参加人员}`, `{缺席人员}`, `{缺席人员原因}`, `{记录人}`,
		`{@客观还原事故经过}`, `{@分层剖析事故原因}`, `{@对照自查举一反三}`,
		`{@制定整改清单}`, `{@讨论防范措施}`, `{@定期督导检查防范措施落实情况和整改完成情况}`,
	)
	mustContain(t, out, `firstLineChars="200"`)

	// 整份文档可重新打包为合法 docx
	built, err := BuildDocx(map[string][]byte{documentXMLPath: []byte(out)})
	if err != nil {
		t.Fatal(err)
	}
	again, err := ReadDocx(built)
	if err != nil {
		t.Fatalf("重打包后无法回读: %v", err)
	}
	mustContain(t, string(again[documentXMLPath]), `检修一班`)
}

func TestRenderTaskSheetTemplate(t *testing.T) {
	files, err := ReadDocx(readTemplate(t, filepath.Join("worklog", "task-sheet-template.docx")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderDocument(string(files[documentXMLPath]), map[string]string{
		"线路杆塔号": "基面清理",
		"工作班组":  "检修一班",
		"工作负责人": "张三",
		"年":     "2026",
		"月":     "10",
		"日":     "27",
		"工作地点":  "220kV 变电站",
		"工作班成员": "李四 王五",
		"派车情况":  "冀A12345",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, `基面清理`, `检修一班`, `冀A12345`, `2026`, `220kV 变电站`)
	mustNotContain(t, out, `{线路杆塔号}`, `{工作班组}`)
}

// ---------- 分页合并 ----------

func TestMergeDocxPageBreak(t *testing.T) {
	doc1 := minimalDocx(para("卡片一") + testSectPr)
	doc2 := minimalDocx(para("卡片二") + testSectPr)
	doc3 := minimalDocx(para("卡片三") + testSectPr)

	merged, err := MergeDocx([][]byte{doc1, doc2, doc3})
	if err != nil {
		t.Fatal(err)
	}
	out := unzipEntry(t, merged, documentXMLPath)
	mustContain(t, out, `卡片一`, `卡片二`, `卡片三`, `<w:pageBreakBefore/>`)
	if c := strings.Count(out, `<w:pageBreakBefore/>`); c != 2 {
		t.Fatalf("pageBreakBefore 数 = %d，期望 2（仅追加份首段）", c)
	}
	// 追加段落应位于结尾 sectPr 之前
	if strings.Index(out, `卡片三`) > strings.Index(out, `<w:sectPr`) {
		t.Fatal("追加内容应位于 sectPr 之前")
	}
}

func TestMergeDocxErrors(t *testing.T) {
	if _, err := MergeDocx(nil); err == nil {
		t.Fatal("空输入应报错")
	}
	// 基准文档缺 sectPr 且有待追加内容 → 报错
	xml := `<?xml version="1.0"?><w:document><w:body>` + para("x") + `</w:body></w:document>`
	badDocx, err := BuildDocx(map[string][]byte{documentXMLPath: []byte(xml)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MergeDocx([][]byte{badDocx, minimalDocx(para("a") + testSectPr)}); err == nil {
		t.Fatal("缺 sectPr 的基准文档应报错")
	}
}

// ---------- 费用表动态列 ----------

func TestPersonColWidths(t *testing.T) {
	w7 := PersonColWidths(7)
	for i, w := range w7 {
		if w != FeePersonColW {
			t.Fatalf("7 人第 %d 列宽 = %d，期望 %d", i, w, FeePersonColW)
		}
	}
	if FeePersonColW != 1631 {
		t.Fatalf("FeePersonColW = %d，期望 1631", FeePersonColW)
	}
	w8 := PersonColWidths(8)
	sum := 0
	for _, w := range w8 {
		sum += w
	}
	if sum != 7*FeePersonColW {
		t.Fatalf("8 人人名列总宽 = %d，期望 %d", sum, 7*FeePersonColW)
	}
	if w8[7] != 1428 {
		t.Fatalf("8 人末列宽 = %d，期望 1428（余数并入末列）", w8[7])
	}
	if PersonColWidths(0) != nil {
		t.Fatal("0 人应返回 nil")
	}
}

func TestFillCell(t *testing.T) {
	tpl := `<w:tc><w:tcW w:w="1263" w:type="dxa"/><w:r><w:t>旧1</w:t></w:r><w:r><w:t>旧2</w:t></w:r></w:tc>`
	keep := FillCell(tpl, 1631, nil)
	mustContain(t, keep, `w:w="1631"`, `旧1`, `旧2`)

	text := "60×1=60"
	got := FillCell(tpl, 1631, &text)
	mustContain(t, got, `w:w="1631"`, `<w:t>60×1=60</w:t>`, `<w:t></w:t>`)
	mustNotContain(t, got, `旧1`, `旧2`)

	empty := ""
	got = FillCell(tpl, 2220, &empty)
	mustContain(t, got, `w:w="2220"`)
	mustNotContain(t, got, `旧1`, `旧2`, `<w:t>60`)
}

func TestRenderFeeTableRealTemplate(t *testing.T) {
	files, err := ReadDocx(readTemplate(t, filepath.Join("worklog", "fee-summary-template.docx")))
	if err != nil {
		t.Fatal(err)
	}
	xml := string(files[documentXMLPath])
	mustContain(t, xml, `<w:tbl>`, `出差任务单日期`) // 模板关键结构

	data := FeeTableData{
		Persons: []FeePerson{{ID: 1, Name: "甲"}, {ID: 2, Name: "乙"}},
		Entries: []FeeEntry{
			{ID: 10, Date: "2026-07-20"},
			{ID: 11, Date: "2026-07-21"},
		},
		MemberSet: map[int64]map[int64]bool{
			10: {1: true, 2: true},
			11: {1: true},
		},
		FeeMap: map[string]float64{
			"1|2026-07-20": 60,
			"2|2026-07-20": 45,
			"1|2026-07-21": 30,
		},
	}
	out, err := RenderFeeTableXML(xml, data)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out,
		`<w:gridCol w:w="2220"/>`,
		`<w:gridCol w:w="1631"/>`,
		`<w:tblW w:w="5482" w:type="dxa"/>`, // 2220+1631×2（保留模板 dxa 属性）
		`<w:t>甲</w:t>`, `<w:t>乙</w:t>`,
		`<w:t>7月20日</w:t>`, `<w:t>7月21日</w:t>`,
		`60×1=60`, `45×1=45`, `30×1=30`,
		`出差任务单日期`, // 斜线表头格保留原文
		`<w:t>合计</w:t>`,
		`<w:t>90</w:t>`, // 甲列合计 60+30
		`<w:t>45</w:t>`, // 乙列合计
	)
	if c := strings.Count(out, `<w:tr `); c != 4 { // 表头 + 2 数据 + 合计（不把 w:trPr 计入）
		t.Fatalf("表格行数 = %d，期望 4", c)
	}
	// 模板原文件不得被修改
	files2, err := ReadDocx(readTemplate(t, filepath.Join("worklog", "fee-summary-template.docx")))
	if err != nil {
		t.Fatal(err)
	}
	if string(files2[documentXMLPath]) != xml {
		t.Fatal("模板文件内容被改动")
	}
}

func TestRenderFeeTableBadTemplate(t *testing.T) {
	if _, err := RenderFeeTableXML(`<w:document></w:document>`, FeeTableData{}); err == nil {
		t.Fatal("缺 w:tbl 应报错")
	}
	xml := `<w:tbl><w:tr><w:tc><w:t>x</w:t></w:tc></w:tr></w:tbl>`
	if _, err := RenderFeeTableXML(xml, FeeTableData{}); err == nil {
		t.Fatal("样板行不足 3 行应报错")
	}
}

// TestDocxRoundTrip 条目内容打包-解包不变。
func TestDocxRoundTrip(t *testing.T) {
	files := map[string][]byte{
		documentXMLPath:       []byte(`<w:document/>`),
		"word/styles.xml":     []byte(`<w:styles/>`),
		"[Content_Types].xml": []byte(`<Types/>`),
	}
	data, err := BuildDocx(files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadDocx(data)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		if string(got[name]) != string(want) {
			t.Fatalf("条目 %s 回读不一致", name)
		}
	}
	if _, err := BuildDocx(map[string][]byte{"a.txt": []byte("x")}); err == nil {
		t.Fatal("缺 document.xml 应报错")
	}
}
