package render

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildPNGWrapHTML(t *testing.T) {
	html := BuildPNGWrapHTML("data:image/png;base64,AAAA")
	if strings.Contains(html, pngURIPlaceholder) {
		t.Fatal("占位符未替换")
	}
	if !strings.Contains(html, `<img src="data:image/png;base64,AAAA">`) {
		t.Fatalf("图片地址未填入: %s", html)
	}
	// 保留 A4 横向与包版样式
	for _, want := range []string{"A4 landscape", "272mm", "-webkit-print-color-adjust"} {
		if !strings.Contains(html, want) {
			t.Fatalf("包版模板缺少 %q", want)
		}
	}
}

func TestPNGDataURI(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47}
	uri := PNGDataURI(png)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if uri != want {
		t.Fatalf("data URI 不对: %s", uri)
	}
}

func TestBuildPrintFullHTML(t *testing.T) {
	html := buildPrintFullHTML("<style>.a{}</style>", "<div class='a'>x</div>")
	if !strings.HasPrefix(html, "<!DOCTYPE html><html><head><meta charset='utf-8'>") {
		t.Fatalf("头不对: %s", html)
	}
	if !strings.Contains(html, "<style>.a{}</style></head><body><div class='a'>x</div></body></html>") {
		t.Fatalf("结构不对: %s", html)
	}
}
