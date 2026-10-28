package uvmpapi

import "testing"

const goldenMtk = "efbfbd071c72efbfbd071c72efbfbd071c72efbfbd071c72efbfbd071c72"

// TestMakeXContentTokenGolden 黄金向量 1：派车单查询（params JSON + tycl 中文），
// 与 vehicle_export.make_x_content_token（浏览器真实样本双样本验证过）逐字节对拍。
func TestMakeXContentTokenGolden(t *testing.T) {
	newData := map[string]any{
		"params":            `{"pageIndex":1,"pageSize":50,"filter":"planSendTime=2026-09-01 00:00&planSendTime2=2026-09-30 23:59:00&runState=2&vehicleState=04&keepTag=00,01"}`,
		"tycl_parentMenuId": "running",
		"tycl_sonMenuId":    "pcdmanager",
		"tycl_sonMenuTitle": "派车单管理",
	}
	got, err := MakeXContentToken("tok_en.abc-123", goldenMtk, "/running-service-provider/pcdmanager/", newData)
	if err != nil {
		t.Fatal(err)
	}
	want := "MAC wfj21spEffMVxOmyz1k1_bdYRal0AJRZWDZrcSd_ri0"
	if got != want {
		t.Fatalf("sign1 = %s, want %s", got, want)
	}
}

// TestMakeXContentTokenGolden2 黄金向量 2：getPrintDetails（base_path 自带 query 解码值 + 空值）。
func TestMakeXContentTokenGolden2(t *testing.T) {
	newData := map[string]any{
		"id":                "a,b,c",
		"tycl_parentMenuId": "running",
		"tycl_sonMenuId":    "pcdmanager",
		"tycl_sonMenuTitle": "派车单管理",
		"empty":             "",
	}
	got, err := MakeXContentToken("T2.token", goldenMtk, "/running-service-provider/pcdmanager/getPrintDetails", newData)
	if err != nil {
		t.Fatal(err)
	}
	want := "MAC 9EngT0fFc9cjXYBpALA_kgW9Mqy40URLC67p9Eh4AR0"
	if got != want {
		t.Fatalf("sign2 = %s, want %s", got, want)
	}
}

// TestMakeXContentTokenGolden3 黄金向量 3：POST body 字符串化口径（数值→"20"，中文原文）。
func TestMakeXContentTokenGolden3(t *testing.T) {
	newData := map[string]any{
		"num":               "20",
		"vehicleNumber":     "晋A",
		"tycl_sonMenuTitle": "行驶轨迹",
	}
	got, err := MakeXContentToken("post.token-x", goldenMtk, "/zuul/common-service-provider/getVehicleInfo", newData)
	if err != nil {
		t.Fatal(err)
	}
	want := "MAC CZCYmjvjNKgC1vnN1wRsxAN4jUNwrzbdK-Yj6Gd6Ly0"
	if got != want {
		t.Fatalf("sign3 = %s, want %s", got, want)
	}
}

// TestMakeXContentTokenPythonSemantics 黄金向量 4：POST body Python 真值语义——
// false/null/0 假值保持原类型（false/null/0），1.0→"1.0"、true→"True"，
// 空容器保持 []/{}，Unicode 原文（ensure_ascii=False）。
// 期望值由 Python vehicle_export.make_x_content_token 语义离线复算。
func TestMakeXContentTokenPythonSemantics(t *testing.T) {
	newData := map[string]any{
		"b":     false,
		"n":     nil,
		"z":     0,
		"f":     1.0,
		"t":     true,
		"elist": []any{},
		"edict": map[string]any{},
		"u":     "派车单管理",
	}
	got, err := MakeXContentToken("py.token-01", goldenMtk, "/zuul/common-service-provider/save", newData)
	if err != nil {
		t.Fatal(err)
	}
	// 签名 JSON：{"b":false,"edict":{},"elist":[],"f":"1.0","n":null,"t":"True","u":"派车单管理","z":0}
	want := "MAC fw0PiPkV2hiqRKNgv8vU1pRc-Ow8X31FNl5JXb_fP3I"
	if got != want {
		t.Fatalf("sign4 = %s, want %s", got, want)
	}
}

// TestMakeXContentTokenPythonFloats 黄金向量 5：浮点/整数字符串化口径——
// int 20→"20"、1.5→"1.5"、-2.5→"-2.5"、0.0001→"0.0001"、1e16→"1e+16"、
// 假值 0.0 保持 0.0、空串保持 ""。
func TestMakeXContentTokenPythonFloats(t *testing.T) {
	newData := map[string]any{
		"i":     20,
		"fs":    1.5,
		"big":   1e16,
		"small": 0.0001,
		"neg":   -2.5,
		"zs":    0.0,
		"es":    "",
		"uni":   "树障隐患×2",
	}
	got, err := MakeXContentToken("py.token-02", goldenMtk, "/zuul/common-service-provider/save2", newData)
	if err != nil {
		t.Fatal(err)
	}
	// 签名 JSON：{"big":"1e+16","es":"","fs":"1.5","i":"20","neg":"-2.5","small":"0.0001","uni":"树障隐患×2","zs":0.0}
	want := "MAC Wi7hQ53Bz7uWNRn4V6R-AbzIgJGlPyhhtV5ncLDOXOg"
	if got != want {
		t.Fatalf("sign5 = %s, want %s", got, want)
	}
}

// TestMakeXContentTokenIntStrEq 黄金向量 6：int 20 经 str(v) 与字符串 "20" 逐字节等价
// （与黄金向量 3 同签名）。
func TestMakeXContentTokenIntStrEq(t *testing.T) {
	newData := map[string]any{
		"num":               20,
		"vehicleNumber":     "晋A",
		"tycl_sonMenuTitle": "行驶轨迹",
	}
	got, err := MakeXContentToken("post.token-x", goldenMtk, "/zuul/common-service-provider/getVehicleInfo", newData)
	if err != nil {
		t.Fatal(err)
	}
	want := "MAC CZCYmjvjNKgC1vnN1wRsxAN4jUNwrzbdK-Yj6Gd6Ly0"
	if got != want {
		t.Fatalf("sign6 = %s, want %s（应与黄金向量 3 一致）", got, want)
	}
}

// TestBuildURL URL 构造与 new_data 合并（vehicle_export.UvmpClient._build）：
// rnd/cl_u_id/_ 不参与签名，base_path 自带 query 解码后入签，p_data 原文入签。
func TestBuildURL(t *testing.T) {
	b := buildURL("http://gw/", "running-service-provider/pcdmanager/getPrintDetails?id=a%2Cb%2Cc&rnd=123&cl_u_id=null&_=1690000000000&ticket=TK",
		map[string]string{"params": "P 1", "tycl_sonMenuTitle": "派车单管理"},
		"0123456789abcdef", "1690000000001")
	if b.tempURL != "/running-service-provider/pcdmanager/getPrintDetails" {
		t.Fatalf("tempURL = %s", b.tempURL)
	}
	if b.url != "http://gw/running-service-provider/pcdmanager/getPrintDetails?id=a%2Cb%2Cc&rnd=123&cl_u_id=null&_=1690000000000&ticket=TK&"+
		"rnd=0123456789abcdef&cl_u_id=null&params=P+1&tycl_sonMenuTitle=%E6%B4%BE%E8%BD%A6%E5%8D%95%E7%AE%A1%E7%90%86&_=1690000000001" {
		t.Fatalf("url = %s", b.url)
	}
	wantNew := map[string]any{
		"id":                "a,b,c", // 解码后入签
		"params":            "P 1",   // 原文（空格不编码）
		"tycl_sonMenuTitle": "派车单管理",
	}
	if len(b.newData) != len(wantNew) {
		t.Fatalf("newData = %v", b.newData)
	}
	for k, v := range wantNew {
		if b.newData[k] != v {
			t.Fatalf("newData[%s] = %q, want %q", k, b.newData[k], v)
		}
	}
}

// TestBuildURLNoQuery 无 query 的 base_path 用 "?" 拼 rnd。
func TestBuildURLNoQuery(t *testing.T) {
	b := buildURL("http://gw/", "a/b/", nil, "0123456789abcdef", "1")
	if b.url != "http://gw/a/b/?rnd=0123456789abcdef&cl_u_id=null&_=1" {
		t.Fatalf("url = %s", b.url)
	}
}
