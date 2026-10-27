package sgccclockin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeoReverseAndForward(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("key") != "test-map-key" {
			t.Error("高德密钥未从运行时配置读取")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3/geocode/geo" {
			if r.URL.Query().Get("address") != "测试地址" || r.URL.Query().Get("city") != "临汾市" {
				t.Errorf("正向解析参数错误: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"status":"1","geocodes":[{"adcode":"141002","location":"111.1,37.2"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"1","regeocode":{"addressComponent":{"country":"中国","province":"山西省","city":"临汾市","district":"尧都区","township":"辛寺街道","adcode":"141002","streetNumber":{"street":"解放东路"}}}}`))
	}))
	defer server.Close()
	cfg := newMapCfg()
	cfg.set(CfgAMapKey, "test-map-key")
	cfg.set(CfgAMapBase, server.URL)
	m := New(nil)
	m.Cfg = cfg
	result, err := m.reverseGeo(context.Background(), 111.1, 37.2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Position != "中国山西省临汾市尧都区辛寺街道解放东路" || result.CityName != "临汾市" || result.CityCode != "141002" {
		t.Fatalf("逆地理结果不符: %+v", result)
	}
	var forward struct {
		Status   string `json:"status"`
		Geocodes []struct {
			Adcode   string `json:"adcode"`
			Location string `json:"location"`
		} `json:"geocodes"`
	}
	if err := m.geoGET(context.Background(), "/v3/geocode/geo", map[string][]string{"address": {"测试地址"}, "city": {"临汾市"}}, &forward); err != nil {
		t.Fatal(err)
	}
	if forward.Geocodes[0].Adcode != "141002" || calls != 2 {
		t.Fatalf("正向解析结果或调用次数不符: %+v calls=%d", forward, calls)
	}
	cfg.set(CfgAMapKey, "")
	if _, err := m.reverseGeo(context.Background(), 111.1, 37.2); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("未配置密钥时不应发起外部请求")
	}
}
