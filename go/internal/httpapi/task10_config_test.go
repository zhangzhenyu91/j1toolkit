package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 多键配置更新必须一次批量提交：handler 不得逐键 Update 造成中途部分成功。
func TestConfigUpdate_UsesSingleBatchCommit(t *testing.T) {
	cfg := demoCfg()
	cfg.items["app.mode"] = ConfigItem{Key: "app.mode", Group: "基础配置", Value: "old", Sensitive: false}
	h := NewRouter(Deps{Cfg: cfg})
	body := `{"items":[{"key":"app.secret","value":"new-secret-value-42"},{"key":"app.mode","value":"new"}]}`
	status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", body)
	assertOK(t, status, r)
	if cfg.batchCalls != 1 {
		t.Fatalf("应调用 UpdateBatch 1 次，实际 %d", cfg.batchCalls)
	}
	if len(cfg.updates) != 2 {
		t.Fatalf("批量更新应收到 2 项，实际 %+v", cfg.updates)
	}
	if got := cfg.items["app.secret"].Value; got != "new-secret-value-42" {
		t.Fatalf("批量更新未写入新值：%q", got)
	}
	if got := cfg.items["app.mode"].Value; got != "new" {
		t.Fatalf("批量更新未写入第二键：%q", got)
	}
}

// 任一键校验失败时不得调用批量写入。
func TestConfigUpdate_UnknownKeySkipsBatch(t *testing.T) {
	cfg := demoCfg()
	h := NewRouter(Deps{Cfg: cfg})
	body := `{"items":[{"key":"app.secret","value":"x"},{"key":"nope","value":"y"}]}`
	status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", body)
	assertErr(t, status, http.StatusNotFound, r, CodeNotFound)
	if cfg.batchCalls != 0 {
		t.Fatalf("校验失败不应调用 UpdateBatch，实际 %d 次", cfg.batchCalls)
	}
	if value := cfg.items["app.secret"].Value; value != "s3cr3t-token-abcdef" {
		t.Fatalf("校验失败不得部分写入，app.secret 已被改为 %q", value)
	}
}

// 批量写入失败整体映射为错误，不返回成功信封。
func TestConfigUpdate_BatchFailureReportsError(t *testing.T) {
	cfg := demoCfg()
	cfg.batchErr = fmt.Errorf("mock transaction failed")
	h := NewRouter(Deps{Cfg: cfg})
	body := `{"items":[{"key":"app.secret","value":"x"},{"key":"app.notify_key","value":"y"}]}`
	status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", body)
	assertErr(t, status, http.StatusInternalServerError, r, CodeInternal)
	if strings.Contains(string(r.Data), "new") {
		t.Fatalf("失败响应不应包含成功项：%s", r.Data)
	}
}
