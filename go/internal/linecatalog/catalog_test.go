package linecatalog

// 线路名归一与目录匹配纯函数测试（不接数据库）。

import "testing"

// Normalize：去电压前缀、统一罗马数字（含 12 → ⅠⅡ），已规范名称幂等。
func TestNormalize(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"峰义12线", "峰义ⅠⅡ线"},
		{"110kV吕汾1线", "吕汾Ⅰ线"},
		{"吕汾II线", "吕汾Ⅱ线"},
		{"峰义ⅠⅡ线", "峰义ⅠⅡ线"},
	}
	for _, c := range cases {
		if got := Normalize(c.raw); got != c.want {
			t.Fatalf("Normalize(%q) = %q，期望 %q", c.raw, got, c.want)
		}
	}
	// 幂等：归一结果再归一不变。
	for _, c := range cases {
		if again := Normalize(c.want); again != c.want {
			t.Fatalf("Normalize 非幂等：%q → %q", c.want, again)
		}
	}
}

// linkedCatalog 关联后的目录：统一线路 峰义ⅠⅡ线 + 两条原线路名别名。
func linkedCatalog() *Catalog {
	return New(
		[]Line{{Voltage: "110kV", Name: "峰义ⅠⅡ线"}},
		[]Alias{
			{Alias: "峰义Ⅰ线", Name: "峰义ⅠⅡ线"},
			{Alias: "峰义Ⅱ线", Name: "峰义ⅠⅡ线"},
		},
	)
}

// Resolve：原线路名、数字写法、统一名均命中同一条统一线路。
func TestResolveLinkAliases(t *testing.T) {
	cat := linkedCatalog()
	for _, raw := range []string{"峰义Ⅰ线", "峰义Ⅱ线", "峰义12线", "峰义ⅠⅡ线"} {
		m := cat.Resolve(raw)
		if m.Status != "ok" || m.Name != "峰义ⅠⅡ线" {
			t.Fatalf("Resolve(%q) = %+v，期望 ok/峰义ⅠⅡ线", raw, m)
		}
		if m.Voltage != "110" {
			t.Fatalf("Resolve(%q) 电压应为 110，got %q", raw, m.Voltage)
		}
	}
}

// Resolve 支持唯一前缀猜测，ResolveExact 不做前缀猜测。
func TestResolvePrefixVsExact(t *testing.T) {
	cat := linkedCatalog()
	if m := cat.ResolveExact("峰义"); m.Status != "unmatched" {
		t.Fatalf("ResolveExact(\"峰义\") 不应唯一前缀命中，got %+v", m)
	}
	m := cat.Resolve("峰义")
	if m.Status != "ok" || m.Name != "峰义ⅠⅡ线" {
		t.Fatalf("Resolve(\"峰义\") 应唯一前缀命中 峰义ⅠⅡ线，got %+v", m)
	}
}

// Resolve：多个候选时返回 ambiguous 且给出候选；ResolveExact 仍为 unmatched。
func TestResolveAmbiguous(t *testing.T) {
	cat := New([]Line{
		{Voltage: "110", Name: "峰义Ⅰ线"},
		{Voltage: "110", Name: "峰义Ⅱ线"},
	}, nil)
	m := cat.Resolve("峰义")
	if m.Status != "ambiguous" || len(m.Candidates) != 2 {
		t.Fatalf("Resolve(\"峰义\") 应为 ambiguous 且 2 个候选，got %+v", m)
	}
	if m := cat.ResolveExact("峰义"); m.Status != "unmatched" {
		t.Fatalf("ResolveExact(\"峰义\") 应为 unmatched，got %+v", m)
	}
	// 精确名称仍可命中。
	if m := cat.Resolve("峰义Ⅱ线"); m.Status != "ok" || m.Name != "峰义Ⅱ线" {
		t.Fatalf("Resolve(\"峰义Ⅱ线\") 应 ok，got %+v", m)
	}
}
