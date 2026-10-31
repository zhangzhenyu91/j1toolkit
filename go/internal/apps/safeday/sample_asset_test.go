package safeday

import (
	"os"
	"strings"
	"testing"

	"shade/internal/assetpath"
	"shade/internal/compat/exceldoc"
)

// TestDynamicSampleAsset 仓库提供的动态样例 docx 必须存在、可校验并能渲染章节。
func TestDynamicSampleAsset(t *testing.T) {
	path := assetpath.ResolveFile("safeday/activity-record-dynamic-sample.docx")
	if path == "" {
		t.Skip("未找到 server/assets（不在仓库内运行），跳过动态样例用例")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读动态样例失败: %v", err)
	}
	if err := validateTemplateDocx(data); err != nil {
		t.Fatalf("动态样例未通过模板校验: %v", err)
	}
	renderer := &DocxRenderer{}
	out, err := renderer.RenderDynamic(data, RecordFields{
		"班组名称": "检修一班", "学习内容": "样例学习", "活动时间": "2026.10.30",
		"主持人": "李班长", "本班组参加人员": "张三", "记录人": "王记录",
	}, []exceldoc.SectionBlockData{
		{Title: "一、样例章节", Text: "样例正文"},
	})
	if err != nil {
		t.Fatalf("动态样例渲染失败: %v", err)
	}
	plain := docxText(t, out)
	if strings.Contains(plain, "{@章节区}") {
		t.Fatalf("动态样例渲染后不应残留占位符: %s", plain)
	}
	if !strings.Contains(plain, "检修一班") || !strings.Contains(plain, "样例学习") {
		t.Fatalf("动态样例字段未替换: %s", plain)
	}
	if !strings.Contains(plain, "一、样例章节") || !strings.Contains(plain, "样例正文") {
		t.Fatalf("动态样例章节未注入: %s", plain)
	}

	// 生产构造路径应自动解析到样例与内置模板
	store, err := NewTemplateStore(t.TempDir(), testCipher(t))
	if err != nil {
		t.Fatalf("构造模板库失败: %v", err)
	}
	sample, err := store.SampleDocx()
	if err != nil {
		t.Fatalf("生产路径取样例失败: %v", err)
	}
	if err := validateTemplateDocx(sample); err != nil {
		t.Fatalf("生产路径样例未通过校验: %v", err)
	}
	if _, err := store.Snapshot(BuiltinTemplateID); err != nil {
		t.Fatalf("生产路径内置模板快照失败: %v", err)
	}
}
