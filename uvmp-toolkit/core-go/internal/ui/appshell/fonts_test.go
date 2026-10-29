package appshell

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func TestEmbeddedFontSelection(t *testing.T) {
	th := NewTheme()
	tests := []struct {
		name  string
		style fyne.TextStyle
		want  fyne.Resource
	}{
		{name: "正文", style: fyne.TextStyle{}, want: notoSansSCRegular},
		{name: "粗体", style: fyne.TextStyle{Bold: true}, want: notoSansSCBold},
		{name: "斜体不丢中文", style: fyne.TextStyle{Italic: true}, want: notoSansSCRegular},
		{name: "粗斜体不丢中文", style: fyne.TextStyle{Bold: true, Italic: true}, want: notoSansSCBold},
		{name: "等宽", style: fyne.TextStyle{Monospace: true}, want: jetBrainsMapleMono},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := th.Font(tt.style)
			if got.Name() != tt.want.Name() {
				t.Fatalf("字体资源 = %q，期望 %q", got.Name(), tt.want.Name())
			}
			if len(got.Content()) < 4 {
				t.Fatalf("字体资源 %q 内容不完整", got.Name())
			}
		})
	}
}

func TestEmbeddedFontSymbolUsesBaseTheme(t *testing.T) {
	th := NewTheme()
	style := fyne.TextStyle{Symbol: true}
	got := th.Font(style)
	want := th.(*enterpriseTheme).base.Font(style)
	if got.Name() != want.Name() {
		t.Fatalf("Symbol 字体 = %q，期望基础主题资源 %q", got.Name(), want.Name())
	}
}

func TestLogViewUsesMonospaceFont(t *testing.T) {
	v := NewLogView()
	v.Append("2026-10-29 22:44:17 壹匣")
	v.flushPending()
	if len(v.rich.Segments) != 1 {
		t.Fatalf("日志段数 = %d，期望 1", len(v.rich.Segments))
	}
	segment, ok := v.rich.Segments[0].(*widget.TextSegment)
	if !ok {
		t.Fatalf("日志段类型 = %T，期望 *widget.TextSegment", v.rich.Segments[0])
	}
	if !segment.Style.TextStyle.Monospace {
		t.Fatal("日志未请求等宽字体")
	}
	got := NewTheme().Font(segment.Style.TextStyle)
	if got.Name() != jetBrainsMapleMono.Name() {
		t.Fatalf("日志字体 = %q，期望 %q", got.Name(), jetBrainsMapleMono.Name())
	}
}
