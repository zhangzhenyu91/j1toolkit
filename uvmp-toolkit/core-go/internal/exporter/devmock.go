package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

// devmock 原生移植（toolkit/devmock.py）：dev.mock=true 时替代真实派车接口，
// 当日 7 单 + 昨日 2 单；FakeClient 查询/轨迹响应形状（pcdmanager 分页查询含
// runCode/日期过滤、getPrintDetails、轨迹点查询、行程段查询）逐字保留。

// mkOrder 单条模拟单据（devmock._mk_order 逐字段移植）。
func mkOrder(idx int, day time.Time, runState string) map[string]any {
	begin := time.Date(day.Year(), day.Month(), day.Day(), 8+idx%8, 30, 0, 0, day.Location())
	end := begin.Add(time.Duration(2+idx%4) * time.Hour)
	oid := fmt.Sprintf("MOCK%04d", idx)
	realTime := ""
	if runState == "2" {
		realTime = begin.Format("2006-01-02 15:04:05") + ".0"
	}
	return map[string]any{
		"id":                oid,
		"runCode":           fmt.Sprintf("PCD%s%02d", day.Format("20060102"), idx),
		"vehicleId":         fmt.Sprintf("V%04d", idx%3+1),
		"vehicleNumber":     fmt.Sprintf("晋AD%05d", 10000+idx),
		"licensePlateColor": "黄",
		"driverName":        fmt.Sprintf("测试司机%d", idx),
		"driverTel":         fmt.Sprintf("1380000%04d", idx),
		"userName":          fmt.Sprintf("测试用车人%d", idx),
		"userTel":           fmt.Sprintf("1390000%04d", idx),
		"postLevel":         "高级技师",
		"departName":        "检修一班",
		"isOutTag":          "否",
		"vehicleProperty":   "生产用车",
		"vehicleType":       "小型普通客车",
		"powerType":         "燃油",
		"planSendTime":      begin.Format("2006-01-02 15:04:05"),
		"planBackTime":      end.Format("2006-01-02 15:04:05"),
		"planSendTimeStr":   begin.Format("2006-01-02 15:04:05"),
		"planBackTimeStr":   end.Format("2006-01-02 15:04:05"),
		"planUseTime":       2 + idx%4,
		"frmAddr":           "单位大院",
		"crossAddr":         "",
		"toAddr":            fmt.Sprintf("测试现场%d", idx),
		"useReason":         "检修作业",
		"useApplyRemark":    "",
		"dispatchName":      "测试派车人",
		"dispatchTime":      begin.AddDate(0, 0, -1).UnixMilli(),
		"runState":          runState,
		"orgName":           "国网山西检修公司",
		"fixedNumbers":      "3",
		"oilType":           "02",
		"realSendTime":      realTime,
		"realBackTime":      realTimeOf(runState, end),
		"realSendTimeStr":   realStrOf(runState, begin),
		"realBackTimeStr":   realStrOf(runState, end),
		"startMiles":        12000 + idx*100,
		"endMiles":          12000 + idx*100 + 87,
		"totalMiles":        87,
		"realNumber":        2,
		"oilFee":            "",
		"oilUse":            "",
		"oilPrice":          "",
		"sxCardNumber":      "",
		"roadbridgeFee":     "",
		"crossAddrRegister": "",
		"toAddrRegister":    "",
		"remark":            "",
		"audinList": []any{map[string]any{
			"wfInstanceName": "部门审批",
			"operatorName":   "测试班长",
			"operatorType":   "审批",
			"operateTime":    begin.Add(-2 * time.Hour).Format("2006-01-02 15:04:05"),
		}},
	}
}

func realTimeOf(runState string, t time.Time) string {
	if runState == "2" {
		return t.Format("2006-01-02 15:04:05") + ".0"
	}
	return ""
}

func realStrOf(runState string, t time.Time) string {
	if runState == "2" {
		return t.Format("2006-01-02 15:04:05")
	}
	return ""
}

// makeMockItems 当日 7 单 + 昨日 2 单（devmock.make_items）：
// 当日 1-5 已完结、6 已派车未完结、7 待派车；昨日 11、12 已完结。
func makeMockItems(today time.Time) []map[string]any {
	var items []map[string]any
	for i := 1; i <= 5; i++ {
		items = append(items, mkOrder(i, today, "2"))
	}
	items = append(items, mkOrder(6, today, "1")) // 已派车未完结：每日同步要包含，轨迹导出应跳过
	items = append(items, mkOrder(7, today, "0")) // 待派车
	items = append(items, mkOrder(11, today.AddDate(0, 0, -1), "2"))
	items = append(items, mkOrder(12, today.AddDate(0, 0, -1), "2"))
	return items
}

// FakeAPI 模拟派车接口（devmock.FakeClient 同形状：get 分发 + 响应信封）。
type FakeAPI struct {
	mu    sync.Mutex
	Items []map[string]any
	Delay time.Duration // 每次 get 模拟网络耗时（devmock 默认 50ms，测试可置 0）

	QueryCalls   int
	DetailsCalls int
	PointsCalls  int
	SegmentCalls int
}

// NewMockAPI dev.mock=true 用：当日 7 + 昨日 2，50ms 网络耗时模拟。
func NewMockAPI() *FakeAPI {
	return NewFakeAPIAt(time.Now())
}

// NewFakeAPIAt 固定“当日”构造（测试可复现）。
func NewFakeAPIAt(today time.Time) *FakeAPI {
	return &FakeAPI{Items: makeMockItems(today), Delay: 50 * time.Millisecond}
}

func (f *FakeAPI) sleep() {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
}

// ---------------------------------------------------------------- FakeClient 响应形状

// respQueryPcd pcdmanager 分页查询（FakeClient._query_pcd）。
func (f *FakeAPI) respQueryPcd(pData map[string]string) map[string]any {
	params := map[string]any{}
	_ = json.Unmarshal([]byte(pData["params"]), &params)
	flt := parseFilter(pData["params"])
	pageIndex := 1
	if v, ok := params["pageIndex"].(float64); ok {
		pageIndex = int(v)
	}
	pageSize := 50
	if v, ok := params["pageSize"].(float64); ok {
		pageSize = int(v)
	}
	items := f.Items
	if rc := flt["runCode"]; rc != "" {
		var out []map[string]any
		for _, it := range items {
			if strAny(it["runCode"]) == rc {
				out = append(out, it)
			}
		}
		items = out
	}
	d1 := cutLen(flt["planSendTime"], 10)
	d2 := cutLen(flt["planSendTime2"], 10)
	if d1 != "" {
		var out []map[string]any
		for _, it := range items {
			if cutLen(strAny(it["planSendTime"]), 10) >= d1 {
				out = append(out, it)
			}
		}
		items = out
	}
	if d2 != "" {
		var out []map[string]any
		for _, it := range items {
			if cutLen(strAny(it["planSendTime"]), 10) <= d2 {
				out = append(out, it)
			}
		}
		items = out
	}
	if rs := flt["runState"]; rs != "" {
		var out []map[string]any
		for _, it := range items {
			if strAny(it["runState"]) == rs {
				out = append(out, it)
			}
		}
		items = out
	}
	total := len(items)
	lo := (pageIndex - 1) * pageSize
	if lo > total {
		lo = total
	}
	hi := lo + pageSize
	if hi > total {
		hi = total
	}
	page := items[lo:hi]
	pageAny := make([]any, 0, len(page))
	for _, it := range page {
		pageAny = append(pageAny, it)
	}
	return map[string]any{
		"successful": true,
		"resultValue": map[string]any{
			"itemCount": total,
			"items":     pageAny,
		},
	}
}

// parseFilter params JSON 里的 "filter" 串 → 键值表（devmock._parse_filter）。
func parseFilter(paramsStr string) map[string]string {
	out := map[string]string{}
	var obj struct {
		Filter string `json:"filter"`
	}
	if json.Unmarshal([]byte(paramsStr), &obj) != nil {
		return out
	}
	for _, kv := range strings.Split(obj.Filter, "&") {
		k, _, v, ok := cut3(kv, "=")
		if ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func cut3(s, sep string) (string, string, string, bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, "", "", false
	}
	return s[:i], sep, s[i+len(sep):], true
}

// respPrintDetails 打印详情（FakeClient._print_details：base_path 取 id= 逗号表）。
func (f *FakeAPI) respPrintDetails(basePath string) map[string]any {
	idsPart := ""
	if i := strings.Index(basePath, "id="); i >= 0 {
		rest := basePath[i+3:]
		if j := strings.Index(rest, "&"); j >= 0 {
			rest = rest[:j]
		}
		idsPart = rest
	}
	var ids []string
	for _, x := range strings.Split(idsPart, ",") {
		if x != "" {
			ids = append(ids, x)
		}
	}
	byID := map[string]map[string]any{}
	for _, it := range f.Items {
		byID[strAny(it["id"])] = it
	}
	var got []any
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			got = append(got, it)
		}
	}
	return map[string]any{
		"successful":  true,
		"resultValue": map[string]any{"items": got},
	}
}

// respPoints 轨迹点（FakeClient._points：40 点，2 分钟间隔）。
func (f *FakeAPI) respPoints(pData map[string]string) map[string]any {
	begin := cutLen(pData["beginTimeStr"], 19)
	if begin == "" {
		begin = "2026-09-23 08:30:00"
	}
	t0, err := time.ParseInLocation("2006-01-02 15:04:05", begin, time.Local)
	if err != nil {
		now := time.Now()
		t0 = time.Date(now.Year(), now.Month(), now.Day(), 8, 30, 0, 0, now.Location())
	}
	pts := make([]any, 0, 40)
	for i := 0; i < 40; i++ {
		t := t0.Add(time.Duration(2*i) * time.Minute)
		pts = append(pts, map[string]any{
			"dx":         roundTo(112.548+float64(i)*0.006, 6),
			"dy":         roundTo(37.857+float64(i)*0.004, 6),
			"speed":      30 + i%25,
			"direction":  (45 + i*3) % 360,
			"locatetime": t.Format("2006-01-02 15:04:05"),
		})
	}
	return map[string]any{"successful": true, "data": pts}
}

// respSegments 行程段（FakeClient._segments：仅已完结且有实际出车时间的单）。
func (f *FakeAPI) respSegments() map[string]any {
	var segs []any
	for _, it := range f.Items {
		if strAny(it["runState"]) != "2" || strAny(it["realSendTime"]) == "" {
			continue
		}
		segs = append(segs, map[string]any{
			"id":            "SEG-" + strAny(it["id"]),
			"vehicleId":     it["vehicleId"],
			"vehicleNumber": it["vehicleNumber"],
			"routeNo":       "R" + strAny(it["id"]),
			"starttime":     it["realSendTimeStr"],
			"endtime":       it["realBackTimeStr"],
			"miles":         it["totalMiles"],
			"startX":        112.548,
			"startY":        37.857,
			"endX":          112.782,
			"endY":          38.013,
		})
	}
	return map[string]any{
		"successful":  true,
		"resultValue": map[string]any{"itemCount": len(segs), "items": segs},
	}
}

// fakeGet FakeClient.get 同分发（devmock.FakeClient.get）。
func (f *FakeAPI) fakeGet(basePath string, pData map[string]string) (map[string]any, error) {
	f.sleep()
	switch {
	case strings.Contains(basePath, "getPrintDetails"):
		return f.respPrintDetails(basePath), nil
	case strings.Contains(basePath, "queryTrackHisByVehicle"):
		return f.respPoints(pData), nil
	case strings.Contains(basePath, "simpleQuery"):
		return f.respSegments(), nil
	case strings.Contains(basePath, "pcdmanager/"):
		return f.respQueryPcd(pData), nil
	}
	return nil, fmt.Errorf("FakeAPI 未模拟的接口: %s", basePath)
}

// ---------------------------------------------------------------- OrderAPI 实现

// queryPcdAllFilter 自由 filter 全量页（uvmpapi.QueryPcdAllFilter 同口径）。
func (f *FakeAPI) queryPcdAllFilter(ctx context.Context, filterStr string, pageSize int) ([]map[string]any, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	var items []map[string]any
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.QueryCalls++
		f.mu.Unlock()
		params, _ := json.Marshal(map[string]any{"pageIndex": page, "pageSize": pageSize, "filter": filterStr})
		data, err := f.fakeGet("running-service-provider/pcdmanager/", map[string]string{"params": string(params)})
		if err != nil {
			return nil, err
		}
		rv, _ := data["resultValue"].(map[string]any)
		batch := anyList(rv["items"])
		items = append(items, batch...)
		total := 0
		if v, ok := rv["itemCount"].(int); ok {
			total = v
		}
		if len(items) >= total || len(batch) == 0 {
			return items, nil
		}
	}
}

// QueryPcdByRunCode 按单号查找（uvmpapi.QueryPcdByRunCode 同算法：runCode 直查 +
// 日期范围兜底 + 客户端精确匹配取 planSendTime 最晚）。
func (f *FakeAPI) QueryPcdByRunCode(ctx context.Context, code, dateHint string, lookbackDays int, log uvmpapi.LogFunc) (uvmpapi.Dict, bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, false, nil
	}
	pick := func(items []map[string]any) map[string]any {
		var best map[string]any
		for _, it := range items {
			if strings.TrimSpace(strAny(it["runCode"])) != code {
				continue
			}
			if best == nil || strAny(it["planSendTime"]) > strAny(best["planSendTime"]) {
				best = it
			}
		}
		return best
	}
	if hit, err := f.queryPcdAllFilter(ctx, "runCode="+code, 100); err != nil {
		if log != nil {
			log("  [提示] 按单号直查失败（%v），改用日期范围兜底", err)
		}
	} else if h := pick(hit); h != nil {
		return h, true, nil
	}
	type win struct{ from, to string }
	var windows []win
	if dateHint != "" {
		if d, err := time.ParseInLocation("2006-01-02", cutLen(dateHint, 10), time.Local); err == nil {
			windows = append(windows, win{
				from: d.AddDate(0, 0, -3).Format("2006-01-02"),
				to:   d.AddDate(0, 0, 3).Format("2006-01-02"),
			})
		}
	}
	today := time.Now()
	chunks := (max(1, lookbackDays) + 29) / 30
	for i := 0; i < chunks; i++ {
		end := today.AddDate(0, 0, -30*i)
		start := end.AddDate(0, 0, -29)
		windows = append(windows, win{from: start.Format("2006-01-02"), to: end.Format("2006-01-02")})
	}
	for _, w := range windows {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		flt := fmt.Sprintf("planSendTime=%s 00:00&planSendTime2=%s 23:59:00", w.from, w.to)
		items, err := f.queryPcdAllFilter(ctx, flt, 100)
		if err != nil {
			if log != nil {
				log("  [提示] 日期兜底查询失败（%s~%s：%v）", w.from, w.to, err)
			}
			continue
		}
		if h := pick(items); h != nil {
			return h, true, nil
		}
	}
	return nil, false, nil
}

// GetPrintDetails 打印详情（FakeClient._print_details 形状 + get_print_details 分批口径）。
func (f *FakeAPI) GetPrintDetails(ctx context.Context, ids []string, chunk int) ([]uvmpapi.Dict, error) {
	if chunk <= 0 {
		chunk = 1
	}
	var result []uvmpapi.Dict
	for i := 0; i < len(ids); i += chunk {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		part := ids[i:min(i+chunk, len(ids))]
		f.mu.Lock()
		f.DetailsCalls++
		f.mu.Unlock()
		data, err := f.fakeGet("running-service-provider/pcdmanager/getPrintDetails?id="+strings.Join(part, ","), nil)
		if err != nil {
			return nil, err
		}
		rv, _ := data["resultValue"].(map[string]any)
		result = append(result, anyList(rv["items"])...)
	}
	return result, nil
}

// QueryTrackPoints 轨迹点查询（FakeClient._points 形状）。
func (f *FakeAPI) QueryTrackPoints(ctx context.Context, vehicleID, begin, end string) ([]uvmpapi.Dict, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.PointsCalls++
	f.mu.Unlock()
	data, err := f.fakeGet("monitor-service-provider/tyclTrack/queryTrackHisByVehicle",
		map[string]string{"beginTimeStr": begin, "vehicleId": vehicleID, "endTimeStr": end})
	if err != nil {
		return nil, err
	}
	if d, ok := data["data"].([]any); ok {
		return anyList(d), nil
	}
	return []uvmpapi.Dict{}, nil
}

// QueryTrackSegment 行程段查询（FakeClient._segments 形状 + vehicle_export.
// query_track_segment 选段口径：vehicleId 相同取 starttime 距 begin 最近）。
func (f *FakeAPI) QueryTrackSegment(ctx context.Context, vehicleID, begin, end string) (uvmpapi.Dict, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.SegmentCalls++
	f.mu.Unlock()
	data, err := f.fakeGet("monitor-service-provider/tyclMonTrackSub/simpleQuery", nil)
	if err != nil {
		return nil, nil // 与 Python 版一致：查询失败回退按派车单弹窗
	}
	rv, _ := data["resultValue"].(map[string]any)
	var best map[string]any
	bestDist := 1e18
	for _, it := range anyList(rv["items"]) {
		if strAny(it["vehicleId"]) != vehicleID {
			continue
		}
		d := segTimeDist(it, begin)
		if best == nil || d < bestDist {
			best, bestDist = it, d
		}
	}
	return best, nil
}

// segTimeDist 行程段 starttime 与 begin 的时间差绝对秒（uvmpapi.segTimeDist 口径）。
func segTimeDist(it map[string]any, begin string) float64 {
	s, err1 := time.ParseInLocation("2006-01-02 15:04:05", cutLen(strAny(it["starttime"]), 19), time.Local)
	b, err2 := time.ParseInLocation("2006-01-02 15:04:05", cutLen(begin, 19), time.Local)
	if err1 != nil || err2 != nil {
		return 1e18
	}
	d := s.Sub(b).Seconds()
	if d < 0 {
		d = -d
	}
	return d
}

func anyList(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// roundTo 保留 n 位小数（Python round(x, n) 口径：正确舍入，Go 格式化同为就近舍入）。
func roundTo(v float64, n int) float64 {
	s := strconv.FormatFloat(v, 'f', n, 64)
	out, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return v
	}
	return out
}
