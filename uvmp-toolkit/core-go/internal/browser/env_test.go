package browser

import "testing"

// 显式浏览器选择与宿主探测环境变量口径（自动检测本身见 detect_test.go）。

// TestExplicitChrome 显式 render.chrome：config.json 优先于 config.ini。
func TestExplicitChrome(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"空配置", map[string]any{}, ""},
		{"json 手选", map[string]any{"render": map[string]any{"chrome": "C:\\a.exe"}}, "C:\\a.exe"},
		{"ini 回退", map[string]any{"_ini": map[string]any{"render": map[string]any{"chrome": "/opt/b"}}}, "/opt/b"},
		{
			"json 优先于 ini",
			map[string]any{
				"render": map[string]any{"chrome": "json.exe"},
				"_ini":   map[string]any{"render": map[string]any{"chrome": "ini.exe"}},
			},
			"json.exe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExplicitChrome(c.cfg); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
