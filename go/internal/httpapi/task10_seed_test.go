package httpapi

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// 空库必须补齐既有五件套之外的五个业务应用（网盘/出工/树障/交跨/班组数据），
// 否则初始管理员在空库首启后没有这些应用的入口与权限。
func TestSeedAppExtrasCoversCoreBusinessApps(t *testing.T) {
	db, f := newFakeDB(t)
	if err := SeedAppExtras(context.Background(), db); err != nil {
		t.Fatalf("SeedAppExtras: %v", err)
	}
	apps := callsWith(f, seedAppsSQL)
	if len(apps) != 1 {
		t.Fatalf("基础应用种子执行 %d 次", len(apps))
	}
	for _, want := range []string{
		"('netdisk', '团队网盘', 'folder-open', '/pkg-netdisk/pages/index/index', 'both', 2, 1)",
		"('work-log', '出工日志', 'calendar', '/pkg-worklog/pages/index/index', 'both', 3, 1)",
		"('tree-hazard', '树障隐患', 'forest', '/pkg-treehazard/pages/index/index', 'both', 4, 1)",
		"('crossing', '交跨记录', 'fork', '/pkg-crossing/pages/index/index', 'both', 5, 1)",
		"('team-data', '班组数据', 'chart-bar', '/pkg-teamdata/pages/index/index', 'both', 11, 1)",
	} {
		if !strings.Contains(apps[0].query, want) {
			t.Fatalf("基础应用种子缺少 %s", want)
		}
	}
}

// 空库初始管理员必须获得五个业务应用的默认授权。
func TestBootstrapAccountsGrantsCoreBusinessApps(t *testing.T) {
	db, f := newFakeDB(t)
	f.onRows("SELECT COUNT(*) FROM sys_team", []string{"cnt"}, []driver.Value{int64(2)})
	f.onRows("SELECT id FROM sys_user WHERE username = ?", []string{"id"}, []driver.Value{int64(9)})
	if err := BootstrapAccounts(context.Background(), db, "boss", "secret123"); err != nil {
		t.Fatalf("BootstrapAccounts: %v", err)
	}
	grant := callsWith(f, "INSERT IGNORE INTO sys_user_app (user_id, app_id)")
	got := map[string]bool{}
	for _, c := range grant {
		if key, ok := c.args[1].(string); ok {
			got[key] = true
		}
	}
	for _, key := range []string{"call-me", "safe-day", "kvm", "file-transfer", "wm-add", "netdisk", "work-log", "tree-hazard", "crossing", "team-data"} {
		if !got[key] {
			t.Fatalf("管理员默认授权缺少 %s（实际 %v）", key, got)
		}
	}
}
