// 地理服务（对应 server/src/worklog/geo.js）：高德「地点 + 天气」取值——逆地理编码拼「区县·地标」、
// 实时天气拼「天气 温度」；接口可注入，HTTP 实现不实连（测试用 fake）。
package worklog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AMapGeoService 高德 Web 服务实现（regeo + weatherInfo 两步串行）。
type AMapGeoService struct {
	BaseURL string // AMAP_BASE_URL（默认 https://restapi.amap.com）
	APIKey  string // 高德 Web 服务 key
	HTTP    *http.Client
}

var _ GeoService = (*AMapGeoService)(nil)

// poiMaxDistM 最近 POI 距离上限（米）：超出回退乡镇/街道。
const poiMaxDistM = 200

func (g *AMapGeoService) httpc() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (g *AMapGeoService) base() string {
	if g.BaseURL != "" {
		return strings.TrimRight(g.BaseURL, "/")
	}
	return "https://restapi.amap.com"
}

// FetchLocationWeather 逆编码 + 天气；任何失败返回空值（前端留空手填）。
func (g *AMapGeoService) FetchLocationWeather(ctx context.Context, lng, lat string) LocationWeather {
	empty := LocationWeather{}
	if g.APIKey == "" {
		return empty
	}
	lngF, err1 := strconv.ParseFloat(strings.TrimSpace(lng), 64)
	latF, err2 := strconv.ParseFloat(strings.TrimSpace(lat), 64)
	if err1 != nil || err2 != nil {
		return empty
	}
	if lngF < -180 || lngF > 180 || latF < -90 || latF > 90 {
		return empty
	}
	coord := fmt.Sprintf("%.6f,%.6f", lngF, latF) // 高德：经度在前
	regeo := map[string]any{}
	if err := g.getJSON(ctx, "/v3/geocode/regeo", map[string]string{
		"location": coord, "extensions": "all", "key": g.APIKey,
	}, &regeo); err != nil {
		return empty
	}
	if fmt.Sprint(regeo["status"]) != "1" {
		return empty
	}
	rg, _ := regeo["regeocode"].(map[string]any)
	ac, _ := rg["addressComponent"].(map[string]any)
	if ac == nil {
		return empty
	}
	gstr := func(v any) string {
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	area := gstr(ac["district"])
	if area == "" {
		area = gstr(ac["city"])
	}
	// pois 不保证按距离升序，显式取距离最小者
	var nearestName string
	nearestDist := 0.0
	if pois, ok := rg["pois"].([]any); ok {
		for _, item := range pois {
			p, _ := item.(map[string]any)
			if p == nil {
				continue
			}
			name := gstr(p["name"])
			dist, err := strconv.ParseFloat(gstr(p["distance"]), 64)
			if name == "" || err != nil {
				continue
			}
			if nearestName == "" || dist < nearestDist {
				nearestName, nearestDist = name, dist
			}
		}
	}
	detail := ""
	if nearestName != "" && nearestDist <= poiMaxDistM {
		detail = nearestName
	}
	if detail == "" {
		detail = gstr(ac["township"])
	}
	if detail == "" {
		if sn, ok := ac["streetNumber"].(map[string]any); ok {
			detail = gstr(sn["street"])
		}
	}
	location := ""
	switch {
	case area != "" && detail != "":
		location = area + "·" + detail
	case area != "":
		location = area
	default:
		location = detail
	}

	weather := ""
	adcode := gstr(ac["adcode"])
	if adcode != "" {
		var wd map[string]any
		if err := g.getJSON(ctx, "/v3/weather/weatherInfo", map[string]string{
			"city": adcode, "extensions": "base", "key": g.APIKey,
		}, &wd); err == nil && fmt.Sprint(wd["status"]) == "1" {
			if lives, ok := wd["lives"].([]any); ok && len(lives) > 0 {
				if live, ok := lives[0].(map[string]any); ok {
					w := gstr(live["weather"])
					t, _ := strconv.ParseFloat(gstr(live["temperature"]), 64)
					if w != "" {
						weather = fmt.Sprintf("%s %d°C", w, int(t+0.5))
					}
				}
			}
		}
	}
	return LocationWeather{Location: location, Weather: weather}
}

// getJSON GET 查询参数编码后取 JSON。
func (g *AMapGeoService) getJSON(ctx context.Context, path string, params map[string]string, out any) error {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base()+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := g.httpc().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
