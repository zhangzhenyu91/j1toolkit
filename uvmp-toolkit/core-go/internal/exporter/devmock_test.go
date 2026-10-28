package exporter

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestMockItemsShape devmock 数据形状：当日 7 + 昨日 2，runState 分布正确。
func TestMockItemsShape(t *testing.T) {
	today := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	items := makeMockItems(today)
	if len(items) != 9 {
		t.Fatalf("应 9 单，实际 %d", len(items))
	}
	ymd := today.Format("20060102")
	ymdY := today.AddDate(0, 0, -1).Format("20060102")
	wantCodes := map[string]string{ // runCode → runState
		"PCD" + ymd + "01": "2", "PCD" + ymd + "02": "2", "PCD" + ymd + "03": "2",
		"PCD" + ymd + "04": "2", "PCD" + ymd + "05": "2",
		"PCD" + ymd + "06": "1", "PCD" + ymd + "07": "0",
		"PCD" + ymdY + "11": "2", "PCD" + ymdY + "12": "2",
	}
	for _, it := range items {
		code := strAny(it["runCode"])
		st, ok := wantCodes[code]
		if !ok {
			t.Fatalf("意外单据 %s", code)
		}
		if strAny(it["runState"]) != st {
			t.Fatalf("%s runState=%v want %s", code, it["runState"], st)
		}
		if strAny(it["planSendTimeStr"]) == "" || strAny(it["vehicleNumber"]) == "" {
			t.Fatalf("%s 关键字段缺失", code)
		}
		if st == "2" {
			if !strings.HasSuffix(strAny(it["realSendTime"]), ".0") {
				t.Fatalf("%s realSendTime 形状不符: %q", code, it["realSendTime"])
			}
		} else if strAny(it["realSendTime"]) != "" {
			t.Fatalf("%s 未完结单不应有实际时间", code)
		}
	}
}

// TestFakeAPIQueryShapes FakeClient 查询/轨迹形状：过滤分页、打印详情、40 轨迹点、行程段。
func TestFakeAPIQueryShapes(t *testing.T) {
	today := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	api := NewFakeAPIAt(today)
	api.Delay = 0
	ctx := context.Background()
	ymd := today.Format("20060102")

	// runCode 直查
	o, ok, err := api.QueryPcdByRunCode(ctx, "PCD"+ymd+"01", "", 62, nil)
	if err != nil || !ok {
		t.Fatalf("直查失败: %v %v", ok, err)
	}
	if strAny(o["id"]) != "MOCK0001" {
		t.Fatalf("直查命中不符: %v", o["id"])
	}
	// 不存在
	_, ok, _ = api.QueryPcdByRunCode(ctx, "NOPE", "", 62, nil)
	if ok {
		t.Fatal("不应命中")
	}
	// 日期兜底（date_hint 与单据同日）
	o, ok, _ = api.QueryPcdByRunCode(ctx, "PCD"+ymd+"03", ymd, 62, nil)
	if !ok || strAny(o["id"]) != "MOCK0003" {
		t.Fatalf("日期兜底不符: %v", o)
	}

	// 打印详情
	ds, err := api.GetPrintDetails(ctx, []string{"MOCK0001", "NOPE"}, 1)
	if err != nil || len(ds) != 1 {
		t.Fatalf("打印详情不符: %v %v", ds, err)
	}

	// 轨迹点：40 点、字段形状
	pts, err := api.QueryTrackPoints(ctx, "V1", "2026-10-28 09:30:00", "2026-10-28 11:30:00")
	if err != nil || len(pts) != 40 {
		t.Fatalf("轨迹点数不符: %d %v", len(pts), err)
	}
	for _, k := range []string{"dx", "dy", "speed", "direction", "locatetime"} {
		if _, ok := pts[0][k]; !ok {
			t.Fatalf("轨迹点缺字段 %s", k)
		}
	}

	// 行程段：仅已完结单，按 vehicleId + 最近 starttime 选段
	seg, err := api.QueryTrackSegment(ctx, "V0002", "2026-10-28 09:30:00", "2026-10-28 11:30:00")
	if err != nil || seg == nil {
		t.Fatalf("行程段查询失败: %v %v", seg, err)
	}
	if !strings.HasPrefix(strAny(seg["id"]), "SEG-") {
		t.Fatalf("行程段 id 形状不符: %v", seg["id"])
	}
	if strAny(seg["starttime"]) == "" || strAny(seg["endtime"]) == "" {
		t.Fatalf("行程段缺起止时间: %v", seg)
	}
}
