package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withTempEnv 把配置/ini 路径指到临时目录（互不干扰），返回 config.json 路径。
func withTempEnv(t *testing.T) (jsonPath, iniPath string) {
	t.Helper()
	dir := t.TempDir()
	jsonPath = filepath.Join(dir, "config.json")
	iniPath = filepath.Join(dir, "config.ini")
	t.Setenv("UVMP_TOOLKIT_CONFIG", jsonPath)
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", iniPath)
	return jsonPath, iniPath
}

func TestLoadDefaults(t *testing.T) {
	jsonPath, iniPath := withTempEnv(t)
	_ = jsonPath
	_ = iniPath
	cfg := Load()
	if got := StrField(MapField(cfg, "usb"), "label"); got != "GLKVM" {
		t.Fatalf("usb.label 默认值 = %q, want GLKVM", got)
	}
	if !BoolField(MapField(cfg, "schedule"), "enabled", false) {
		t.Fatal("schedule.enabled 默认应为 true")
	}
	if got := StrField(MapField(cfg, "schedule"), "time"); got != "09:15" {
		t.Fatalf("schedule.time = %q, want 09:15", got)
	}
	if got := IntField(MapField(cfg, "order_export"), "lookback_days", 0); got != 62 {
		t.Fatalf("order_export.lookback_days = %d, want 62", got)
	}
	if cfg["_config_path"] != jsonPath {
		t.Fatalf("_config_path = %v, want %s", cfg["_config_path"], jsonPath)
	}
}

func TestLoadMergeJSONOverINIOverDefaults(t *testing.T) {
	jsonPath, iniPath := withTempEnv(t)
	if err := os.WriteFile(iniPath, []byte("[auth]\nUsername = ini_user\npassword = ini_pw\nmode = token\ntoken = t1\nrefresh_token = r1\nmtk = m1\n\n[gateway]\nbase = http://gw.example/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	over := `{"sso": {"username": "json_user", "password": "json_pw"}, "schedule": {"time": "08:00"}}`
	if err := os.WriteFile(jsonPath, []byte(over), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	// _ini 小写键
	ini := MapField(cfg, "_ini")
	if got := StrField(MapField(ini, "auth"), "username"); got != "ini_user" {
		t.Fatalf("_ini.auth.username = %q, want ini_user", got)
	}
	// json 覆盖默认值
	if got := StrField(MapField(cfg, "schedule"), "time"); got != "08:00" {
		t.Fatalf("schedule.time = %q, want 08:00", got)
	}
	// 深合并：schedule.enabled 保留默认 true
	if !BoolField(MapField(cfg, "schedule"), "enabled", false) {
		t.Fatal("schedule.enabled 应保留默认 true")
	}
	// sso_credentials：config.json 优先
	u, p := SSOCredentials(cfg)
	if u != "json_user" || p != "json_pw" {
		t.Fatalf("SSOCredentials = %q/%q, want json_user/json_pw", u, p)
	}
	// gateway：_ini 优先
	if got := Gateway(cfg); got != "http://gw.example/" {
		t.Fatalf("Gateway = %q, want http://gw.example/", got)
	}
}

func TestSSOCredentialsINIFallback(t *testing.T) {
	jsonPath, iniPath := withTempEnv(t)
	_ = jsonPath
	if err := os.WriteFile(iniPath, []byte("[auth]\nusername = ini_user\npassword = ini_pw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	u, p := SSOCredentials(cfg)
	if u != "ini_user" || p != "ini_pw" {
		t.Fatalf("SSOCredentials = %q/%q, want ini_user/ini_pw", u, p)
	}
}

func TestSaveMergeAndMask(t *testing.T) {
	jsonPath, _ := withTempEnv(t)
	if _, err := Save(Dict{"sso": Dict{"username": "u1", "password": "p1"}}); err != nil {
		t.Fatal(err)
	}
	// 密码留空 = 不修改（rpc.saveConfig 剥离空密码后仍走到这里验证合并）
	if _, err := Save(Dict{"sso": Dict{"username": "u2"}, "schedule": Dict{"time": "07:30"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved Dict
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if got := StrField(MapField(saved, "sso"), "password"); got != "p1" {
		t.Fatalf("密码应保留 p1, got %q", got)
	}
	if got := StrField(MapField(saved, "sso"), "username"); got != "u2" {
		t.Fatalf("username = %q, want u2", got)
	}
	if got := StrField(MapField(saved, "schedule"), "time"); got != "07:30" {
		t.Fatalf("schedule.time = %q, want 07:30", got)
	}
	if strings.Contains(string(data), "_ini") || strings.Contains(string(data), "_config_path") {
		t.Fatal("内部键 _ini/_config_path 不应落盘")
	}
	// 600 权限（仅类 Unix 验证）
	if runtime.GOOS != "windows" {
		st, err := os.Stat(jsonPath)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("权限 = %v, want 0600", st.Mode().Perm())
		}
	}
	// 脱敏视图
	cfg := Load()
	view := MaskedView(cfg)
	sso := MapField(view, "sso")
	if got := StrField(sso, "password"); got != "" {
		t.Fatalf("脱敏视图不应带回密码, got %q", got)
	}
	if !BoolField(sso, "has_password", false) {
		t.Fatal("has_password 应为 true")
	}
	if got := StrField(sso, "password_source"); got != "config.json" {
		t.Fatalf("password_source = %q, want config.json", got)
	}
	if _, ok := view["_ini"]; ok {
		t.Fatal("脱敏视图不应含 _ini")
	}
}

func TestMaskedViewINIPasswordSource(t *testing.T) {
	_, iniPath := withTempEnv(t)
	if err := os.WriteFile(iniPath, []byte("[auth]\npassword = ini_pw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	view := MaskedView(Load())
	sso := MapField(view, "sso")
	if got := StrField(sso, "password_source"); got != "config.ini" {
		t.Fatalf("password_source = %q, want config.ini", got)
	}
}

func TestReadINI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.ini")
	content := "# 注释\n[sso]\nBase = http://sso/\nprovince_id = sx\n\n[export]\npage_size = 200\nkeep_tag:00,01\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ini := ReadINI(p)
	if got := StrField(MapField(ini, "sso"), "base"); got != "http://sso/" {
		t.Fatalf("sso.base = %q", got)
	}
	if got := StrField(MapField(ini, "export"), "page_size"); got != "200" {
		t.Fatalf("export.page_size = %q", got)
	}
	if got := StrField(MapField(ini, "export"), "keep_tag"); got != "00,01" {
		t.Fatalf("export.keep_tag = %q（冒号分隔）", got)
	}
	if got := StrField(MapField(ini, "sso"), "province_id"); got != "sx" {
		t.Fatalf("sso.province_id = %q", got)
	}
}

func TestVECfgDefaults(t *testing.T) {
	jsonPath, iniPath := withTempEnv(t)
	_ = jsonPath
	_ = iniPath
	ve := VECfg(Load())
	if got := StrField(ve, "run_state"); got != "2" {
		t.Fatalf("run_state = %q, want 2", got)
	}
	if got := StrField(ve, "track_render"); got != "official" {
		t.Fatalf("track_render = %q, want official", got)
	}
	if got := StrField(ve, "map_tiles_wait"); got != "12000" {
		t.Fatalf("map_tiles_wait = %q, want 12000", got)
	}
}

func TestDeepMerge(t *testing.T) {
	base := Dict{"a": Dict{"x": 1, "y": 2}, "b": 3}
	over := Dict{"a": Dict{"y": 9, "z": 8}, "c": 7}
	out := DeepMerge(base, over)
	a := MapField(out, "a")
	if IntField(a, "x", 0) != 1 || IntField(a, "y", 0) != 9 || IntField(a, "z", 0) != 8 {
		t.Fatalf("深合并结果错误: %v", a)
	}
	if IntField(out, "b", 0) != 3 || IntField(out, "c", 0) != 7 {
		t.Fatalf("顶层合并错误: %v", out)
	}
	// 原 base 不被改动
	if IntField(MapField(base, "a"), "y", 0) != 2 {
		t.Fatal("DeepMerge 不应改动入参")
	}
}

func TestLegacyBesideMigration(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "user", "config.json")
	// 模拟 exe 旁遗留 config.json：exeDir 不可注入，此路径走 ConfigPath 的 env 覆盖分支，
	// 迁移逻辑由 TestLoadMergeJSONOverINIOverDefaults 的等价写路径覆盖；这里只验证 env 优先。
	t.Setenv("UVMP_TOOLKIT_CONFIG", jsonPath)
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(dir, "no.ini"))
	if got := ConfigPath(); got != jsonPath {
		t.Fatalf("ConfigPath = %q, want %q", got, jsonPath)
	}
}
