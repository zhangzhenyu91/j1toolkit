// 安全日活动记录：非 PDF 文件转 PDF（对齐 server/src/safeday/convert.js，LibreOffice headless）。
// soffice 为系统命令而非库依赖：Docker 镜像须安装 LibreOffice；路径可用 SAFEDAY_SOFFICE_PATH 覆盖
// （默认 PATH 中的 soffice），未安装时转友好报错。转换器可注入（测试用桩，不实际调用 LibreOffice）。
package safeday

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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
	cmd := exec.CommandContext(runCtx, soffice,
		"-env:UserInstallation=file://"+filepath.ToSlash(filepath.Join(tmp, "lo-profile")),
		"--headless", "--norestore",
		"--convert-to", "pdf",
		"--outdir", tmp,
		input,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || isENOENT(err) {
			return nil, errors.New("服务端未安装 LibreOffice（soffice），无法转换非 PDF 文件，请联系管理员")
		}
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, errors.New("格式转换超时（120s），文件可能过大或损坏")
		}
		msg := stderr.String()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("格式转换失败：%s", msg)
	}
	out, err := os.ReadFile(filepath.Join(tmp, "source.pdf"))
	if err != nil {
		msg := stderr.String()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			msg = "无输出"
		}
		return nil, fmt.Errorf("格式转换失败：未产出 PDF（%s）", msg)
	}
	return out, nil
}

// isENOENT 判定命令不存在（Windows/POSIX 双口径）。
func isENOENT(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return errors.Is(pathErr.Err, os.ErrNotExist)
	}
	return false
}
