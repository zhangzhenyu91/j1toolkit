package appshell

import (
	"context"
	"fmt"
	"image/color"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageScroll 把卡片序列包成整页滚动体（页面根对象）。
func pageScroll(cards ...fyne.CanvasObject) *container.Scroll {
	v := container.NewVBox()
	for i, c := range cards {
		v.Add(c)
		if i < len(cards)-1 {
			v.Add(spacer(12))
		}
	}
	return container.NewVScroll(container.NewPadded(v))
}

// spacer 固定间距块（透明矩形，MinSize 精确可控）。
func spacer(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	r.SetMinSize(fyne.NewSize(0, h))
	return r
}

// kvCard 键值状态卡（值字段可更新）。
type kvCard struct {
	Root  fyne.CanvasObject
	box   *fyne.Container
	title string
}

// newKVCard 构造键值面板（外层卡片由调用方以 newCard 包裹）。
func newKVCard(title string, pairs ...[2]string) *kvCard {
	k := &kvCard{title: title}
	k.box = container.NewVBox()
	k.Root = k.box
	k.SetValues(pairs...)
	return k
}

// SetValues 刷新键值对。
func (k *kvCard) SetValues(pairs ...[2]string) {
	runUI(func() {
		k.box.Objects = []fyne.CanvasObject{kvGrid(pairs...)}
		k.box.Refresh()
		reflowInScroll(k.box) // 值换行/行数变化后同步外层卡片高度
	})
}

// progressRow 进度条 + 文案（任务执行区共用）。
type progressRow struct {
	Root  fyne.CanvasObject
	bar   *widget.ProgressBar
	label *widget.Label
}

func newProgressRow() *progressRow {
	bar := widget.NewProgressBar()
	label := muted("")
	return &progressRow{Root: container.NewVBox(bar, label), bar: bar, label: label}
}

// Set 更新进度（可在任意线程调用；total<=0 只显示文案）。
func (p *progressRow) Set(cur, total int, text string) {
	runUI(func() {
		if total > 0 {
			p.bar.SetValue(float64(cur) / float64(total))
		} else {
			p.bar.SetValue(0)
		}
		p.label.SetText(text)
	})
}

// Reset 归零。
func (p *progressRow) Reset() {
	p.Set(0, 1, "")
	runUI(func() { p.bar.SetValue(0) })
}

// setProgress 按 JobProgress 更新进度行。
func setProgress(p *progressRow, pr ui.JobProgress) {
	p.Set(pr.Current, pr.Total, fmt.Sprintf("%s %d/%d", pr.Label, pr.Current, pr.Total))
}

// ssoReady 凭据预检：已配 SSO 密码或 dev.mock 开启才可跑导出任务。
func ssoReady(s *Shell) (bool, error) {
	cfg, err := s.Backend.GetConfig(context.Background())
	if err != nil {
		return true, err // 预检失败不阻塞，照常下发
	}
	return cfg.SSO.HasPassword || cfg.Dev.Mock, nil
}

// reloadHistory 刷新执行记录卡。
func reloadHistory(s *Shell, appID string, h *HistoryList) {
	s.Async(func() error {
		recs, err := s.Backend.History(context.Background(), appID)
		if err != nil {
			return err
		}
		h.SetRecords(recs)
		return nil
	})
}

// showJobLog 查看单次任务完整日志（弹层）。
func showJobLog(s *Shell, jobID string) {
	s.Async(func() error {
		text, err := s.Backend.JobLog(context.Background(), jobID)
		if err != nil {
			return err
		}
		runUI(func() {
			lv := NewLogView()
			lv.Root.Resize(fyne.NewSize(640, 420))
			lv.SetText(text)
			actions := newLogActions(s, func() string { return text }, "任务日志-"+jobID+".txt")
			body := container.NewBorder(actions, nil, nil, nil, lv.Root)
			d := dialog.NewCustom("排错日志（供技术支持）", "关闭", body, s.Win)
			d.Resize(fyne.NewSize(720, 480))
			d.Show()
		})
		return nil
	})
}

// hhmmRe 时间格式 HH:MM。
var hhmmRe = regexp.MustCompile(`^\d{1,2}:\d{2}$`)
