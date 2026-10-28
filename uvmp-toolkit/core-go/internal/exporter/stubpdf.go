package exporter

import (
	"fmt"
	"os"
	"strconv"
)

// writeStubPDF mock 联调占位 PDF：手写最小合法单页 PDF（不依赖任何渲染器，
// backend._write_stub_pdf 逐字移植）。只用于 dev.mock 下验证命名/顺序/断点续传
// 管道；生产永远走官方渲染器。text 中非 ASCII 字符替换为 '?'（Python
// encode("ascii","replace") 口径）。
func writeStubPDF(path, text string) error {
	stream := []byte("BT /F1 14 Tf 60 780 Td (" + asciiReplace(text) + ") Tj ET")
	objs := [][]byte{
		[]byte("<</Type/Catalog/Pages 2 0 R>>"),
		[]byte("<</Type/Pages/Kids[3 0 R]/Count 1>>"),
		[]byte("<</Type/Page/Parent 2 0 R/MediaBox[0 0 595 842]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>"),
		append(append([]byte(fmt.Sprintf("<</Length %d>>stream\n", len(stream))), stream...), []byte("\nendstream")...),
		[]byte("<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>"),
	}
	out := []byte("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = len(out)
		out = append(out, []byte(strconv.Itoa(i+1)+" 0 obj\n")...)
		out = append(out, body...)
		out = append(out, []byte("\nendobj\n")...)
	}
	xrefPos := len(out)
	out = append(out, []byte(fmt.Sprintf("xref\n0 %d\n", len(objs)+1))...)
	out = append(out, []byte("0000000000 65535 f \n")...)
	for _, off := range offsets {
		out = append(out, []byte(fmt.Sprintf("%010d 00000 n \n", off))...)
	}
	out = append(out, []byte(fmt.Sprintf("trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xrefPos))...)
	return os.WriteFile(path, out, 0o644)
}

// asciiReplace 非 ASCII 字符替换为 '?'（Python bytes.encode("ascii", "replace")）。
func asciiReplace(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 128 {
			b = append(b, byte(r))
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}
