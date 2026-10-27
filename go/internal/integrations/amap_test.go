package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// amapTestService 构造指向假服务的高德客户端。
func amapTestService(baseURL string) *AMapService {
	return &AMapService{
		Cfg: NewMapProvider(map[string]string{
			"AMAP_MAP_KEY":  "test-amap-key",
			"AMAP_BASE_URL": baseURL,
		}),
	}
}

// TestAMapFetchLocationWeather 两步接口：地点拼「区县·最近POI」、天气拼「天气 温度」。
func TestAMapFetchLocationWeather(t *testing.T) {
	var regeoQuery, weatherQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			regeoQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{
				"status": "1",
				"regeocode": {
					"addressComponent": {"district": "尧都区", "city": "临汾市", "township": "段店乡", "adcode": "141002"},
					"pois": [
						{"name": "较远地标", "distance": "500"},
						{"name": "临汾电力高级技工学校", "distance": "85"}
					]
				}
			}`))
		case "/v3/weather/weatherInfo":
			weatherQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"status": "1", "lives": [{"weather": "多云", "temperature": "31.6"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	got := amapTestService(srv.URL).FetchLocationWeather(context.Background(), "116.4808812", "39.9858764")
	if got.Location != "尧都区·临汾电力高级技工学校" {
		t.Fatalf("location = %q", got.Location)
	}
	if got.Weather != "多云 32°C" {
		t.Fatalf("weather = %q", got.Weather)
	}
	if regeoQuery.Get("location") != "116.480881,39.985876" || regeoQuery.Get("extensions") != "all" ||
		regeoQuery.Get("key") != "test-amap-key" {
		t.Fatalf("regeo query = %v", regeoQuery)
	}
	if weatherQuery.Get("city") != "141002" || weatherQuery.Get("extensions") != "base" {
		t.Fatalf("weather query = %v", weatherQuery)
	}
}

// TestAMapFallbacks 地标回退：POI 超距用乡镇、无乡镇用街道。
func TestAMapFallbacks(t *testing.T) {
	var regeoResp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			_, _ = w.Write([]byte(regeoResp))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status": "1", "lives": [{"weather": "晴", "temperature": "10" }]}`))
		}
	}))
	defer srv.Close()
	svc := amapTestService(srv.URL)

	// POI 距离超上限 → 乡镇
	regeoResp = `{"status": "1", "regeocode": {
		"addressComponent": {"district": "尧都区", "township": "段店乡", "adcode": "141002"},
		"pois": [{"name": "较远地标", "distance": "500"}]}}`
	if got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "尧都区·段店乡" {
		t.Fatalf("乡镇回退 location = %q", got.Location)
	}

	// 无乡镇 → streetNumber.street
	regeoResp = `{"status": "1", "regeocode": {
		"addressComponent": {"district": "尧都区", "streetNumber": {"street": "解放东路"}, "adcode": "141002"}}}`
	if got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "尧都区·解放东路" {
		t.Fatalf("街道回退 location = %q", got.Location)
	}

	// district 缺失回退 city；POI 最近者取最小距离
	regeoResp = `{"status": "1", "regeocode": {
		"addressComponent": {"city": "临汾市", "township": "x", "adcode": "141002"},
		"pois": [{"name": "B", "distance": "30"}, {"name": "A", "distance": "10"}]}}`
	if got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "临汾市·A" {
		t.Fatalf("最近 POI location = %q", got.Location)
	}
}

// TestAMapFailures 失败留空：逆编码失败全空、天气失败留地点、非法坐标不发请求。
func TestAMapFailures(t *testing.T) {
	var calls int
	var regeoResp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			_, _ = w.Write([]byte(regeoResp))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status": "0", "infocode": "INVALID_USER_KEY"}`))
		}
	}))
	defer srv.Close()
	svc := amapTestService(srv.URL)

	regeoResp = `{"status": "0", "info": "INVALID_USER_KEY"}`
	if got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "" || got.Weather != "" {
		t.Fatalf("逆编码失败 = %+v，期望空", got)
	}

	regeoResp = `{"status": "1", "regeocode": {
		"addressComponent": {"district": "尧都区", "township": "段店乡", "adcode": "141002"}}}`
	got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98")
	if got.Location != "尧都区·段店乡" || got.Weather != "" {
		t.Fatalf("天气失败 = %+v", got)
	}

	before := calls
	for _, pair := range [][2]string{{"abc", "39.9"}, {"200", "39.9"}, {"116", ""}} {
		if g := svc.FetchLocationWeather(context.Background(), pair[0], pair[1]); g.Location != "" || g.Weather != "" {
			t.Fatalf("非法坐标 %v 应留空，got %+v", pair, g)
		}
	}
	if calls != before {
		t.Fatalf("非法坐标不应发请求，调用 +%d", calls-before)
	}

	// 未配置 key：不发请求
	empty := &AMapService{Cfg: NewMapProvider(nil)}
	if got := empty.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "" {
		t.Fatalf("未配置 = %+v", got)
	}
}

// TestAMapNegativeTempRounding 负温取整对齐 JS Math.round（向 +∞ 半数进位）。
func TestAMapNegativeTempRounding(t *testing.T) {
	var temp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			_, _ = w.Write([]byte(`{"status": "1", "regeocode": {
				"addressComponent": {"district": "d", "township": "t", "adcode": "1"}}}`))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status": "1", "lives": [{"weather": "小雪", "temperature": "` + temp + `"}]}`))
		}
	}))
	defer srv.Close()
	svc := amapTestService(srv.URL)
	ctx := context.Background()

	temp = "-1.6"
	if got := svc.FetchLocationWeather(ctx, "116.48", "39.98").Weather; got != "小雪 -2°C" {
		t.Fatalf("-1.6 → %q，期望 小雪 -2°C", got)
	}
	temp = "-1.5"
	if got := svc.FetchLocationWeather(ctx, "116.48", "39.98").Weather; got != "小雪 -1°C" {
		t.Fatalf("-1.5 → %q，期望 小雪 -1°C", got)
	}
}

// TestAMapHTTPTimeout 客户端超时不抛出，返回空值。
func TestAMapHTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()
	svc := amapTestService(srv.URL)
	svc.HTTP = &http.Client{Timeout: 20 * time.Millisecond}
	if got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "" || got.Weather != "" {
		t.Fatalf("超时应留空，got %+v", got)
	}
}
