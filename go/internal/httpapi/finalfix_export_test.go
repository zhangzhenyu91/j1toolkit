// Final review fix：导出必须能识别坏行/半行/持久写入失败/重启后 DB-only 回退，
// 无法证明文件副本完整时写 incomplete 哨兵与 trailer，绝不标 complete。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shade/internal/logging"
)

// exportMarkerMap 解析导出流末尾哨兵（保留全部字段）。
func exportMarkerMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var marker map[string]any
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if !strings.Contains(line, `"__export__"`) {
			continue
		}
		if err := json.Unmarshal([]byte(line), &marker); err != nil {
			t.Fatalf("解析导出哨兵失败: %v line=%s", err, line)
		}
	}
	if marker == nil {
		t.Fatalf("导出流缺少哨兵: %s", body)
	}
	return marker
}

func exportRecorder(t *testing.T, d Deps, query string) *httptest.ResponseRecorder {
	t.Helper()
	h := adminRouter(d)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export?"+query, nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败: %d %s", rec.Code, rec.Body.String())
	}
	return rec
}

const validExportLine = `{"log_id":"file-1","occurred_at":"2026-10-30T08:00:00Z","app_code":"system","level":"info","category":"runtime","message":"ok"}` + "\n"

// 坏行 + 半行：必须 incomplete，且不得出现 complete。
func TestExportBadAndHalfLinesMarkIncomplete(t *testing.T) {
	dir := t.TempDir()
	content := validExportLine + "{bad json}\n" + `{"log_id":"file-2"`
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(content), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}, "view=all")
	body := rec.Body.String()
	marker := exportMarkerMap(t, body)
	if marker["__export__"] != "incomplete" {
		t.Fatalf("坏行/半行必须标 incomplete，实际 %v body=%s", marker["__export__"], body)
	}
	if rec.Header().Get("X-Log-Export-Status") != "incomplete" {
		t.Fatalf("trailer 必须 incomplete，实际 %q", rec.Header().Get("X-Log-Export-Status"))
	}
	if n, _ := marker["corrupt_lines"].(float64); n < 1 {
		t.Fatalf("哨兵应声明坏行数: %v", marker)
	}
	if n, _ := marker["half_lines"].(float64); n < 1 {
		t.Fatalf("哨兵应声明半行数: %v", marker)
	}
}

// 持久写入失败计数（含重启后恢复）：即使文件当前无坏行也必须 incomplete。
func TestExportPersistedWriteFailureMarksIncomplete(t *testing.T) {
	dir := t.TempDir()
	// integrity.json 是 DurableSink 在写/fsync/回退失败时原子落盘的状态（此处模拟重启前已失败）。
	state := `{"write_errors":2,"fsync_errors":1,"disk_fallbacks":1}`
	if err := os.WriteFile(filepath.Join(dir, "integrity.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("写完整性状态: %v", err)
	}
	sink2, err := logging.NewDurableSinkWithOptions(dir, logging.DurableOptions{
		BatchSize: 10, FsyncMode: logging.FsyncBatch, PollInterval: 5 * time.Millisecond, Stderr: &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("重开 DurableSink: %v", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sink2.CloseContext(c)
	}()
	if h := sink2.Health(); h.WriteErrors == 0 {
		t.Fatalf("重启后应识别写失败: %+v", h)
	}
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: DurableLogHealth{Sink: sink2},
	}, "view=all")
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("持久写失败必须标 incomplete，实际 %v", marker["__export__"])
	}
	if rec.Header().Get("X-Log-Export-Status") != "incomplete" {
		t.Fatalf("trailer 必须 incomplete: %q", rec.Header().Get("X-Log-Export-Status"))
	}
	if rec.Header().Get("X-Log-Export-File-Copy") != "incomplete" {
		t.Fatalf("文件副本头必须 incomplete: %q", rec.Header().Get("X-Log-Export-File-Copy"))
	}
}

// DB 中存在、文件不存在的 log_id（磁盘回退行）：全量导出必须 incomplete。
func TestExportDBOnlyIDsMarkIncomplete(t *testing.T) {
	dir := t.TempDir()
	line := `{"log_id":"file-1","occurred_at":"2026-10-30T08:00:00Z","app_code":"system","level":"info","category":"runtime","message":"ok"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(line), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	src := NDJSONLogSource{Dir: dir, DBIDs: func(context.Context) (map[string]struct{}, error) {
		return map[string]struct{}{"file-1": {}, "db-only-1": {}}, nil
	}}
	rec := exportRecorder(t, Deps{
		LogFiles:  src,
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}, "view=all")
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("DB-only 行必须标 incomplete，实际 %v", marker["__export__"])
	}
	if n, _ := marker["db_only"].(float64); n != 1 {
		t.Fatalf("哨兵应声明 db_only=1: %v", marker)
	}
}

// DB 对账查询失败：无法证明完整，必须 incomplete（不得因文件本身可读就 complete）。
func TestExportDBUnavailableMarksIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(validExportLine), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	src := NDJSONLogSource{Dir: dir, DBIDs: func(context.Context) (map[string]struct{}, error) {
		return nil, errors.New("db down")
	}}
	rec := exportRecorder(t, Deps{
		LogFiles:  src,
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: false}},
	}, "view=all")
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("DB 对账失败必须 incomplete: %v", marker)
	}
	if marker["db_unavailable"] != true {
		t.Fatalf("哨兵应声明数据库对账不可用: %v", marker)
	}
	if rec.Result().Trailer.Get("X-Log-Export-File-Copy") != "incomplete" {
		t.Fatalf("文件副本 trailer 必须 incomplete: %q", rec.Result().Trailer.Get("X-Log-Export-File-Copy"))
	}
}

// integrity.json 损坏：历史失败状态未知，必须按已知不完整导出，不能当作无故障。
func TestExportCorruptIntegrityMarksIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(validExportLine), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "integrity.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("写损坏状态: %v", err)
	}
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}, "view=all")
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("integrity.json 损坏必须 incomplete: %v", marker)
	}
	if marker["integrity_corrupt"] != true {
		t.Fatalf("哨兵应声明完整性状态损坏: %v", marker)
	}
	if rec.Result().Trailer.Get("X-Log-Export-File-Copy") != "incomplete" {
		t.Fatalf("文件副本 trailer 必须 incomplete: %q", rec.Result().Trailer.Get("X-Log-Export-File-Copy"))
	}
}

// 扫描前响应头不得写 complete：初始 File-Copy=unknown，坏行/半行最终以 trailer/哨兵标 incomplete。
func TestExportHeadersUnknownBeforeScanBadLines(t *testing.T) {
	dir := t.TempDir()
	content := validExportLine + "{bad json}\n" + `{"log_id":"file-2"`
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(content), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}, "view=all")
	res := rec.Result()
	if got := res.Header.Get("X-Log-Export-File-Copy"); got != "unknown" {
		t.Fatalf("扫描前响应头不得声称 complete/incomplete，应为 unknown: %q", got)
	}
	if got := res.Trailer.Get("X-Log-Export-File-Copy"); got != "incomplete" {
		t.Fatalf("坏行时最终 trailer 必须 incomplete: %q", got)
	}
	if got := res.Trailer.Get("X-Log-Export-Status"); got != "incomplete" {
		t.Fatalf("坏行时状态 trailer 必须 incomplete: %q", got)
	}
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("坏行哨兵必须 incomplete: %v", marker)
	}
}

// 完整导出：初始头 unknown，最终 trailer/哨兵才 complete。
func TestExportCompleteTrailerFileCopyComplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000001.log"), []byte(validExportLine), 0o600); err != nil {
		t.Fatalf("写日志段: %v", err)
	}
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}, "view=all")
	res := rec.Result()
	if got := res.Header.Get("X-Log-Export-File-Copy"); got != "unknown" {
		t.Fatalf("扫描前头必须 unknown: %q", got)
	}
	if got := res.Trailer.Get("X-Log-Export-File-Copy"); got != "complete" {
		t.Fatalf("完整导出 trailer 应 complete: %q", got)
	}
	if marker := exportMarkerMap(t, rec.Body.String()); marker["__export__"] != "complete" {
		t.Fatalf("完整导出哨兵应 complete: %v", marker)
	}
}

// 双故障：磁盘写失败且 DB 回退也失败（disk_fallbacks 未增长，仅 write_errors），
// 重启后导出仍必须 incomplete。
func TestExportDualFailureMarksIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "integrity.json"),
		[]byte(`{"write_errors":1,"fsync_errors":0,"disk_fallbacks":0}`), 0o600); err != nil {
		t.Fatalf("写完整性状态: %v", err)
	}
	sink, err := logging.NewDurableSinkWithOptions(dir, logging.DurableOptions{
		BatchSize: 10, FsyncMode: logging.FsyncBatch, PollInterval: 5 * time.Millisecond, Stderr: &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sink.CloseContext(c)
	}()
	rec := exportRecorder(t, Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: DurableLogHealth{Sink: sink},
	}, "view=all")
	marker := exportMarkerMap(t, rec.Body.String())
	if marker["__export__"] != "incomplete" {
		t.Fatalf("双故障必须标 incomplete: %v", marker)
	}
}
