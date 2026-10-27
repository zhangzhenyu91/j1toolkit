// 高德「地点 + 天气」地理服务（协议对齐 server/src/worklog/geo.js）：
// 逆地理编码 /v3/geocode/regeo（extensions=all）拼「区县·地标」，实时天气 /v3/weather/weatherInfo
// （extensions=base）按 adcode 查询拼「天气 温度」（如 多云 32°C）；两步串行，任何一步失败留空
// （前端留空手填），不抛出。鉴权 query 参数 key（「Web 服务」类型）；base URL 热读取，
// 因费用走中转站时改 AMAP_BASE_URL 即可（接口路径 /v3/... 不变）。
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"shade/internal/apps/worklog"
)

// 高德配置键默认名（对齐 server/src/config.js）。
const (
	DefaultAMapKeyKey     = "AMAP_MAP_KEY"
	DefaultAMapBaseURLKey = "AMAP_BASE_URL"
)

// defaultAMapBaseURL 官方接口地址。
const defaultAMapBaseURL = "https://restapi.amap.com"

// defaultAMapTimeout 单步超时（对齐 Node timeout: 8000）。
const defaultAMapTimeout = 8 * time.Second

// poiMaxDistM 最近 POI 距离上限（米）：超出回退乡镇/街道（取范围最小一级，对齐 Node POI_MAX_DIST）。
const poiMaxDistM = 200

// AMapService 高德 Web 服务实现（regeo + weatherInfo 两步串行）。满足 worklog.GeoService。
type AMapService struct {
	// Cfg 配置热读取（nil 视为未配置，返回空值）。
	Cfg Provider
	// KeyKey 高德 key 配置键（默认 AMAP_MAP_KEY）。
	KeyKey string
	// BaseURLKey 接口基地址配置键（默认 AMAP_BASE_URL，空值用官方地址）。
	BaseURLKey string
	// HTTP 传输客户端（nil 用默认 8s 超时）。
	HTTP *http.Client
	// Recorder 外部调用结构化事件写入口（可注入 *logging.Logger；nil 不写日志）。
	Recorder Recorder
}

// record 写本服务的外部调用事件（Recorder nil 静默）。
func (s *AMapService) record(ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	if s == nil {
		return
	}
	record(s.Recorder, ctx, level, event, messageCN, raw, fields)
}

// httpc 取传输客户端。
func (s *AMapService) httpc() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: defaultAMapTimeout}
}

// FetchLocationWeather 取「地点 + 天气」；任何失败返回空值（前端留空手填），不抛出。
// 完成后经 Recorder 写结构化事件（成功 info / 失败 warn + 分类，AMap 软失败不抛），nil 不写。
func (s *AMapService) FetchLocationWeather(ctx context.Context, lng, lat string) worklog.LocationWeather {
	result, class, failRaw := s.fetchLocationWeather(ctx, lng, lat)
	if class == "" {
		s.record(ctx, "info", "amap.location_weather", "高德地点天气查询成功",
			fmt.Sprintf("[amap] location+weather ok location=%q weather=%q", result.Location, result.Weather),
			map[string]any{"lng": lng, "lat": lat, "location": result.Location, "weather": result.Weather})
		return result
	}
	s.record(ctx, "warn", "amap.location_weather.failed",
		"高德地点天气查询失败（"+errorClassCN(class)+"）",
		fmt.Sprintf("[amap] location+weather failed: %s", failRaw),
		map[string]any{"lng": lng, "lat": lat, "error_class": class})
	return result
}

// fetchLocationWeather 主流程；失败返回空值 + 稳定分类与技术原文（供事件记录），不抛出。
func (s *AMapService) fetchLocationWeather(ctx context.Context, lng, lat string) (worklog.LocationWeather, string, string) {
	empty := worklog.LocationWeather{}
	key := Lookup(ctx, s.Cfg, cfgKey(s.KeyKey, DefaultAMapKeyKey))
	if key == "" {
		return empty, classNotConfigured, ""
	}
	lngF, err1 := strconv.ParseFloat(strings.TrimSpace(lng), 64)
	latF, err2 := strconv.ParseFloat(strings.TrimSpace(lat), 64)
	if err1 != nil || err2 != nil {
		return empty, classInvalidInput, ""
	}
	if lngF < -180 || lngF > 180 || latF < -90 || latF > 90 {
		return empty, classInvalidInput, ""
	}
	base := Lookup(ctx, s.Cfg, cfgKey(s.BaseURLKey, DefaultAMapBaseURLKey))
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = defaultAMapBaseURL
	}
	// 高德 location 为「经度,纬度」，小数点后不超 6 位
	coord := fmt.Sprintf("%.6f,%.6f", lngF, latF)

	var regeo map[string]any
	if err := s.getJSON(ctx, base+"/v3/geocode/regeo", map[string]string{
		"location": coord, "extensions": "all", "key": key,
	}, &regeo); err != nil {
		return empty, errorClass(err), err.Error()
	}
	if fmt.Sprint(regeo["status"]) != "1" {
		return empty, classAPIError, fmt.Sprintf("regeo status=%v", regeo["status"])
	}
	rg, _ := regeo["regeocode"].(map[string]any)
	ac, _ := rg["addressComponent"].(map[string]any)
	if ac == nil {
		return empty, classAPIError, "regeo missing addressComponent"
	}
	gstr := func(v any) string {
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	// 区县取 district（空回退 city）
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
	// 地标 = 最近 POI（≤poiMaxDistM 米），回退 乡镇/街道 → 道路 streetNumber.street
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

	// 实时天气按 adcode 查询（逆编码失败则天气一并留空）
	weather := ""
	adcode := gstr(ac["adcode"])
	if adcode != "" {
		var wd map[string]any
		if err := s.getJSON(ctx, base+"/v3/weather/weatherInfo", map[string]string{
			"city": adcode, "extensions": "base", "key": key,
		}, &wd); err == nil && fmt.Sprint(wd["status"]) == "1" {
			if lives, ok := wd["lives"].([]any); ok && len(lives) > 0 {
				if live, ok := lives[0].(map[string]any); ok {
					w := gstr(live["weather"])
					t, _ := strconv.ParseFloat(gstr(live["temperature"]), 64)
					if w != "" {
						// 温度取整对齐 Node Math.round（四舍五入向 +∞，负温同口径）
						weather = fmt.Sprintf("%s %d°C", w, int(math.Floor(t+0.5)))
					}
				}
			}
		}
	}
	if location == "" && weather == "" {
		return empty, classEmptyResult, ""
	}
	return worklog.LocationWeather{Location: location, Weather: weather}, "", ""
}

// getJSON GET 查询参数编码后取 JSON。
func (s *AMapService) getJSON(ctx context.Context, endpoint string, params map[string]string, out any) error {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := s.httpc().Do(req)
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
