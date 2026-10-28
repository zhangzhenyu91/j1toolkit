package render

import (
	"testing"
	"time"
)

func TestTagOr(t *testing.T) {
	if got := tagOr("p3", "openfail"); got != "p3" {
		t.Fatalf("tag 非空应原样: %q", got)
	}
	if got := tagOr("", "openfail"); got != "openfail" {
		t.Fatalf("tag 空应兜底: %q", got)
	}
}

func TestBlankTag(t *testing.T) {
	if got := blankTag(`/tmp/out/01_轨迹_单号_车辆_2026-10-28.png`); got != "01_轨迹_单号_车辆_2026-10-28" {
		t.Fatalf("主名提取不对: %q", got)
	}
	long := blankTag("0123456789012345678901234567890.png")
	if len([]rune(long)) != 24 {
		t.Fatalf("应截到 24 字符: %q", long)
	}
}

func TestStylesWait(t *testing.T) {
	if got := stylesWait(0); got != time.Second {
		t.Fatalf("0 应给 1s 下限: %v", got)
	}
	if got := stylesWait(500 * time.Millisecond); got != time.Second {
		t.Fatalf("不足 1s 应给 1s 下限: %v", got)
	}
	if got := stylesWait(6 * time.Second); got != 6*time.Second {
		t.Fatalf("应原样返回: %v", got)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(t.Context(), 0); err != nil {
		t.Fatalf("零时长应立即返回: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- sleepCtx(t.Context(), 10*time.Millisecond) }()
	if err := <-done; err != nil {
		t.Fatalf("正常等待不应报错: %v", err)
	}
}
