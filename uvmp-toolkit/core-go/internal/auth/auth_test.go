package auth

import "testing"

// TestParseConfigUsername 用户名两侧先各自 TrimSpace 再 config.json→ini 兜底
// （config.sso_credentials 口径：json 全空白时回落 ini）；密码不裁剪、不兜底裁剪。
func TestParseConfigUsername(t *testing.T) {
	load := func(jsonUser, iniUser, jsonPw, iniPw string) map[string]any {
		return map[string]any{
			"sso": map[string]any{"username": jsonUser, "password": jsonPw},
			"_ini": map[string]any{
				"auth": map[string]any{"username": iniUser, "password": iniPw},
			},
		}
	}
	cases := []struct {
		jsonUser, iniUser string
		want              string
	}{
		{"alice", "bob", "alice"},    // json 优先
		{" alice  ", "bob", "alice"}, // 各自 TrimSpace
		{"   ", "bob", "bob"},        // json 全空白 → 回落 ini
		{"", " bob ", "bob"},         // json 缺失 → 回落 ini 并裁剪
		{"alice", "", "alice"},       // ini 缺失不影响 json
	}
	for _, tc := range cases {
		cfg, err := ParseConfig(load(tc.jsonUser, tc.iniUser, "pw-json", "pw-ini"))
		if err != nil {
			t.Fatalf("ParseConfig(%q,%q) err = %v", tc.jsonUser, tc.iniUser, err)
		}
		if cfg.Username != tc.want {
			t.Fatalf("Username = %q, want %q（json=%q ini=%q）", cfg.Username, tc.want, tc.jsonUser, tc.iniUser)
		}
		if cfg.Password != "pw-json" {
			t.Fatalf("Password = %q, want pw-json", cfg.Password)
		}
	}
	// 密码不裁剪（Python 口径不 strip）
	cfg, err := ParseConfig(load("alice", "", " pw ", "pw-ini"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != " pw " {
		t.Fatalf("Password = %q, want 保留空格", cfg.Password)
	}
	// 两侧皆空白 → 报认证错误
	if _, err := ParseConfig(load("  ", "  ", "pw", "pw")); !IsAuthError(err) {
		t.Fatalf("err = %v, want AuthError", err)
	}
	// 空载荷（无 sso 段）→ 报认证错误
	if _, err := ParseConfig(map[string]any{}); !IsAuthError(err) {
		t.Fatalf("err = %v, want AuthError", err)
	}
}
