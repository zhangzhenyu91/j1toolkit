package uvmpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pcdResp 构造派车单查询响应。
func pcdResp(items []Dict, total int) string {
	b, _ := json.Marshal(map[string]any{
		"successful":  true,
		"resultValue": map[string]any{"itemCount": total, "items": items},
	})
	return string(b)
}

// readParams 解析请求里的 params 参数。
func readParams(t *testing.T, r *http.Request) (pageIndex, pageSize int, filter string) {
	t.Helper()
	var p struct {
		PageIndex int    `json:"pageIndex"`
		PageSize  int    `json:"pageSize"`
		Filter    string `json:"filter"`
	}
	if err := json.Unmarshal([]byte(r.URL.Query().Get("params")), &p); err != nil {
		t.Fatalf("params 解析失败: %v (%q)", err, r.URL.Query().Get("params"))
	}
	return p.PageIndex, p.PageSize, p.Filter
}

// TestQueryPcdPageShape 派车单查询的 filter/tycl 参数形态（vehicle_export.query_pcd_page）。
func TestQueryPcdPageShape(t *testing.T) {
	var gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/running-service-provider/pcdmanager/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("tycl_parentMenuId") != "running" || q.Get("tycl_sonMenuId") != "pcdmanager" ||
			q.Get("tycl_sonMenuTitle") != "派车单管理" {
			t.Errorf("tycl 参数错: %s", r.URL.RawQuery)
		}
		_, _, gotFilter = readParams(t, r)
		fmt.Fprint(w, pcdResp(nil, 0))
	}))
	defer srv.Close()

	c := newTestClient(newTestManager(t, srv.URL+"/"))
	qcfg := QueryConfig{PageSize: 50, RunState: "2", VehicleState: "04", KeepTag: "00,01"}
	if _, err := c.QueryPcdPage(context.Background(), qcfg, "2026-09-01", "2026-09-30", 1); err != nil {
		t.Fatal(err)
	}
	want := "planSendTime=2026-09-01 00:00&planSendTime2=2026-09-30 23:59:00&runState=2&vehicleState=04&keepTag=00,01"
	if gotFilter != want {
		t.Fatalf("filter = %q, want %q", gotFilter, want)
	}
}

// TestQueryAllPcdPagination 分页合并（vehicle_export.query_all_pcd）。
func TestQueryAllPcdPagination(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pi, _, _ := readParams(t, r)
		pages++
		if pi != pages {
			t.Errorf("pageIndex = %d, want %d", pi, pages)
		}
		fmt.Fprint(w, pcdResp([]Dict{{"id": fmt.Sprintf("i%d", pi)}}, 3))
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	qcfg := QueryConfig{PageSize: 1} // 3 条 → 3 页
	items, err := c.QueryAllPcd(context.Background(), qcfg, "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || pages != 3 {
		t.Fatalf("items=%d pages=%d", len(items), pages)
	}
}

// TestQueryPcdByRunCode 按单号查找：runCode 直查命中（含精确匹配过滤与取最晚单）。
func TestQueryPcdByRunCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, filter := readParams(t, r)
		if filter != "runCode=ABC" {
			t.Errorf("直查 filter = %q", filter)
		}
		fmt.Fprint(w, pcdResp([]Dict{
			{"runCode": "ABC-2", "planSendTime": "2026-09-02"}, // 模糊匹配干扰项，客户端须过滤
			{"runCode": "ABC", "planSendTime": "2026-09-01"},
			{"runCode": "ABC", "planSendTime": "2026-09-05"}, // 同号多单取最晚
		}, 3))
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	got, found, err := c.QueryPcdByRunCode(context.Background(), "ABC", "", 62, nil)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got["planSendTime"] != "2026-09-05" {
		t.Fatalf("picked = %v", got)
	}
}

// TestQueryPcdByRunCodeFallback 直查失败/为空 → 日期范围兜底（dateHint ±3 天窗优先）。
func TestQueryPcdByRunCodeFallback(t *testing.T) {
	var filters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, filter := readParams(t, r)
		filters = append(filters, filter)
		if strings.HasPrefix(filter, "runCode=") {
			fmt.Fprint(w, pcdResp(nil, 0)) // 直查为空
			return
		}
		if strings.Contains(filter, "2026-09-08") { // dateHint 2026-09-11 ±3 的窗口
			fmt.Fprint(w, pcdResp([]Dict{{"runCode": "ABC", "planSendTime": "2026-09-09"}}, 1))
			return
		}
		fmt.Fprint(w, pcdResp(nil, 0))
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	got, found, err := c.QueryPcdByRunCode(context.Background(), "ABC", "2026-09-11", 62, nil)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v filters=%v", found, err, filters)
	}
	if got["runCode"] != "ABC" {
		t.Fatalf("picked = %v", got)
	}
	if len(filters) < 2 || filters[0] != "runCode=ABC" || !strings.Contains(filters[1], "2026-09-08 00:00") {
		t.Fatalf("查询序列错: %v", filters)
	}
}

// TestQueryPcdByRunCodeNotFound 全部窗口为空 → (nil,false,nil)。
func TestQueryPcdByRunCodeNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, pcdResp(nil, 0))
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	got, found, err := c.QueryPcdByRunCode(context.Background(), "NOPE", "2026-09-11", 62, nil)
	if err != nil || found || got != nil {
		t.Fatalf("got=%v found=%v err=%v", got, found, err)
	}
}

// TestGetPrintDetailsChunk 打印详情分批（vehicle_export.get_print_details，chunk=2）。
func TestGetPrintDetailsChunk(t *testing.T) {
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/running-service-provider/pcdmanager/getPrintDetails" {
			t.Errorf("path = %s", r.URL.Path)
		}
		ids = append(ids, r.URL.Query().Get("id"))
		fmt.Fprint(w, `{"successful":true,"resultValue":{"items":[{"id":"x"}]}}`)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	items, err := c.GetPrintDetails(context.Background(), []string{"a", "b", "c"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 { // 每批返回 1 条 × 2 批
		t.Fatalf("items = %d", len(items))
	}
	if len(ids) != 2 || ids[0] != "a,b" || ids[1] != "c" {
		t.Fatalf("批次 id = %v", ids)
	}
}

// TestQueryTrackPoints 轨迹点响应三种形态（vehicle_export.query_track_points）。
func TestQueryTrackPoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"data列表", `{"data":[{"dx":"113.0"}]}`},
		{"resultValue列表", `{"resultValue":[{"dx":"113.0"}]}`},
		{"resultValue.items", `{"resultValue":{"items":[{"dx":"113.0"}]}}`},
		{"空", `{"successful":true}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("vehicleId") != "v1" || q.Get("beginTimeStr") != "2026-09-01 08:00:00" ||
				q.Get("endTimeStr") != "2026-09-01 12:00:00" {
				t.Errorf("%s: 参数错 %s", tc.name, r.URL.RawQuery)
			}
			if q.Get("tycl_parentMenuId") != "monitor" || q.Get("tycl_sonMenuId") != "montrack" {
				t.Errorf("%s: tycl 错", tc.name)
			}
			fmt.Fprint(w, tc.body)
		}))
		c := newTestClient(newTestManager(t, srv.URL+"/"))
		pts, err := c.QueryTrackPoints(context.Background(), "v1", "2026-09-01 08:00:00", "2026-09-01 12:00:00")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.name == "空" {
			if len(pts) != 0 {
				t.Fatalf("%s: pts = %v", tc.name, pts)
			}
		} else if len(pts) != 1 || pts[0]["dx"] != "113.0" {
			t.Fatalf("%s: pts = %v", tc.name, pts)
		}
	}
}

// TestQueryTrackSegment 行程段：按 vehicleId 过滤 + 取 starttime 最接近 begin 的一条
// （vehicle_export.query_track_segment）。
func TestQueryTrackSegment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monitor-service-provider/tyclMonTrackSub/simpleQuery" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _, filter := readParams(t, r)
		if filter != "startTime=2026-09-01 08:00:00&endTime=2026-09-01 12:00:00&vehicleState=04" {
			t.Errorf("filter = %q", filter)
		}
		fmt.Fprint(w, `{"successful":true,"resultValue":{"items":[
			{"vehicleId":"other","starttime":"2026-09-01 08:00:00"},
			{"vehicleId":"v1","id":"seg-far","starttime":"2026-09-01 11:00:00"},
			{"vehicleId":"v1","id":"seg-near","starttime":"2026-09-01 08:05:00"}
		]}}`)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	seg, err := c.QueryTrackSegment(context.Background(), "v1", "2026-09-01 08:00:00", "2026-09-01 12:00:00")
	if err != nil {
		t.Fatal(err)
	}
	if seg["id"] != "seg-near" {
		t.Fatalf("seg = %v", seg)
	}
}

// TestQueryTrackSegmentMiss 查不到返回 nil（不报错）。
func TestQueryTrackSegmentMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"successful":true,"resultValue":{"items":[]}}`)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	seg, err := c.QueryTrackSegment(context.Background(), "v1", "2026-09-01 08:00:00", "2026-09-01 12:00:00")
	if err != nil || seg != nil {
		t.Fatalf("seg=%v err=%v, want (nil,nil)", seg, err)
	}
}

// TestQueryTrackSegmentSwallowError 查询失败吞掉并记提示（Python 版口径：回退派车单弹窗）。
func TestQueryTrackSegmentSwallowError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	var logs []string
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	c.logf = LogFunc(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	seg, err := c.QueryTrackSegment(context.Background(), "v1", "2026-09-01 08:00:00", "2026-09-01 12:00:00")
	if err != nil || seg != nil {
		t.Fatalf("seg=%v err=%v, want (nil,nil)", seg, err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "行程段查询失败（回退按派车单弹窗）") {
		t.Fatalf("提示日志缺失: %s", joined)
	}
}

// TestParseQueryConfig _ini.export 解析与缺省回退（toolkit/config.py ve_cfg 口径）。
func TestParseQueryConfig(t *testing.T) {
	got := ParseQueryConfig(map[string]any{})
	def := DefaultQueryConfig()
	if got != def {
		t.Fatalf("缺省 = %+v, want %+v", got, def)
	}
	got = ParseQueryConfig(map[string]any{
		"_ini": map[string]any{"export": map[string]any{
			"page_size": "20", "run_state": "3", "vehicle_state": "05", "keep_tag": "02",
		}},
	})
	if got.PageSize != 20 || got.RunState != "3" || got.VehicleState != "05" || got.KeepTag != "02" {
		t.Fatalf("解析 = %+v", got)
	}
}

// TestQueryPcdAllFilter 自由 filter 查询（不附加 runState 等）。
func TestQueryPcdAllFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pi, ps, filter := readParams(t, r)
		if pi != 1 || ps != 100 || filter != "runCode=Z9" {
			t.Errorf("pi=%d ps=%d filter=%q", pi, ps, filter)
		}
		fmt.Fprint(w, pcdResp([]Dict{{"runCode": "Z9"}}, 1))
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	items, err := c.QueryPcdAllFilter(context.Background(), "runCode=Z9", 0) // 0 → 回退 100
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
}

// TestClientContextCancel ctx 取消即刻返回（对应进程协议 {"command":"cancel"}）。
func TestClientContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	_, err := c.Get(ctx, "svc/x", nil, "")
	if err == nil {
		t.Fatal("ctx 超时应报错")
	}
}
