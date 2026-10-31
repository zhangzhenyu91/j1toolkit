// 安全日活动记录：非 PDF 文件转 PDF（对齐 server/src/safeday/convert.js，LibreOffice headless）。
// soffice 为系统命令而非库依赖：Docker 镜像须安装 LibreOffice；路径可用 SAFEDAY_SOFFICE_PATH 覆盖
// （默认 PATH 中的 soffice），未安装时转友好报错。转换器可注入（测试用桩，不实际调用 LibreOffice）。
package safeday

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"shade/internal/logging"
	"shade/internal/logsafe"
)

// PDFConverter 非 PDF → PDF 转换适配层。
type PDFConverter interface {
	// ToPDF 单个非 PDF 字节 → PDF 字节；ext 不含点（doc/docx/ppt/pptx/xls/xlsx）。
	ToPDF(ctx context.Context, data []byte, ext string) ([]byte, error)
}

// convertTimeout 单文件转换超时 120s（大体积 ppt/xls 实测可能较慢）。
const convertTimeout = 120 * time.Second

// LibreOfficeConverter 依赖系统 soffice 的实现。
type LibreOfficeConverter struct {
	// SofficePath soffice 可执行文件路径（空取 PATH 中的 soffice）。
	SofficePath string
	// Timeout 单文件转换超时（缺省 120s）。
	Timeout time.Duration
	// Log 转换日志（耗时/退出状态/截断 stderr；nil 不记录）。
	Log *logging.Logger
	// CommandContext 命令构造（测试注入；nil 用 exec.CommandContext）。
	CommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// ToPDF 转 PDF：mkdtemp 建临时目录，输入落盘 source.{ext}，转换后读回 source.pdf。
func (c *LibreOfficeConverter) ToPDF(ctx context.Context, data []byte, ext string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = convertTimeout
	}
	soffice := c.SofficePath
	if soffice == "" {
		soffice = "soffice"
	}
	start := time.Now()
	tmp, err := os.MkdirTemp("", "safeday-cv-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	input := filepath.Join(tmp, "source."+ext)
	if err := os.WriteFile(input, data, 0o644); err != nil {
		return nil, err
	}
	// 每次转换独立 UserInstallation：避免并发/残留锁导致 headless 实例互斥失败
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"-env:UserInstallation=file://" + filepath.ToSlash(filepath.Join(tmp, "lo-profile")),
		"--headless", "--norestore",
		"--convert-to", "pdf",
		"--outdir", tmp,
		input,
	}
	cmd := c.command(runCtx, soffice, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	durationMs := float64(time.Since(start)) / float64(time.Millisecond)
	if runErr != nil {
		status := exitStatus(runErr)
		msg := truncateStderr(stderr.String())
		if errors.Is(runErr, exec.ErrNotFound) || isENOENT(runErr) {
			c.logConvert(ctx, "error", "safeday.convert.failed", "LibreOffice 转换失败（未安装）", durationMs, status, ext, 0, msg)
			return nil, errors.New("服务端未安装 LibreOffice（soffice），无法转换非 PDF 文件，请联系管理员")
		}
		if runCtx.Err() == context.DeadlineExceeded {
			c.logConvert(ctx, "error", "safeday.convert.failed", "LibreOffice 转换超时", durationMs, status, ext, 0, msg)
			return nil, errors.New("格式转换超时（120s），文件可能过大或损坏")
		}
		if msg == "" {
			msg = logsafe.TextForLog(runErr.Error())
		}
		c.logConvert(ctx, "error", "safeday.convert.failed", "LibreOffice 转换失败", durationMs, status, ext, 0, msg)
		return nil, fmt.Errorf("格式转换失败：%s", msg)
	}
	out, err := os.ReadFile(filepath.Join(tmp, "source.pdf"))
	if err != nil {
		msg := truncateStderr(stderr.String())
		if msg == "" {
			msg = "无输出"
		}
		c.logConvert(ctx, "error", "safeday.convert.failed", "LibreOffice 未产出 PDF", durationMs, 0, ext, 0, msg)
		return nil, fmt.Errorf("格式转换失败：未产出 PDF（%s）", msg)
	}
	c.logConvert(ctx, "info", "safeday.convert", "LibreOffice 转换完成", durationMs, 0, ext, len(out), "")
	return out, nil
}

// command 构造执行命令（测试可注入）。
func (c *LibreOfficeConverter) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if c.CommandContext != nil {
		return c.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

// exitStatus 取进程退出码（未启动/信号终止返回 -1）。
func exitStatus(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// truncateStderr 先脱敏再按 rune 截断 200 字符（避免切坏 UTF-8 多字节字符）。
func truncateStderr(s string) string {
	s = logsafe.TextForLog(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// logConvert 写转换事件：耗时、退出状态、扩展名、输出字节与截断 stderr；Log 为 nil 静默。
func (c *LibreOfficeConverter) logConvert(ctx context.Context, level, event, messageCN string, durationMs float64, exitStatus int, ext string, outputBytes int, stderr string) {
	if c == nil || c.Log == nil {
		return
	}
	fields := map[string]any{
		"duration_ms":  math.Round(durationMs*10) / 10,
		"exit_status":  exitStatus,
		"ext":          ext,
		"output_bytes": outputBytes,
	}
	raw := fmt.Sprintf("soffice ext=%s exit=%d duration_ms=%.1f output_bytes=%d", ext, exitStatus, durationMs, outputBytes)
	if stderr != "" {
		fields["stderr"] = stderr
		raw += " stderr=" + stderr
	}
	_ = c.Log.Record(ctx, level, "business", event, messageCN, raw, fields)
}

// isENOENT 判定命令不存在（Windows/POSIX 双口径）。
func isENOENT(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return errors.Is(pathErr.Err, os.ErrNotExist)
	}
	return false
}
