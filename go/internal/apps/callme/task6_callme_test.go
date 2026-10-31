package callme

// Task 6 边界回归：群内提问图片/附件 base64 必须在解码前按编码长度限总量。

import (
	"strings"
	"testing"
)

// TestCheckImageDataUrlEncodedLimit 编码长度超上限直接判 size，不进入解码。
func TestCheckImageDataUrlEncodedLimit(t *testing.T) {
	payload := strings.Repeat("A", 12) // 解码 9 字节
	url := "data:image/png;base64," + payload
	if got := checkImageDataUrlLimit(url, 8); got != "size" {
		t.Fatalf("编码长度超限应 size，实际 %q", got)
	}
	if got := checkImageDataUrlLimit(url, 9); got != "" {
		t.Fatalf("恰好 9 字节应通过，实际 %q", got)
	}
	if got := checkImageDataUrlLimit("data:text/plain;base64,"+payload, 9); got != "format" {
		t.Fatalf("非图片格式应 format，实际 %q", got)
	}
}

// TestBase64EncodedTooLarge 编码长度预算公式覆盖恰好上限与超一位。
func TestBase64EncodedTooLarge(t *testing.T) {
	if base64EncodedTooLarge(strings.Repeat("A", 12), 8) {
		t.Fatal("8 字节上限的编码长度 12 不应判超限")
	}
	if !base64EncodedTooLarge(strings.Repeat("A", 16), 8) {
		t.Fatal("编码长度 16 应判超限")
	}
}
