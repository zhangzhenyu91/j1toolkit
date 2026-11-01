package worklog

// .xls 转换器行为测试：用测试二进制充当 soffice，不实际调用 LibreOffice。

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func xlsConverterWithHelper(mode string, output []byte) *LibreOfficeXLSConverter {
	c := &LibreOfficeXLSConverter{}
	c.CommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestXLSHelperProcess$", "--"}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_XLS_HELPER=1",
			"XLS_HELPER_MODE="+mode,
			"XLS_HELPER_OUTPUT="+base64.StdEncoding.EncodeToString(output),
		)
		return cmd
	}
	return c
}

func TestLibreOfficeXLSConverterSuccess(t *testing.T) {
	want := []byte("PK-fake-xlsx-bytes")
	out, err := xlsConverterWithHelper("ok", want).ToXLSX(context.Background(), []byte("BIFF"))
	if err != nil {
		t.Fatalf("ToXLSX: %v", err)
	}
	if string(out) != string(want) {
		t.Fatalf("输出不符：%q", out)
	}
}

func TestLibreOfficeXLSConverterFailureTruncated(t *testing.T) {
	out, err := xlsConverterWithHelper("fail", nil).ToXLSX(context.Background(), []byte("BIFF"))
	if err == nil || out != nil {
		t.Fatal("期望转换失败且无输出")
	}
	msg := err.Error()
	if !strings.Contains(msg, "表格转换失败") {
		t.Fatalf("错误口径不符：%s", msg)
	}
	if !strings.Contains(msg, strings.Repeat("E", 50)) {
		t.Fatalf("错误缺 stderr 摘要：%s", msg)
	}
	if strings.Contains(msg, strings.Repeat("E", 201)) {
		t.Fatalf("stderr 未截断：%s", msg)
	}
}

func TestLibreOfficeXLSConverterNotInstalled(t *testing.T) {
	c := &LibreOfficeXLSConverter{SofficePath: filepath.Join(t.TempDir(), "no-such-soffice")}
	_, err := c.ToXLSX(context.Background(), []byte("BIFF"))
	if err == nil || !strings.Contains(err.Error(), "未安装 LibreOffice") {
		t.Fatalf("未安装应转友好报错，实际 %v", err)
	}
}

// TestXLSHelperProcess 测试二进制的 soffice 替身：ok 按 --outdir 写出 source.xlsx，fail 写长 stderr 并退出 3。
func TestXLSHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_XLS_HELPER") != "1" {
		return
	}
	args := os.Args
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		os.Exit(2)
	}
	sofficeArgs := args[sep+1:]
	if os.Getenv("XLS_HELPER_MODE") == "fail" {
		_, _ = os.Stderr.WriteString(strings.Repeat("E", 500))
		os.Exit(3)
	}
	outDir := ""
	for i, a := range sofficeArgs {
		if a == "--outdir" && i+1 < len(sofficeArgs) {
			outDir = sofficeArgs[i+1]
		}
	}
	if outDir == "" {
		os.Exit(2)
	}
	raw, err := base64.StdEncoding.DecodeString(os.Getenv("XLS_HELPER_OUTPUT"))
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(outDir, "source.xlsx"), raw, 0o644); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
