package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactorApply(t *testing.T) {
	r := &redactor{}
	r.add("tok-SECRET-1")
	r.add("") // 空串忽略
	out := r.apply("Auth_Tk=tok-SECRET-1&x=tok-SECRET-1")
	if strings.Contains(out, "tok-SECRET-1") {
		t.Fatalf("脱敏失败: %s", out)
	}
	if strings.Count(out, redactMask) != 2 {
		t.Fatalf("应替换 2 处: %s", out)
	}
}

func TestRedactorDuplicateAndNoSecrets(t *testing.T) {
	r := &redactor{}
	r.add("s1")
	r.add("s1")
	if len(r.secrets) != 1 {
		t.Fatalf("重复登记应去重: %v", r.secrets)
	}
	if got := r.apply("plain"); got != "plain" {
		t.Fatalf("无机密命中应原样返回: %s", got)
	}
}

func TestRedactingWriterLineBuffer(t *testing.T) {
	r := &redactor{}
	r.add("tok-XYZ")
	var buf bytes.Buffer
	w := newRedactingWriter(r, &buf)

	// 半行不落盘
	if _, err := w.Write([]byte("hello tok-XYZ")); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("半行不应输出: %q", buf.String())
	}
	// 整行落盘并脱敏
	if _, err := w.Write([]byte(" tail\nnext tok-XYZ line\n")); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "tok-XYZ") {
		t.Fatalf("日志泄漏令牌: %q", got)
	}
	if !strings.Contains(got, "hello *** tail\n") || !strings.Contains(got, "next *** line\n") {
		t.Fatalf("行内容不对: %q", got)
	}
	// Flush 输出残留
	if _, err := w.Write([]byte("rest tok-XYZ")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "tok-XYZ") {
		t.Fatalf("Flush 后泄漏令牌: %q", buf.String())
	}
}
