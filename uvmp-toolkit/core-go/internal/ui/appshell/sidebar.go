package appshell

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
)

// rebuildNavEntries 主应用始终直接可见；同步工具默认收进开发者工具，搜索时仍可直接发现。
func (s *Shell) rebuildNavEntries(nav *fyne.Container, query string) {
	s.navQuery = query
	for id := range s.navEntries {
		if id != "settings" {
			delete(s.navEntries, id)
		}
	}
	nav.Layout = navListLayout{}
	nav.Objects = nil
	hasGroup := false
	addGroupGap := func() {
		if hasGroup {
			nav.Add(spacer(navGroupGap))
		}
	}
	addEntry := func(item navItem) {
		entry := newNavEntry(item.title, item.icon, func() { s.Navigate(item.id) })
		entry.SetActive(s.current == item.id)
		s.navEntries[item.id] = entry
		nav.Add(entry)
	}
	groups := []struct {
		caption string
		ids     []string
	}{
		{"工作台", []string{"home", "history"}},
		{"主应用", []string{"pcd_export", "quiz_search", "course_autoplay"}},
	}
	for _, group := range groups {
		matches := make([]navItem, 0, len(group.ids))
		for _, id := range group.ids {
			if item, ok := s.navByID(id); ok && navMatches(item, query) {
				matches = append(matches, item)
			}
		}
		if len(matches) == 0 {
			continue
		}
		addGroupGap()
		caption := canvas.NewText(group.caption, TextTertiary)
		caption.TextSize = navCaptionSize
		caption.TextStyle = fyne.TextStyle{Bold: true}
		nav.Add(padAll(0, 0, navInnerX, navInnerX, caption))
		nav.Add(spacer(navItemGap))
		for i, item := range matches {
			if i > 0 {
				nav.Add(spacer(navItemGap))
			}
			addEntry(item)
		}
		hasGroup = true
	}

	searching := strings.TrimSpace(query) != ""
	addGroupGap()
	advanced := newNavEntry("开发者工具", "settings", func() {
		if strings.TrimSpace(s.navQuery) != "" {
			return
		}
		s.advancedNavExpanded = !s.advancedNavExpanded
		s.rebuildNavEntries(nav, s.navQuery)
	})
	advanced.expanded = s.advancedNavExpanded || searching
	advanced.arrow = canvas.NewImageFromResource(theme.MenuExpandIcon())
	advanced.arrow.FillMode = canvas.ImageFillContain
	advanced.arrow.ScaleMode = canvas.ImageScaleSmooth
	advanced.arrow.SetMinSize(fyne.NewSize(navIconSize, navIconSize))
	advanced.applyStyle()
	nav.Add(advanced)
	if s.advancedNavExpanded || searching {
		for _, id := range []string{"daily_sync", "tree_sync", "crossing_sync"} {
			item, ok := s.navByID(id)
			if !ok || !navMatches(item, query) {
				continue
			}
			nav.Add(spacer(navItemGap))
			addEntry(item)
		}
	}
	nav.Refresh()
}

// navListLayout 只使用显式 token 间距，避免 VBox 默认间距与 spacer 叠加。
type navListLayout struct{}

func (navListLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	width, height := float32(0), float32(0)
	for _, obj := range visibleHomeObjects(objects) {
		min := obj.MinSize()
		width = fyne.Max(width, min.Width)
		height += min.Height
	}
	return fyne.NewSize(width, height)
}

func (navListLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, obj := range visibleHomeObjects(objects) {
		height := obj.MinSize().Height
		obj.Move(fyne.NewPos(0, y))
		obj.Resize(fyne.NewSize(size.Width, height))
		y += height
	}
}
