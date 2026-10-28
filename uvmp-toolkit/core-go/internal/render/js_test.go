package render

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildInjectAuthJSEscapes(t *testing.T) {
	js, err := buildInjectAuthJS(`tok"en`, "refresh", map[string]any{"userName": "张三"})
	if err != nil {
		t.Fatal(err)
	}
	// 参数必须是合法 JSON 字面量（引号/尖括号等不能破坏脚本）
	if !strings.Contains(js, `"tok\"en"`) && !strings.Contains(js, `"tok\u0022en"`) {
		// encoding/json 对引号转义为 \"
		if !strings.Contains(js, `tok\"en`) {
			t.Fatalf("token 未正确转义: %s", js)
		}
	}
	if strings.Count(js, ")(\"") == 0 && strings.Count(js, ")(") == 0 {
		t.Fatalf("注入脚本结构不对: %s", js)
	}
	if !strings.Contains(js, "cube.token = token") {
		t.Fatalf("缺少赋值: %s", js)
	}
}

func TestBuildInjectAuthJSNilLoginUser(t *testing.T) {
	js, err := buildInjectAuthJS("t", "r", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js, "({})") && !strings.Contains(js, "({})\n") {
		// json.Marshal(nil map) 给 "null"；这里要求给 {}
		if strings.Contains(js, ", null)") {
			t.Fatalf("loginUser 为 nil 时应注入空对象: %s", js)
		}
	}
}

func TestBuildOpenDialogJS(t *testing.T) {
	js, err := buildOpenDialogJS(TemplateRoute, map[string]any{"routeId": "R1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js, `"factoryLayout.monitor.realMonitor.track_picture"`) {
		t.Fatalf("模板名未注入: %s", js)
	}
	if !strings.Contains(js, `{"routeId":"R1"}`) {
		t.Fatalf("参数未注入: %s", js)
	}
	if !strings.Contains(js, `cube.showDialog`) {
		t.Fatalf("缺少 showDialog: %s", js)
	}
}

func TestBuildOpenPCDPrintJS(t *testing.T) {
	js, err := buildOpenPCDPrintJS([]map[string]any{{"runCode": "RC1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js, TemplatePCDPrint) {
		t.Fatalf("打印模板名未注入: %s", js)
	}
	if !strings.Contains(js, `[{"runCode":"RC1"}]`) {
		t.Fatalf("items 未注入: %s", js)
	}
}

func TestBuildDocumentWriteJS(t *testing.T) {
	js, err := buildDocumentWriteJS(`<html>中文"x</html>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(js, "document.write(") || !strings.HasSuffix(js, "); document.close(); 'ok'") {
		t.Fatalf("结构不对: %s", js)
	}
	// 参数须为合法 JSON 字符串
	start := len("document.write(")
	end := strings.Index(js, "); document.close()")
	var s string
	if err := json.Unmarshal([]byte(js[start:end]), &s); err != nil {
		t.Fatalf("HTML 参数不是合法 JSON 字符串: %v", err)
	}
	if s != `<html>中文"x</html>` {
		t.Fatalf("往返不一致: %q", s)
	}
}

func TestTrimForDiag(t *testing.T) {
	if got := trimForDiag("abcdef", 3); got != "abc" {
		t.Fatalf("截断不对: %q", got)
	}
	if got := trimForDiag("中文测试", 2); got != "中文" {
		t.Fatalf("中文截断不对: %q", got)
	}
	if got := trimForDiag("abc", 10); got != "abc" {
		t.Fatalf("短串应原样: %q", got)
	}
}
