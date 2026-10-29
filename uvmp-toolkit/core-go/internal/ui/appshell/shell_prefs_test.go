package appshell

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
)

func newShellPrefsTestShell(t *testing.T) *Shell {
	t.Helper()
	app := test.NewApp()
	t.Cleanup(app.Quit)
	return &Shell{App: app}
}

func TestShellFavoriteIDsOrderDedupAndInvalidID(t *testing.T) {
	s := newShellPrefsTestShell(t)
	if !s.toggleFavorite("tree_sync") || !s.toggleFavorite("pcd_export") {
		t.Fatal("合法工具应能加入收藏")
	}
	if got, want := s.favoriteIDs(), []string{"pcd_export", "tree_sync"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("收藏未按目录顺序返回：got=%v want=%v", got, want)
	}
	if !s.isFavorite("pcd_export") || s.isFavorite("daily_sync") || s.isFavorite("非法工具") {
		t.Fatal("收藏状态判断不符")
	}

	before := s.favoriteIDs()
	if s.toggleFavorite("非法工具") {
		t.Fatal("非法工具不应返回收藏状态")
	}
	if got := s.favoriteIDs(); !reflect.DeepEqual(got, before) {
		t.Fatalf("非法工具改变了收藏：got=%v before=%v", got, before)
	}
	if s.toggleFavorite("pcd_export") {
		t.Fatal("再次切换应取消收藏")
	}

	s.App.Preferences().SetStringList(shellFavoritesPreferenceKey, []string{
		"tree_sync", "tree_sync", "非法工具", "daily_sync", "pcd_export",
	})
	if got, want := s.favoriteIDs(), []string{"pcd_export", "daily_sync", "tree_sync"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("存量收藏未去重或过滤：got=%v want=%v", got, want)
	}
}

func TestShellRecentToolIDsDedupAndLimit(t *testing.T) {
	s := newShellPrefsTestShell(t)
	for _, appID := range []string{"pcd_export", "daily_sync", "pcd_export"} {
		s.recordRecentTool(appID)
	}
	if got, want := s.recentToolIDs(), []string{"pcd_export", "daily_sync"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("最近工具未去重置前：got=%v want=%v", got, want)
	}

	before := s.recentToolIDs()
	s.recordRecentTool("非法工具")
	s.recordRecentTool("")
	if got := s.recentToolIDs(); !reflect.DeepEqual(got, before) {
		t.Fatalf("非法工具改变了最近记录：got=%v before=%v", got, before)
	}

	for _, appID := range []string{"course_autoplay", "quiz_search", "tree_sync", "crossing_sync"} {
		s.recordRecentTool(appID)
	}
	want := []string{"crossing_sync", "tree_sync", "quiz_search", "course_autoplay", "pcd_export", "daily_sync"}
	if got := s.recentToolIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("最近工具顺序或上限不符：got=%v want=%v", got, want)
	}

	s.App.Preferences().SetStringList(shellRecentToolsPreferenceKey, []string{
		"daily_sync", "daily_sync", "非法工具", "pcd_export",
	})
	if got, want := s.recentToolIDs(), []string{"daily_sync", "pcd_export"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("存量最近记录未去重或过滤：got=%v want=%v", got, want)
	}
}

func TestShellJobParamsAssociationDeepCopyAndDelete(t *testing.T) {
	s := newShellPrefsTestShell(t)
	dispatchIDs := []any{"派车单甲", "派车单乙"}
	nested := map[string]any{"output_dir": `D:\exports`, "count": 2}
	original := map[string]any{"dispatch_ids": dispatchIDs, "nested": nested}
	s.saveJobParams("任务甲", "pcd_export", original)
	s.saveJobParams("任务乙", "daily_sync", map[string]any{"date": "2026-10-29"})

	dispatchIDs[0] = "已修改"
	nested["output_dir"] = `D:\changed`
	got, ok := s.jobParams("任务甲")
	if !ok {
		t.Fatal("已保存的任务快照未读取到")
	}
	gotNested := got["nested"].(map[string]any)
	if got["dispatch_ids"].([]any)[0] != "派车单甲" || gotNested["output_dir"] != `D:\exports` {
		t.Fatalf("保存时未深拷贝：%v", got)
	}
	got["dispatch_ids"].([]any)[0] = "读取后修改"
	gotNested["output_dir"] = `D:\read_changed`
	again, ok := s.jobParams("任务甲")
	if !ok || again["dispatch_ids"].([]any)[0] != "派车单甲" || again["nested"].(map[string]any)["output_dir"] != `D:\exports` {
		t.Fatalf("读取结果未与存储隔离：%v", again)
	}

	other, ok := s.jobParams("任务乙")
	if !ok || other["date"] != "2026-10-29" {
		t.Fatalf("任务快照关联错误：%v ok=%v", other, ok)
	}
	s.deleteJobParams("任务甲")
	if _, ok := s.jobParams("任务甲"); ok {
		t.Fatal("删除后仍能读取任务快照")
	}
	if _, ok := s.jobParams("任务乙"); !ok {
		t.Fatal("删除一个任务不应影响其他任务")
	}
	if _, ok := s.jobParams("旧记录无快照"); ok {
		t.Fatal("没有快照的旧记录应返回不存在")
	}
}

func TestShellJobParamsSensitiveKeysRejected(t *testing.T) {
	s := newShellPrefsTestShell(t)
	keys := []string{"Password", "PASSWD", "SeCrEt", "ApiToken", "credential", "Authorization", "COOKIE", "SSO"}
	for index, key := range keys {
		jobID := fmt.Sprintf("敏感任务-%d", index)
		params := map[string]any{
			"safe":   "保留项",
			"nested": []any{map[string]any{key: "不应保存", "path": `D:\exports`}},
		}
		s.saveJobParams(jobID, "pcd_export", params)
		if _, ok := s.jobParams(jobID); ok {
			t.Fatalf("含敏感键 %q 的快照不应保存", key)
		}
	}

	s.saveJobParams("重试任务", "daily_sync", map[string]any{"date": "2026-10-29"})
	s.saveJobParams("重试任务", "daily_sync", map[string]any{
		"date":   "2026-10-30",
		"nested": map[string]any{"session_token": "不应保存"},
	})
	got, ok := s.jobParams("重试任务")
	if !ok || got["date"] != "2026-10-29" {
		t.Fatalf("敏感重试不应覆盖完整旧快照：%v ok=%v", got, ok)
	}
}

func TestShellJobParamsAllowlistSizeAndStructure(t *testing.T) {
	s := newShellPrefsTestShell(t)
	for _, appID := range []string{"pcd_export", "daily_sync", "tree_sync", "crossing_sync", "course_autoplay"} {
		s.saveJobParams("允许-"+appID, appID, map[string]any{"path": `D:\exports`})
		if _, ok := s.jobParams("允许-" + appID); !ok {
			t.Fatalf("允许快照的应用未保存：%s", appID)
		}
	}
	for _, appID := range []string{"quiz_search", "非法工具", ""} {
		s.saveJobParams("拒绝-"+appID, appID, map[string]any{"value": 1})
		if _, ok := s.jobParams("拒绝-" + appID); ok {
			t.Fatalf("不允许快照的应用被保存：%q", appID)
		}
	}
	s.saveJobParams("", "pcd_export", map[string]any{"value": 1})
	if _, ok := s.jobParams(""); ok {
		t.Fatal("空任务编号不应有快照")
	}

	s.saveJobParams("超限任务", "pcd_export", map[string]any{"content": strings.Repeat("字", shellJobParamsMaxBytes)})
	if _, ok := s.jobParams("超限任务"); ok {
		t.Fatal("超过 512KiB 的快照不应保存")
	}

	for jobID, raw := range map[string]string{
		"坏结构-json": "{",
		"坏结构-缺参数":  `{"app_id":"pcd_export"}`,
		"坏结构-空参数":  `{"app_id":"pcd_export","params":null}`,
		"坏结构-非法应用": `{"app_id":"quiz_search","params":{}}`,
	} {
		s.App.Preferences().SetString(shellJobParamsPreferenceKey(jobID), raw)
		if _, ok := s.jobParams(jobID); ok {
			t.Fatalf("结构无效的快照不应读取：%s", jobID)
		}
	}
}
