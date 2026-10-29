package appshell

import "strings"

// toolNavItems 以首页六工具目录为唯一元数据来源，侧栏、命令面板和首页共用。
func toolNavItems() []navItem {
	tasks := homeTaskList()
	items := make([]navItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, navItem{id: task.id, title: task.name, desc: task.desc, icon: task.icon})
	}
	return items
}

func shellIsPrimaryTool(appID string) bool {
	switch appID {
	case "pcd_export", "quiz_search", "course_autoplay":
		return true
	default:
		return false
	}
}

func homeTaskByID(appID string) (homeTask, bool) {
	for _, task := range homeTaskList() {
		if task.id == appID {
			return task, true
		}
	}
	return homeTask{}, false
}

// quickToolIDs 收藏优先、最近打开补充，去重后最多六项。
func (s *Shell) quickToolIDs() []string {
	ids := make([]string, 0, len(homeTaskList()))
	seen := map[string]bool{}
	for _, id := range s.favoriteIDs() {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range s.recentToolIDs() {
		if len(ids) == len(homeTaskList()) {
			break
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// syncFavorites 同步页头与首页快捷入口；所有收藏入口都只改壳层偏好。
func (s *Shell) syncFavorites() {
	if s.header != nil {
		s.header.SetFavorite(s.current, s.isFavorite(s.current))
	}
	if page, ok := s.pages["home"].(*pageHome); ok {
		page.refreshQuick()
	}
}

func navMatches(item navItem, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return query == "" || strings.Contains(strings.ToLower(item.title+"\n"+item.desc), query)
}
