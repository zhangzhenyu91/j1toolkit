package appshell

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestWindowBrand_SingleTextBaseline(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	brand := newWindowBrand()
	if brand.label == nil || brand.label.Text != "Shade 壹匣 · 内网工具箱" {
		t.Fatal("品牌区未使用同一文本对象")
	}
	runUI(func() { brand.Resize(fyne.NewSize(300, titleBarHeight)) })
	iconCenter := brand.icon.Position().Y + brand.icon.Size().Height/2
	labelCenter := brand.label.Position().Y + brand.label.Size().Height/2
	if iconCenter != labelCenter {
		t.Fatalf("图标与文字未按同一中心线对齐：%.2f / %.2f", iconCenter, labelCenter)
	}
}

func TestWindowFrame_HasRoundedOnePixelOutline(t *testing.T) {
	content := newMinBox(fyne.NewSize(900, 600), nil)
	framed := newWindowFrame(&fakeWindowOps{}, content)
	root, ok := framed.(*fyne.Container)
	if !ok || len(root.Objects) != 2 {
		t.Fatalf("窗口缺少圆角描边层：%T", framed)
	}
	inner, ok := root.Objects[1].(*fyne.Container)
	if !ok || len(inner.Objects) != 2 {
		t.Fatalf("窗口画布缺少内容或描边：%T", root.Objects[1])
	}
	outline, ok := inner.Objects[1].(*canvas.Rectangle)
	if !ok || outline.StrokeWidth != 1 || outline.StrokeColor != Border || outline.CornerRadius != 12 {
		t.Fatalf("窗口描边不符合 1px / 12px 圆角约定：%+v", outline)
	}
}

func TestHistoryList_UsableScrollViewport(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	h := NewHistoryList("最近执行记录", true, nil)
	h.SetRecords([]ui.HistoryRecord{{JobID: "1"}, {JobID: "2"}, {JobID: "3"}})
	if !h.listBox.Visible() || h.listBox.MinSize().Height != 224 {
		t.Fatalf("执行记录缺少可用的滚动视口：visible=%v min=%v", h.listBox.Visible(), h.listBox.MinSize())
	}
	h.SetRecords(nil)
	if h.listBox.Visible() || !h.empty.Visible() {
		t.Fatal("空记录应显示空态，不保留空白列表")
	}
}
