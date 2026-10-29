package appshell

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// rebuildNavEntries 主应用始终直接可见；同步工具默认收进开发者工具，搜索时仍可直接发现。
func (s *Shell) rebuildNavEntries(nav *fyne.Container, query string) {
	s.navQuery = query
	for id := range s.navEntries {
		if id != "settings" {
			delete(s.navEntries, id)
		}
	}
	nav.Objects = nil
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
		caption := canvas.NewText(group.caption, TextTertiary)
		caption.TextSize = 11
		caption.TextStyle = fyne.TextStyle{Bold: true}
		nav.Add(padAll(6, 2, navPadX+2, 0, caption))
		for _, item := range matches {
			entry := newNavEntry(item.title, item.icon, func() { s.Navigate(item.id) })
			entry.SetActive(s.current == item.id)
			s.navEntries[item.id] = entry
			nav.Add(entry)
		}
	}

	searching := strings.TrimSpace(query) != ""
	advancedLabel := "开发者工具（展开）"
	if s.advancedNavExpanded || searching {
		advancedLabel = "开发者工具（收起）"
	}
	advancedBtn := widget.NewButton(advancedLabel, func() {
		s.advancedNavExpanded = !s.advancedNavExpanded
		s.rebuildNavEntries(nav, s.navQuery)
	})
	advancedBtn.Importance = widget.LowImportance
	if searching {
		advancedBtn.Disable()
	}
	nav.Add(padAll(8, 4, 0, 4, advancedBtn))
	if s.advancedNavExpanded || searching {
		for _, id := range []string{"daily_sync", "tree_sync", "crossing_sync"} {
			item, ok := s.navByID(id)
			if !ok || !navMatches(item, query) {
				continue
			}
			entry := newNavEntry(item.title, item.icon, func() { s.Navigate(item.id) })
			entry.SetActive(s.current == item.id)
			s.navEntries[item.id] = entry
			nav.Add(entry)
		}
	}
	nav.Refresh()
}
