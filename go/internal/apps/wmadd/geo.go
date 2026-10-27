// 地理服务：高德「地点 + 天气」取值（对齐 server/src/worklog/geo.js）。
// 逆地理编码 /v3/geocode/regeo（location 为「经度,纬度」，小数点后不超 6 位）、
// 实时天气 /v3/weather/weatherInfo（按 adcode 两步串行；逆编码失败则天气一并留空）。
// 任何一步失败均返回空串（前端留空手填），不抛出；客户端可注入（测试用 httptest，不实连高德）。
package wmadd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// LocationWeather 地理取值结果（location/weather 失败或未配置时为空串）。
type LocationWeather struct {
	Location string `json:"location"`
	Weather  string `json:"weather"`
}

// GeoClient 地理服务适配层（高德实现见 AMapClient；测试可注入桩）。
type GeoClient interface {
	FetchLocationWeather(ctx context.Context, lng, lat float64) (LocationWeather, error)
}

// ConfigReader 运行时配置读取（*config.Store 天然满足；测试可注入桩）。
// AMAP_MAP_KEY / AMAP_BASE_URL 每次请求动态读取，密钥不硬编码、配置中心改值即生效。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// AMapClient 高德 Web 服务客户端（密钥经构造注入或 Cfg 动态读取，不硬编码）。
type AMapClient struct {
	Key     string       // AMAP_MAP_KEY 固定缺省（Cfg 未接入或取值为空时用）
	BaseURL string       // AMAP_BASE_URL 固定缺省（同上；缺省 https://restapi.amap.com）
	Cfg     ConfigReader // 运行时配置（可注入；nil 用固定值）
	HTTP    *http.Client // 可注入；nil 用 8s 超时默认
}

// 编译期确认 AMapClient 满足 GeoClient（可装配）。
var _ GeoClient = (*AMapClient)(nil)

// NewAMapClient 构造高德客户端（baseURL 空取默认；尾随 / 自动去除）。
func NewAMapClient(key, baseURL string) *AMapClient {
	return &AMapClient{Key: key, BaseURL: normalizeBase(baseURL)}
}

// normalizeBase 归一接口 base URL：去尾随 /，空取官方地址。
func normalizeBase(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://restapi.amap.com"
	}
	return baseURL
}

// Configured 是否已配置 key（未配置恒回空串，与 Node configured() 口径一致；仅看固定缺省，
// 动态取值以 FetchLocationWeather 内 resolve 为准）。
func (c *AMapClient) Configured() bool { return c != nil && c.Key != "" }

// resolve 解析当前生效 key 与 base URL：Cfg（AMAP_MAP_KEY / AMAP_BASE_URL）优先，空值/读取失败回退固定值。
func (c *AMapClient) resolve(ctx context.Context) (string, string) {
	key, base := strings.TrimSpace(c.Key), normalizeBase(c.BaseURL)
	if c.Cfg != nil {
		if v, err := c.Cfg.Get(ctx, "AMAP_MAP_KEY"); err == nil && strings.TrimSpace(v) != "" {
			key = strings.TrimSpace(v)
		}
		if v, err := c.Cfg.Get(ctx, "AMAP_BASE_URL"); err == nil && strings.TrimSpace(v) != "" {
			base = normalizeBase(v)
		}
	}
	return key, base
}

// 最近 POI 距离上限（米）：取范围最小一级中距离最近者为地标，超出则回退乡镇/街道（道路不取值）。
const poiMaxDist = 200

// regeoResp /v3/geocode/regeo 响应（只取用到的字段）。
type regeoResp struct {
	Status    string `json:"status"`
	Info      string `json:"info"`
	Regeocode *struct {
		AddressComponent struct {
			Adcode       string `json:"adcode"`
			District     string `json:"district"`
			City         string `json:"city"`
			Township     string `json:"township"`
			StreetNumber struct {
				Street string `json:"street"`
			} `json:"streetNumber"`
		} `json:"addressComponent"`
		Pois []struct {
			Name     string `json:"name"`
			Distance string `json:"distance"`
		} `json:"pois"`
	} `json:"regeocode"`
}

// weatherResp /v3/weather/weatherInfo 响应（base 模式 lives）。
type weatherResp struct {
	Status string `json:"status"`
	Lives  []struct {
		Weather     string `json:"weather"`
		Temperature string `json:"temperature"`
	} `json:"lives"`
}

// FetchLocationWeather 按经纬度取 { location, weather }；未配置或任何一步失败均返回空串，不抛出。
// location 格式：区县·具体位置（如 尧都区·临汾电力高级技工学校）；weather 格式：天气 温度（如 多云 32°C）。
func (c *AMapClient) FetchLocationWeather(ctx context.Context, lng, lat float64) (LocationWeather, error) {
	empty := LocationWeather{}
	key, base := c.resolve(ctx)
	if key == "" {
		return empty, nil
	}
	coord := fmt.Sprintf("%.6f,%.6f", lng, lat) // 高德：经度在前
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	// 逆地理编码：地点拼「区县·地标」——区县取 district（空回退 city）；
	// 地标 = 距离最近的 POI（extensions=all；距离超 poiMaxDist 米不用），回退 乡镇/街道 → 道路
	var rg regeoResp
	q := url.Values{}
	q.Set("location", coord)
	q.Set("extensions", "all")
	q.Set("key", key)
	if err := c.getJSON(ctx, client, base, "/v3/geocode/regeo", q, &rg); err != nil || rg.Status != "1" || rg.Regeocode == nil {
		return empty, nil // 失败留空，前端手填
	}
	ac := rg.Regeocode.AddressComponent
	area := gstr(ac.District)
	if area == "" {
		area = gstr(ac.City)
	}
	var nearestName string
	nearestDist := 0.0
	for _, p := range rg.Regeocode.Pois {
		name := gstr(p.Name)
		dist, err := strconv.ParseFloat(gstr(p.Distance), 64)
		if name == "" || err != nil {
			continue
		}
		if nearestName == "" || dist < nearestDist {
			nearestName, nearestDist = name, dist
		}
	}
	detail := ""
	if nearestName != "" && nearestDist <= poiMaxDist {
		detail = nearestName
	}
	if detail == "" {
		detail = gstr(ac.Township)
	}
	if detail == "" {
		detail = gstr(ac.StreetNumber.Street)
	}
	location := area
	switch {
	case area != "" && detail != "":
		location = area + "·" + detail
	case detail != "":
		location = detail
	}

	// 实时天气：按逆编码所得 adcode 查询（extensions=base），拼「天气 温度」（温度四舍五入）
	weather := ""
	adcode := gstr(ac.Adcode)
	if adcode != "" {
		var wr weatherResp
		q2 := url.Values{}
		q2.Set("city", adcode)
		q2.Set("extensions", "base")
		q2.Set("key", key)
		if err := c.getJSON(ctx, client, base, "/v3/weather/weatherInfo", q2, &wr); err == nil && wr.Status == "1" && len(wr.Lives) > 0 {
			w := gstr(wr.Lives[0].Weather)
			t := gstr(wr.Lives[0].Temperature)
			if w != "" {
				if n, err := strconv.ParseFloat(t, 64); err == nil {
					weather = w + " " + strconv.FormatFloat(math.Round(n), 'f', 0, 64) + "°C"
				}
			}
		}
	}
	return LocationWeather{Location: location, Weather: weather}, nil
}

// getJSON GET 并解析 JSON 响应（非 2xx / 解析失败返回错误，由调用方按留空口径吞掉）。
func (c *AMapClient) getJSON(ctx context.Context, client *http.Client, base, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("amap: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// gstr 高德空值字段返回空数组（[]）而非空串，统一收敛为去首尾空格字符串。
func gstr(v string) string { return strings.TrimSpace(v) }
