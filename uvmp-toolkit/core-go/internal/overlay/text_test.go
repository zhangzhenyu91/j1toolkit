// 文本绘制测试：折行/省略/宽度自洽（不断言具体字体度量，CJK 与 ASCII 均须成立）。
package overlay

import "testing"

func TestTextWidthPositive(t *testing.T) {
	if TextWidth("安全生产", 12) <= 0 {
		t.Error("width should be positive")
	}
	if TextWidth("", 12) != 0 {
		t.Error("empty string width should be 0")
	}
}

func TestEllipsize(t *testing.T) {
	long := "我国安全生产工作的方针是安全第一预防为主综合治理"
	short := "安全"
	if got := Ellipsize(short, 12, 500); got != short {
		t.Errorf("short = %q", got)
	}
	got := Ellipsize(long, 12, 60)
	if got == long {
		t.Fatal("long text should be truncated")
	}
	if TextWidth(got, 12) > 60 {
		t.Errorf("ellipsized width %d > 60", TextWidth(got, 12))
	}
	rs := []rune(got)
	if rs[len(rs)-1] != '…' {
		t.Errorf("should end with …, got %q", got)
	}
}

func TestWrapText(t *testing.T) {
	lines := WrapText("下列属于个人防护用品的有安全帽防护手套普通眼镜防护鞋", 12, 80)
	if len(lines) < 2 {
		t.Fatalf("want multiple lines, got %v", lines)
	}
	for i, ln := range lines {
		if TextWidth(ln, 12) > 80 {
			t.Errorf("line %d width %d > 80: %q", i, TextWidth(ln, 12), ln)
		}
	}
	// ASCII 词不拆开
	lines = WrapText("hello world ABCDEFGH 甲乙丙丁戊己庚辛壬癸子丑寅卯", 12, 100)
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if TextWidth(ln, 12) > 100 {
			t.Errorf("line too wide: %q", ln)
		}
	}
	// 空文本至少返回一行
	if lines := WrapText("", 12, 50); len(lines) != 1 {
		t.Errorf("empty wrap = %v", lines)
	}
}
