package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shade/internal/apps/safeday"
)

// TestPhotoVerifier 适配 worklog.DifyClient：inputs 为 date/destination/picture(remote_url)，
// outputs 映射 VerifyResult，天气 LaTeX 归一，异常归 OK=false。
func TestPhotoVerifier(t *testing.T) {
	var gotInputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotInputs, _ = body["inputs"].(map[string]any)
		_, _ = w.Write([]byte(`{
			"workflow_run_id": "wr", "task_id": "t",
			"data": {"status": "succeeded", "outputs": {
				"title": "10kV线路检修", "time": "2026-10-27 09:30",
				"weather": "$17^{\\circ} \\mathrm{C}$ 东南风1级",
				"location": "尧都区·临汾电力高级技工学校",
				"lng": "116.480881", "lat": "39.985876"}}
		}`))
	}))
	defer srv.Close()

	v := NewPhotoVerifier(difyTestClient(srv.URL))
	vr := v.VerifyPhoto(context.Background(), "zhangsan", "2026.10.27", "尧都区", "https://cos/x.jpg")
	if !vr.OK {
		t.Fatal("VerifyPhoto 应 OK")
	}
	if vr.WorkContent != "10kV线路检修" || vr.Time != "2026-10-27 09:30" ||
		vr.Location != "尧都区·临汾电力高级技工学校" || vr.Lng != "116.480881" || vr.Lat != "39.985876" {
		t.Fatalf("VerifyResult = %+v", vr)
	}
	if vr.Weather != "17℃ 东南风1级" {
		t.Fatalf("weather 归一 = %q", vr.Weather)
	}
	// picture 单文件变量传对象（数组会报 invalid_param: must be a file）
	pic, ok := gotInputs["picture"].(map[string]any)
	if !ok || pic["transfer_method"] != "remote_url" || pic["url"] != "https://cos/x.jpg" || pic["type"] != "image" {
		t.Fatalf("picture = %v", gotInputs["picture"])
	}
	if gotInputs["date"] != "2026.10.27" || gotInputs["destination"] != "尧都区" {
		t.Fatalf("inputs = %v", gotInputs)
	}

	// 异常归 OK=false 不抛出
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer fail.Close()
	if vr := NewPhotoVerifier(difyTestClient(fail.URL)).VerifyPhoto(context.Background(), "u", "d", "x", "y"); vr.OK {
		t.Fatalf("异常应 OK=false，got %+v", vr)
	}
	if vr := NewPhotoVerifier(nil).VerifyPhoto(context.Background(), "u", "d", "x", "y"); vr.OK {
		t.Fatal("nil client 应 OK=false")
	}
}

// TestStreamRunner 适配 safeday.WorkflowRunner：inputs url/date/class、user safeday-web，
// 触发成功立即返回，失败事件回调 OnFailed。
func TestStreamRunner(t *testing.T) {
	var gotInputs map[string]any
	failed := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotInputs, _ = body["inputs"].(map[string]any)
		if u, _ := body["user"].(string); u != "safeday-web" {
			t.Errorf("user = %v", body["user"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"failed\", \"error\": \"模型异常\"}}\n\n"))
	}))
	defer srv.Close()

	r := &StreamRunner{Client: difyTestClient(srv.URL)}
	err := r.Run(context.Background(), safeday.WorkflowInput{
		URL:      "https://cos/safeday/1.pdf",
		Date:     "2026.10.27",
		Class:    "检修一班",
		OnFailed: func(e error) { failed <- e },
	})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if gotInputs["url"] != "https://cos/safeday/1.pdf" || gotInputs["date"] != "2026.10.27" ||
		gotInputs["class"] != "检修一班" {
		t.Fatalf("inputs = %v", gotInputs)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Error(), "模型异常") {
			t.Fatalf("OnFailed = %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnFailed 未回调")
	}

	// 未接入（nil client）返回错误，不 panic
	if err := (&StreamRunner{}).Run(context.Background(), safeday.WorkflowInput{}); err == nil {
		t.Fatal("nil client 应报错")
	}
}

// StreamRunner 必须把 safeday.WorkflowInput 的 record_id/batch_id 原值透传到 Dify 请求体；
// 空 ID 不下发（历史调用保持三变量可选语义）。
func TestStreamRunnerForwardsRecordAndBatchIDs(t *testing.T) {
	var gotInputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		gotInputs, _ = body["inputs"].(map[string]any)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"succeeded\"}}\n\n"))
	}))
	defer srv.Close()

	r := &StreamRunner{Client: difyTestClient(srv.URL)}
	if err := r.Run(context.Background(), safeday.WorkflowInput{
		URL:      "https://cos/safeday/1.pdf",
		Date:     "2026.10.27",
		Class:    "检修一班",
		RecordID: "rec-20261027-1",
		BatchID:  "batch-20261027-1",
	}); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if gotInputs["record_id"] != "rec-20261027-1" || gotInputs["batch_id"] != "batch-20261027-1" {
		t.Fatalf("Dify 请求体应原样包含 record_id/batch_id: %v", gotInputs)
	}

	// 空 ID：不下发键，保持旧调用的三变量可选语义。
	gotInputs = nil
	if err := r.Run(context.Background(), safeday.WorkflowInput{
		URL: "https://cos/safeday/2.pdf", Date: "2026.10.28", Class: "检修一班",
	}); err != nil {
		t.Fatalf("Run(empty IDs) = %v", err)
	}
	if _, ok := gotInputs["record_id"]; ok {
		t.Fatalf("空 RecordID 不应下发: %v", gotInputs)
	}
	if _, ok := gotInputs["batch_id"]; ok {
		t.Fatalf("空 BatchID 不应下发: %v", gotInputs)
	}
}
