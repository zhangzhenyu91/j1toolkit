package browser

import (
	"fmt"
	"strings"
)

// buildArgs 组装浏览器命令行。纯函数，便于单测断言安全默认值。
//
// 宿主浏览器（RuntimeDir 为空，默认）：
//   - 两模式：--user-data-dir（独立/持久 profile）/ --window-size /
//     --remote-allow-origins=*（CDP WebSocket 客户端跨源）/ --disable-dev-shm-usage；
//   - export：--headless=new + --remote-debugging-port=0（随机端口，DevToolsActivePort
//     通告）+ --disable-gpu + --no-sandbox（独立临时 profile，不弹窗、官方页面直出）；
//   - course：--remote-debugging-port=<CDPURL 端口>（默认 9222）+ --no-first-run /
//     --no-default-browser-check，start_url 直接作为首窗位置参数（可见窗口、持久 profile），
//     root 补 --no-sandbox；不附任何 Electron 专属参数。
//
// Electron 回退（RuntimeDir 非空，显式配置）：
//   - export：--uvmp-browser-mode=export（不可见 worker 窗口）+ 随机端口 + --disable-gpu；
//   - course：--uvmp-browser-mode=course + 端口 + --uvmp-start-url=<start_url>；
//   - 末尾追加运行时应用目录（Electron 按 package.json main 加载 main.cjs）。
//
// SPKIPins：--ignore-certificate-errors-spki-list（分号分隔），两模式均可配。
func (c Config) buildArgs() []string {
	electron := c.ElectronFallback()
	args := []string{
		"--user-data-dir=" + c.ProfileDir,
		fmt.Sprintf("--window-size=%d,%d", c.Width, c.Height),
		"--remote-allow-origins=*",
		"--disable-dev-shm-usage",
	}
	switch c.Mode {
	case ModeExport:
		if electron {
			args = append(args, "--uvmp-browser-mode=export")
		} else {
			// 宿主浏览器导出必须不可见：独立临时 profile + 无头新架构
			args = append(args, "--headless=new")
		}
		args = append(args,
			"--remote-debugging-port=0",
			"--disable-gpu",
			"--no-sandbox",
		)
	case ModeCourse:
		// CDPURL 已过 ValidateLoopbackURL（normalize 内），端口必然合法
		_, port, _ := ValidateLoopbackURL(c.CDPURL)
		args = append(args,
			fmt.Sprintf("--remote-debugging-port=%d", port),
			"--no-first-run",
			"--no-default-browser-check",
		)
		if electron {
			args = append(args, "--uvmp-browser-mode=course")
			if strings.TrimSpace(c.StartURL) != "" {
				args = append(args, "--uvmp-start-url="+strings.TrimSpace(c.StartURL))
			}
		}
		if isRoot() {
			args = append(args, "--no-sandbox")
		}
	}
	if len(c.SPKIPins) > 0 {
		args = append(args, "--ignore-certificate-errors-spki-list="+strings.Join(c.SPKIPins, ";"))
	}
	if electron {
		return append(args, c.RuntimeDir)
	}
	if c.Mode == ModeCourse {
		// 宿主浏览器：启动网址直接作为首窗位置参数（空则不追加，落 about:blank）
		if u := strings.TrimSpace(c.StartURL); u != "" {
			args = append(args, u)
		}
	}
	return args
}
