package sgccclockin

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

type geoResult struct {
	Position  string `json:"position"`
	CityCode  string `json:"cityCode"`
	CityName  string `json:"cityName"`
	Longitude string `json:"longitude"`
	Latitude  string `json:"latitude"`
}

func (m *Module) geo(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enter(w, r); !ok {
		return
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address != "" {
		var forward struct {
			Status   string `json:"status"`
			Geocodes []struct {
				Adcode   string `json:"adcode"`
				Location string `json:"location"`
			} `json:"geocodes"`
		}
		q := url.Values{"address": {truncateStr(address, 120)}}
		if region := strings.TrimSpace(r.URL.Query().Get("region")); region != "" {
			q.Set("city", truncateStr(region, 32))
		}
		if err := m.geoGET(r.Context(), "/v3/geocode/geo", q, &forward); err != nil {
			writeErr(w, err)
			return
		}
		if forward.Status != "1" || len(forward.Geocodes) == 0 {
			writeOK(w, geoResult{})
			return
		}
		coords := strings.Split(forward.Geocodes[0].Location, ",")
		if len(coords) != 2 {
			writeOK(w, geoResult{})
			return
		}
		lng, err1 := strconv.ParseFloat(coords[0], 64)
		lat, err2 := strconv.ParseFloat(coords[1], 64)
		if err1 != nil || err2 != nil {
			writeOK(w, geoResult{})
			return
		}
		result, err := m.reverseGeo(r.Context(), lng, lat)
		if err != nil {
			writeErr(w, err)
			return
		}
		if result.CityCode == "" {
			result.CityCode = forward.Geocodes[0].Adcode
		}
		writeOK(w, result)
		return
	}
	lng, err1 := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lng")), 64)
	lat, err2 := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lat")), 64)
	if err1 != nil || err2 != nil || lng < -180 || lng > 180 || lat < -90 || lat > 90 {
		fail(w, http.StatusBadRequest, 40040, "经纬度参数无效")
		return
	}
	result, err := m.reverseGeo(r.Context(), lng, lat)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (m *Module) geoGET(ctx context.Context, path string, q url.Values, out any) error {
	key := cfgValue(ctx, m.Cfg, CfgAMapKey)
	if key == "" {
		return nil
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("key", key)
	base := cfgValue(ctx, m.Cfg, CfgAMapBase)
	if base == "" {
		base = "https://restapi.amap.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	client := m.GeoHTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("高德接口返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

func (m *Module) reverseGeo(ctx context.Context, lng, lat float64) (geoResult, error) {
	result := geoResult{Longitude: strconv.FormatFloat(lng, 'f', 6, 64), Latitude: strconv.FormatFloat(lat, 'f', 6, 64)}
	var body struct {
		Status string `json:"status"`
		Regeo  struct {
			Address map[string]any `json:"addressComponent"`
		} `json:"regeocode"`
	}
	if err := m.geoGET(ctx, "/v3/geocode/regeo", url.Values{"location": {result.Longitude + "," + result.Latitude}}, &body); err != nil {
		return geoResult{}, err
	}
	if body.Status != "1" {
		return geoResult{}, nil
	}
	ac := body.Regeo.Address
	get := func(key string) string {
		v, _ := ac[key].(string)
		return strings.TrimSpace(v)
	}
	city, district := get("city"), get("district")
	result.CityCode, result.CityName = get("adcode"), city
	if result.CityName == "" {
		result.CityName = district
	}
	if city == get("province") {
		city = ""
	}
	if district == get("city") {
		district = ""
	}
	street := ""
	if number, ok := ac["streetNumber"].(map[string]any); ok {
		street, _ = number["street"].(string)
	}
	township := get("township")
	if township == street {
		township = ""
	}
	for _, part := range []string{get("country"), get("province"), city, district, township, street} {
		result.Position += part
	}
	return result, nil
}
