package browser

import (
	"fmt"
	"strings"
)

// buildArgs 组装宿主命令行：全部为 --开关 + 末尾运行时应用目录（browser-runtime，
// Electron 按 package.json main 加载 main.cjs）。纯函数，便于单测断言安全默认值。
//
// 开关口径：
//   - 两模式：--user-data-dir（独立 profile）/ --window-size / --remote-allow-origins=*
//     （CDP WebSocket 客户端跨源，Playwright connect_over_cdp 需要）/ --disable-dev-shm-usage；
//   - export：--remote-debugging-port=0（随机端口，DevToolsActivePort 通告）+ --disable-gpu
//     （不可见窗口）+ --no-sandbox（与 render 渲染链路一致）；
//   - course：--remote-debugging-port=<CDPURL 端口>（默认 9222）+ --no-first-run /
//     --no-default-browser-check（与 Python browser_automation.launch 口径一致），root 补
//     --no-sandbox；start_url 非空时经 --uvmp-start-url 传给运行时首窗自动访问；
//   - SPKIPins：--ignore-certificate-errors-spki-list（分号分隔）。配置后导出模式运行时
//     不再使用地图域名豁免（见 browser-runtime/main.cjs 证书策略）。
func (c Config) buildArgs() []string {
	args := []string{
		"--user-data-dir=" + c.ProfileDir,
		fmt.Sprintf("--window-size=%d,%d", c.Width, c.Height),
		"--remote-allow-origins=*",
		"--disable-dev-shm-usage",
	}
	switch c.Mode {
	case ModeExport:
		args = append(args,
			"--uvmp-browser-mode=export",
			"--remote-debugging-port=0",
			"--disable-gpu",
			"--no-sandbox",
		)
	case ModeCourse:
		// CDPURL 已过 ValidateLoopbackURL（normalize 内），端口必然合法
		_, port, _ := ValidateLoopbackURL(c.CDPURL)
		args = append(args,
			"--uvmp-browser-mode=course",
			fmt.Sprintf("--remote-debugging-port=%d", port),
			"--no-first-run",
			"--no-default-browser-check",
		)
		if strings.TrimSpace(c.StartURL) != "" {
			args = append(args, "--uvmp-start-url="+strings.TrimSpace(c.StartURL))
		}
		if isRoot() {
			args = append(args, "--no-sandbox")
		}
	}
	if len(c.SPKIPins) > 0 {
		args = append(args, "--ignore-certificate-errors-spki-list="+strings.Join(c.SPKIPins, ";"))
	}
	return append(args, c.RuntimeDir)
}
