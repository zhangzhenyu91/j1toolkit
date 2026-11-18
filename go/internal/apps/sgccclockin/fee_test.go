package sgccclockin

// 费用模板装配与上传测试：toMoshi 语义、saveFeeInfoNew 装配、uploadImageV2 multipart、
// 配置热生效。全部 httptest fixture + 本地生成密钥，不触真实商旅。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/compat/cryptoaudio"
	"shade/internal/integrations"
)

// mapCfg 测试用运行时配置桩（键值表可变，模拟 config.Store 变更）。
type mapCfg struct {
	mu sync.Mutex
	m  map[string]string
}

func newMapCfg() *mapCfg { return &mapCfg{m: map[string]string{}} }

func (c *mapCfg) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if !ok {
		return "", sql.ErrNoRows
	}
	return v, nil
}

func (c *mapCfg) set(key, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = v
}

// jsonxServer jsonx 通道 fixture 服务：解请求、按路径回加密应答（响应按请求同租户密钥加密）。
func jsonxServer(t *testing.T, k Keys, route func(path string, payload map[string]any) map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		tenant := "slapp"
		payload, err := k.jsonxDecryptBody("slapp", string(buf))
		if err != nil {
			tenant = "default"
			payload, err = k.jsonxDecryptBody("default", string(buf))
		}
		if err != nil {
			t.Errorf("fixture 解请求失败: %v", err)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		resp := route(path, payload)
		ct, err := k.jsonxEncryptBody(tenant, resp, 1)
		if err != nil {
			t.Errorf("fixture 加密应答失败: %v", err)
			return
		}
		_, _ = w.Write([]byte(ct))
	}))
}

// fixtureProtocol 构造指向 fixture 服务的协议客户端（池已预置，不再拉池）。
func fixtureProtocol(t *testing.T, k Keys, srv *httptest.Server) *Protocol {
	t.Helper()
	p := NewProtocol(k)
	p.Scheme = "http"
	host := strings.TrimPrefix(srv.URL, "http://")
	p.HostH5 = host
	p.HostZB = host
	p.poolM = []string{"uid-m"}
	p.poolAT = time.Now()
	p.poolX = []string{"uid-x"}
	p.poolXA = time.Now()
	return p
}

// sampleTpl 极简费用模板（含 id=4 成本分配、id=10 补助明细、id=5 上传图片）。
func sampleTpl() map[string]any {
	return map[string]any{
		"id": 7, "name": "差旅报销", "createtime": 1700000000000, "type": 1, "no": "FT-1",
		"dtComponentList": []any{
			map[string]any{"id": 4, "name": "成本分配", "value": "", "data": "", "belongToType": 2},
			map[string]any{"id": 5, "name": "上传图片", "value": "", "data": ""},
			map[string]any{"id": 10, "name": "补助明细", "value": "", "data": ""},
			map[string]any{"id": 11, "name": "备注", "value": "出差", "data": "出差", "maxlen": "200"},
		},
	}
}

// TestToMoshiFieldSemantics Moshi 语义：I/J 恒输出数字（空→0）、Z 恒输出布尔（空→false）、
// 其余仅非空输出；未登记字段不透传。
func TestToMoshiFieldSemantics(t *testing.T) {
	out := toMoshi(map[string]any{
		"belongToType": "2", // I：字符串数字 → 2
		"createtime":   nil, // J：空 → 0
		"canModify":    nil, // Z：空 → false
		"fileUpload":   "0", // Z：非空串 → true（JS Boolean 口径）
		"label":        "补", // 仅非空输出
		"data":         nil, // 空不输出
		"unknownField": "x", // 未登记字段不透传
	})
	if out["belongToType"] != float64(2) && out["belongToType"] != 2 {
		t.Fatalf("I 类型应输出数字: %v", out["belongToType"])
	}
	if out["createtime"] != 0 && out["createtime"] != float64(0) {
		t.Fatalf("J 空值应输出 0: %v", out["createtime"])
	}
	if out["canModify"] != false {
		t.Fatalf("Z 空值应输出 false: %v", out["canModify"])
	}
	if out["fileUpload"] != true {
		t.Fatalf("Z 非空串应输出 true: %v", out["fileUpload"])
	}
	if out["label"] != "补" {
		t.Fatalf("非空字符串应输出: %v", out["label"])
	}
	if _, ok := out["data"]; ok {
		t.Fatalf("空字符串不应输出")
	}
	if _, ok := out["unknownField"]; ok {
		t.Fatalf("未登记字段不应透传")
	}
}

// TestBuildFeeTplJSONOverride overrides 同时覆盖 value/data；外层只留五键 + dtComponentList。
func TestBuildFeeTplJSONOverride(t *testing.T) {
	s := BuildFeeTplJSON(sampleTpl(), map[string]string{
		"10": SubsidyJSON(25, 0),
		"5":  `[{"fileInfoId":"F1","url":"u"}]`,
	})
	var bean map[string]any
	if err := json.Unmarshal([]byte(s), &bean); err != nil {
		t.Fatalf("dtContent 应为 JSON: %v", err)
	}
	for _, k := range []string{"id", "name", "createtime", "type", "no", "dtComponentList"} {
		if _, ok := bean[k]; !ok {
			t.Fatalf("tplBean 缺 %s: %s", k, s)
		}
	}
	comps, _ := bean["dtComponentList"].([]any)
	if len(comps) != 4 {
		t.Fatalf("组件数不符: %d", len(comps))
	}
	for _, c := range comps {
		cm := c.(map[string]any)
		id := compIDKey(cm["id"])
		if id == "10" {
			if cm["value"] != cm["data"] || !strings.Contains(cm["value"].(string), `"foodFee":"25"`) {
				t.Fatalf("id=10 overrides 应同时覆盖 value/data: %v", cm)
			}
			if !strings.Contains(cm["value"].(string), `"arrive":"0"`) {
				t.Fatalf("补助明细 arrive 口径不符: %v", cm["value"])
			}
		}
		if id == "4" {
			if cm["belongToType"] != float64(2) {
				t.Fatalf("toMoshi 应输出原始类型: %v", cm)
			}
		}
	}
	// 无 overrides 的组件保持原值
	s2 := BuildFeeTplJSON(sampleTpl(), nil)
	if !strings.Contains(s2, "出差") {
		t.Fatalf("未覆盖组件应保留原值: %s", s2)
	}
}

// TestSubsidyJSON JS String(number) 口径。
func TestSubsidyJSON(t *testing.T) {
	if got := SubsidyJSON(25, 0); got != `{"arrive":"0","foodFee":"25"}` && got != `{"foodFee":"25","arrive":"0"}` {
		t.Fatalf("补助明细串不符: %s", got)
	}
	if got := SubsidyJSON(12.5, 1000000); !strings.Contains(got, `"foodFee":"12.5"`) || !strings.Contains(got, `"arrive":"1000000"`) {
		t.Fatalf("小数/大数口径不符: %s", got)
	}
}

// TestSaveFeeInfoNewAssemblesTpl saveFeeInfoNew 请求体装配（fixture 解密断言）+ overrides 透传。
func TestSaveFeeInfoNewAssemblesTpl(t *testing.T) {
	k := testKeys(t)
	var gotPayload map[string]any
	srv := jsonxServer(t, k, func(path string, payload map[string]any) map[string]any {
		if path != "clockin/saveFeeInfoNew" {
			t.Errorf("路径不符: %s", path)
		}
		gotPayload = payload
		return map[string]any{"statusCode": 200}
	})
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)

	d, err := p.SaveFeeInfoNew(context.Background(), "tok", "2026-10-27", sampleTpl(),
		map[string]string{"10": SubsidyJSON(30, 2)}, CallOpts{Mobile: "13800138000"})
	if err != nil {
		t.Fatalf("保存费用: %v", err)
	}
	if num(d["statusCode"]) != 200 {
		t.Fatalf("应答不符: %v", d)
	}
	if gotPayload["clockInDate"] != "2026-10-27" {
		t.Fatalf("clockInDate 缺失: %v", gotPayload)
	}
	if num(gotPayload["changeSubsidyFlag"]) != 0 {
		t.Fatalf("changeSubsidyFlag 应为 0: %v", gotPayload)
	}
	dt, _ := gotPayload["dtContentDetail"].(map[string]any)
	content, _ := dt["dtContent"].(string)
	// dtContent 内嵌组件值为转义 JSON 串（\"foodFee\"）
	if !strings.Contains(content, `\"foodFee\":\"30\"`) || !strings.Contains(content, "FT-1") {
		t.Fatalf("dtContent 装配不符: %s", content)
	}
}

// TestUploadImageV2Multipart multipart 口径：boundary/文件名/无 sign 头/slapp 头集 + 明文应答解析。
func TestUploadImageV2Multipart(t *testing.T) {
	k := testKeys(t)
	var gotHeader http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/fastdfs/upload/image/sample/v2") {
			t.Errorf("上传路径不符: %s", r.URL.Path)
		}
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"statusCode":200,"data":{"result":{"success":true,"fileInfoId":"F123","httpUrl":"https://x/f.jpg"}}}`))
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)

	img, err := p.UploadImageV2(context.Background(), "tok", []byte("JPEGDATA"), CallOpts{Mobile: "13800138000"})
	if err != nil {
		t.Fatalf("上传: %v", err)
	}
	if img.FileInfoID != "F123" || img.HTTPURL != "https://x/f.jpg" {
		t.Fatalf("返回不符: %+v", img)
	}
	ct := gotHeader.Get("Content-Type")
	if !regexp.MustCompile(`^multipart/form-data; boundary=Boundary\+[0-9A-F]{16}$`).MatchString(ct) {
		t.Fatalf("boundary 口径不符: %s", ct)
	}
	if gotHeader.Get("sign") != "" {
		t.Fatalf("multipart 不应带 sign 头")
	}
	if gotHeader.Get("tenant") != "slapp" || gotHeader.Get("secretKeyType") != "2" {
		t.Fatalf("slapp 头集不符: %v", gotHeader)
	}
	if gotHeader.Get("uniID") != "uid-x" {
		t.Fatalf("uniID 应走 slapp 池: %s", gotHeader.Get("uniID"))
	}
	body := string(gotBody)
	if !regexp.MustCompile(`filename="\d{17}\.jpg"`).MatchString(body) {
		t.Fatalf("文件名应为东八区 yyyyMMddHHmmssSSS.jpg: %s", body)
	}
	if !strings.Contains(body, `name="file"; `) || !strings.Contains(body, "JPEGDATA") {
		t.Fatalf("multipart 单字段 file 体不符: %s", body)
	}
}

// TestUploadImageV2FailureMsg 失败口径：带 HTTP 状态码与平台 statusCode。
func TestUploadImageV2FailureMsg(t *testing.T) {
	k := testKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"statusCode":500,"msg":"服务丢了"}`))
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	_, err := p.UploadImageV2(context.Background(), "tok", []byte("x"), CallOpts{})
	if err == nil || !strings.Contains(err.Error(), "商旅图片上传失败（HTTP 200 statusCode=500）：服务丢了") {
		t.Fatalf("失败文案不符: %v", err)
	}
}

// TestSaveFeeFullFlow 无 tpl 快照时先取模板再装配保存（补助明细 id=10 自动装配）。
func TestSaveFeeFullFlow(t *testing.T) {
	k := testKeys(t)
	var savedContent string
	srv := jsonxServer(t, k, func(path string, payload map[string]any) map[string]any {
		switch path {
		case "clockin/getFeeInfoNew":
			return map[string]any{"statusCode": 200, "data": map[string]any{
				"body": map[string]any{"clockTemplate": sampleTpl()},
			}}
		case "clockin/saveFeeInfoNew":
			dt, _ := payload["dtContentDetail"].(map[string]any)
			savedContent, _ = dt["dtContent"].(string)
			return map[string]any{"statusCode": 200}
		default:
			t.Errorf("未知路径: %s", path)
			return map[string]any{"statusCode": 500}
		}
	})
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)

	d, err := p.SaveFee(context.Background(), "tok", map[string]any{
		"date": "2026-10-27", "foodFee": float64(25), "transitFee": float64(3),
	}, CallOpts{})
	if err != nil {
		t.Fatalf("保存费用: %v", err)
	}
	if num(d["statusCode"]) != 200 {
		t.Fatalf("应答不符: %v", d)
	}
	if !strings.Contains(savedContent, `\"foodFee\":\"25\"`) || !strings.Contains(savedContent, `\"arrive\":\"3\"`) {
		t.Fatalf("补助明细未装配: %s", savedContent)
	}
}

// TestCostAllocNormalize 成本分配归一：缺码默认 1、误写文案映射回码；value/data 不对称。
func TestCostAllocNormalize(t *testing.T) {
	comp := map[string]any{"id": 4}
	code := NormalizeCostAllocComp(comp, map[string]any{
		"costCenterName": "检修一班", "costCenterCode": "CC-1",
	})
	if code != "1" {
		t.Fatalf("缺码应默认 1: %s", code)
	}
	var v, d map[string]any
	_ = json.Unmarshal([]byte(comp["value"].(string)), &v)
	_ = json.Unmarshal([]byte(comp["data"].(string)), &d)
	if v["value"] != "1" || d["value"] != "成本中心" {
		t.Fatalf("value/data 不对称口径不符: %v / %v", v, d)
	}
	// 误写文案 → 映射回选项码
	comp2 := map[string]any{"id": 4}
	if code2 := NormalizeCostAllocComp(comp2, map[string]any{
		"value": "WBS", "costCenterCode": "CC-2",
	}); code2 != "2" {
		t.Fatalf("文案应映射回码: %s", code2)
	}
}

// TestParseFeeImageList 图片组件解析：id/url 键兼容，识别不出的元素返回 nil 保护本地。
func TestParseFeeImageList(t *testing.T) {
	items := ParseFeeImageList(`[{"fileInfoId":"F1","url":"u1"},{"id":"F2","url":"u2"},{"imageId":"F3","imageUrl":"u3"},{"url":"裸url"}]`)
	if len(items) != 4 {
		t.Fatalf("解析数不符: %d", len(items))
	}
	if it := ParseFeeImageItem(items[0]); it == nil || it.ID != "F1" || it.URL != "u1" {
		t.Fatalf("fileInfoId 口径不符: %v", it)
	}
	if it := ParseFeeImageItem(items[1]); it == nil || it.ID != "F2" {
		t.Fatalf("id 兼容口径不符: %v", it)
	}
	if it := ParseFeeImageItem(items[2]); it == nil || it.ID != "F3" || it.URL != "u3" {
		t.Fatalf("imageId 兼容口径不符: %v", it)
	}
	if it := ParseFeeImageItem(items[3]); it != nil {
		t.Fatalf("无可识别图片 id 应返回 nil: %v", it)
	}
}

// TestConfigHotEffect 密钥/代理配置变更后下一次调用热生效：版本头即时变化、密钥指纹变化清 uniID 池、
// 代理键覆盖静态字段（空串=关闭）。
func TestConfigHotEffect(t *testing.T) {
	k := testKeys(t)
	var versions []string
	var mu sync.Mutex
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		versions = append(versions, r.Header.Get("version"))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/ads") {
			ct, _ := k.jsonxEncryptBody("slapp", map[string]any{
				"statusCode": 200,
				"data":       map[string]any{"list": []any{"uid-1", "uid-2"}},
			}, 1)
			_, _ = w.Write([]byte(ct))
			return
		}
		ct, _ := k.jsonxEncryptBody("slapp", map[string]any{"statusCode": 200}, 1)
		_, _ = w.Write([]byte(ct))
	}))
	defer srv2.Close()
	p := NewProtocol(k)
	p.Scheme = "http"
	host := strings.TrimPrefix(srv2.URL, "http://")
	p.HostH5, p.HostZB = host, host
	p.poolX = []string{"uid-x"}
	p.poolXA = time.Now()

	cfg := newMapCfg()
	p.Cfg = cfg
	ctx := context.Background()
	slapp := CallOpts{Tenant: "slapp"}

	// 静态回退：未配置时用缺省版本
	if _, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{}, slapp); err != nil {
		t.Fatalf("调用1: %v", err)
	}
	// 热生效：库内配置新版本 → 下一次调用即用新值
	cfg.set(CfgVersion, "4.0.0")
	if _, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{}, slapp); err != nil {
		t.Fatalf("调用2: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), versions...)
	mu.Unlock()
	if len(got) < 2 || got[0] != "3.3.7" || got[1] != "4.0.0" {
		t.Fatalf("版本热生效不符: %v", got)
	}
	// 密钥指纹变化 → 清 uniID 池（下次调用先拉池再消耗，池内为服务端新发的 uid）
	p.mu.Lock()
	p.poolX = []string{"stale-uid"}
	p.mu.Unlock()
	cfg.set(CfgJWTSecret, "rotated-secret")
	if _, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{}, slapp); err != nil {
		t.Fatalf("调用3: %v", err)
	}
	p.mu.Lock()
	remain := len(p.poolX)
	var head string
	if remain > 0 {
		head = p.poolX[0]
	}
	p.mu.Unlock()
	if remain != 1 || head != "uid-2" {
		t.Fatalf("密钥变更后应清旧池重新拉池（剩 uid-2）: remain=%d head=%q", remain, head)
	}
	// 代理键权威覆盖：库内空串=关闭代理（即使静态 ProxyURL 非空）
	p.ProxyURL = "socks5://127.0.0.1:1080"
	cfg.set(CfgProxyURL, "")
	_, proxy := p.resolve(ctx)
	if proxy != "" {
		t.Fatalf("库内空串应关闭代理: %q", proxy)
	}
	// 键不存在 → 回退静态字段
	cfg2 := newMapCfg()
	p.Cfg = cfg2
	_, proxy = p.resolve(ctx)
	if proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("键缺失应回退静态字段: %q", proxy)
	}
}

// TestLoginWireReplay 登录链路请求线格式回放（fixture 解密 + 头集/签名断言，
// 与 server/src/sgccclockin/protocol.js loginByPassword / jsonxHeaders 同口径）。
func TestLoginWireReplay(t *testing.T) {
	k := testKeys(t)
	var gotPath string
	var gotPayload map[string]any
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = strings.TrimPrefix(r.URL.Path, "/api/")
		gotHeader = r.Header.Clone()
		buf, _ := io.ReadAll(r.Body)
		payload, err := k.jsonxDecryptBody("slapp", string(buf))
		if err != nil {
			t.Errorf("fixture 解请求失败: %v", err)
			return
		}
		gotPayload = payload
		ct, err := k.jsonxEncryptBody("slapp", map[string]any{
			"statusCode": 200,
			"data":       map[string]any{"token": "tok-1"},
		}, 1)
		if err != nil {
			t.Errorf("fixture 加密应答失败: %v", err)
			return
		}
		_, _ = w.Write([]byte(ct))
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)

	token, err := p.LoginByPassword(context.Background(), "13800138000", "pw", "cap-tok", "const-1",
		CallOpts{Mobile: "13800138000"})
	if err != nil || token != "tok-1" {
		t.Fatalf("密码登录: token=%q err=%v", token, err)
	}
	if gotPath != "user/token/v4" {
		t.Fatalf("路径不符: %s", gotPath)
	}
	// 请求体字段集（Node loginByPassword 同口径）
	if gotPayload["mobile"] != "13800138000" || gotPayload["password"] != "pw" {
		t.Fatalf("凭据字段不符: %v", gotPayload)
	}
	if gotPayload["source"] != "3" || gotPayload["riskFlag"] != "Y" {
		t.Fatalf("source/riskFlag 不符: %v", gotPayload)
	}
	if gotPayload["riskReviewFlag"] != true {
		t.Fatalf("riskReviewFlag 应为 true: %v", gotPayload)
	}
	if gotPayload["captchaToken"] != "cap-tok" || gotPayload["constId"] != "const-1" {
		t.Fatalf("风控字段不符: %v", gotPayload)
	}
	if _, ok := gotPayload["_r"]; !ok {
		t.Fatalf("缺 _r: %v", gotPayload)
	}
	// 头集（jsonx slapp 公共内核；登录调用不带 token 头）
	for key, want := range map[string]string{
		"tenant": "slapp", "platform": "1", "secretKeyType": "2", "encFlag": "2",
		"version": "3.3.7", "version-code": "202609232038", "grayversion": "2.4.7.1",
		"appType": "0", "source": "1", "uniID": "uid-x",
		"systemVersion": "Android 13", "deviceType": "Pixel 7",
	} {
		if gotHeader.Get(key) != want {
			t.Fatalf("头 %s = %q，期望 %q", key, gotHeader.Get(key), want)
		}
	}
	if gotHeader.Get("token") != "" {
		t.Fatalf("登录请求不应带 token 头: %q", gotHeader.Get("token"))
	}
	if ua := gotHeader.Get("User-Agent"); ua != "guo wang shang lu yun/3.3.7 (Pixel 7; Android 13; Scale/2.00)" {
		t.Fatalf("User-Agent 不符: %q", ua)
	}
	if gotHeader.Get("Content-Type") != "application/jsonx" {
		t.Fatalf("Content-Type 不符: %q", gotHeader.Get("Content-Type"))
	}
	// sign 头：keyList「uniID,tenant;」+ md5(值串联 + r)（登录调用无 token）
	rVal := int64(num(gotPayload["_r"]))
	sign := MakeSignHeaderX(map[string]string{
		"token": gotHeader.Get("token"), "uniID": gotHeader.Get("uniID"), "tenant": gotHeader.Get("tenant"),
	}, rVal)
	if gotHeader.Get("sign") != sign {
		t.Fatalf("sign 头不符: %q ≠ %q", gotHeader.Get("sign"), sign)
	}
	if !strings.HasPrefix(sign, "uniID,tenant;") {
		t.Fatalf("sign keyList 不符: %q", sign)
	}
}

// TestLoginSmsWireReplay 短信链路双通道请求体字段（v2 jsonm / v3 slapp）回放。
func TestLoginSmsWireReplay(t *testing.T) {
	k := testKeys(t)
	var v3Payload map[string]any
	var v3Path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		buf, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(path, "user/sendSmsCode/v3") {
			v3Path = path
			payload, err := k.jsonxDecryptBody("slapp", string(buf))
			if err != nil {
				t.Errorf("fixture 解请求失败: %v", err)
				return
			}
			v3Payload = payload
			ct, err := k.jsonxEncryptBody("slapp", map[string]any{"statusCode": 200}, 1)
			if err != nil {
				t.Errorf("fixture 加密应答失败: %v", err)
				return
			}
			_, _ = w.Write([]byte(ct))
			return
		}
		// jsonm 通道（v2/ads）：解请求后回加密应答
		var j struct {
			Data string `json:"data"`
			Sign string `json:"sign"`
		}
		if err := json.Unmarshal(buf, &j); err != nil {
			t.Errorf("jsonm 体解析失败: %v", err)
			return
		}
		keyHex := strings.TrimPrefix(j.Sign, "04")
		key, err := cryptoaudio.SM2DecryptHex(keyHex, k.SM2ClientPriv)
		if err != nil {
			t.Errorf("SM2 解 key 失败: %v", err)
			return
		}
		plain, err := cryptoaudio.AESDecryptECBB64(j.Data, key)
		if err != nil {
			t.Errorf("AES 解失败: %v", err)
			return
		}
		if !strings.Contains(plain, `"checkImgCode":"6789"`) {
			t.Errorf("v2 请求体不符: %s", plain)
		}
		respKey, err := cryptoaudio.GenAESKey()
		if err != nil {
			t.Errorf("gen key: %v", err)
			return
		}
		ct, err := cryptoaudio.AESEncryptECBB64(`{"statusCode":200}`, respKey)
		if err != nil {
			t.Errorf("AES 加密: %v", err)
			return
		}
		sign, err := cryptoaudio.SM2EncryptHex(respKey, k.SM2ServerPub)
		if err != nil {
			t.Errorf("SM2 加密: %v", err)
			return
		}
		out, _ := json.Marshal(map[string]any{"data": ct, "sign": sign})
		w.Header().Set("Content-Type", "application/jsonm")
		_, _ = w.Write(out)
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)

	if _, err := p.LoginSendSmsV2(context.Background(), "13800138000", "6789"); err != nil {
		t.Fatalf("v2 发短信: %v", err)
	}
	if _, err := p.LoginSendSmsSlapp(context.Background(), "13800138000", "cap-tok", "const-1",
		CallOpts{Mobile: "13800138000"}); err != nil {
		t.Fatalf("v3 发短信: %v", err)
	}
	if v3Path != "user/sendSmsCode/v3" {
		t.Fatalf("v3 路径不符: %s", v3Path)
	}
	// v3 字段（Node loginSendSmsSlapp 同口径：riskReviewFlag 固定 true）
	if v3Payload["source"] != "3" || v3Payload["riskFlag"] != "Y" || v3Payload["riskReviewFlag"] != true {
		t.Fatalf("v3 风控字段不符: %v", v3Payload)
	}
	if v3Payload["captchaToken"] != "cap-tok" || v3Payload["constId"] != "const-1" {
		t.Fatalf("v3 滑块凭据不符: %v", v3Payload)
	}
}

// feeRoundTripFunc 测试用 RoundTripper：按次数造传输故障或透传到 fixture。
type feeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f feeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// feeTimeoutErr 测试用瞬时传输超时（net.Error 口径）。
type feeTimeoutErr struct{}

func (feeTimeoutErr) Error() string   { return "i/o timeout" }
func (feeTimeoutErr) Timeout() bool   { return true }
func (feeTimeoutErr) Temporary() bool { return true }

// zeroWaitReadRetry 测试用读重试策略：口径同默认（两次/300ms），等待注入为零耗时。
func zeroWaitReadRetry() integrations.RetryPolicy {
	return integrations.RetryPolicy{
		MaxAttempts: 2,
		BaseWait:    300 * time.Millisecond,
		MaxWait:     time.Second,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
}

// TestNewProtocolReadRetryDefault 默认读重试口径：最多两次尝试、300ms 间隔，单请求超时仍 20s。
func TestNewProtocolReadRetryDefault(t *testing.T) {
	p := NewProtocol(Keys{})
	if p.ReadRetry.MaxAttempts != 2 || p.ReadRetry.BaseWait != 300*time.Millisecond {
		t.Fatalf("默认读重试策略不符: %+v", p.ReadRetry)
	}
	if p.HTTP == nil || p.HTTP.Timeout != 20*time.Second {
		t.Fatalf("单请求超时应保持 20s: %v", p.HTTP)
	}
}

// TestGetFeeInfoNewRetriesTransientTimeout 费用模板读取遇瞬时传输超时重试一次：恰好两次 HTTP，
// 第二次重新取 uniID 并重新加密原请求，最终正常解析。
func TestGetFeeInfoNewRetriesTransientTimeout(t *testing.T) {
	k := testKeys(t)
	srv := jsonxServer(t, k, func(path string, _ map[string]any) map[string]any {
		if path != "clockin/getFeeInfoNew" {
			t.Errorf("路径不符: %s", path)
		}
		return map[string]any{"statusCode": 200, "data": map[string]any{
			"body": map[string]any{"clockTemplate": sampleTpl()},
		}}
	})
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	p.poolM = []string{"uid-m1", "uid-m2"} // 每次尝试消耗一枚 uniID
	p.ReadRetry = zeroWaitReadRetry()

	base := &http.Transport{}
	defer base.CloseIdleConnections()
	var calls atomic.Int32
	var uniIDs []string
	p.HTTP = &http.Client{Transport: feeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		// 头按原始键写入（不经 CanonicalMIMEHeaderKey），读取须用原始键
		raw := r.Header["uniID"]
		id := ""
		if len(raw) > 0 {
			id = raw[0]
		}
		uniIDs = append(uniIDs, id)
		if calls.Add(1) == 1 {
			return nil, feeTimeoutErr{}
		}
		return base.RoundTrip(r)
	})}

	decoded, err := p.GetFeeInfoNew(context.Background(), "tok", FeeTplParams{
		ClockInDate: "2026-10-27", CityName: "驻地", CityCode: "330100", Position: "120.1,30.2",
	}, CallOpts{Mobile: "13800138000"})
	if err != nil {
		t.Fatalf("取费用模板: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("HTTP 尝试次数 = %d，期望 2（首次超时后重试一次）", got)
	}
	// 每次尝试重新取 uniID / 重新加密：两次请求的 uniID 必须不同且池内两枚都已消耗
	if len(uniIDs) != 2 || uniIDs[0] == uniIDs[1] || uniIDs[0] == "" || uniIDs[1] == "" {
		t.Fatalf("每次尝试应重新取 uniID: %v", uniIDs)
	}
	if remain := len(p.poolM); remain != 0 {
		t.Fatalf("两次尝试应各消耗一枚 uniID，池内剩余 %d", remain)
	}
	if num(decoded["statusCode"]) != 200 || ExtractClockTemplate(decoded) == nil {
		t.Fatalf("重试后应答未解析: %v", decoded)
	}
}

// TestGetFeeInfoNewStopsWhenContextCanceled 父 ctx 在首次请求中取消：不得再发第二次 HTTP，
// 返回取消错误。
func TestGetFeeInfoNewStopsWhenContextCanceled(t *testing.T) {
	k := testKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ctx 取消后不应再发请求: %s", r.URL.Path)
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	p.poolM = []string{"uid-m1", "uid-m2"}
	p.ReadRetry = zeroWaitReadRetry()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	p.HTTP = &http.Client{Transport: feeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		return nil, context.Canceled
	})}

	_, err := p.GetFeeInfoNew(ctx, "tok", FeeTplParams{ClockInDate: "2026-10-27"}, CallOpts{Mobile: "13800138000"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消错误: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP 尝试次数 = %d，期望 1（父 ctx 取消后不得再发第二次）", got)
	}
}

// TestSaveFeeInfoNewDoesNotRetryTransientTimeout 写接口遇瞬时传输超时不重试：恰好一次尝试。
func TestSaveFeeInfoNewDoesNotRetryTransientTimeout(t *testing.T) {
	k := testKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("写请求不应到达服务端: %s", r.URL.Path)
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	p.poolX = []string{"uid-x1", "uid-x2"}
	p.ReadRetry = zeroWaitReadRetry()

	var calls atomic.Int32
	p.HTTP = &http.Client{Transport: feeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, feeTimeoutErr{}
	})}

	_, err := p.SaveFeeInfoNew(context.Background(), "tok", "2026-10-27", sampleTpl(), nil, CallOpts{Mobile: "13800138000"})
	if err == nil {
		t.Fatal("写请求遇传输超时应报错")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("写请求 HTTP 尝试次数 = %d，期望 1（写接口不得重试）", got)
	}
}

// feeTimeoutMsgErr 带自定义文案的瞬时超时（模拟代理拨号失败时错误文本回显代理地址）。
type feeTimeoutMsgErr struct{ msg string }

func (e feeTimeoutMsgErr) Error() string   { return e.msg }
func (e feeTimeoutMsgErr) Timeout() bool   { return true }
func (e feeTimeoutMsgErr) Temporary() bool { return true }

// proxyFixture 代理路径 fixture：传输层注入假 RoundTripper（不真连代理），
// 代理地址含账密以校验脱敏口径。
const proxyFixtureURL = "socks5://bob:s3cret@127.0.0.1:1080"

func withProxyFixture(t *testing.T, p *Protocol, rt http.RoundTripper) {
	t.Helper()
	p.ProxyURL = proxyFixtureURL
	p.transports = transportCache{url: proxyFixtureURL, rt: rt}
}

// TestGetFeeInfoNewRetriesTransientTimeoutViaProxy 代理路径首次瞬时超时同样重试：恰好两次请求、
// 第二次重新取 uniID 并成功。原实现把代理错误重建为纯字符串，错误链断掉，瞬时分类丢失，
// 只发一次请求即失败。
func TestGetFeeInfoNewRetriesTransientTimeoutViaProxy(t *testing.T) {
	k := testKeys(t)
	srv := jsonxServer(t, k, func(path string, _ map[string]any) map[string]any {
		if path != "clockin/getFeeInfoNew" {
			t.Errorf("路径不符: %s", path)
		}
		return map[string]any{"statusCode": 200, "data": map[string]any{
			"body": map[string]any{"clockTemplate": sampleTpl()},
		}}
	})
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	p.poolM = []string{"uid-m1", "uid-m2"}
	p.ReadRetry = zeroWaitReadRetry()

	base := &http.Transport{}
	defer base.CloseIdleConnections()
	var calls atomic.Int32
	var uniIDs []string
	withProxyFixture(t, p, feeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw := r.Header["uniID"]
		id := ""
		if len(raw) > 0 {
			id = raw[0]
		}
		uniIDs = append(uniIDs, id)
		if calls.Add(1) == 1 {
			return nil, feeTimeoutMsgErr{msg: "socks connect tcp " + proxyFixtureURL + ": i/o timeout"}
		}
		return base.RoundTrip(r)
	}))

	decoded, err := p.GetFeeInfoNew(context.Background(), "tok", FeeTplParams{
		ClockInDate: "2026-10-27", CityName: "驻地", CityCode: "330100", Position: "120.1,30.2",
	}, CallOpts{Mobile: "13800138000"})
	if err != nil {
		t.Fatalf("代理路径取费用模板: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("代理路径 HTTP 尝试次数 = %d，期望 2（首次瞬时超时后重试一次）", got)
	}
	if len(uniIDs) != 2 || uniIDs[0] == uniIDs[1] {
		t.Fatalf("每次尝试应重新取 uniID: %v", uniIDs)
	}
	if num(decoded["statusCode"]) != 200 || ExtractClockTemplate(decoded) == nil {
		t.Fatalf("重试后应答未解析: %v", decoded)
	}
}

// TestGetFeeInfoNewProxyErrorMasksAndKeepsTransient 两次都失败时：对外文案仍脱敏（不含账密），
// 错误链保留 net.Error 瞬时分类（否则上层无法识别代理抖动）。
func TestGetFeeInfoNewProxyErrorMasksAndKeepsTransient(t *testing.T) {
	k := testKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("代理路径由假传输层拦截，不应到达服务端: %s", r.URL.Path)
	}))
	defer srv.Close()
	p := fixtureProtocol(t, k, srv)
	p.poolM = []string{"uid-m1", "uid-m2"}
	p.ReadRetry = zeroWaitReadRetry()

	var calls atomic.Int32
	withProxyFixture(t, p, feeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, feeTimeoutMsgErr{msg: "socks connect tcp " + proxyFixtureURL + ": i/o timeout"}
	}))

	_, err := p.GetFeeInfoNew(context.Background(), "tok", FeeTplParams{ClockInDate: "2026-10-27"}, CallOpts{Mobile: "13800138000"})
	if err == nil {
		t.Fatal("两次瞬时失败应返回错误")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("代理路径 HTTP 尝试次数 = %d，期望 2", got)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("对外文案不应泄露代理账密: %v", err)
	}
	if !strings.Contains(err.Error(), "socks5://***@127.0.0.1:1080") {
		t.Fatalf("代理地址应按既有口径脱敏: %v", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) {
		t.Fatalf("错误链应保留 net.Error 分类: %v", err)
	}
	if !integrations.TransientError(err) {
		t.Fatalf("脱敏后仍应判为瞬时错误: %v", err)
	}
}
