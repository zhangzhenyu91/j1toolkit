package course

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// feedAPI 喂一条接口响应进监视器（fake 驱动/测试共用）。
func feedAPI(m *ResponseMonitor, scope, url string, status int, body string) {
	m.Add(MonitoredResponse{Scope: scope, URL: url, Status: status, Body: []byte(body)})
}

func TestMonitorWaitSuccess(t *testing.T) {
	m := NewResponseMonitor()
	feedAPI(m, "popup", "http://h/learningc/course/ware-catalog-relation/listRes?id=v", 200,
		`{"resultCode":"200","resultValue":[{"wareType":1}]}`)
	mark := m.Seq()
	feedAPI(m, "popup", "http://h/learningc/course/study-record-info/getPassWare", 200,
		`{"resultCode":"200","resultValue":1}`)

	payload, err := m.WaitSuccess(context.Background(), "popup", SaveStudyAPI, mark, time.Second, "学习记录上报")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%v", payload["resultCode"]) != "200" {
		t.Fatalf("载荷 = %v", payload)
	}
	// mark 之前的响应不应被匹配
	if _, err := m.WaitSuccess(context.Background(), "popup", SaveStudyAPI, 0, 300*time.Millisecond, "学习记录上报"); err != nil {
		t.Fatalf("mark=0 应命中已有响应: %v", err)
	}
}

func TestMonitorErrors(t *testing.T) {
	m := NewResponseMonitor()
	feedAPI(m, "p", "http://h/learningc/course/study-record-info/getPassWare", 500, `{}`)
	if _, err := m.WaitSuccess(context.Background(), "p", SaveStudyAPI, 0, time.Second, "学习记录上报"); err == nil || err.Error() != "学习记录上报 HTTP 状态为 500" {
		t.Fatalf("HTTP 错误 = %v", err)
	}

	m2 := NewResponseMonitor()
	feedAPI(m2, "p", "http://h/learningc/course/study-record-info/getPassWare", 200,
		`{"resultCode":"500","resultValue":"系统繁忙"}`)
	if _, err := m2.WaitSuccess(context.Background(), "p", SaveStudyAPI, 0, time.Second, "学习记录上报"); err == nil {
		t.Fatal("业务失败应报错")
	}

	m3 := NewResponseMonitor()
	feedAPI(m3, "p", "http://h/learningc/course/study-record-info/getPassWare", 200, `not-json`)
	if _, err := m3.WaitSuccess(context.Background(), "p", SaveStudyAPI, 0, time.Second, "学习记录上报"); err == nil {
		t.Fatal("坏 JSON 应报错")
	}

	m4 := NewResponseMonitor()
	m4.MarkClosed("p")
	if _, err := m4.WaitSuccess(context.Background(), "p", SaveStudyAPI, 0, time.Second, "学习记录上报"); err == nil || err.Error() != "等待学习记录上报时学习页已关闭" {
		t.Fatalf("页面关闭错误 = %v", err)
	}

	m5 := NewResponseMonitor()
	start := time.Now()
	if _, err := m5.WaitSuccess(context.Background(), "p", SaveStudyAPI, 0, 300*time.Millisecond, "课件清单"); err == nil {
		t.Fatal("超时应报错")
	} else if time.Since(start) < 250*time.Millisecond {
		t.Fatalf("超时过短: %v", err)
	}

	// scope 过滤：他页响应不算
	m6 := NewResponseMonitor()
	feedAPI(m6, "other", "http://h/learningc/course/ware-catalog-relation/listRes", 200,
		`{"resultCode":"200","resultValue":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := m6.WaitSuccess(ctx, "popup", WareListAPI, 0, time.Second, "课件清单"); err == nil {
		t.Fatal("scope 不匹配应超时")
	}
}
