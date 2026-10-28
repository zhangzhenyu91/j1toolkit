package render

import (
	"fmt"
	"testing"

	"github.com/chromedp/cdproto"
)

func TestIsPrintToPDFMissing(t *testing.T) {
	missing := []error{
		&cdproto.Error{Code: -32601, Message: "'Page.printToPDF' wasn't found"},
		fmt.Errorf("运行失败: %w", &cdproto.Error{Code: -32601, Message: "unknown method"}),
		fmt.Errorf("protocol error: PrintToPDF is not implemented"),
	}
	for _, e := range missing {
		if !isPrintToPDFMissing(e) {
			t.Fatalf("应识别为方法缺失: %v", e)
		}
	}
	notMissing := []error{
		nil,
		fmt.Errorf("context deadline exceeded"),
		&cdproto.Error{Code: -32000, Message: "printToPDF 无数据"},
	}
	for _, e := range notMissing {
		if isPrintToPDFMissing(e) {
			t.Fatalf("不应识别为方法缺失: %v", e)
		}
	}
}

func TestIsCreateTargetMissing(t *testing.T) {
	missing := []error{
		&cdproto.Error{Code: -32000, Message: "Not supported"},
		fmt.Errorf("开 tab 失败: %w", &cdproto.Error{Code: -32000, Message: "Not supported"}),
	}
	for _, e := range missing {
		if !isCreateTargetMissing(e) {
			t.Fatalf("应识别为 createTarget 缺失: %v", e)
		}
	}
	notMissing := []error{
		nil,
		fmt.Errorf("context deadline exceeded"),
		&cdproto.Error{Code: -32602, Message: "No target with given id found"},
	}
	for _, e := range notMissing {
		if isCreateTargetMissing(e) {
			t.Fatalf("不应识别为 createTarget 缺失: %v", e)
		}
	}
}
