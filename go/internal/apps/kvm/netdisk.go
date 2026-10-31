// 团队网盘对接口（对齐 server/src/netdisk/save.js + netdisk/space.js 的 kvm 侧用法）：
// 网盘 → 设备推送与设备 → 网盘保存；网盘客户端归 netdisk 应用实现，此处只定窄接口供注入，
// 路径净化 normRel/sanitizeName 防路径穿越。真实外部调用不在此包内发起，测试用桩。
package kvm

import (
	"context"
	"errors"
	"io"
	"strings"

	"shade/internal/auth"
)

// NetdiskBridge 团队网盘桥接（可注入；nil 时相关接口报「未接入」）。
type NetdiskBridge interface {
	// AssertAccess 校验用户持有 netdisk 应用权限（无权限返回可读 BizError 40301）。
	AssertAccess(ctx context.Context, userID int64) error
	// Download 拉取网盘文件内容（space: my/public；rel 为空间内相对路径，已由 normRel 净化）。
	Download(ctx context.Context, user auth.Identity, space, rel string) ([]byte, error)
	// Save 保存文件到网盘（dir 为空间内相对目录、name 为文件名，body 为文件内容流——
	// 大文件流式直传不整读内存；size 为已知字节数，≤0 表示未知；返回保存路径）。
	Save(ctx context.Context, user auth.Identity, space, dir, name string, body io.Reader, size int64) (string, error)
}

// NetdiskStreamBridge 可选流式拉取能力（bridge.KvmBridge 生产实现满足）：
// 网盘 → 设备两跳全程流式，大文件不整读内存；未实现时回退 Download 整读兼容。
type NetdiskStreamBridge interface {
	// DownloadStream 返回文件流与元数据大小（调用方必须 Close）；size=0 表示空文件。
	DownloadStream(ctx context.Context, user auth.Identity, space, rel string) (io.ReadCloser, int64, error)
}

// normRel 归一相对路径：反斜杠转正、去多余斜杠、拒绝 .. 与单点、限长；'/' 表示空间根。
// 非法返回 ("", false)（对齐 netdisk/space.js normRel）。
func normRel(input string) (string, bool) {
	p := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
	if p == "" || p == "/" {
		return "/", true
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	parts := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		if seg == ".." || seg == "." {
			return "", false
		}
		parts = append(parts, seg)
	}
	joined := "/" + strings.Join(parts, "/")
	if len(joined) > 400 {
		return "", false
	}
	return joined, true
}

// sanitizeName 文件名净化：去路径分隔符与非法字符（与 worklog zip 同口径防路径穿越）。
func sanitizeName(name string) (string, bool) {
	n := strings.TrimSpace(name)
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", `"`, "_", "<", "_", ">", "_", "|", "_", "\r", "_", "\n", "_")
	n = repl.Replace(n)
	runes := []rune(n)
	if len(runes) > 200 {
		n = string(runes[:200])
	}
	if n == "" || n == "." || n == ".." {
		return "", false
	}
	return n, true
}

// errNetdiskNotConfigured 网盘未接入。
var errNetdiskNotConfigured = errors.New("团队网盘未接入")
