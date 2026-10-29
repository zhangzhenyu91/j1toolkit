package appshell

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// commandPaletteAction 限定命令面板可执行的应用内动作。
type commandPaletteAction uint8

const (
	commandPaletteNavigate commandPaletteAction = iota
	commandPaletteToggleFavorite
)

// commandPaletteItem 是可直接展示和执行的静态命令，不保存任意命令文本。
type commandPaletteItem struct {
	title  string
	desc   string
	pageID string
	action commandPaletteAction
}

// commandEntry 仅补充面板导航键，其余键盘与文本输入仍由原 Entry 处理。
type commandEntry struct {
	*widget.Entry

	onUp      func()
	onDown    func()
	onExecute func()
	onClose   func()
}

func newCommandEntry() *commandEntry {
	entry := &commandEntry{Entry: widget.NewEntry()}
	entry.Entry.ExtendBaseWidget(entry)
	return entry
}

// TypedKey 只拦截选择、执行和关闭按键，复制粘贴等快捷键不受影响。
func (e *commandEntry) TypedKey(event *fyne.KeyEvent) {
	switch event.Name {
	case fyne.KeyUp:
		if e.onUp != nil {
			e.onUp()
		}
	case fyne.KeyDown:
		if e.onDown != nil {
			e.onDown()
		}
	case fyne.KeyReturn, fyne.KeyEnter:
		if e.onExecute != nil {
			e.onExecute()
		}
	case fyne.KeyEscape:
		if e.onClose != nil {
			e.onClose()
		}
	default:
		e.Entry.TypedKey(event)
	}
}

// TypedRune 将普通文本输入完整委托给原 Entry。
func (e *commandEntry) TypedRune(r rune) {
	e.Entry.TypedRune(r)
}

// buildCommandPaletteItems 按导航顺序生成页面命令，并只为业务工具附带收藏命令。
func buildCommandPaletteItems(nav []navItem, isFavorite func(string) bool) []commandPaletteItem {
	items := make([]commandPaletteItem, 0, len(nav)*2)
	for _, meta := range nav {
		items = append(items, commandPaletteItem{
			title:  meta.title,
			desc:   meta.desc,
			pageID: meta.id,
			action: commandPaletteNavigate,
		})
		if !shellIsBusinessTool(meta.id) {
			continue
		}
		favorite := isFavorite != nil && isFavorite(meta.id)
		verb := "收藏 "
		if favorite {
			verb = "取消收藏 "
		}
		items = append(items, commandPaletteItem{
			title:  verb + meta.title,
			desc:   meta.desc,
			pageID: meta.id,
			action: commandPaletteToggleFavorite,
		})
	}
	return items
}

// filterCommandPaletteItems 对标题与描述做大小写不敏感的子串过滤。
func filterCommandPaletteItems(items []commandPaletteItem, query string) []commandPaletteItem {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]commandPaletteItem(nil), items...)
	}
	filtered := make([]commandPaletteItem, 0, len(items))
	for _, item := range items {
		haystack := strings.ToLower(item.title + "\n" + item.desc)
		if strings.Contains(haystack, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// bindShortcuts 只注册当前窗口画布快捷键，不安装系统级全局热键。
func (s *Shell) bindShortcuts() {
	if s.Win == nil || s.Win.Canvas() == nil {
		return
	}
	canvas := s.Win.Canvas()
	canvas.AddShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyK,
		Modifier: fyne.KeyModifierControl,
	}, func(fyne.Shortcut) {
		s.showCommandPalette()
	})
	canvas.AddShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyComma,
		Modifier: fyne.KeyModifierControl,
	}, func(fyne.Shortcut) {
		s.Navigate("settings")
	})
}

// showCommandPalette 展示轻量应用内命令面板。
func (s *Shell) showCommandPalette() {
	runUI(func() {
		if s.Win == nil || s.Win.Canvas() == nil {
			return
		}
		canvas := s.Win.Canvas()
		previousFocus := canvas.Focused()
		allItems := buildCommandPaletteItems(s.navMeta, s.isFavorite)
		filtered := filterCommandPaletteItems(allItems, "")
		selected := 0

		var popup *widget.PopUp
		var list *widget.List
		var refresh func()
		var moveSelection func(int)
		var activate func(int)

		restoreFocus := func() {
			if object, ok := previousFocus.(fyne.CanvasObject); ok && object.Visible() {
				canvas.Focus(previousFocus)
				return
			}
			canvas.Unfocus()
		}
		closePalette := func(restore bool) {
			popup.Hide()
			if restore {
				restoreFocus()
			}
		}

		search := newCommandEntry()
		search.PlaceHolder = "搜索工具、首页、执行历史或设置"
		search.onUp = func() { moveSelection(-1) }
		search.onDown = func() { moveSelection(1) }
		search.onExecute = func() { activate(selected) }
		search.onClose = func() { closePalette(true) }

		empty := widget.NewLabel("没有匹配的工具或页面")
		empty.Importance = widget.MediumImportance
		empty.Alignment = fyne.TextAlignCenter
		empty.Hide()

		list = widget.NewList(
			func() int { return len(filtered) },
			func() fyne.CanvasObject {
				title := widget.NewLabel("")
				title.TextStyle = fyne.TextStyle{Bold: true}
				desc := widget.NewLabel("")
				desc.Importance = widget.MediumImportance
				return container.NewVBox(title, desc)
			},
			func(id widget.ListItemID, object fyne.CanvasObject) {
				if id < 0 || id >= len(filtered) {
					return
				}
				row := object.(*fyne.Container)
				row.Objects[0].(*widget.Label).SetText(filtered[id].title)
				row.Objects[1].(*widget.Label).SetText(filtered[id].desc)
			},
		)

		selectWithoutRunning := func(index int) {
			callback := list.OnSelected
			list.OnSelected = nil
			list.Select(index)
			list.OnSelected = callback
		}
		moveSelection = func(delta int) {
			if len(filtered) == 0 {
				return
			}
			next := selected + delta
			if next < 0 {
				next = 0
			}
			if next >= len(filtered) {
				next = len(filtered) - 1
			}
			selected = next
			selectWithoutRunning(selected)
			list.ScrollTo(selected)
		}
		activate = func(index int) {
			if index < 0 || index >= len(filtered) {
				return
			}
			item := filtered[index]
			switch item.action {
			case commandPaletteNavigate:
				closePalette(false)
				if shellIsBusinessTool(item.pageID) {
					s.recordRecentTool(item.pageID)
				}
				s.Navigate(item.pageID)
				// 切页后再恢复原焦点；原控件已随页面隐藏时不再聚焦。
				restoreFocus()
			case commandPaletteToggleFavorite:
				if !shellIsBusinessTool(item.pageID) {
					return
				}
				s.toggleFavorite(item.pageID)
				refresh()
			}
		}
		list.OnSelected = func(id widget.ListItemID) {
			selected = id
			activate(id)
		}

		refresh = func() {
			allItems = buildCommandPaletteItems(s.navMeta, s.isFavorite)
			filtered = filterCommandPaletteItems(allItems, search.Text)
			selected = 0
			list.Refresh()
			if len(filtered) == 0 {
				list.Hide()
				empty.Show()
				return
			}
			empty.Hide()
			list.Show()
			selectWithoutRunning(0)
			list.ScrollToTop()
		}
		search.OnChanged = func(string) { refresh() }

		heading := widget.NewLabel("工具搜索")
		heading.TextStyle = fyne.TextStyle{Bold: true}
		hint := widget.NewLabel("上下键选择 · Enter 确认 · Esc 关闭")
		hint.Importance = widget.MediumImportance
		hint.Alignment = fyne.TextAlignCenter
		results := container.NewStack(list, empty)
		content := container.NewBorder(
			container.NewVBox(heading, search),
			hint, nil, nil,
			results,
		)
		popup = widget.NewModalPopUp(container.NewPadded(content), canvas)
		popup.Resize(fyne.NewSize(640, 480))
		refresh()
		popup.Show()
		canvas.Focus(search)
	})
}
