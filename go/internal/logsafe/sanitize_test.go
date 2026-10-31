package logsafe

import (
	"net/url"
	"strings"
	"testing"
)

// TestURLForLogPreservesStructureAndMasksSecrets 保留 scheme/host/完整 path/查询参数名，
// 仅掩码 userinfo 与敏感参数值。
func TestURLForLogPreservesStructureAndMasksSecrets(t *testing.T) {
	raw := "https://bob:p%40ss@example.com:8443/a/b%20c/d?fileInfoId=abc123&Signature=SECRET&X-Amz-Credential=CRED&page=2#frag"
	got, err := URLForLog(raw)
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	if !strings.HasPrefix(got, "https://****:****@example.com:8443/a/b%20c/d?") {
		t.Fatalf("scheme/host/path/userinfo 不符：%s", got)
	}
	for _, want := range []string{"fileInfoId=abc123", "page=2", "Signature=****", "X-Amz-Credential=****"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	for _, secret := range []string{"SECRET", "CRED", "bob", "p%40ss"} {
		if strings.Contains(got, secret) {
			t.Fatalf("泄露 %q：%s", secret, got)
		}
	}
	if !strings.Contains(got, "#****") || strings.Contains(got, "#frag") {
		t.Fatalf("fragment 应整体掩码：%s", got)
	}
}

// TestURLForLogMasksFragmentAndSemicolon fragment 必须掩码，query 同时按 ; 分隔。
func TestURLForLogMasksFragmentAndSemicolon(t *testing.T) {
	got, err := URLForLog("https://example.com/p?token=abc;q=1&x=2#access_token=SECRET")
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	for _, want := range []string{"token=****;q=1", "x=2", "#****"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	for _, secret := range []string{"abc", "SECRET"} {
		if strings.Contains(got, secret) {
			t.Fatalf("泄露 %q：%s", secret, got)
		}
	}
}

// TestURLForLogMasksAdditionalSensitiveKeys authorization/session_id/cookie/x-api-key/pass 等。
func TestURLForLogMasksAdditionalSensitiveKeys(t *testing.T) {
	raw := "https://example.com/p?authorization=a1&session_id=s1&cookie=c1&x-api-key=k1&pass=p1&X-Auth-Token=t1&normal=ok"
	got, err := URLForLog(raw)
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	for _, want := range []string{
		"authorization=****", "session_id=****", "cookie=****", "x-api-key=****",
		"pass=****", "X-Auth-Token=****", "normal=ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	for _, secret := range []string{"=a1", "=s1", "=c1", "=k1", "=p1", "=t1"} {
		if strings.Contains(got, secret) {
			t.Fatalf("敏感值 %q 未掩码：%s", secret, got)
		}
	}
}

// TestURLForLogMasksSensitiveQueryValues 敏感查询参数值统一 ****，非敏感值保持原样。
func TestURLForLogMasksSensitiveQueryValues(t *testing.T) {
	raw := "http://example.com/p?token=abc&ACCESS_TOKEN=xyz&Code=qq&api_key=k&password=p&ticket=t&sig=s&normal=1&monkey=2&flag"
	got, err := URLForLog(raw)
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	for _, want := range []string{
		"token=****", "ACCESS_TOKEN=****", "Code=****", "api_key=****",
		"password=****", "ticket=****", "sig=****", "normal=1", "monkey=2", "flag",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	for _, secret := range []string{"abc", "xyz", "=qq", "=k", "=p", "=t", "=s"} {
		if strings.Contains(got, secret) {
			t.Fatalf("敏感值 %q 未掩码：%s", secret, got)
		}
	}
}

// TestURLForLogMasksArraySensitiveKey 数组形式敏感键（token[]）同样掩码。
func TestURLForLogMasksArraySensitiveKey(t *testing.T) {
	got, err := URLForLog("https://example.com/p?token[]=abc&q=1")
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	if !strings.Contains(got, "token[]=****") || strings.Contains(got, "abc") {
		t.Fatalf("数组敏感键未掩码：%s", got)
	}
}

// TestWXLoginCodeMasked 一次性微信登录凭证 js_code 视同 code：URL query、自由文本、
// JSON 片段与结构化字段均不得保留明文。
func TestWXLoginCodeMasked(t *testing.T) {
	raw := "https://api.weixin.qq.com/sns/jscode2session?appid=wx-app&js_code=wx-code-1&secret=s1&grant_type=authorization_code"
	got, err := URLForLog(raw)
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	for _, want := range []string{"js_code=****", "secret=****", "appid=wx-app"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	if strings.Contains(got, "wx-code-1") {
		t.Fatalf("js_code 明文未掩码：%s", got)
	}
	if !SensitiveKey("js_code") {
		t.Fatal("js_code 应为敏感键")
	}
	if out := QueryForLog(url.Values{"js_code": {"wx-code-1"}}); out["js_code"] != Mask {
		t.Fatalf("QueryForLog js_code = %q，期望 %q", out["js_code"], Mask)
	}
	text := TextForLog(`Get "` + raw + `": dial failed`)
	if strings.Contains(text, "wx-code-1") || !strings.Contains(text, "js_code=****") {
		t.Fatalf("自由文本 js_code 未脱敏：%s", text)
	}
	jsonText := TextForLog(`{"js_code":"wx-code-1","ok":1}`)
	if strings.Contains(jsonText, "wx-code-1") || !strings.Contains(jsonText, `"js_code":"****"`) {
		t.Fatalf("JSON js_code 未脱敏：%s", jsonText)
	}
	fields := FieldsForLog(map[string]any{"js_code": "wx-code-1"})
	if fields["js_code"] != Mask {
		t.Fatalf("FieldsForLog js_code = %v，期望 %q", fields["js_code"], Mask)
	}
}

// TestURLForLogRelativeAndNoQuery 相对路径与无查询参数保持可用。
func TestURLForLogRelativeAndNoQuery(t *testing.T) {
	got, err := URLForLog("/api/x?signature=s&q=1")
	if err != nil {
		t.Fatalf("URLForLog(relative): %v", err)
	}
	if got != "/api/x?signature=****&q=1" {
		t.Fatalf("相对路径结果 = %q", got)
	}
	got, err = URLForLog("https://example.com/a/b")
	if err != nil {
		t.Fatalf("URLForLog(no query): %v", err)
	}
	if got != "https://example.com/a/b" {
		t.Fatalf("无查询参数被改写：%q", got)
	}
}

// TestURLForLogUsernameOnly 仅用户名也须掩码。
func TestURLForLogUsernameOnly(t *testing.T) {
	got, err := URLForLog("https://bob@example.com/x")
	if err != nil {
		t.Fatalf("URLForLog: %v", err)
	}
	if !strings.HasPrefix(got, "https://****@example.com/x") || strings.Contains(got, "bob") {
		t.Fatalf("仅用户名未掩码：%s", got)
	}
}

// TestURLForLogInvalid 非法 URL 返回错误且不产生输出。
func TestURLForLogInvalid(t *testing.T) {
	for _, raw := range []string{"", "   ", "http://[::1", "http://exa mple.com/\x00"} {
		got, err := URLForLog(raw)
		if err == nil {
			t.Fatalf("%q 应返回错误，得到 %q", raw, got)
		}
		if got != "" {
			t.Fatalf("%q 出错时输出应为空，得到 %q", raw, got)
		}
	}
}

// TestQueryForLog query 脱敏：敏感键（含 code/ticket/signature）掩码，业务键 fileInfoId 保留。
func TestQueryForLog(t *testing.T) {
	q := url.Values{
		"token":      {"t1"},
		"Signature":  {"SECRET"},
		"code":       {"c1"},
		"ticket":     {"tk1"},
		"fileInfoId": {"abc123"},
		"page":       {"2"},
		"monkey":     {"m"},
	}
	out := QueryForLog(q)
	for _, key := range []string{"token", "Signature", "code", "ticket"} {
		if out[key] != Mask {
			t.Fatalf("%s = %q，期望 %q：%v", key, out[key], Mask, out)
		}
	}
	if out["fileInfoId"] != "abc123" || out["page"] != "2" || out["monkey"] != "m" {
		t.Fatalf("业务参数被误伤：%v", out)
	}
	if QueryForLog(nil) != nil {
		t.Fatal("空 query 应返回 nil")
	}
}

// TestTextForLog 自由文本脱敏：name=value、Bearer、JSON 密钥字段。
func TestTextForLog(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Authorization: Bearer abc.def", "Authorization: Bearer ****"},
		{"failed https://x/y?token=abc&page=2", "failed https://x/y?token=****&page=2"},
		{`{"password":"p@ss","ok":1}`, `{"password":"****","ok":1}`},
		{"key=abc&key_name=keep&fileInfoId=keep2", "key=****&key_name=keep&fileInfoId=keep2"},
		{"no secrets here", "no secrets here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := TextForLog(c.in); got != c.want {
			t.Fatalf("TextForLog(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestFieldsForLog 结构化字段脱敏：精确敏感键整值掩码，路径/键名类 key 不误伤，嵌套递归。
func TestFieldsForLog(t *testing.T) {
	out := FieldsForLog(map[string]any{
		"key":        "sekrit",
		"token":      "tok",
		"signature":  "sig",
		"password":   "p",
		"path":       "worklog/a.jpg",
		"key_name":   "DIFY_WORKLOG_API_KEY",
		"object_key": "worklog/b.jpg",
		"cos_key":    "worklog/c.jpg",
		"fileInfoId": "abc",
		"error":      "Get https://a?secret=s1 failed",
		"n":          3,
		"nested":     map[string]any{"token": "nt", "path": "p2"},
		"list":       []any{map[string]any{"ticket": "lt", "page": "2"}},
	})
	for _, key := range []string{"key", "token", "signature", "password"} {
		if out[key] != Mask {
			t.Fatalf("%s = %v，期望 %q", key, out[key], Mask)
		}
	}
	for _, key := range []string{"path", "key_name", "object_key", "cos_key", "fileInfoId"} {
		if out[key] == Mask {
			t.Fatalf("%s 被误伤：%v", key, out[key])
		}
	}
	if out["error"] != "Get https://a?secret=**** failed" {
		t.Fatalf("字符串值未脱敏：%v", out["error"])
	}
	if out["n"] != 3 {
		t.Fatalf("数值字段被改动：%v", out["n"])
	}
	nested, _ := out["nested"].(map[string]any)
	if nested == nil || nested["token"] != Mask || nested["path"] != "p2" {
		t.Fatalf("嵌套字段脱敏不符：%v", out["nested"])
	}
	list, _ := out["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("切片字段被改动：%v", out["list"])
	}
	item, _ := list[0].(map[string]any)
	if item == nil || item["ticket"] != Mask || item["page"] != "2" {
		t.Fatalf("切片元素脱敏不符：%v", list[0])
	}
}

// TestPathForLog 路径型票据（Office raw / netdisk preview / share）只掩码票据段，文件名保留。
func TestPathForLog(t *testing.T) {
	cases := map[string]string{
		"/api/v1/office/raw/ticket-123/报告.docx":           "/api/v1/office/raw/****/报告.docx",
		"/api/v1/office/callback/ticket-123":              "/api/v1/office/callback/****",
		"/api/v1/netdisk/preview/ticket-123":              "/api/v1/netdisk/preview/****",
		"/api/v1/netdisk/public/share/share-123/download": "/api/v1/netdisk/public/share/****/download",
		"/api/v1/share/share-123":                         "/api/v1/share/****",
		"/api/v1/worklog/logs/12":                         "/api/v1/worklog/logs/12",
		"/api/v1/netdisk/preview/sign":                    "/api/v1/netdisk/preview/sign",
		"/api/v1/office/raw":                              "/api/v1/office/raw",
		"":                                                "",
	}
	for in, want := range cases {
		if got := PathForLog(in); got != want {
			t.Fatalf("PathForLog(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestSensitiveKeyExported 敏感键判定与查询参数同口径，路径类 key 不过度掩码。
func TestSensitiveKeyExported(t *testing.T) {
	sensitive := []string{"token", "ACCESS_TOKEN", "Signature", "x-amz-signature", "code", "ticket", "api_key", "key", "password", "x-auth-token"}
	for _, key := range sensitive {
		if !SensitiveKey(key) {
			t.Fatalf("%q 应为敏感键", key)
		}
	}
	plain := []string{"fileInfoId", "page", "monkey", "keyword", "key_name", "object_key", "path", "normal"}
	for _, key := range plain {
		if SensitiveKey(key) {
			t.Fatalf("%q 不应为敏感键", key)
		}
	}
}

// TestTextForLogFullURLUserinfoPathTicket 文本中的完整 URL、userinfo、路径票据统一脱敏；
// 非敏感 fileInfoId 保留。
func TestTextForLogFullURLUserinfoPathTicket(t *testing.T) {
	in := "下载失败 https://bob:p%40ss@example.com:8443/api/v1/office/raw/TICKET123/report.docx?fileInfoId=abc123&Signature=SECRET 已跳过"
	got := TextForLog(in)
	for _, want := range []string{
		"https://****:****@example.com:8443/api/v1/office/raw/****/report.docx",
		"fileInfoId=abc123", "Signature=****",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q：%s", want, got)
		}
	}
	for _, secret := range []string{"bob", "p%40ss", "TICKET123", "SECRET"} {
		if strings.Contains(got, secret) {
			t.Fatalf("文本秘密 %q 泄漏：%s", secret, got)
		}
	}
	if got := TextForLog("path=/api/v1/netdisk/preview/TICKET9"); strings.Contains(got, "TICKET9") {
		t.Fatalf("路径票据未脱敏：%s", got)
	}
	for _, in := range []string{
		"session_key=sk-1&secret_key=sk-2&private_key=pk-3&normal=ok",
		`{"session_key":"sk-4","secret_key":"sk-5","private_key":"pk-6","ok":1}`,
	} {
		out := TextForLog(in)
		for _, secret := range []string{"sk-1", "sk-2", "pk-3", "sk-4", "sk-5", "pk-6"} {
			if strings.Contains(out, secret) {
				t.Fatalf("密钥 %q 泄漏：%s", secret, out)
			}
		}
	}
}

// TestFieldsForLogNestedStringMaps 嵌套 map[string]string / []map[string]string 递归脱敏。
func TestFieldsForLogNestedStringMaps(t *testing.T) {
	out := FieldsForLog(map[string]any{
		"headers": map[string]string{"session_key": "sk", "path": "p", "url": "https://x/y?Signature=S&fileInfoId=F"},
		"items":   []map[string]string{{"secret_key": "sk2", "page": "2"}},
	})
	headers, _ := out["headers"].(map[string]any)
	if headers == nil || headers["session_key"] != Mask || headers["path"] != "p" {
		t.Fatalf("嵌套 map[string]string 脱敏不符：%v", out["headers"])
	}
	if safe, _ := headers["url"].(string); strings.Contains(safe, "Signature=S") || !strings.Contains(safe, "fileInfoId=F") {
		t.Fatalf("URL 未脱敏：%v", headers["url"])
	}
	items, _ := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("[]map[string]string 未递归：%v", out["items"])
	}
	item, _ := items[0].(map[string]any)
	if item == nil || item["secret_key"] != Mask || item["page"] != "2" {
		t.Fatalf("切片 map[string]string 脱敏不符：%v", items[0])
	}
}
