// 派车单旧版 .xls 解析依赖：LibreOffice headless 转 .xlsx 后交 excelize 读取。
// excelize 只认 OOXML；派车系统常见的 BIFF8 .xls 与 HTML/SpreadsheetML 伪装的 .xls
// 都先经 soffice 归一为 .xlsx，再走同一套固定列位解析。
// soffice 为系统命令而非库依赖：Docker 镜像须安装 LibreOffice（已含 libreoffice-calc），
// 路径可用 WORKLOG_SOFFICE_PATH 覆盖（默认 PATH 中的 soffice），未安装时转友好报错。
// 转换器可注入（测试用桩，不实际调用 LibreOffice）。
package worklog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"shade/internal/logsafe"
)

// XLSConverter 旧版 .xls / 非 OOXML 表格 → .xlsx 转换适配层。
type XLSConverter interface {
	// ToXLSX 单个表格字节 → .xlsx 字节；解析失败时返回可读中文错误。
	ToXLSX(ctx context.Context, data []byte) ([]byte, error)
}

// xlsConvertTimeout 单文件转换超时（与安全日文档转换同口径：大体积表实测可能较慢）。
const xlsConvertTimeout = 120 * time.Second

// LibreOfficeXLSConverter 依赖系统 soffice 的实现。
type LibreOfficeXLSConverter struct {
	// SofficePath soffice 可执行文件路径（空取 PATH 中的 soffice）。
	SofficePath string
	// Timeout 单文件转换超时（缺省 120s）。
	Timeout time.Duration
	// CommandContext 命令构造（测试注入；nil 用 exec.CommandContext）。
	CommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// ToXLSX 转 .xlsx：临时目录落盘 source.xls（扩展名固定，soffice 按内容识别），
// 转换后读回 source.xlsx；每次独立 UserInstallation，避免并发/残留锁互斥。
func (c *LibreOfficeXLSConverter) ToXLSX(ctx context.Context, data []byte) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = xlsConvertTimeout
	}
	soffice := c.SofficePath
	if soffice == "" {
		soffice = "soffice"
	}
	tmp, err := os.MkdirTemp("", "worklog-xls-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	input := filepath.Join(tmp, "source.xls")
	if err := os.WriteFile(input, data, 0o644); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"-env:UserInstallation=file://" + filepath.ToSlash(filepath.Join(tmp, "lo-profile")),
		"--headless", "--norestore",
		"--convert-to", "xlsx",
		"--outdir", tmp,
		input,
	}
	cmd := c.command(runCtx, soffice, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil {
		if errors.Is(runErr, exec.ErrNotFound) || xlsCmdMissing(runErr) {
			return nil, errors.New("服务端未安装 LibreOffice（soffice），无法解析该表格，请另存为 .xlsx 后导入")
		}
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("表格转换超时（%ds），文件可能过大或损坏", int(timeout.Seconds()))
		}
		msg := xlsErrText(stderr.String())
		if msg == "" {
			msg = logsafe.TextForLog(runErr.Error())
		}
		return nil, fmt.Errorf("表格转换失败：%s", msg)
	}
	out, err := os.ReadFile(filepath.Join(tmp, "source.xlsx"))
	if err != nil {
		msg := xlsErrText(stderr.String())
		if msg == "" {
			msg = "无输出"
		}
		return nil, fmt.Errorf("表格转换失败：未产出 .xlsx（%s）", msg)
	}
	return out, nil
}

// command 构造执行命令（测试可注入）。
func (c *LibreOfficeXLSConverter) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if c.CommandContext != nil {
		return c.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

// xlsCmdMissing 判定命令不存在（Windows/POSIX 双口径）。
func xlsCmdMissing(err error) bool {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, os.ErrNotExist)
	}
	return false
}

// xlsErrText 先脱敏再按 rune 截断 200 字符（避免切坏 UTF-8 多字节字符）。
func xlsErrText(s string) string {
	s = logsafe.TextForLog(s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}
