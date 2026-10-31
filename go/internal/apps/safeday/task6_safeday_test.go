package safeday

// Task 6 边界回归：multipart 上传在读取文件内容前先限总量/单文件大小。

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
)

// TestCheckUploadBudget 只依据 FileHeader 元数据拒绝超限，不读取文件内容。
func TestCheckUploadBudget(t *testing.T) {
	ok := []*multipart.FileHeader{
		{Filename: "a.pdf", Size: maxUploadFileSize},
		{Filename: "b.pdf", Size: 1},
	}
	if err := checkUploadBudget(ok); err != nil {
		t.Fatalf("预算内文件不应拒绝: %v", err)
	}
	tooBig := []*multipart.FileHeader{{Filename: "big.pdf", Size: maxUploadFileSize + 1}}
	if err := checkUploadBudget(tooBig); err != errFileTooLarge {
		t.Fatalf("单文件超限应 errFileTooLarge，实际 %v", err)
	}
	many := make([]*multipart.FileHeader, 0, maxUploadFiles+1)
	for i := 0; i <= maxUploadFiles; i++ {
		many = append(many, &multipart.FileHeader{Filename: "f.pdf", Size: 1})
	}
	if err := checkUploadBudget(many); err != errTooManyFiles {
		t.Fatalf("文件数量超限应 errTooManyFiles，实际 %v", err)
	}
}

// TestDifyWorkflowStreamWithoutFinishFails 流在完成事件前结束必须回调失败。
func TestDifyWorkflowStreamWithoutFinishFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"event\":\"workflow_started\",\"data\":{\"id\":\"1\"}}\n\n"))
	}))
	defer srv.Close()
	d := &DifyWorkflow{APIURL: srv.URL, APIKey: "k", HTTP: srv.Client()}
	failed := make(chan error, 1)
	if err := d.Run(context.Background(), WorkflowInput{
		URL: "u", Date: "d", Class: "c", OnFailed: func(e error) { failed <- e },
	}); err != nil {
		t.Fatalf("触发应成功: %v", err)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Error(), "完成事件") {
			t.Fatalf("应提示缺少完成事件：%v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("流未完成却未回调 OnFailed")
	}
}

// TestGenerateRejectsTooManyFiles 真实 multipart 请求：超过 10 个文件 400。
func TestGenerateRejectsTooManyFiles(t *testing.T) {
	env := newTestServer(t, auth.Identity{ID: 1, Username: "u", Role: "team_admin", TeamID: 1})
	files := map[string][]byte{}
	for i := 0; i <= maxUploadFiles; i++ {
		files[string(rune('a'+i))+".pdf"] = []byte("x")
	}
	rec := env.doMultipart(t, "/api/v1/safeday/generate", map[string]string{"name": "安全日活动", "date": "2026.10.31"}, files)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超文件数应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}
