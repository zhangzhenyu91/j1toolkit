package exporter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestEmitterEvents 事件形状：log/progress/done/error 与协议一致。
func TestEmitterEvents(t *testing.T) {
	buf := &bytes.Buffer{}
	em := NewEmitter(buf)
	em.Log("你好 %d", 1)
	em.Progress(2, 5, "查询 X")
	em.Done(map[string]any{"total": 3})
	em.Error("失败原因")
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("非法 NDJSON: %q", line)
		}
	}
	if !strings.Contains(buf.String(), `"event":"log"`) || !strings.Contains(buf.String(), `"line":"你好 1"`) {
		t.Fatalf("log 事件不符: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"event":"progress"`) || !strings.Contains(buf.String(), `"label":"查询 X"`) {
		t.Fatalf("progress 事件不符: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"event":"done"`) || !strings.Contains(buf.String(), `"total":3`) {
		t.Fatalf("done 事件不符: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"event":"error"`) || !strings.Contains(buf.String(), `"error":"失败原因"`) {
		t.Fatalf("error 事件不符: %s", buf.String())
	}
}

// TestEmitterThreadSafe 并发写事件：每行仍是合法 JSON（线程安全 NDJSON）。
func TestEmitterThreadSafe(t *testing.T) {
	buf := &bytes.Buffer{}
	em := NewEmitter(buf)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			em.Log("line %d", i)
			em.Progress(i, 50, fmt.Sprint(i))
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("事件行数不符: %d", len(lines))
	}
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("并发写出非法行 %q: %v", line, err)
		}
	}
}

// TestReadRequestAndPumpCancel 首行请求 + 后续 cancel 指令。
func TestReadRequestAndPumpCancel(t *testing.T) {
	in := bufio.NewReader(strings.NewReader(
		`{"config":{"dev":{"mock":true}},"params":{"outdir":"o"},"codes":[{"seq":1,"code":"C","date_hint":""}]}` + "\n" +
			`{"command":"other"}` + "\n" + `{"command":"cancel"}` + "\n"))
	req, err := ReadRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Codes) != 1 || req.Codes[0].Code != "C" || !boolField(mapField(req.Config, "dev"), "mock", false) {
		t.Fatalf("请求解析不符: %+v", req)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PumpCancel(in, cancel)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancel 指令应触发取消")
	}
}

// TestReadRequestEmpty stdin 无请求时报错（不 panic）。
func TestReadRequestEmpty(t *testing.T) {
	_, err := ReadRequest(bufio.NewReader(strings.NewReader("")))
	if err == nil {
		t.Fatal("空 stdin 应报错")
	}
}
