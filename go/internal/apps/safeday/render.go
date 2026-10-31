// 安全日活动记录：后端模板渲染（对齐 server/src/safeday/render.js，经 compat/exceldoc 实现）。
// 模板：assets/safeday/activity-record-template.docx（容器布局；仓根开发布局回退
// server/assets/safeday/…，兼容占位符被 Word 拆散到多个 run）：
//   - 普通字段：纯文本 {占位符}，值内换行落入文档
//   - 内置静态模板：六段正文 {@占位符} 原生 XML 注入——按换行拆为独立段落，每段首行缩进两字符
//   - 自定义动态模板：独占 {@章节区} 整段替换为「标题 + 正文」章节块（顺序/标题取记录快照）
//
// 渲染器不缓存模板字节：每次按路径或内容寻址 sha 读取，天然并发安全。
// 占位符口径见《开发指南》8.1。
package safeday

import (
	"fmt"
	"os"

	"shade/internal/assetpath"
	"shade/internal/compat/exceldoc"
)

// 默认模板路径（容器布局缺省：assets/safeday/…，仓根开发布局经 assetpath 回退
// server/assets/safeday/…）；可由 DocxRenderer.TemplatePath 覆盖。
const defaultTemplatePath = "assets/safeday/activity-record-template.docx"

// ResolveTemplatePath 解析活动记录模板路径：assetpath 探测（容器 assets/… 优先、
// 仓根 server/assets/… 回退），均未命中回退容器布局缺省值。
func ResolveTemplatePath() string {
	if p := assetpath.ResolveFile("safeday/activity-record-template.docx"); p != "" {
		return p
	}
	return defaultTemplatePath
}

// rawXMLKeys 内置模板中以 {@xxx} 形式存在的六段正文占位符（按 builtinSections 顺序生成）。
var rawXMLKeys = func() []string {
	keys := make([]string, len(builtinSections))
	for i, section := range builtinSections {
		keys[i] = section.Title
	}
	return keys
}()

// maxSectionChars 六段正文/动态章节各截 20000 字符兜底（防异常超长；实测活动通报类内容可达 8 千字）。
const maxSectionChars = 20000

// DocxRenderer 活动记录 docx 渲染器（无内部缓存，可并发使用）。
type DocxRenderer struct {
	TemplatePath string // 内置静态模板路径；空用 ResolveTemplatePath
}

// RecordFields 渲染入参（模板占位符中文键名）。
type RecordFields map[string]string

// templatePath 生效模板路径。
func (r *DocxRenderer) templatePath() string {
	if r != nil && r.TemplatePath != "" {
		return r.TemplatePath
	}
	return ResolveTemplatePath()
}

// Render 以内置静态模板渲染一条活动记录为 docx 字节。
func (r *DocxRenderer) Render(fields RecordFields) ([]byte, error) {
	path := r.templatePath()
	tpl, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("safeday: 读取模板 %s: %w", path, err)
	}
	textFields := make(map[string]string, len(fields))
	for k, v := range fields {
		textFields[k] = v
	}
	rawFields := make(map[string]string, len(rawXMLKeys))
	for _, key := range rawXMLKeys {
		rawFields[key] = exceldoc.IndentedParagraphs(truncateSection(fields[key]))
	}
	return renderDocxTemplate(tpl, textFields, rawFields)
}

// RenderDynamic 以自定义动态模板渲染：docx 内独占 {@章节区} 替换为按序章节块（每段截 20000 字）。
func (r *DocxRenderer) RenderDynamic(tpl []byte, fields RecordFields, sections []exceldoc.SectionBlockData) ([]byte, error) {
	textFields := make(map[string]string, len(fields))
	for k, v := range fields {
		textFields[k] = v
	}
	blocks := make([]exceldoc.SectionBlockData, len(sections))
	for i, section := range sections {
		blocks[i] = exceldoc.SectionBlockData{Title: section.Title, Text: truncateSection(section.Text)}
	}
	rawFields := map[string]string{sectionsPlaceholder: exceldoc.SectionBlocks(blocks)}
	return renderDocxTemplate(tpl, textFields, rawFields)
}

// renderDocxTemplate 打开 docx → 渲染 document.xml → 重新打包。
func renderDocxTemplate(tpl []byte, textFields, rawFields map[string]string) ([]byte, error) {
	parts, err := exceldoc.ReadDocx(tpl)
	if err != nil {
		return nil, fmt.Errorf("safeday: 打开模板: %w", err)
	}
	xml, ok := parts["word/document.xml"]
	if !ok {
		return nil, fmt.Errorf("safeday: 模板缺少 word/document.xml")
	}
	rendered, err := exceldoc.RenderDocument(string(xml), textFields, rawFields)
	if err != nil {
		return nil, fmt.Errorf("safeday: 模板渲染: %w", err)
	}
	parts["word/document.xml"] = []byte(rendered)
	out, err := exceldoc.BuildDocx(parts)
	if err != nil {
		return nil, fmt.Errorf("safeday: 打包 docx: %w", err)
	}
	return out, nil
}

// truncateSection 按 rune 截断章节正文到 maxSectionChars。
func truncateSection(value string) string {
	runes := []rune(value)
	if len(runes) > maxSectionChars {
		return string(runes[:maxSectionChars])
	}
	return value
}

// RenderRecordData 组装渲染入参（字段映射；动态模板的章节正文由快照 key 另行组装）。
func RenderRecordData(rec Record, sections map[string]string) RecordFields {
	form := rec.Form
	absentees := form.Absentees
	if absentees == "" {
		absentees = "无"
	}
	absentReason := form.AbsentReason
	if absentReason == "" {
		absentReason = "无"
	}
	return RecordFields{
		"班组名称":     rec.Team,
		"学习内容":     rec.Name,
		"活动时间":     rec.Date,
		"主持人":      form.Host,
		"上级参加人员":   form.Superior,
		"本班组参加人员":  form.Attendees,
		"缺席人员":     absentees,
		"缺席人员原因":   absentReason,
		"客观还原事故经过": sections["accident_process"],
		"分层剖析事故原因": sections["cause_analysis"],
		"对照自查举一反三": sections["self_check"],
		"制定整改清单":   sections["rectification_list"],
		"讨论防范措施":   sections["prevention_measures"],
		"定期督导检查防范措施落实情况和整改完成情况": sections["supervision_inspection"],
		"记录人": form.Recorder,
	}
}
