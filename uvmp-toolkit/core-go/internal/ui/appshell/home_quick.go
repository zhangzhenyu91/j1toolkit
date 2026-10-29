package appshell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// homeQuickStart 首页“常用与最近”：只提供工具打开和收藏，不引入第二层任务工作台。
type homeQuickStart struct {
	shell *Shell
	root  fyne.CanvasObject
	box   *fyne.Container
	empty *widget.Label
}

func newHomeQuickStart(s *Shell) *homeQuickStart {
	q := &homeQuickStart{shell: s}
	q.box = container.NewVBox()
	q.empty = muted("还没有收藏或最近打开的工具。进入任意工具后可点击页头“收藏”，下次从这里快速打开。")
	q.root = cardSurface(container.NewVBox(
		hrowGrow(secTitle("常用与最近"), tag("收藏优先", TextSecondary, BlockBG)),
		q.empty,
		q.box,
	))
	return q
}

func (q *homeQuickStart) refresh() {
	if q == nil || q.box == nil {
		return
	}
	runUI(func() {
		ids := q.shell.quickToolIDs()
		q.box.Objects = nil
		for i, id := range ids {
			task, ok := homeTaskByID(id)
			if !ok {
				continue
			}
			if i > 0 {
				q.box.Add(widget.NewSeparator())
			}
			icon := canvas.NewImageFromResource(lineIcon(task.icon, BrandBlue))
			icon.FillMode = canvas.ImageFillContain
			icon.SetMinSize(fyne.NewSize(20, 20))
			title := canvas.NewText(task.name, TextPrimary)
			title.TextSize = 14
			title.TextStyle = fyne.TextStyle{Bold: true}
			desc := muted(task.desc)
			left := container.NewBorder(nil, nil, padAll(0, 0, 10, 0, icon), nil,
				container.NewVBox(title, desc))
			open := newTextLink("打开", func() { q.shell.Navigate(task.id) })
			favorite := newTextLink("收藏", func() { q.shell.toggleFavorite(task.id) })
			if q.shell.isFavorite(task.id) {
				favorite.text.Text = "取消收藏"
				favorite.text.Refresh()
			}
			q.box.Add(container.NewBorder(nil, nil, nil, container.NewHBox(favorite, open), left))
		}
		if len(ids) == 0 {
			q.empty.Show()
			q.box.Hide()
		} else {
			q.empty.Hide()
			q.box.Show()
		}
		q.box.Refresh()
		q.root.Refresh()
		reflowInScroll(q.box)
	})
}

func (p *pageHome) refreshQuick() {
	if p.quick != nil {
		p.quick.refresh()
	}
}
