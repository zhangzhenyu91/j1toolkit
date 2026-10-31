package appshell

import (
	"context"
	"image/color"
	"os"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 首页信息架构：
//   主任务（唯一主按钮）：轨迹派车单导出
//   同级工具：学堂刷课、题库搜题
//   后台同步（紧凑状态）：派车同步、树障同步、交跨同步
//   最近结果：宽窗右侧独立栏，窄窗落到主列末尾
// 数据仍只走 Ping + 一次 GetConfig + 六个应用 History，不调用 SystemInfo/AppStatus 等重接口。

const (
	homeWideBreak float32 = 840 // 下部双栏阈值（内容区宽度）
	homeAsideW    float32 = 320
	homeGap               = columnGap
)

// homeTask 首页任务入口定义（顺序即展示顺序；描述面向非开发者的一句话说明）。
type homeTask struct {
	id   string
	name string
	desc string
	icon string
}

// homeToolList 第一视觉层级的三项主工具。
func homeToolList() []homeTask {
	return []homeTask{
		{"pcd_export", "轨迹派车单导出", "按派车单号批量导出派车单与轨迹 PDF", "route"},
		{"course_autoplay", "学堂刷课", "受控浏览器自动学习国网学堂课程", "course"},
		{"quiz_search", "题库搜题", "屏幕选词实时匹配本机题库", "search"},
	}
}

// homeBackgroundList 第二层级的后台同步任务。
func homeBackgroundList() []homeTask {
	return []homeTask{
		{"daily_sync", "派车同步", "按计划把当日派车单导出到 GLKVM U 盘", "calendar"},
		{"tree_sync", "树障同步", "按服务端 U 盘指令同步树障统计表", "forest"},
		{"crossing_sync", "交跨同步", "按服务端 U 盘指令同步交跨统计表", "fork"},
	}
}

// homeTaskList 六个业务应用的首页入口（与常驻页面一一对应）。
func homeTaskList() []homeTask {
	return append(homeToolList(), homeBackgroundList()...)
}

// homeState 任务入口状态行（文案 + 语义色）。
type homeState struct {
	text string
	imp  widget.Importance
}

// homeRecent 首页「最近结果」一条（跨应用合并后按时间倒序）。
type homeRecent struct {
	appID   string
	appName string
	summary string
	rec     ui.HistoryRecord
}

// homeTaskCard 首页任务卡（主卡/特色卡/后台行共用状态回调）。
type homeTaskCard struct {
	Root   fyne.CanvasObject
	status *widget.Label
	apply  func(homeState)
}

func (c *homeTaskCard) setState(st homeState) {
	if c == nil || c.apply == nil {
		return
	}
	c.apply(st)
}

// pageHome 首页（工作台）。
type pageHome struct {
	shell  *Shell
	pane   *homePane
	quick  *homeQuickStart
	bottom *homeBottom
	pair   fyne.CanvasObject

	alertWrap fyne.CanvasObject
	alert     fyne.CanvasObject
	cards     map[string]*homeTaskCard
	recentBox *fyne.Container
	empty     *widget.Label
	recent    []homeRecent
	views     []*recentView
	maximized bool
}

func newPageHome(s *Shell) *pageHome {
	p := &pageHome{shell: s, cards: map[string]*homeTaskCard{}}

	p.alert = statusBar("warning", "")
	p.alert.Hide()
	p.alertWrap = container.NewVBox(p.alert, spacer(10))
	p.alertWrap.Hide()
	p.quick = newHomeQuickStart(s)

	hero := p.newHeroCard(homeToolList()[0])
	tools := homeToolList()
	quizCard := p.newFeatureCard(tools[2])
	courseCard := p.newFeatureCard(tools[1])
	pair := newToolPairLayout(520, 16, quizCard.Root, courseCard.Root)
	p.pair = pair
	background := newCollapsibleSection("开发者工具：后台同步", p.newBackgroundCard(), false).Root

	recent := newRecentView(s)
	p.views = []*recentView{recent}
	p.recentBox = recent.box
	p.empty = recent.empty
	// 最近结果常规不占位，最大化才在右栏出现（隐藏对象不计入 MinSize）。
	recent.root.Hide()
	bottom := newHomeBottom(background, recent.root)
	p.bottom = bottom

	mainCol := container.NewVBox(
		p.alertWrap,
		p.quick.root,
		spacer(cardGap),
		hero.Root,
		spacer(cardGap),
		pair,
		spacer(cardGap),
		bottom,
	)
	p.pane = newHomePane(mainCol, bottom)
	p.refreshQuick()
	return p
}

func (p *pageHome) Object() fyne.CanvasObject { return p.pane }

func (p *pageHome) OnShow() {
	p.refreshQuick()
	p.reload()
}

// SetMaximized 只切换预建区域的显隐与常用入口排布；不重建页面、不刷新任务数据。
func (p *pageHome) SetMaximized(maximized bool) {
	if p == nil || p.maximized == maximized {
		return
	}
	runUI(func() {
		p.maximized = maximized
		if len(p.views) > 0 {
			recent := p.views[0]
			if maximized {
				recent.showForShell(p.shell)
			} else {
				recent.hideForShell(p.shell)
			}
		}
		if p.quick != nil {
			p.quick.setMaximized(maximized)
		}
		if p.bottom != nil {
			p.bottom.Refresh()
		}
		if p.pane != nil && p.pane.mainScroll != nil {
			p.pane.mainScroll.Refresh()
		}
	})
}

var _ windowModePage = (*pageHome)(nil)

// newHeroCard 主任务卡：紧凑的信息列与明确的唯一主按钮，不使用大面积装饰插画。
func (p *pageHome) newHeroCard(task homeTask) *homeTaskCard {
	status := newStatusLine("正在读取状态…", widget.LowImportance, "")
	title := canvas.NewText(task.name, TextPrimary)
	title.TextSize = 18
	title.TextStyle = fyne.TextStyle{Bold: true}
	desc := muted(task.desc)
	start := newAppButton("开始导出", "play", widget.HighImportance, func() { p.shell.Navigate(task.id) })

	left := container.NewVBox(title, desc, status.Root)
	body := container.NewBorder(nil, nil, nil, padAll(0, 0, 16, 0, container.NewCenter(start)), left)
	root := cardSurface(body)
	c := &homeTaskCard{Root: root, status: status.label, apply: func(st homeState) {
		status.Set(st.text, st.imp)
	}}
	p.cards[task.id] = c
	return c
}

// newFeatureCard 同级工具卡：标题、状态、简短说明和低强调入口。
func (p *pageHome) newFeatureCard(task homeTask) *homeTaskCard {
	status := newStatusLine("正在读取状态…", widget.LowImportance, theme.SizeNameCaptionText)
	title := canvas.NewText(task.name, TextPrimary)
	title.TextSize = 15.5
	title.TextStyle = fyne.TextStyle{Bold: true}
	desc := muted(task.desc)
	openLabel := "进入"
	if task.id == "quiz_search" {
		openLabel = "进入题库"
	} else if task.id == "course_autoplay" {
		openLabel = "打开学堂"
	}
	open := newAppButton(openLabel, "arrow-right", widget.LowImportance, func() { p.shell.Navigate(task.id) })
	root := cardSurface(container.NewVBox(title, status.Root, desc, spacer(2), hrow(open)))
	c := &homeTaskCard{Root: root, status: status.label, apply: func(st homeState) {
		status.Set(st.text, st.imp)
	}}
	p.cards[task.id] = c
	return c
}

// newBackgroundCard 开发者工具内容：默认由外层折叠区承载，不与三项主应用争首屏。
func (p *pageHome) newBackgroundCard() fyne.CanvasObject {
	body := container.NewVBox(muted("供维护人员使用；普通用户无需配置这些同步任务。"), spacer(4))
	tasks := homeBackgroundList()
	for i, task := range tasks {
		task := task
		row := newHomeRow(task, func() { p.shell.Navigate(task.id) })
		p.cards[task.id] = &homeTaskCard{Root: row, status: row.status, apply: row.SetState}
		if i > 0 {
			body.Add(widget.NewSeparator())
		}
		body.Add(row)
	}
	return body
}

// newToolPairLayout 两张特色卡并排；窄于 breakAt 时上下单列。
func newToolPairLayout(breakAt, gap float32, objs ...fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&toolPairLayout{breakAt: breakAt, gap: gap}, objs...)
}

type toolPairLayout struct {
	breakAt float32
	gap     float32
	width   float32 // 最近一次 Layout 的实际宽度
}

func (l *toolPairLayout) narrow() bool {
	return l.width > 0 && l.width < l.breakAt
}

func (l *toolPairLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	visible := visibleHomeObjects(objs)
	if len(visible) == 0 {
		return fyne.Size{}
	}
	if l.narrow() {
		return stackedCardsSize(visible, l.gap)
	}
	w, h := float32(0), float32(0)
	for i, o := range visible {
		m := o.MinSize()
		if i > 0 {
			w += l.gap
		}
		w += m.Width
		if m.Height > h {
			h = m.Height
		}
	}
	return fyne.NewSize(w, h)
}

func (l *toolPairLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	l.width = size.Width
	visible := visibleHomeObjects(objs)
	if len(visible) == 0 {
		return
	}
	// 零宽/窄宽都退化为单列，避免 (width-gap)/n 产生负 cell。
	if len(visible) == 1 || size.Width < l.breakAt {
		y := float32(0)
		for _, o := range visible {
			h := o.MinSize().Height
			o.Move(fyne.NewPos(0, y))
			o.Resize(fyne.NewSize(size.Width, h))
			y += h + l.gap
		}
		return
	}
	cellW := (size.Width - l.gap*float32(len(visible)-1)) / float32(len(visible))
	h := float32(0)
	for _, o := range visible {
		if mh := o.MinSize().Height; mh > h {
			h = mh
		}
	}
	x := float32(0)
	for _, o := range visible {
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(cellW, h))
		x += cellW + l.gap
	}
}

// pageSplitLayout 业务页共用的常规/最大化布局：
// 按当前模式的 split 索引把可见卡片分到左右两列；split<=0 时常规单列。
// 卡片对象常驻，页面只负责显隐；隐藏卡片不占 MinSize，也不在列内留空位。
type pageSplitLayout struct {
	gap         float32
	normalSplit int
	maxSplit    int
	maximized   bool
	width       float32 // 最近一次 Layout 的实际宽度，供窄宽 MinSize 与布局保持一致
}

func (l *pageSplitLayout) split() int {
	if l.maximized {
		return l.maxSplit
	}
	return l.normalSplit
}

func (l *pageSplitLayout) narrow() bool {
	return l.width > 0 && l.width < 600
}

func (l *pageSplitLayout) columns(objects []fyne.CanvasObject) (left, right []fyne.CanvasObject) {
	split := l.split()
	if split <= 0 {
		return visibleHomeObjects(objects), nil
	}
	for i, obj := range objects {
		if obj == nil || !obj.Visible() {
			continue
		}
		if i < split {
			left = append(left, obj)
		} else {
			right = append(right, obj)
		}
	}
	return left, right
}

func (l *pageSplitLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return l.minSizeAt(l.width, objects)
}

// minSizeAt 是宽度确定的纯函数版本；MinSize 只负责传入最近一次 Layout 宽度。
func (l *pageSplitLayout) minSizeAt(width float32, objects []fyne.CanvasObject) fyne.Size {
	if width > 0 && width < 600 {
		return stackedCardsSize(visibleHomeObjects(objects), l.gap)
	}
	left, right := l.columns(objects)
	if len(right) == 0 {
		return stackedCardsSize(left, l.gap)
	}
	leftSize := stackedCardsSize(left, l.gap)
	rightSize := stackedCardsSize(right, l.gap)
	return fyne.NewSize(leftSize.Width+l.gap+rightSize.Width, fyne.Max(leftSize.Height, rightSize.Height))
}

func (l *pageSplitLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.width = size.Width
	left, right := l.columns(objects)
	if size.Width <= 0 {
		stackPageCards(visibleHomeObjects(objects), 0, 0, l.gap)
		return
	}
	if len(right) == 0 || l.narrow() {
		// 窄窗按原顺序把右列叠到左列下方，避免右列悬空或互相重叠。
		stackPageCards(visibleHomeObjects(objects), 0, size.Width, l.gap)
		return
	}
	rightW := (size.Width - l.gap) / 2
	leftW := size.Width - l.gap - rightW
	stackPageCards(left, 0, leftW, l.gap)
	stackPageCards(right, leftW+l.gap, rightW, l.gap)
}

func stackedCardsSize(objects []fyne.CanvasObject, gap float32) fyne.Size {
	width, height := float32(0), float32(0)
	for i, obj := range objects {
		if i > 0 {
			height += gap
		}
		min := obj.MinSize()
		width = fyne.Max(width, min.Width)
		height += min.Height
	}
	return fyne.NewSize(width, height)
}

func stackPageCards(objects []fyne.CanvasObject, x, width, gap float32) {
	y := float32(0)
	for i, obj := range objects {
		if i > 0 {
			y += gap
		}
		height := obj.MinSize().Height
		obj.Move(fyne.NewPos(x, y))
		obj.Resize(fyne.NewSize(width, height))
		y += height
	}
}

func visibleHomeObjects(objs []fyne.CanvasObject) []fyne.CanvasObject {
	visible := make([]fyne.CanvasObject, 0, len(objs))
	for _, obj := range objs {
		if obj != nil && obj.Visible() {
			visible = append(visible, obj)
		}
	}
	return visible
}

// homeRow 后台同步状态行：图标 + 名称 + 圆点状态 + 箭头，整行可点、hover 浅灰底。
type homeRow struct {
	widget.BaseWidget

	title    string
	iconName string
	status   *widget.Label
	onTap    func()

	bg      *canvas.Rectangle
	iconBG  *canvas.Rectangle
	icon    *canvas.Image
	name    *canvas.Text
	dot     *canvas.Circle
	chevron *canvas.Image
	hover   bool
	focused bool
}

func newHomeRow(task homeTask, onTap func()) *homeRow {
	r := &homeRow{title: task.name, iconName: task.icon, onTap: onTap}
	r.ExtendBaseWidget(r)
	r.bg = canvas.NewRectangle(color.Transparent)
	r.bg.CornerRadius = 8
	r.iconBG = canvas.NewRectangle(LightBlueBG)
	r.iconBG.CornerRadius = 9
	r.icon = canvas.NewImageFromResource(lineIcon(task.icon, BrandBlue))
	r.icon.FillMode = canvas.ImageFillContain
	r.icon.ScaleMode = canvas.ImageScaleSmooth
	r.icon.SetMinSize(fyne.NewSize(17, 17))
	r.name = canvas.NewText(task.name, TextPrimary)
	r.name.TextSize = 14
	r.name.TextStyle = fyne.TextStyle{Bold: true}
	r.dot = canvas.NewCircle(Placeholder)
	r.status = widget.NewLabel("正在读取状态…")
	r.status.Importance = widget.LowImportance
	r.status.SizeName = theme.SizeNameCaptionText
	r.status.Wrapping = fyne.TextWrapOff
	r.status.Truncation = fyne.TextTruncateEllipsis
	r.chevron = canvas.NewImageFromResource(lineIcon("chevron-right", TextTertiary))
	r.chevron.FillMode = canvas.ImageFillContain
	r.chevron.ScaleMode = canvas.ImageScaleSmooth
	r.chevron.SetMinSize(fyne.NewSize(16, 16))
	return r
}

var _ fyne.Tappable = (*homeRow)(nil)
var _ fyne.Focusable = (*homeRow)(nil)
var _ fyne.Accessible = (*homeRow)(nil)
var _ desktop.Hoverable = (*homeRow)(nil)
var _ desktop.Cursorable = (*homeRow)(nil)

// SetState 更新状态行（圆点语义色 + 文案）。
func (r *homeRow) SetState(st homeState) {
	r.status.SetText(st.text)
	r.dot.FillColor = statusColor(st.imp)
	r.dot.Refresh()
}

// Tapped 打开对应业务页。
func (r *homeRow) Tapped(*fyne.PointEvent) {
	if c := fyne.CurrentApp().Driver().CanvasForObject(r); c != nil {
		c.Focus(r)
	}
	if r.onTap != nil {
		r.onTap()
	}
}

func (r *homeRow) AccessibilityLabel() string { return r.title }
func (r *homeRow) AccessibilityRole() fyne.AccessibleRole {
	return fyne.AccessibleRoleButton
}
func (r *homeRow) Cursor() desktop.Cursor { return desktop.PointerCursor }
func (r *homeRow) TypedRune(rune)         {}
func (r *homeRow) TypedKey(event *fyne.KeyEvent) {
	if event.Name == fyne.KeySpace || event.Name == fyne.KeyReturn || event.Name == fyne.KeyEnter {
		r.Tapped(nil)
	}
}
func (r *homeRow) FocusGained() {
	r.focused = true
	r.bg.StrokeColor = BrandBlue
	r.bg.StrokeWidth = 1.5
	r.bg.Refresh()
}
func (r *homeRow) FocusLost() {
	r.focused = false
	r.bg.StrokeColor = color.Transparent
	r.bg.StrokeWidth = 0
	r.bg.Refresh()
}

func (r *homeRow) MouseIn(*desktop.MouseEvent) {
	r.hover = true
	r.bg.FillColor = BlockBG
	r.bg.Refresh()
}

func (r *homeRow) MouseMoved(*desktop.MouseEvent) {}

func (r *homeRow) MouseOut() {
	r.hover = false
	r.bg.FillColor = color.Transparent
	r.bg.Refresh()
}

func (r *homeRow) CreateRenderer() fyne.WidgetRenderer { return &homeRowRenderer{r: r} }

type homeRowRenderer struct{ r *homeRow }

func (r *homeRowRenderer) Layout(size fyne.Size) {
	row := r.r
	row.bg.Move(fyne.NewPos(0, 0))
	row.bg.Resize(size)
	row.iconBG.Move(fyne.NewPos(0, (size.Height-32)/2))
	row.iconBG.Resize(fyne.NewSize(32, 32))
	row.icon.Move(fyne.NewPos(7.5, (size.Height-17)/2))
	row.icon.Resize(fyne.NewSize(17, 17))
	row.name.Move(fyne.NewPos(44, 5))
	row.name.Resize(fyne.NewSize(size.Width-44-26, 20))
	row.dot.Move(fyne.NewPos(44, size.Height-22))
	row.dot.Resize(fyne.NewSize(7, 7))
	row.status.Move(fyne.NewPos(57, size.Height-27))
	row.status.Resize(fyne.NewSize(size.Width-57-26, 20))
	row.chevron.Move(fyne.NewPos(size.Width-22, (size.Height-16)/2))
	row.chevron.Resize(fyne.NewSize(16, 16))
}

func (r *homeRowRenderer) MinSize() fyne.Size { return fyne.NewSize(220, 50) }

func (r *homeRowRenderer) Refresh() {}

func (r *homeRowRenderer) Destroy() {}

func (r *homeRowRenderer) Objects() []fyne.CanvasObject {
	row := r.r
	return []fyne.CanvasObject{row.bg, row.iconBG, row.icon, row.name, row.dot, row.status, row.chevron}
}

// recentView 最近结果独立栏（宽窗右侧 / 窄窗主列末尾各一份，数据同源）。
type recentView struct {
	root  fyne.CanvasObject
	box   *fyne.Container
	scope *appTooltipScope
	empty *widget.Label
	count *widget.Label
	shell *Shell
}

func newRecentView(s *Shell) *recentView {
	v := &recentView{shell: s}
	v.box = container.NewVBox()
	v.scope = newAppTooltipScope(v.box)
	v.empty = muted("还没有执行结果。运行任意任务后，结果会出现在这里。")
	v.count = widget.NewLabel("0 条")
	v.count.Importance = widget.LowImportance
	v.count.SizeName = theme.SizeNameCaptionText

	head := hrowGrow(secTitle("最近结果"), v.count)
	body := container.NewVBox(head, spacer(2), widget.NewSeparator(), spacer(2), v.empty, v.scope)
	v.root = cardSurface(body)
	return v
}

// hideForShell/showForShell 动态显隐最近结果右栏：先按当前窗口清理工具提示，
// 避免旧 Shell 的 pending tooltip 在隐藏后继续落在新窗口。
func (v *recentView) hideForShell(s *Shell) {
	v.scope.resetForShell(s)
	v.root.Hide()
}

func (v *recentView) showForShell(s *Shell) {
	v.scope.resetForShell(s)
	v.root.Show()
}

func (v *recentView) render(list []homeRecent) {
	v.scope.Reset()
	v.box.Objects = nil
	if len(list) == 0 {
		v.empty.Show()
		v.scope.Hide()
		v.count.SetText("0 条")
		reflowInScroll(v.scope)
		return
	}
	v.empty.Hide()
	v.scope.Show()
	v.count.SetText(strconv.Itoa(len(list)) + " 条")
	for i, item := range list {
		item := item
		if i > 0 {
			v.box.Add(widget.NewSeparator())
		}
		imp := historyImportance(item.rec)
		name := canvas.NewText(item.appName, TextPrimary)
		name.TextSize = 13
		name.TextStyle = fyne.TextStyle{Bold: true}
		when := canvas.NewText(shortTime(item.rec.Time), TextTertiary)
		when.TextSize = 11
		head := container.NewBorder(nil, nil, hrow(statusDot(imp, 7), padAll(0, 0, 6, 0, name)), when)
		summary := widget.NewLabel(item.summary)
		summary.Wrapping = fyne.TextWrapWord
		summary.SizeName = theme.SizeNameCaptionText
		summary.Importance = widget.MediumImportance

		ops := container.NewHBox(newAppIconButton("查看日志", "file", widget.LowImportance, func() { showJobLog(v.shell, item.rec.JobID) }))
		if item.rec.Dir != "" {
			dir := item.rec.Dir
			ops.Add(newAppIconButton("打开目录", "folder", widget.LowImportance, func() {
				v.shell.Async(func() error { return v.shell.Backend.OpenPath(context.Background(), dir) })
			}))
		}
		v.box.Add(container.NewVBox(head, summary, ops))
	}
	reflowInScroll(v.scope)
}

// homeBottom 宽窗让后台同步与最近结果并排，窄窗纵向排列。
// 主任务与工具卡始终使用完整内容宽度，不被右侧信息栏长期挤窄。
type homeBottom struct {
	widget.BaseWidget
	background fyne.CanvasObject
	recent     fyne.CanvasObject
	wide       bool
}

func newHomeBottom(background, recent fyne.CanvasObject) *homeBottom {
	b := &homeBottom{background: background, recent: recent}
	b.ExtendBaseWidget(b)
	return b
}

func (b *homeBottom) Resize(size fyne.Size) {
	b.wide = size.Width >= homeWideBreak
	b.BaseWidget.Resize(size)
}

func (b *homeBottom) MinSize() fyne.Size {
	left, right := b.background.MinSize(), b.recent.MinSize()
	leftVisible, rightVisible := b.background.Visible(), b.recent.Visible()
	switch {
	case !leftVisible && !rightVisible:
		return fyne.Size{}
	case !leftVisible:
		return right
	case !rightVisible:
		return left
	}
	if b.wide {
		return fyne.NewSize(homeAsideW+homeGap+left.Width, fyne.Max(left.Height, right.Height))
	}
	return fyne.NewSize(fyne.Max(left.Width, right.Width), left.Height+right.Height+homeGap)
}

func (b *homeBottom) CreateRenderer() fyne.WidgetRenderer { return &homeBottomRenderer{b: b} }

type homeBottomRenderer struct{ b *homeBottom }

func (r *homeBottomRenderer) Layout(size fyne.Size) {
	if !r.b.background.Visible() {
		if r.b.recent.Visible() {
			r.b.recent.Move(fyne.NewPos(0, 0))
			r.b.recent.Resize(fyne.NewSize(size.Width, r.b.recent.MinSize().Height))
		}
		return
	}
	if !r.b.recent.Visible() {
		r.b.background.Move(fyne.NewPos(0, 0))
		r.b.background.Resize(fyne.NewSize(size.Width, r.b.background.MinSize().Height))
		return
	}
	if r.b.wide && size.Width >= homeWideBreak {
		leftW := size.Width - homeAsideW - homeGap
		h := fyne.Max(r.b.background.MinSize().Height, r.b.recent.MinSize().Height)
		r.b.background.Move(fyne.NewPos(0, 0))
		r.b.background.Resize(fyne.NewSize(leftW, h))
		r.b.recent.Move(fyne.NewPos(leftW+homeGap, 0))
		r.b.recent.Resize(fyne.NewSize(homeAsideW, h))
		return
	}
	y := float32(0)
	for _, obj := range []fyne.CanvasObject{r.b.background, r.b.recent} {
		h := obj.MinSize().Height
		obj.Move(fyne.NewPos(0, y))
		obj.Resize(fyne.NewSize(size.Width, h))
		y += h + homeGap
	}
}
func (r *homeBottomRenderer) MinSize() fyne.Size { return r.b.MinSize() }
func (r *homeBottomRenderer) Refresh() {
	r.b.background.Refresh()
	r.b.recent.Refresh()
}
func (r *homeBottomRenderer) Destroy() {}
func (r *homeBottomRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.b.background, r.b.recent}
}

// homePane 只有一个滚动区：没有中央滚动条，也不再维护两份最近结果。
type homePane struct {
	widget.BaseWidget
	mainScroll *container.Scroll
	bottom     *homeBottom
	wide       bool
}

func newHomePane(mainCol *fyne.Container, bottom *homeBottom) *homePane {
	p := &homePane{bottom: bottom}
	p.ExtendBaseWidget(p)
	p.mainScroll = container.NewVScroll(mainCol)
	return p
}

func (p *homePane) Resize(size fyne.Size) {
	p.BaseWidget.Resize(size)
	if p.bottom != nil {
		p.wide = p.bottom.wide
	}
}
func (p *homePane) MinSize() fyne.Size { return fyne.NewSize(360, 240) }
func (p *homePane) CreateRenderer() fyne.WidgetRenderer {
	return &homePaneRenderer{p: p}
}

type homePaneRenderer struct{ p *homePane }

func (r *homePaneRenderer) Layout(size fyne.Size) {
	r.p.mainScroll.Move(fyne.NewPos(0, 0))
	r.p.mainScroll.Resize(size)
}
func (r *homePaneRenderer) MinSize() fyne.Size { return fyne.NewSize(360, 240) }
func (r *homePaneRenderer) Refresh()           { r.p.mainScroll.Refresh() }
func (r *homePaneRenderer) Destroy()           {}
func (r *homePaneRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.p.mainScroll}
}

// shortTime 最近结果时间只保留月-日 时:分（完整时间仍在本机记录里）。
func shortTime(s string) string {
	if len(s) >= 16 {
		return s[5:16]
	}
	return s
}

// historyImportance 历史记录状态 → 语义档位（圆点色）。
func historyImportance(rec ui.HistoryRecord) widget.Importance {
	if rec.Paused() {
		return widget.MediumImportance
	}
	switch rec.Status {
	case ui.JobSuccess:
		return widget.SuccessImportance
	case ui.JobFailed:
		return widget.DangerImportance
	case ui.JobCancelled:
		return widget.LowImportance
	}
	return widget.MediumImportance
}

// reload 首页轻量刷新：Ping 健康检查 + 一次配置读取 + 六个执行记录。
// 不调用 SystemInfo / AppStatus / QuizScanState / CourseStatus：这些接口会触发
// USB PowerShell 探测（Windows 可达 30s）或题库扫描服务装配，曾导致首页所有状态
// 长时间停在「正在读取状态…」。U 盘等详细状态仍由各业务页自己检测。
func (p *pageHome) reload() {
	p.shell.Async(func() error {
		ctx := context.Background()
		alerts := []string{}
		if err := p.shell.Backend.Ping(ctx); err != nil {
			alerts = append(alerts, "客户端服务未就绪，任务可能无法执行："+err.Error())
		}
		cfg, err := p.shell.Backend.GetConfig(ctx)
		if err != nil {
			alerts = append(alerts, "读取本机配置失败："+err.Error())
		}
		alert := strings.Join(alerts, "；")

		states := map[string]homeState{}
		tasks := homeTaskList()
		recents := make([]homeRecent, 0, len(tasks))
		for _, task := range tasks {
			recs, _ := p.shell.Backend.History(ctx, task.id)
			states[task.id] = p.taskState(task.id, recs, cfg)
			if shellIsPrimaryTool(task.id) && len(recs) > 0 {
				recents = append(recents, homeRecent{
					appID:   task.id,
					appName: task.name,
					summary: homeResultSummary(task.id, recs[0]),
					rec:     recs[0],
				})
			}
		}
		sort.SliceStable(recents, func(i, j int) bool {
			if recents[i].rec.Time == recents[j].rec.Time {
				return recents[i].rec.JobID > recents[j].rec.JobID
			}
			return recents[i].rec.Time > recents[j].rec.Time
		})
		if len(recents) > 6 {
			recents = recents[:6]
		}

		runUI(func() {
			if alert != "" {
				p.alert.Show()
				p.alertWrap.Show()
			} else {
				p.alert.Hide()
				p.alertWrap.Hide()
			}
			for id, st := range states {
				if card := p.cards[id]; card != nil {
					card.setState(st)
				}
			}
			p.recent = recents
			for _, v := range p.views {
				v.render(recents)
			}
			if p.pane != nil && p.pane.mainScroll != nil {
				p.pane.mainScroll.Refresh()
			}
		})
		return nil
	})
}

// taskState 单个任务入口的轻量状态：只用本机配置与最近执行记录，不访问后端状态/硬件。
func (p *pageHome) taskState(appID string, recs []ui.HistoryRecord, cfg ui.Config) homeState {
	switch appID {
	case "pcd_export":
		return homePcdState(cfg, recs)
	case "quiz_search":
		if text, imp, ok := homeLatestState(recs); ok {
			return homeState{"最近 " + text, imp}
		}
		return homeState{"尚未使用", widget.LowImportance}
	case "course_autoplay":
		url := strings.TrimSpace(cfg.BrowserAutomation.StartURL)
		if text, imp, ok := homeLatestState(recs); ok {
			if url == "" {
				return homeState{"未配置启动网址", widget.WarningImportance}
			}
			return homeState{"最近 " + text, imp}
		}
		if url == "" {
			return homeState{"未配置启动网址", widget.WarningImportance}
		}
		return homeState{"启动网址已配置", widget.MediumImportance}
	case "daily_sync":
		return homeDailyState(cfg.Schedule, recs)
	case "tree_sync":
		return homeSyncState(cfg.TreeSync, recs)
	case "crossing_sync":
		return homeSyncState(cfg.CrossingSync, recs)
	}
	return homeState{"等待执行", widget.LowImportance}
}

// homePcdState 主任务状态：账号配置 + 最近导出（不触发任何任务）。
func homePcdState(cfg ui.Config, recs []ui.HistoryRecord) homeState {
	needsAccount := !cfg.SSO.HasPassword && !cfg.Dev.Mock
	if len(recs) == 0 {
		if needsAccount {
			return homeState{"尚无执行记录 · 尚未配置派车系统账号", widget.WarningImportance}
		}
		return homeState{"尚无执行记录 · 选择单号清单即可开始", widget.LowImportance}
	}
	text, imp := historyStatusStyle(recs[0])
	state := homeState{"最近导出 " + text + " · " + shortTime(recs[0].Time), imp}
	switch {
	case recs[0].Status == ui.JobFailed:
		state.imp = widget.DangerImportance
	case recs[0].Status == ui.JobSuccess:
		state.imp = widget.SuccessImportance
	}
	if needsAccount {
		state.text += " · 尚未配置账号"
		state.imp = widget.WarningImportance
	}
	return state
}

// homeDailyState 派车同步：最近执行 + 配置定时（停用明确），无成功记录时按历史表达。
func homeDailyState(sch ui.ScheduleConfig, recs []ui.HistoryRecord) homeState {
	sched := "定时未设置"
	if t := strings.TrimSpace(sch.Time); t != "" {
		sched = "定时 " + t
	}
	if !sch.Enabled {
		sched = "定时已停用"
	}
	if len(recs) == 0 {
		imp := widget.MediumImportance
		if !sch.Enabled {
			imp = widget.WarningImportance
		}
		return homeState{"尚未执行 · " + sched, imp}
	}

	text, imp := historyStatusStyle(recs[0])
	base := "最近执行 " + text
	if !homeHasSuccess(recs) {
		base = "尚无成功记录 · " + base
	}
	switch {
	case recs[0].Status == ui.JobFailed:
		imp = widget.DangerImportance
	case !sch.Enabled:
		imp = widget.WarningImportance
	case recs[0].Status == ui.JobSuccess:
		imp = widget.SuccessImportance
	}
	return homeState{base + " · " + sched, imp}
}

// homeSyncState 树障/交跨：配置路径 + 夜间兜底时刻 + 最近执行；路径用 os.Stat 本地检查。
func homeSyncState(cfg ui.SyncAppConfig, recs []ui.HistoryRecord) homeState {
	night := ""
	if t := strings.TrimSpace(cfg.NightlyTime); t != "" {
		night = " · 夜间 " + t
	}
	path := strings.TrimSpace(cfg.LocalPath)
	if path == "" {
		return homeState{"未配置内网统计表" + night, widget.WarningImportance}
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return homeState{"内网表不存在，请检查路径" + night, widget.DangerImportance}
	}

	text := "内网表已就绪"
	imp := widget.SuccessImportance
	if latest, latestImp, ok := homeLatestState(recs); ok {
		text += " · 最近 " + latest
		imp = latestImp
		if recs[0].Status == ui.JobFailed {
			imp = widget.DangerImportance
		}
	} else {
		text += " · 尚未执行"
		imp = widget.MediumImportance
	}
	return homeState{text + night, imp}
}

// homeLatestState 最近一条执行记录的状态文案与语义色。
func homeLatestState(recs []ui.HistoryRecord) (string, widget.Importance, bool) {
	if len(recs) == 0 {
		return "", widget.LowImportance, false
	}
	text, imp := historyStatusStyle(recs[0])
	return text, imp, true
}

// homeHasSuccess 历史中是否有成功记录（History 为新→旧）。
func homeHasSuccess(recs []ui.HistoryRecord) bool {
	for _, rec := range recs {
		if rec.Status == ui.JobSuccess {
			return true
		}
	}
	return false
}

// homeResultSummary 最近结果摘要（与各页执行记录文案同口径）。
func homeResultSummary(appID string, rec ui.HistoryRecord) string {
	if rec.Status != ui.JobSuccess && rec.Error != "" {
		return friendlyTaskError(rec.Error)
	}
	switch appID {
	case "daily_sync":
		return ui.DailySummary(ui.DailyResultFrom(rec.Result))
	case "tree_sync", "crossing_sync":
		return ui.SyncSummary(ui.SyncRunResultFrom(rec.Result))
	case "pcd_export":
		return ui.PcdSummary(ui.PcdResultFrom(rec.Result))
	case "course_autoplay":
		if rec.Paused() {
			return "已暂停，可回到页面继续"
		}
		return ui.CourseCountText(ui.CourseResultFrom(rec.Result))
	}
	if rec.Status == ui.JobSuccess {
		return "已完成"
	}
	return rec.Status.CN()
}
