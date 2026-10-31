package config

import "testing"

// TestDefaultSpecsSGCCConcurrency 商旅并发键进默认注册表（后台不再按敏感项打码），
// 旧固定间隔键已不再导入/读取。
func TestDefaultSpecsSGCCConcurrency(t *testing.T) {
	want := map[string]string{
		"SGCC_SYNC_CONCURRENCY":  "4",
		"SGCC_PHOTO_CONCURRENCY": "2",
	}
	seen := map[string]Spec{}
	for _, spec := range DefaultSpecs() {
		seen[spec.Key] = spec
	}
	for key, def := range want {
		spec, ok := seen[key]
		if !ok {
			t.Fatalf("DefaultSpecs 缺少 %s", key)
		}
		if spec.Default != def {
			t.Fatalf("%s 默认值应为 %q，实际 %q", key, def, spec.Default)
		}
		if spec.Sensitive {
			t.Fatalf("%s 不应为敏感项（否则后台会打码）", key)
		}
	}
	if _, ok := seen["SGCC_SYNC_INTERVAL_MS"]; ok {
		t.Fatalf("旧 SGCC_SYNC_INTERVAL_MS 不应再出现在默认注册表")
	}
}

// TestDefaultSpecsOOSaveTempDir OO_SAVE_TEMP_DIR 进运行配置（团队网盘分组），
// 空值表示由 office 模块回退默认持久 staging 目录。
func TestDefaultSpecsOOSaveTempDir(t *testing.T) {
	var spec Spec
	found := false
	for _, s := range DefaultSpecs() {
		if s.Key == "OO_SAVE_TEMP_DIR" {
			spec, found = s, true
			break
		}
	}
	if !found {
		t.Fatal("DefaultSpecs 缺少 OO_SAVE_TEMP_DIR")
	}
	if spec.Env != "OO_SAVE_TEMP_DIR" || spec.Default != "" || spec.Sensitive {
		t.Fatalf("OO_SAVE_TEMP_DIR 规格不符: %+v", spec)
	}
	if got := GroupForKey("OO_SAVE_TEMP_DIR"); got != "团队网盘" {
		t.Fatalf("OO_SAVE_TEMP_DIR 分组 = %q，期望 团队网盘", got)
	}
}

// TestDefaultSpecsAppLogRetention 运行配置只含保留天数（默认 0 永久），
// 启动期 APP_LOG_DIR / APP_LOG_BATCH_SIZE / APP_LOG_FSYNC 不得进配置中心。
func TestDefaultSpecsAppLogRetention(t *testing.T) {
	seen := map[string]Spec{}
	for _, spec := range DefaultSpecs() {
		seen[spec.Key] = spec
	}
	for _, key := range []string{"APP_LOG_RETENTION_DAYS", "APP_LOG_ARCHIVE_RETENTION_DAYS"} {
		spec, ok := seen[key]
		if !ok {
			t.Fatalf("DefaultSpecs 缺少 %s", key)
		}
		if spec.Default != "0" {
			t.Fatalf("%s 默认值应为 \"0\"（永久），实际 %q", key, spec.Default)
		}
		if spec.Sensitive {
			t.Fatalf("%s 不应为敏感项", key)
		}
		if GroupForKey(key) != "应用日志" {
			t.Fatalf("%s 分组 = %q", key, GroupForKey(key))
		}
	}
	for _, key := range []string{"APP_LOG_DIR", "APP_LOG_BATCH_SIZE", "APP_LOG_FSYNC"} {
		if _, ok := seen[key]; ok {
			t.Fatalf("启动期 %s 不应进入运行配置注册表", key)
		}
	}
}
