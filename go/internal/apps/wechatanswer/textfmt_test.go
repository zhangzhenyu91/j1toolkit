// 微信纯文本格式化测试：Markdown 降级、标识符保护、表格/代码块、长文分条与截断提示。
package wechatanswer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatWeChatText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "标题与强调",
			in:   "## 结论\n**垂距不足**，*建议*砍伐。~~旧结论~~",
			want: "【结论】\n\n垂距不足，建议砍伐。旧结论",
		},
		{
			name: "列表与任务列表",
			in:   "- 12# 杨树\n- [ ] 待复核\n- [x] 已复核\n1. 第一步",
			want: "• 12# 杨树\n• ☐ 待复核\n• ☑ 已复核\n1. 第一步",
		},
		{
			name: "引用与分割线",
			in:   "> 引用内容\n\n---\n\n正文",
			want: "引用内容\n\n正文",
		},
		{
			name: "代码块保留原文",
			in:   "示例：\n```go\nfmt.Println(\"**x**\")\n```\n结束",
			want: "示例：\n\nfmt.Println(\"**x**\")\n\n结束",
		},
		{
			name: "表格转分隔行",
			in:   "| 杆号 | 垂距 |\n| :--- | ---: |\n| 12# | 1.2m |",
			want: "杆号 ｜ 垂距\n12# ｜ 1.2m",
		},
		{
			name: "宽表格转纵向卡片",
			in: "| 线路 | 杆号 | 树种 | 垂距 | 建议 |\n| --- | --- | --- | --- | --- |\n" +
				"| 10kV某线 | 12# | 杨树 | 1.2m | 砍伐 |\n| 10kV某线 | 15# | 榆树 | 2.0m | 修剪 |",
			want: "【线路：10kV某线】\n杆号：12#\n树种：杨树\n垂距：1.2m\n建议：砍伐\n\n" +
				"【线路：10kV某线】\n杆号：15#\n树种：榆树\n垂距：2.0m\n建议：修剪",
		},
		{
			name: "两列长文本表格转纵向卡片",
			in:   "| 项目 | 说明 |\n| --- | --- |\n| 处理建议 | 该区段树木生长较快，建议本月内安排砍伐并复核垂距。 |",
			want: "【项目：处理建议】\n说明：该区段树木生长较快，建议本月内安排砍伐并复核垂距。",
		},
		{
			name: "链接图片与自动链接",
			in:   "[规程](https://e.com/a_b) ![示意](https://e.com/p.png) <https://e.com/x>",
			want: "规程（https://e.com/a_b） [图片] 示意（https://e.com/p.png） https://e.com/x",
		},
		{
			name: "行内代码保护",
			in:   "配置 `SGCC_JWT_SECRET` 与 `a*b_c`",
			want: "配置 SGCC_JWT_SECRET 与 a*b_c",
		},
		{
			name: "标识符下划线不误删",
			in:   "变量 snake_case_name 与 SGCC_JWT_SECRET",
			want: "变量 snake_case_name 与 SGCC_JWT_SECRET",
		},
		{
			name: "HTML 与实体",
			in:   "第一行<br>第二行 &amp; 结束",
			want: "第一行 第二行 & 结束",
		},
		{
			name: "空输入",
			in:   "  \n\n",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatWeChatText(c.in); got != c.want {
				t.Fatalf("formatWeChatText(%q)\n got=%q\nwant=%q", c.in, got, c.want)
			}
		})
	}
}

func TestSplitWeChatText(t *testing.T) {
	// 短文本不切分。
	if got := splitWeChatText("短回答", 100); len(got) != 1 || got[0] != "短回答" {
		t.Fatalf("短文本不应切分: %v", got)
	}
	// 超长单行硬切，每块不超过上限。
	chunks := splitWeChatText(strings.Repeat("长", 2500), 1000)
	if len(chunks) != 3 {
		t.Fatalf("硬切条数 = %d，期望 3", len(chunks))
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c); n > 1000 {
			t.Fatalf("分块超限: %d", n)
		}
	}
	// 按行切分：优先在换行处断开，不把段落拆碎。
	lines := strings.Join([]string{strings.Repeat("甲", 40), strings.Repeat("乙", 40), strings.Repeat("丙", 40)}, "\n")
	chunks = splitWeChatText(lines, 100)
	if len(chunks) != 2 || chunks[0] != strings.Repeat("甲", 40)+"\n"+strings.Repeat("乙", 40) {
		t.Fatalf("按行切分不符: %v", chunks)
	}
}

func TestCapWeChatChunks(t *testing.T) {
	chunks := make([]string, wechatTextMaxChunks+2)
	for i := range chunks {
		chunks[i] = "片段"
	}
	capped := capWeChatChunks(chunks)
	if len(capped) != wechatTextMaxChunks {
		t.Fatalf("条数上限 = %d，期望 %d", len(capped), wechatTextMaxChunks)
	}
	if !strings.Contains(capped[len(capped)-1], "已截断") {
		t.Fatalf("末条应带截断提示: %q", capped[len(capped)-1])
	}
}
