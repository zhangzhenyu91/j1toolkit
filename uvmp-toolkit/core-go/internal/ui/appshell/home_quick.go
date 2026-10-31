package appshell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// homeQuickStart 首页“常用与最近”：只提供工具打开和收藏，不引入第二层任务工作台。
type homeQuickStart struct {
	shell *Shell
	root  fyne.CanvasObject
	box   *fyne.Container
	scope *appTooltipScope
	empty *widget.Label
	rows  []*homeQuickRow

	maximized bool
}

func newHomeQuickStart(s *Shell) *homeQuickStart {
	q := &homeQuickStart{shell: s}
	q.box = container.New(&homeQuickLayout{gap: 12})
	q.scope = newAppTooltipScope(q.box)
	q.empty = muted("还没有收藏或最近打开的工具。进入任意工具后可点击页头“收藏”，下次从这里快速打开。")
	q.root = cardSurface(container.NewVBox(
		hrowGrow(secTitle("常用与最近"), tag("收藏优先", TextSecondary, BlockBG)),
		q.empty,
		q.scope,
	))
	return q
}

func (q *homeQuickStart) refresh() {
	if q == nil || q.box == nil {
		return
	}
	runUI(func() {
		ids := q.shell.quickToolIDs()
		q.scope.Reset()
		q.box.Objects = nil
		q.rows = q.rows[:0]
		for _, id := range ids {
			task, ok := homeTaskByID(id)
			if !ok {
				continue
			}
			row := newHomeQuickRow(task, q.shell.isFavorite(task.id),
				func() { q.shell.toggleFavorite(task.id) },
				func() { q.shell.Navigate(task.id) })
			row.setMaximized(q.maximized)
			q.rows = append(q.rows, row)
			q.box.Add(row)
		}
		if len(ids) == 0 {
			q.empty.Show()
			q.scope.Hide()
		} else {
			q.empty.Hide()
			q.scope.Show()
		}
		q.scope.Refresh()
		q.root.Refresh()
		reflowInScroll(q.scope)
	})
}

// setMaximized 切换常用入口的紧凑/完整描述；网格列数由可用宽度决定。
func (q *homeQuickStart) setMaximized(maximized bool) {
	if q == nil || q.maximized == maximized {
		return
	}
	q.maximized = maximized
	for _, row := range q.rows {
		row.setMaximized(maximized)
	}
	q.box.Refresh()
	reflowInScroll(q.box)
}

// homeQuickLayout 常用入口网格：六项在 600px 以上排成三列两行；窄于 600 时单列，
// MinSize 同步按当前列数计算，避免窄宽六行溢出。
type homeQuickLayout struct {
	gap   float32
	width float32 // 最近一次 Layout 的实际宽度
}

func (l *homeQuickLayout) narrow() bool {
	return l.width > 0 && l.width < 600
}

func (l *homeQuickLayout) columns(count int) int {
	if l.narrow() || count <= 1 {
		return 1
	}
	if count == 2 {
		return 2
	}
	return 3
}

func (l *homeQuickLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	visible := visibleHomeObjects(objects)
	if len(visible) == 0 {
		return fyne.Size{}
	}
	cols := l.columns(len(visible))
	if cols == 1 {
		return stackedCardsSize(visible, l.gap)
	}
	colWidth := make([]float32, cols)
	width, height := float32(0), float32(0)
	for i := 0; i < len(visible); i += cols {
		rowHeight := float32(0)
		for col := 0; col < cols && i+col < len(visible); col++ {
			min := visible[i+col].MinSize()
			colWidth[col] = fyne.Max(colWidth[col], min.Width)
			rowHeight = fyne.Max(rowHeight, min.Height)
		}
		if i > 0 {
			height += l.gap
		}
		height += rowHeight
	}
	for col := 0; col < cols; col++ {
		if col > 0 {
			width += l.gap
		}
		width += colWidth[col]
	}
	return fyne.NewSize(width, height)
}

func (l *homeQuickLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.width = size.Width
	visible := visibleHomeObjects(objects)
	if len(visible) == 0 {
		return
	}
	if size.Width <= 0 {
		for _, obj := range visible {
			obj.Move(fyne.NewPos(0, 0))
			obj.Resize(fyne.NewSize(0, 0))
		}
		return
	}
	cols := l.columns(len(visible))
	if cols == 1 {
		y := float32(0)
		for i, obj := range visible {
			if i > 0 {
				y += l.gap
			}
			height := obj.MinSize().Height
			obj.Move(fyne.NewPos(0, y))
			obj.Resize(fyne.NewSize(size.Width, height))
			y += height
		}
		return
	}
	cellWidth := (size.Width - l.gap*float32(cols-1)) / float32(cols)
	y := float32(0)
	for i := 0; i < len(visible); i += cols {
		rowHeight := float32(0)
		for col := 0; col < cols && i+col < len(visible); col++ {
			rowHeight = fyne.Max(rowHeight, visible[i+col].MinSize().Height)
		}
		if i > 0 {
			y += l.gap
		}
		for col := 0; col < cols && i+col < len(visible); col++ {
			obj := visible[i+col]
			obj.Move(fyne.NewPos(float32(col)*(cellWidth+l.gap), y))
			obj.Resize(fyne.NewSize(cellWidth, rowHeight))
		}
		y += rowHeight
	}
}

const (
	homeQuickIconSize float32 = 20
	homeQuickIconGap  float32 = 10
	homeQuickDescGap  float32 = 4
)

// homeQuickRow 首页快捷入口：图标与标题同中心线，描述在操作区下方占满剩余宽度。
type homeQuickRow struct {
	widget.BaseWidget

	icon       *canvas.Image
	title      *canvas.Text
	descNormal *widget.Label
	descFull   *widget.Label
	actions    *fyne.Container
	maximized  bool
}

func newHomeQuickRow(task homeTask, favorite bool, onFavorite, onOpen func()) *homeQuickRow {
	r := &homeQuickRow{}
	r.ExtendBaseWidget(r)
	r.icon = canvas.NewImageFromResource(lineIcon(task.icon, BrandBlue))
	r.icon.FillMode = canvas.ImageFillContain
	r.icon.ScaleMode = canvas.ImageScaleSmooth
	r.icon.SetMinSize(fyne.NewSize(homeQuickIconSize, homeQuickIconSize))
	r.title = canvas.NewText(task.name, TextPrimary)
	r.title.TextSize = 14
	r.title.TextStyle = fyne.TextStyle{Bold: true}
	// 常规单行省略、最大化完整换行：两套描述预建，只切显隐。
	r.descNormal = widget.NewLabel(task.desc)
	r.descNormal.Importance = widget.LowImportance
	r.descNormal.SizeName = theme.SizeNameCaptionText
	r.descNormal.Wrapping = fyne.TextWrapOff
	r.descNormal.Truncation = fyne.TextTruncateEllipsis
	r.descFull = widget.NewLabel(task.desc)
	r.descFull.Importance = widget.LowImportance
	r.descFull.SizeName = theme.SizeNameCaptionText
	r.descFull.Wrapping = fyne.TextWrapBreak
	r.descFull.Hide()
	favoriteLabel, favoriteIcon := "收藏", "star"
	if favorite {
		favoriteLabel, favoriteIcon = "取消收藏", "close"
	}
	r.actions = container.NewHBox(
		newAppIconButton(favoriteLabel, favoriteIcon, widget.LowImportance, onFavorite),
		newAppIconButton("打开", "arrow-right", widget.LowImportance, onOpen),
	)
	return r
}

// setMaximized 切换紧凑/完整描述（只切显隐，不重建行）。
func (r *homeQuickRow) setMaximized(maximized bool) {
	if r == nil || r.maximized == maximized {
		return
	}
	r.maximized = maximized
	if maximized {
		r.descFull.Show()
		r.descNormal.Hide()
	} else {
		r.descNormal.Show()
		r.descFull.Hide()
	}
	r.Refresh()
}

func (r *homeQuickRow) visibleDesc() *widget.Label {
	if r.maximized {
		return r.descFull
	}
	return r.descNormal
}

func (r *homeQuickRow) CreateRenderer() fyne.WidgetRenderer {
	return &homeQuickRowRenderer{row: r}
}

type homeQuickRowRenderer struct {
	row *homeQuickRow
}

func (r *homeQuickRowRenderer) Layout(size fyne.Size) {
	row := r.row
	desc := row.visibleDesc()
	titleMin := row.title.MinSize()
	actionsMin := row.actions.MinSize()
	topHeight := fyne.Max(homeQuickIconSize, fyne.Max(titleMin.Height, actionsMin.Height))
	textX := homeQuickIconSize + homeQuickIconGap

	row.icon.Move(fyne.NewPos(0, (topHeight-homeQuickIconSize)/2))
	row.icon.Resize(fyne.NewSize(homeQuickIconSize, homeQuickIconSize))
	row.title.Move(fyne.NewPos(textX, (topHeight-titleMin.Height)/2))
	titleWidth := size.Width - textX - columnGap - actionsMin.Width
	if titleWidth < titleMin.Width {
		titleWidth = titleMin.Width
	}
	row.title.Resize(fyne.NewSize(titleWidth, titleMin.Height))
	row.actions.Move(fyne.NewPos(size.Width-actionsMin.Width, (topHeight-actionsMin.Height)/2))
	row.actions.Resize(actionsMin)

	descWidth := size.Width - textX
	if descWidth < 0 {
		descWidth = 0
	}
	desc.Move(fyne.NewPos(textX, topHeight+homeQuickDescGap))
	desc.Resize(fyne.NewSize(descWidth, desc.MinSize().Height))
}

func (r *homeQuickRowRenderer) MinSize() fyne.Size {
	row := r.row
	titleMin := row.title.MinSize()
	actionsMin := row.actions.MinSize()
	descMin := row.visibleDesc().MinSize()
	topHeight := fyne.Max(homeQuickIconSize, fyne.Max(titleMin.Height, actionsMin.Height))
	textX := homeQuickIconSize + homeQuickIconGap
	// 列宽不足时优先压缩标题与操作之间 16px 的间距（Layout 已按可用宽度钳制），
	// MinSize 不能再把该间距算成硬下限，否则 900×600 三列会撑宽整页。
	contentWidth := fyne.Max(descMin.Width, titleMin.Width+actionsMin.Width)
	return fyne.NewSize(textX+contentWidth, topHeight+homeQuickDescGap+descMin.Height)
}

func (r *homeQuickRowRenderer) Refresh() {
	r.row.icon.Refresh()
	r.row.title.Refresh()
	r.row.descNormal.Refresh()
	r.row.descFull.Refresh()
	r.row.actions.Refresh()
}

func (r *homeQuickRowRenderer) Destroy() {}

func (r *homeQuickRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.row.icon, r.row.title, r.row.visibleDesc(), r.row.actions}
}

func (p *pageHome) refreshQuick() {
	if p.quick != nil {
		p.quick.refresh()
	}
}
