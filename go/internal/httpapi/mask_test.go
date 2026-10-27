package httpapi

import "testing"

// TestMaskValue 脱敏口径：与 config.MaskSecret 同规则（首尾各 2 字符 + ****，短值全掩）。
func TestMaskValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"a", "****"},
		{"ab12", "****"},
		{"ab123", "ab****23"},
		{"s3cr3t-token-abcdef", "s3****ef"},
		{"超长中文密钥值尾", "超长****值尾"},
	}
	for _, c := range cases {
		if got := MaskValue(c.in); got != c.want {
			t.Errorf("MaskValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestMaskItems 敏感项脱敏、普通项原样，且不修改入参。
func TestMaskItems(t *testing.T) {
	in := []ConfigItem{
		{Key: "app.secret", Value: "s3cr3t-token-abcdef", Sensitive: true},
		{Key: "app.base_url", Value: "http://127.0.0.1:3000", Sensitive: false},
	}
	out := maskItems(in)
	if out[0].Value != "s3****ef" {
		t.Fatalf("敏感项未脱敏: %q", out[0].Value)
	}
	if out[1].Value != "http://127.0.0.1:3000" {
		t.Fatalf("普通项被改动: %q", out[1].Value)
	}
	if in[0].Value != "s3cr3t-token-abcdef" {
		t.Fatalf("入参被修改: %q", in[0].Value)
	}
}
