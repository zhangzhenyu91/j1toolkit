package appshell

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// maxLogLines 日志行数上限（与桌面壳 api.js 同口径）。
const maxLogLines = 1500

// LogView 任务日志视图：多行追加、行数上限、自动滚到底（对应 Electron 的 .logview）。
type LogView struct {
	Root   fyne.CanvasObject
	rich   *widget.RichText
	scroll *container.Scroll
	lines  []string
}

// NewLogView 构造日志视图（初始高度约 180）。
func NewLogView() *LogView {
	rich := widget.NewRichText()
	scroll := container.NewVScroll(rich)
	scroll.SetMinSize(fyne.NewSize(0, 180))
	v := &LogView{rich: rich, scroll: scroll, Root: scroll}
	return v
}

// Append 追加一行并滚动到底部（可在任意线程调用）。
func (v *LogView) Append(line string) {
	runUI(func() {
		v.lines = append(v.lines, line)
		if len(v.lines) > maxLogLines {
			v.lines = v.lines[len(v.lines)-maxLogLines:]
		}
		v.rebuild()
	})
}

// SetText 整体替换（如查看历史日志全文）。
func (v *LogView) SetText(text string) {
	runUI(func() {
		if strings.TrimSpace(text) == "" {
			v.lines = nil
		} else {
			v.lines = strings.Split(text, "\n")
		}
		v.rebuild()
	})
}

// Clear 清空。
func (v *LogView) Clear() {
	runUI(func() {
		v.lines = nil
		v.rebuild()
	})
}

// Lines 当前行快照。
func (v *LogView) Lines() []string {
	return append([]string(nil), v.lines...)
}

func (v *LogView) rebuild() {
	segs := make([]widget.RichTextSegment, 0, len(v.lines)+1)
	for _, ln := range v.lines {
		segs = append(segs, &widget.TextSegment{Text: ln})
	}
	if len(segs) == 0 {
		segs = append(segs, &widget.TextSegment{Text: " "})
	}
	v.rich.Segments = segs
	v.rich.Refresh()
	v.scroll.ScrollToBottom()
}
