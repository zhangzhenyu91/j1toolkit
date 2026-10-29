package appshell

import (
	"encoding/json"
	"strings"
)

const (
	shellFavoritesPreferenceKey   = "shell.favorites.v1"
	shellRecentToolsPreferenceKey = "shell.recent_tools.v1"
	shellJobParamsKeyPrefix       = "shell.job_params."
	shellJobParamsKeySuffix       = ".v1"
	shellJobParamsIndexKey        = "shell.job_params.index.v1"
	shellJobParamsMaxBytes        = 512 * 1024
)

// shellJobParamsSnapshot 是任务参数在偏好存储中的完整快照。
type shellJobParamsSnapshot struct {
	AppID  string         `json:"app_id"`
	Params map[string]any `json:"params"`
}

// favoriteIDs 按首页目录顺序返回收藏，忽略非法项与重复项。
func (s *Shell) favoriteIDs() []string {
	selected := make(map[string]bool)
	for _, appID := range s.App.Preferences().StringList(shellFavoritesPreferenceKey) {
		if shellIsBusinessTool(appID) {
			selected[appID] = true
		}
	}
	return shellOrderedFavorites(selected)
}

// isFavorite 判断合法业务工具是否已收藏。
func (s *Shell) isFavorite(appID string) bool {
	if !shellIsBusinessTool(appID) {
		return false
	}
	for _, favoriteID := range s.favoriteIDs() {
		if favoriteID == appID {
			return true
		}
	}
	return false
}

// toggleFavorite 切换收藏状态并返回切换后的状态，非法工具不修改偏好。
func (s *Shell) toggleFavorite(appID string) bool {
	if !shellIsBusinessTool(appID) {
		return false
	}
	selected := make(map[string]bool)
	for _, favoriteID := range s.favoriteIDs() {
		selected[favoriteID] = true
	}
	nowFavorite := !selected[appID]
	if nowFavorite {
		selected[appID] = true
	} else {
		delete(selected, appID)
	}
	s.App.Preferences().SetStringList(shellFavoritesPreferenceKey, shellOrderedFavorites(selected))
	s.syncFavorites()
	return nowFavorite
}

// recentToolIDs 按最近优先返回去重后的合法业务工具。
func (s *Shell) recentToolIDs() []string {
	stored := s.App.Preferences().StringList(shellRecentToolsPreferenceKey)
	recent := make([]string, 0, len(homeTaskList()))
	seen := make(map[string]bool)
	for _, appID := range stored {
		if len(recent) == len(homeTaskList()) {
			break
		}
		if !shellIsBusinessTool(appID) || seen[appID] {
			continue
		}
		seen[appID] = true
		recent = append(recent, appID)
	}
	return recent
}

// recordRecentTool 将合法业务工具去重置前，最多保留六项。
func (s *Shell) recordRecentTool(appID string) {
	if !shellIsBusinessTool(appID) {
		return
	}
	previous := s.recentToolIDs()
	recent := make([]string, 0, len(homeTaskList()))
	recent = append(recent, appID)
	for _, previousID := range previous {
		if previousID != appID {
			recent = append(recent, previousID)
		}
	}
	s.App.Preferences().SetStringList(shellRecentToolsPreferenceKey, recent)
}

// saveJobParams 保存可重试任务的参数快照；敏感、超限或结构无效的快照不写入。
func (s *Shell) saveJobParams(jobID, appID string, params map[string]any) {
	if jobID == "" || appID == "" || !shellAllowsJobParams(appID) {
		return
	}
	if params == nil {
		params = map[string]any{}
	}
	copied, err := shellDeepCopyJobParams(params)
	if err != nil || shellHasSensitiveParamKey(copied) {
		return
	}
	encoded, err := json.Marshal(shellJobParamsSnapshot{AppID: appID, Params: copied})
	if err != nil || len(encoded) > shellJobParamsMaxBytes {
		return
	}
	prefs := s.App.Preferences()
	prefs.SetString(shellJobParamsPreferenceKey(jobID), string(encoded))
	entry := appID + ":" + jobID
	index := prefs.StringList(shellJobParamsIndexKey)
	for _, existing := range index {
		if existing == entry {
			return
		}
	}
	prefs.SetStringList(shellJobParamsIndexKey, append(index, entry))
}

// jobParams 读取并校验任务快照，返回与存储内容隔离的深拷贝。
func (s *Shell) jobParams(jobID string) (map[string]any, bool) {
	if jobID == "" {
		return nil, false
	}
	raw := s.App.Preferences().String(shellJobParamsPreferenceKey(jobID))
	if raw == "" {
		return nil, false
	}
	var snapshot shellJobParamsSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, false
	}
	if !shellAllowsJobParams(snapshot.AppID) || snapshot.Params == nil || shellHasSensitiveParamKey(snapshot.Params) {
		return nil, false
	}
	params, err := shellDeepCopyJobParams(snapshot.Params)
	if err != nil {
		return nil, false
	}
	return params, true
}

// deleteJobParams 删除指定任务的参数快照及其索引。
func (s *Shell) deleteJobParams(jobID string) {
	if jobID == "" {
		return
	}
	prefs := s.App.Preferences()
	prefs.RemoveValue(shellJobParamsPreferenceKey(jobID))
	index := prefs.StringList(shellJobParamsIndexKey)
	remaining := make([]string, 0, len(index))
	for _, entry := range index {
		_, id, ok := strings.Cut(entry, ":")
		if !ok || id != jobID {
			remaining = append(remaining, entry)
		}
	}
	prefs.SetStringList(shellJobParamsIndexKey, remaining)
}

// deleteAppJobParams 清空某应用历史时同步清理其全部参数快照。
func (s *Shell) deleteAppJobParams(appID string) {
	prefs := s.App.Preferences()
	index := prefs.StringList(shellJobParamsIndexKey)
	remaining := make([]string, 0, len(index))
	for _, entry := range index {
		storedApp, jobID, ok := strings.Cut(entry, ":")
		if ok && storedApp == appID {
			prefs.RemoveValue(shellJobParamsPreferenceKey(jobID))
			continue
		}
		remaining = append(remaining, entry)
	}
	prefs.SetStringList(shellJobParamsIndexKey, remaining)
}

func shellJobParamsPreferenceKey(jobID string) string {
	return shellJobParamsKeyPrefix + jobID + shellJobParamsKeySuffix
}

func shellOrderedFavorites(selected map[string]bool) []string {
	ordered := make([]string, 0, len(selected))
	for _, task := range homeTaskList() {
		if selected[task.id] {
			ordered = append(ordered, task.id)
		}
	}
	return ordered
}

func shellIsBusinessTool(appID string) bool {
	for _, task := range homeTaskList() {
		if task.id == appID {
			return true
		}
	}
	return false
}

func shellAllowsJobParams(appID string) bool {
	switch appID {
	case "pcd_export", "daily_sync", "tree_sync", "crossing_sync", "course_autoplay":
		return true
	default:
		return false
	}
}

// shellDeepCopyJobParams 经 JSON 往返复制参数，同时排除无法 JSON 表示的值。
func shellDeepCopyJobParams(params map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var copied map[string]any
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, err
	}
	return copied, nil
}

// shellHasSensitiveParamKey 递归检查对象与数组中的全部键名。
func shellHasSensitiveParamKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if shellIsSensitiveParamKey(key) || shellHasSensitiveParamKey(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if shellHasSensitiveParamKey(nested) {
				return true
			}
		}
	}
	return false
}

func shellIsSensitiveParamKey(key string) bool {
	normalized := strings.ToLower(key)
	for _, sensitive := range []string{
		"password", "passwd", "secret", "token", "credential", "authorization", "cookie", "sso",
	} {
		if strings.Contains(normalized, sensitive) {
			return true
		}
	}
	return false
}
