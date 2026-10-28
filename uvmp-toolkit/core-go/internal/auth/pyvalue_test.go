package auth

import (
	"math"
	"testing"
)

// TestPyFloatStrGolden Python str(float) 黄金对照（CPython 3 离线实测生成）：
// 最短往返十进制、定点补 ".0"、科学计数 1e+16/1e-05/2.5e-07 口径。
func TestPyFloatStrGolden(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.0, "1.0"},
		{1.5, "1.5"},
		{-2.5, "-2.5"},
		{0.1, "0.1"},
		{0.0001, "0.0001"},
		{1e-5, "1e-05"},
		{1e15, "1000000000000000.0"},
		{1e16, "1e+16"},
		{1234567890123456.0, "1234567890123456.0"},
		{3.14159265358979, "3.14159265358979"},
		{2.5e-7, "2.5e-07"},
		{math.Copysign(0, -1), "-0.0"},
		{0.0, "0.0"},
		{100.0, "100.0"},
		{1e20, "1e+20"},
		{1.0000000000000002, "1.0000000000000002"},
		{20.0, "20.0"},
	}
	for _, tc := range cases {
		if got := pyFloatStr(tc.in); got != tc.want {
			t.Fatalf("pyFloatStr(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPyStrGolden Python str(v) 黄金对照（CPython 3 离线实测生成）：
// 标量短表示、bool True/False、容器 repr 口径（元素 repr、", " 分隔）。
func TestPyStrGolden(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "None"},
		{true, "True"},
		{false, "False"},
		{20, "20"},
		{1.0, "1.0"},
		{20.0, "20.0"},
		{"", ""},
		{"x", "x"},
		{"a'b", "a'b"},
		{"中文", "中文"},
		{[]any{}, "[]"},
		{map[string]any{}, "{}"},
		{[]any{1.0, "a", nil, true}, "[1.0, 'a', None, True]"},
		{map[string]any{"a": 1, "b": "x"}, "{'a': 1, 'b': 'x'}"},
		{[]any{[]any{}, map[string]any{}}, "[[], {}]"},
		// repr 引号口径：含 ' 不含 " 用双引号，否则单引号；控制字符短转义
		{[]any{`a'b`}, `["a'b"]`},
		{[]any{`say "hi"`}, `['say "hi"']`},
		{[]any{"both '\""}, `['both \'"']`},
		{[]any{"line\nbreak"}, `['line\nbreak']`},
		{[]any{"\x00\x01tab\there"}, `['\x00\x01tab\there']`},
		{[]any{`back\slash`}, `['back\\slash']`},
		{[]any{"树障"}, `['树障']`},
	}
	for _, tc := range cases {
		if got := PyStr(tc.in); got != tc.want {
			t.Fatalf("PyStr(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPyTruthyGolden Python bool(v) 黄金对照（CPython 3 离线实测生成）：
// None/False/0/0.0/""/空容器为假；非空字符串恒为真（含 "false"）。
func TestPyTruthyGolden(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{nil, false},
		{false, false},
		{true, true},
		{0, false},
		{1, true},
		{0.0, false},
		{1.0, true},
		{"", false},
		{"x", true},
		{"false", true}, // Python：非空字符串恒为真
		{[]any{}, false},
		{map[string]any{}, false},
		{[]any{0}, true},
		{map[string]any{"k": 1}, true},
		{math.NaN(), true}, // Python bool(nan) == True
	}
	for _, tc := range cases {
		if got := PyTruthy(tc.in); got != tc.want {
			t.Fatalf("PyTruthy(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
