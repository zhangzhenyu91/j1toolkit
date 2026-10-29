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
	homeWideBreak float32 = 840 // 主从分栏阈值（内容区宽度）
	homeAsideW    float32 = 296
	homeGap       float32 = 18
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
	shell *Shell
	pane  *homePane

	alertWrap fyne.CanvasObject
	alert     fyne.CanvasObject
	cards     map[string]*homeTaskCard
	recentBox *fyne.Container
	empty     *widget.Label
	recent    []homeRecent
	views     []*recentView
}

func newPageHome(s *Shell) *pageHome {
	p := &pageHome{shell: s, cards: map[string]*homeTaskCard{}}

	p.alert = statusBar("warning", "")
	p.alert.Hide()
	p.alertWrap = container.NewVBox(p.alert, spacer(10))
	p.alertWrap.Hide()

	hero := p.newHeroCard(homeToolList()[0])
	tools := homeToolList()
	quizCard := p.newFeatureCard(tools[2], quizArt())
	courseCard := p.newFeatureCard(tools[1], courseArt())
	pair := newToolPairLayout(520, 14, quizCard.Root, courseCard.Root)
	background := p.newBackgroundCard()

	asideRecent := newRecentView(s)
	inlineRecent := newRecentView(s)
	inlineRecent.root.Hide()
	p.views = []*recentView{asideRecent, inlineRecent}
	p.recentBox = asideRecent.box
	p.empty = asideRecent.empty

	mainCol := container.NewVBox(
		p.alertWrap,
		hero.Root,
		spacer(10),
		pair,
		spacer(10),
		background,
		spacer(10),
		inlineRecent.root,
	)
	asideCol := container.NewVBox(
		asideRecent.root,
	)
	p.pane = newHomePane(mainCol, asideCol, inlineRecent.root)
	p.pane.mainScroll = container.NewVScroll(padAll(2, 14, 2, 12, mainCol))
	p.pane.asideScroll = container.NewVScroll(padAll(0, 14, 2, 4, asideCol))
	p.pane.refreshRenderers()
	return p
}

func (p *pageHome) Object() fyne.CanvasObject { return p.pane }

func (p *pageHome) OnShow() { p.reload() }

// newHeroCard 主任务卡：轨迹派车单导出（唯一 HighImportance「开始导出」，只切页不执行）。
func (p *pageHome) newHeroCard(task homeTask) *homeTaskCard {
	status := newStatusLine("正在读取状态…", widget.LowImportance, "")

	kicker := tag("主任务", BrandBlue, LightBlueBG)
	title := canvas.NewText(task.name, TextPrimary)
	title.TextSize = 21
	title.TextStyle = fyne.TextStyle{Bold: true}
	desc := muted(task.desc)
	desc.Show()

	start := widget.NewButtonWithIcon("开始导出", lineIcon("arrow-right", CardBG), func() {
		p.shell.Navigate(task.id)
	})
	start.Importance = widget.HighImportance

	left := container.NewVBox(
		hrow(kicker),
		title,
		desc,
		status.Root,
		spacer(4),
		hrow(start),
	)
	art := pcdArt()
	body := container.NewBorder(nil, nil, nil, padAll(0, 0, 12, 0, art), left)
	root := cardSurface(padAll(4, 4, 4, 4, body))

	c := &homeTaskCard{Root: root, status: status.label, apply: func(st homeState) {
		status.Set(st.text, st.imp)
	}}
	p.cards[task.id] = c
	return c
}

// newFeatureCard 同级特色工具卡（学堂/题库）：线性图形 + 状态 + 低强调入口。
func (p *pageHome) newFeatureCard(task homeTask, art fyne.CanvasObject) *homeTaskCard {
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
	open := newTextLink(openLabel, func() {
		p.shell.Navigate(task.id)
	})

	left := container.NewVBox(title, status.Root, desc, spacer(2), hrow(open))
	body := container.NewBorder(nil, nil, nil, padAll(0, 0, 12, 0, art), left)
	root := cardSurface(body)

	c := &homeTaskCard{Root: root, status: status.label, apply: func(st homeState) {
		status.Set(st.text, st.imp)
	}}
	p.cards[task.id] = c
	return c
}

// newBackgroundCard 后台同步卡：三个紧凑状态行（整行可点进对应业务页）。
func (p *pageHome) newBackgroundCard() fyne.CanvasObject {
	titleRow := hrowGrow(secTitle("后台同步"), tag("自动执行", TextSecondary, BlockBG))
	body := container.NewVBox(
		titleRow,
		spacer(4),
	)
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
	return cardSurface(body)
}

// newToolPairLayout 两张特色卡并排；窄于 breakAt 时上下单列。
func newToolPairLayout(breakAt, gap float32, objs ...fyne.CanvasObject) fyne.CanvasObject {
	return container.New(toolPairLayout{breakAt: breakAt, gap: gap}, objs...)
}

type toolPairLayout struct {
	breakAt float32
	gap     float32
}

func (l toolPairLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	w, h := float32(0), float32(0)
	for i, o := range objs {
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

func (l toolPairLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	if len(objs) == 1 || size.Width < l.breakAt {
		y := float32(0)
		for _, o := range objs {
			h := o.MinSize().Height
			o.Move(fyne.NewPos(0, y))
			o.Resize(fyne.NewSize(size.Width, h))
			y += h + l.gap
		}
		return
	}
	cellW := (size.Width - l.gap*float32(len(objs)-1)) / float32(len(objs))
	h := float32(0)
	for _, o := range objs {
		if mh := o.MinSize().Height; mh > h {
			h = mh
		}
	}
	x := float32(0)
	for _, o := range objs {
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(cellW, h))
		x += cellW + l.gap
	}
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
var _ desktop.Hoverable = (*homeRow)(nil)

// SetState 更新状态行（圆点语义色 + 文案）。
func (r *homeRow) SetState(st homeState) {
	r.status.SetText(st.text)
	r.dot.FillColor = statusColor(st.imp)
	r.dot.Refresh()
}

// Tapped 打开对应业务页。
func (r *homeRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap()
	}
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
	empty *widget.Label
	count *widget.Label
	shell *Shell
}

func newRecentView(s *Shell) *recentView {
	v := &recentView{shell: s}
	v.box = container.NewVBox()
	v.empty = muted("还没有执行结果。运行任意任务后，结果会出现在这里。")
	v.count = widget.NewLabel("0 条")
	v.count.Importance = widget.LowImportance
	v.count.SizeName = theme.SizeNameCaptionText

	head := hrowGrow(secTitle("最近结果"), v.count)
	body := container.NewVBox(head, spacer(2), widget.NewSeparator(), spacer(2), v.empty, v.box)
	v.root = cardSurface(body)
	return v
}

func (v *recentView) render(list []homeRecent) {
	v.box.Objects = nil
	if len(list) == 0 {
		v.empty.Show()
		v.box.Hide()
		v.box.Refresh()
		v.count.SetText("0 条")
		reflowInScroll(v.box)
		return
	}
	v.empty.Hide()
	v.box.Show()
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

		ops := container.NewHBox(newTextLink("查看日志", func() { showJobLog(v.shell, item.rec.JobID) }))
		if item.rec.Dir != "" {
			dir := item.rec.Dir
			ops.Add(newTextLink("打开目录", func() {
				v.shell.Async(func() error { return v.shell.Backend.OpenPath(context.Background(), dir) })
			}))
		}
		v.box.Add(container.NewVBox(head, summary, ops))
	}
	v.box.Refresh()
	v.root.Refresh()
	reflowInScroll(v.box)
}

// textLink 轻量文字操作（比 LowImportance 按钮矮，适合密集列表）。
type textLink struct {
	widget.BaseWidget
	text  *canvas.Text
	onTap func()
	hover bool
}

func newTextLink(label string, onTap func()) *textLink {
	l := &textLink{onTap: onTap}
	l.ExtendBaseWidget(l)
	l.text = canvas.NewText(label, BrandBlue)
	l.text.TextSize = 12.5
	l.text.TextStyle = fyne.TextStyle{Bold: true}
	return l
}

var _ fyne.Tappable = (*textLink)(nil)
var _ desktop.Hoverable = (*textLink)(nil)

func (l *textLink) Tapped(*fyne.PointEvent) {
	if l.onTap != nil {
		l.onTap()
	}
}

func (l *textLink) MouseIn(*desktop.MouseEvent) {
	l.hover = true
	l.text.Color = DeepBlue
	l.text.Refresh()
}

func (l *textLink) MouseMoved(*desktop.MouseEvent) {}

func (l *textLink) MouseOut() {
	l.hover = false
	l.text.Color = BrandBlue
	l.text.Refresh()
}

func (l *textLink) CreateRenderer() fyne.WidgetRenderer { return &textLinkRenderer{l} }

type textLinkRenderer struct{ l *textLink }

func (r *textLinkRenderer) Layout(size fyne.Size) {
	r.l.text.Move(fyne.NewPos(0, (size.Height-r.l.text.MinSize().Height)/2))
	r.l.text.Resize(fyne.NewSize(size.Width, r.l.text.MinSize().Height))
}
func (r *textLinkRenderer) MinSize() fyne.Size {
	m := r.l.text.MinSize()
	return fyne.NewSize(m.Width+6, m.Height+8)
}
func (r *textLinkRenderer) Refresh() { r.l.text.Refresh() }
func (r *textLinkRenderer) Destroy() {}
func (r *textLinkRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.l.text}
}

// homePane 首页主从容器：宽窗左主任务区 + 右最近结果；窄窗单列（最近结果移到主列末尾）。
type homePane struct {
	widget.BaseWidget

	mainCol  *fyne.Container
	asideCol *fyne.Container

	mainScroll  *container.Scroll
	asideScroll *container.Scroll

	inlineRecent fyne.CanvasObject
	wide         bool
	applied      bool
}

func newHomePane(mainCol, asideCol *fyne.Container, inlineRecent fyne.CanvasObject) *homePane {
	p := &homePane{mainCol: mainCol, asideCol: asideCol, inlineRecent: inlineRecent}
	p.ExtendBaseWidget(p)
	return p
}

// refreshRenderers 在滚动体装配完成后绑定渲染对象。
func (p *homePane) refreshRenderers() {
	if p.mainScroll != nil {
		p.mainScroll.Show()
	}
	if p.asideScroll != nil {
		p.asideScroll.Show()
	}
	p.Refresh()
}

// Resize 宽度跨过阈值时切换分栏/单列（不重建页面，最近结果内容保留）。
func (p *homePane) Resize(size fyne.Size) {
	wide := size.Width >= homeWideBreak
	if wide != p.wide || !p.applied {
		p.wide = wide
		p.applied = true
		p.applyMode()
	}
	p.BaseWidget.Resize(size)
}

func (p *homePane) applyMode() {
	if p.inlineRecent != nil {
		if p.wide {
			p.inlineRecent.Hide()
		} else {
			p.inlineRecent.Show()
		}
	}
	if p.asideScroll != nil {
		if p.wide {
			p.asideScroll.Show()
		} else {
			p.asideScroll.Hide()
		}
	}
	if p.mainScroll != nil {
		p.mainScroll.Refresh()
	}
	p.Refresh()
}

func (p *homePane) MinSize() fyne.Size { return fyne.NewSize(360, 240) }

func (p *homePane) CreateRenderer() fyne.WidgetRenderer { return &homePaneRenderer{p: p} }

type homePaneRenderer struct{ p *homePane }

func (r *homePaneRenderer) Layout(size fyne.Size) {
	p := r.p
	if p.mainScroll == nil || p.asideScroll == nil {
		return
	}
	if p.wide && size.Width >= homeWideBreak {
		mainW := size.Width - homeAsideW - homeGap
		p.mainScroll.Move(fyne.NewPos(0, 0))
		p.mainScroll.Resize(fyne.NewSize(mainW, size.Height))
		p.asideScroll.Move(fyne.NewPos(mainW+homeGap, 0))
		p.asideScroll.Resize(fyne.NewSize(homeAsideW, size.Height))
		return
	}
	p.mainScroll.Move(fyne.NewPos(0, 0))
	p.mainScroll.Resize(size)
}

func (r *homePaneRenderer) MinSize() fyne.Size { return fyne.NewSize(360, 240) }

func (r *homePaneRenderer) Refresh() {
	if r.p.mainScroll != nil {
		r.p.mainScroll.Refresh()
	}
	if r.p.asideScroll != nil {
		r.p.asideScroll.Refresh()
	}
}

func (r *homePaneRenderer) Destroy() {}

func (r *homePaneRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.p.mainScroll, r.p.asideScroll}
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
			if len(recs) > 0 {
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
			if p.pane != nil && p.pane.asideScroll != nil {
				p.pane.asideScroll.Refresh()
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
		return rec.Error
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
