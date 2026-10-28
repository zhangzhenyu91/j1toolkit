package uvmpapi

// api.go — UVMP 业务接口，逐字移植 vehicle_export.py 的查询原语：
// 派车单分页查询（带 runState/vehicleState/keepTag 与自由 filter 两种）、
// 按单号查找（runCode 直查 + 日期范围兜底）、打印详情、轨迹点、行程段。
// 业务数据以 map[string]any 透传（与 Python dict / 现有 backend 返回口径一致）。

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Dict 业务字典（对应 Python dict）。
type Dict = map[string]any

// QueryConfig 派车单查询参数（config.ini [export] 段，vehicle_export.cfg 同口径）。
type QueryConfig struct {
	PageSize     int    // 分页大小，默认 50
	RunState     string // runState 筛选，默认 "2"（已完结）
	VehicleState string // vehicleState 筛选，默认 "04"
	KeepTag      string // keepTag 筛选，默认 "00,01"
}

// DefaultQueryConfig 缺省查询参数（vehicle_export.main 的 cfg 回退值）。
func DefaultQueryConfig() QueryConfig {
	return QueryConfig{PageSize: 50, RunState: "2", VehicleState: "04", KeepTag: "00,01"}
}

// ParseQueryConfig 从 config.load 字典的 _ini.export 段解析查询参数
// （toolkit/config.py ve_cfg() 口径）。
func ParseQueryConfig(load map[string]any) QueryConfig {
	q := DefaultQueryConfig()
	ini, _ := load["_ini"].(map[string]any)
	exp, _ := ini["export"].(map[string]any)
	if exp == nil {
		return q
	}
	if s, ok := exp["page_size"].(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			q.PageSize = n
		}
	}
	if s, ok := exp["run_state"].(string); ok && s != "" {
		q.RunState = s
	}
	if s, ok := exp["vehicle_state"].(string); ok && s != "" {
		q.VehicleState = s
	}
	if s, ok := exp["keep_tag"].(string); ok && s != "" {
		q.KeepTag = s
	}
	return q
}

// 菜单三元组（tycl_* 参数，原文含中文不编码，vehicle_export.MENU_PCD/MENU_TRACK）。
var (
	menuPCD   = [3]string{"running", "pcdmanager", "派车单管理"}
	menuTrack = [3]string{"monitor", "montrack", "行驶轨迹"}
)

// tyclParams tycl_* 参数以原文（中文不编码）放入 pData（vehicle_export.tycl_params）。
func tyclParams(menu [3]string) map[string]string {
	return map[string]string{
		"tycl_parentMenuId": menu[0],
		"tycl_sonMenuId":    menu[1],
		"tycl_sonMenuTitle": menu[2],
	}
}

// QueryPcdPage 带默认筛选的派车单分页查询（vehicle_export.query_pcd_page）。
// filter 中的日期时间用空格（签名用原始值；发送时 quote_plus 编码为 +，与浏览器一致）。
func (c *Client) QueryPcdPage(ctx context.Context, qcfg QueryConfig, dateFrom, dateTo string, pageIndex int) (Dict, error) {
	flt := fmt.Sprintf("planSendTime=%s 00:00&planSendTime2=%s 23:59:00&runState=%s&vehicleState=%s&keepTag=%s",
		dateFrom, dateTo, qcfg.RunState, qcfg.VehicleState, qcfg.KeepTag)
	params, err := paramsJSON(pageIndex, qcfg.PageSize, flt)
	if err != nil {
		return nil, err
	}
	pData := map[string]string{"params": params}
	for k, v := range tyclParams(menuPCD) {
		pData[k] = v
	}
	return c.Get(ctx, "running-service-provider/pcdmanager/", pData, fmt.Sprintf("派车单查询(第%d页)", pageIndex))
}

// QueryAllPcd 按日期范围拉取全部页派车单并合并返回（vehicle_export.query_all_pcd）。
func (c *Client) QueryAllPcd(ctx context.Context, qcfg QueryConfig, dateFrom, dateTo string) ([]Dict, error) {
	first, err := c.QueryPcdPage(ctx, qcfg, dateFrom, dateTo, 1)
	if err != nil {
		return nil, err
	}
	rv, _ := first["resultValue"].(Dict)
	total := dictInt(rv, "itemCount")
	items := dictList(dictGet(rv, "items"))
	pages := (total + qcfg.PageSize - 1) / qcfg.PageSize
	c.logf.logf("派车单共 %d 条（%d 页）", total, pages)
	for p := 2; p <= pages; p++ {
		if err := sleepCtx(ctx, 300*time.Millisecond); err != nil {
			return nil, err
		}
		data, err := c.QueryPcdPage(ctx, qcfg, dateFrom, dateTo, p)
		if err != nil {
			return nil, err
		}
		rv, _ := data["resultValue"].(Dict)
		items = append(items, dictList(dictGet(rv, "items"))...)
	}
	return items, nil
}

// QueryPcdPageFilter 自由 filter 的派车单分页查询
// （vehicle_export.query_pcd_page_filter：不附加 runState/vehicleState/keepTag，
// 供按单号查找、当日全量导出等需要全部状态单据的场景）。
func (c *Client) QueryPcdPageFilter(ctx context.Context, filterStr string, pageIndex, pageSize int) (Dict, error) {
	params, err := paramsJSON(pageIndex, pageSize, filterStr)
	if err != nil {
		return nil, err
	}
	pData := map[string]string{"params": params}
	for k, v := range tyclParams(menuPCD) {
		pData[k] = v
	}
	return c.Get(ctx, "running-service-provider/pcdmanager/", pData, fmt.Sprintf("派车单查询(第%d页)", pageIndex))
}

// QueryPcdAllFilter 按自由 filter 拉取全部页并合并返回（vehicle_export.query_pcd_all_filter）。
func (c *Client) QueryPcdAllFilter(ctx context.Context, filterStr string, pageSize int) ([]Dict, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	first, err := c.QueryPcdPageFilter(ctx, filterStr, 1, pageSize)
	if err != nil {
		return nil, err
	}
	rv, _ := first["resultValue"].(Dict)
	total := dictInt(rv, "itemCount")
	items := dictList(dictGet(rv, "items"))
	pages := (total + pageSize - 1) / pageSize
	for p := 2; p <= pages; p++ {
		if err := sleepCtx(ctx, 300*time.Millisecond); err != nil {
			return nil, err
		}
		data, err := c.QueryPcdPageFilter(ctx, filterStr, p, pageSize)
		if err != nil {
			return nil, err
		}
		rv, _ := data["resultValue"].(Dict)
		items = append(items, dictList(dictGet(rv, "items"))...)
	}
	return items, nil
}

// QueryPcdByRunCode 按派车单号精确查找单据（不限 runState，任何状态的单都要能打印），
// 逐字移植 vehicle_export.query_pcd_by_run_code：
//
//  1. 先按 runCode 过滤直查（该过滤项未经抓包验证，失败/为空自动兜底）；
//  2. 兜底按日期范围扫描：dateHint(YYYY-MM-DD)±3 天，无提示则回看 lookbackDays 天
//     （按 30 天切块）；每窗失败只记提示继续；
//  3. 无论哪条路径都在客户端按 runCode 精确匹配，防服务端模糊匹配；
//     同号多单（理论上不会）取 planSendTime 最晚的一单。
//
// 找不到返回 (nil, false, nil)；ctx 取消返回 (nil, false, ctx.Err())；
// 单窗查询失败与 Python 版一致只记提示不外抛。log 为 nil 时用客户端日志。
func (c *Client) QueryPcdByRunCode(ctx context.Context, code, dateHint string, lookbackDays int, log LogFunc) (Dict, bool, error) {
	logf := log
	if logf == nil {
		logf = c.logf
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, false, nil
	}
	pick := func(items []Dict) Dict {
		var cands []Dict
		for _, it := range items {
			if strings.TrimSpace(dictStr(it, "runCode")) == code {
				cands = append(cands, it)
			}
		}
		if len(cands) == 0 {
			return nil
		}
		best := cands[0]
		for _, it := range cands[1:] {
			if dictStr(it, "planSendTime") > dictStr(best, "planSendTime") {
				best = it
			}
		}
		return best
	}

	// 1) 直查
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if hit, err := c.QueryPcdAllFilter(ctx, "runCode="+code, 100); err != nil {
		logf.logf("  [提示] 按单号直查失败（%v），改用日期范围兜底", err)
	} else if h := pick(hit); h != nil {
		return h, true, nil
	}

	// 2) 日期范围兜底
	type win struct{ from, to string }
	var windows []win
	if dateHint != "" {
		if d, err := time.ParseInLocation("2006-01-02", dateHint[:min(10, len(dateHint))], time.Local); err == nil {
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
		items, err := c.QueryPcdAllFilter(ctx, flt, 100)
		if err != nil {
			logf.logf("  [提示] 日期兜底查询失败（%s~%s：%v）", w.from, w.to, err)
			continue
		}
		if h := pick(items); h != nil {
			return h, true, nil
		}
	}
	return nil, false, nil
}

// GetPrintDetails 派车单打印详情批量查询（vehicle_export.get_print_details），
// 按 chunk 分批，批间 0.3s。
func (c *Client) GetPrintDetails(ctx context.Context, ids []string, chunk int) ([]Dict, error) {
	if chunk <= 0 {
		chunk = 1
	}
	var result []Dict
	for i := 0; i < len(ids); i += chunk {
		if i > 0 {
			if err := sleepCtx(ctx, 300*time.Millisecond); err != nil {
				return nil, err
			}
		}
		part := ids[i:min(i+chunk, len(ids))]
		pData := tyclParams(menuPCD)
		data, err := c.Get(ctx, "running-service-provider/pcdmanager/getPrintDetails?id="+strings.Join(part, ","),
			pData, fmt.Sprintf("打印详情(%d-%d)", i+1, i+len(part)))
		if err != nil {
			return nil, err
		}
		rv, _ := data["resultValue"].(Dict)
		result = append(result, dictList(dictGet(rv, "items"))...)
	}
	return result, nil
}

// QueryTrackPoints 行驶轨迹点查询（vehicle_export.query_track_points）。
// 时间字符串带空格（签名用原始值；发送时编码为 +）。
func (c *Client) QueryTrackPoints(ctx context.Context, vehicleID, begin, end string) ([]Dict, error) {
	pData := map[string]string{"beginTimeStr": begin, "vehicleId": vehicleID, "endTimeStr": end}
	for k, v := range tyclParams(menuTrack) {
		pData[k] = v
	}
	data, err := c.Get(ctx, "monitor-service-provider/tyclTrack/queryTrackHisByVehicle", pData, "轨迹点查询")
	if err != nil {
		return nil, err
	}
	// 响应形态兼容（vehicle_export.query_track_points 同口径）
	if d, ok := data["data"].([]any); ok {
		return dictList(d), nil
	}
	if d, ok := data["resultValue"].([]any); ok {
		return dictList(d), nil
	}
	if d, ok := data["resultValue"].(Dict); ok {
		if items, ok := d["items"].([]any); ok {
			return dictList(items), nil
		}
	}
	return []Dict{}, nil
}

// QueryTrackSegment 按派车单实际出车/归队时间查对应行程段
// （vehicle_export.query_track_segment：含 id/routeNo/起止时间/里程/时速/起止地点）。
// 官方"行程详情"弹窗需要 routeId 才能打开，故此查询是官方直出的必要输入。
// 返回口径与 Python 版一致：查不到或失败返回 (nil, nil)（调用方回退到按派车单弹窗），
// 失败明细经日志输出提示；仅 ctx 取消返回错误。
func (c *Client) QueryTrackSegment(ctx context.Context, vehicleID, begin, end string) (Dict, error) {
	flt := fmt.Sprintf("startTime=%s&endTime=%s&vehicleState=04", begin, end)
	params, err := paramsJSON(1, 50, flt)
	if err != nil {
		return nil, nil
	}
	pData := map[string]string{"params": params}
	for k, v := range tyclParams(menuTrack) {
		pData[k] = v
	}
	data, err := c.Get(ctx, "monitor-service-provider/tyclMonTrackSub/simpleQuery", pData, "行程段查询")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.logf.logf("  [提示] 行程段查询失败（回退按派车单弹窗）: %v", err)
		return nil, nil
	}
	rv, _ := data["resultValue"].(Dict)
	items := dictList(dictGet(rv, "items"))
	var cands []Dict
	for _, it := range items {
		if dictStr(it, "vehicleId") == vehicleID {
			cands = append(cands, it)
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	best := cands[0]
	bestDist := segTimeDist(best, begin)
	for _, it := range cands[1:] {
		if d := segTimeDist(it, begin); d < bestDist {
			best, bestDist = it, d
		}
	}
	return best, nil
}

// segTimeDist 行程段 starttime 与 begin 的时间差绝对值秒
// （vehicle_export.query_track_segment keyf；解析失败给 1e18）。
func segTimeDist(it Dict, begin string) float64 {
	const layout = "2006-01-02 15:04:05"
	s := dictStr(it, "starttime")
	if len(s) > 19 {
		s = s[:19]
	}
	b := begin
	if len(b) > 19 {
		b = b[:19]
	}
	t1, err1 := time.ParseInLocation(layout, s, time.Local)
	t2, err2 := time.ParseInLocation(layout, b, time.Local)
	if err1 != nil || err2 != nil {
		return 1e18
	}
	return math.Abs(t1.Sub(t2).Seconds())
}

// paramsJSON 派车单/行程段查询的 params 参数（键序 pageIndex/pageSize/filter
// 同 Python json.dumps 插入序；filter 为未编码原文）。
func paramsJSON(pageIndex, pageSize int, filter string) (string, error) {
	fb, err := jsonNoHTMLEscape(filter)
	if err != nil {
		return "", err
	}
	return `{"pageIndex":` + strconv.Itoa(pageIndex) +
		`,"pageSize":` + strconv.Itoa(pageSize) +
		`,"filter":` + fb + `}`, nil
}

// sleepCtx 可取消的间隔等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------- 字典工具

// dictGet 取字典字段（缺失返回 nil）。
func dictGet(m Dict, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// dictStr 取字典字符串字段（非字符串/缺失返回 ""）。
func dictStr(m Dict, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// dictInt 取字典整数字段（JSON 数字为 float64；字符串可解析也算）。
func dictInt(m Dict, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}

// dictList 把 []any 转 []Dict（元素非 map 的跳过）。
func dictList(v any) []Dict {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Dict, 0, len(list))
	for _, it := range list {
		if d, ok := it.(Dict); ok {
			out = append(out, d)
		}
	}
	return out
}
