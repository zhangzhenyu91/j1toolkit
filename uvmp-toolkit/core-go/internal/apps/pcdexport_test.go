package apps

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/xlsx"
)

// mockCfg dev.mock=true 的配置（离线全流程）。
func mockCfg() config.Dict {
	cfg := config.Defaults()
	cfg["dev"] = config.Dict{"mock": true}
	return cfg
}

// mockRunCode MockAPI 的单号形状（PCD<今日>NN）。
func mockRunCode(idx int) string {
	return fmt.Sprintf("PCD%s%02d", time.Now().Format("20060102"), idx)
}

// writeTestXlsx 造一个单号清单 xlsx。
func writeTestXlsx(path string, codes []string) (int, error) {
	rows := [][]any{}
	for i, c := range codes {
		rows = append(rows, []any{i + 1, c})
	}
	return xlsx.WriteTableXlsx(path, "清单", []string{"序号", "派车单号"}, rows, nil)
}

func TestPcdResolveCodesText(t *testing.T) {
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}}
	codes, err := b.resolveCodes(map[string]any{"codes_text": "PC001 PC002\nPC001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 || codes[0].Code != "PC001" || codes[1].Code != "PC002" {
		t.Fatalf("codes = %+v", codes)
	}
}

func TestPcdResolveCodesList(t *testing.T) {
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}}
	codes, err := b.resolveCodes(map[string]any{
		"codes": []any{
			map[string]any{"code": "PC001", "date_hint": "2026-10-28"},
			map[string]any{"code": "PC002"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 || codes[0].DateHint == nil || *codes[0].DateHint != "2026-10-28" {
		t.Fatalf("codes = %+v", codes)
	}
	if codes[1].DateHint != nil {
		t.Fatalf("date_hint 应为 nil: %+v", codes[1])
	}
}

func TestPcdResolveCodesXlsxBase64(t *testing.T) {
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}}
	// 先造一个 xlsx 再 base64 上传
	tmp := filepath.Join(t.TempDir(), "清单.xlsx")
	if _, err := writeTestXlsx(tmp, []string{"PC007", "PC008"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := b.resolveCodes(map[string]any{
		"xlsx_b64": base64.StdEncoding.EncodeToString(data),
		"filename": "我的清单.xlsx",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 || codes[0].Code != "PC007" {
		t.Fatalf("codes = %+v", codes)
	}
	// 落盘在状态目录 uploads
	if _, err := os.Stat(filepath.Join(b.StateSt.UploadsDir(), "我的清单.xlsx")); err != nil {
		t.Fatalf("上传落盘缺失: %v", err)
	}
}

func TestPcdResolveCodesNoSource(t *testing.T) {
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}}
	_, err := b.resolveCodes(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "未提供派车单号来源") {
		t.Fatalf("err = %v", err)
	}
}

func TestPcdPreview(t *testing.T) {
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}}
	text := ""
	for i := 0; i < 120; i++ {
		text += fmt.Sprintf("PC%03d ", i)
	}
	pv, err := b.Preview(map[string]any{"codes_text": text})
	if err != nil {
		t.Fatal(err)
	}
	if pv["total"] != 120 {
		t.Fatalf("total = %v", pv["total"])
	}
	if pv["truncated"] != true {
		t.Fatal("应标记截断")
	}
	codes, ok := pv["codes"].([]xlsx.CodeSeq)
	if !ok || len(codes) != 100 {
		t.Fatalf("codes = %v", pv["codes"])
	}
	if codes[0].Seq != 1 || codes[0].Code != "PC000" {
		t.Fatalf("codes[0] = %+v", codes[0])
	}
}

func TestPcdRunMockEndToEnd(t *testing.T) {
	state.SetRoot(t.TempDir())
	outdir := filepath.Join(t.TempDir(), "out")
	code1, code2, code6, code99 := mockRunCode(1), mockRunCode(2), mockRunCode(6), "PCD9999999999"
	b := &pcdExportBackend{
		StateSt:  &state.Store{Root: t.TempDir()},
		LoadConf: mockCfg,
	}
	j, res, _ := runJobSync(t, b, map[string]any{
		"outdir":       outdir,
		"batch_subdir": false,
		"codes_text":   code1 + " " + code2 + " " + code6 + " " + code99,
	})
	if j.Status != "success" {
		t.Fatalf("status = %s error=%s", j.Status, j.Error)
	}
	total, _ := res["total"].(float64)
	if total != 4 {
		t.Fatalf("total = %v", res["total"])
	}
	found, _ := res["found"].(float64)
	if found != 3 {
		t.Fatalf("found = %v（mock 单 01/02/06 应命中）", res["found"])
	}
	nf, _ := res["not_found"].([]any)
	if len(nf) != 1 || nf[0] != code99 {
		t.Fatalf("not_found = %v", res["not_found"])
	}
	pcdOK, _ := res["pcd_ok"].(float64)
	if pcdOK != 3 {
		t.Fatalf("pcd_ok = %v", res["pcd_ok"])
	}
	trackOK, _ := res["track_ok"].(float64)
	if trackOK != 2 {
		t.Fatalf("track_ok = %v（未完结单应跳过轨迹）", res["track_ok"])
	}
	// 产物落盘（mock 占位 PDF + 清单）
	for _, name := range []string{"_合并_派车单.pdf", "_合并_轨迹.pdf", "_清单.csv", "_未找到.txt", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(outdir, name)); err != nil {
			t.Fatalf("产物缺失 %s: %v", name, err)
		}
	}
	// 事件桥进度已进入 job
	if j.Progress()["label"] == "" {
		t.Fatal("进度事件未桥接")
	}
}

func TestPcdRunValidation(t *testing.T) {
	state.SetRoot(t.TempDir())
	b := &pcdExportBackend{StateSt: &state.Store{Root: t.TempDir()}, LoadConf: mockCfg}
	// 未勾选任何导出
	j, _, _ := runJobSync(t, b, map[string]any{
		"codes_text":   "PC001",
		"export_pcd":   false,
		"export_track": false,
	})
	if j.Status != "failed" || !strings.Contains(j.Error, "至少勾选一项") {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	// 无单号来源
	j2, _, _ := runJobSync(t, b, map[string]any{"codes_text": ""})
	if j2.Status != "failed" || !strings.Contains(j2.Error, "未提供派车单号来源") {
		t.Fatalf("status=%s error=%s", j2.Status, j2.Error)
	}
}

func TestCfgSecrets(t *testing.T) {
	cfg := config.Dict{
		"sso":  config.Dict{"password": "pw1"},
		"_ini": config.Dict{"auth": config.Dict{"password": "pw2"}},
	}
	secrets := cfgSecrets(cfg)
	if len(secrets) != 2 {
		t.Fatalf("secrets = %v", secrets)
	}
	// 事件桥脱敏
	b := newEventBridge(nil, secrets)
	if got := b.scrub("token pw1 and pw2"); got != "token ****** and ******" {
		t.Fatalf("scrub = %q", got)
	}
}
