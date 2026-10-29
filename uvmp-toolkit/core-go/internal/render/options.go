// Package render 是官方页面直出渲染层：用 CDP 驱动独立浏览器进程（默认宿主公司浏览器
// 360ent 等；显式 Electron 回退沿用 browser-runtime）打开真实派车系统，
// 调官方 cube.showDialog 打开「行程轨迹」弹窗（官方 dataform 面板 + 官方 SGMap 轨迹图）截图，
// 以及官方「派车单批量打印」弹窗提取打印区 DOM 后 printToPDF。行为完整移植自
// uvmp-toolkit/official_track.py 的 CdpBrowser / OfficialTrackRenderer，供导出 Worker 调用。
//
// 约束：
//   - 每个 Worker 一次只渲一单（方法间互斥），并行由调用方开多个 Worker 编排；
//   - 浏览器可执行文件路径由调用方传入（不在目标机下载）；
//   - 默认不开启全局 ignore-certificate-errors / disable-web-security，证书信任走 SPKI pins，
//     两个危险开关仅保留显式兼容开关供国密浏览器回退；
//   - 凭证（token/refresh）只经 JS 注入页面，不进命令行、不落日志（日志与诊断输出统一脱敏）。
package render

import (
	"fmt"
	"strings"
	"time"
)

// 等待时长与窗口默认值（与 official_track.py 构造参数默认值一致）。
const (
	DefaultWidth  = 1500
	DefaultHeight = 950

	DefaultWaitDialog = 60 * time.Second  // 轨迹弹窗出现等待上限
	DefaultWaitTiles  = 12 * time.Second  // 地图网络静默检测的兜底上限
	DefaultWaitLogin  = 180 * time.Second // 页面自动登录等待上限
	DefaultIdleQuiet  = 5 * time.Second   // 网络静默判定时长
	DefaultMinWait    = 0                 // 轨迹弹窗出现后的固定等待下限（0=不等待）
	DefaultPCDWait    = 6 * time.Second   // 派车单打印区出现后的固定等待（0=不等待）
)

// Credentials 官方页面登录凭证。密码不进本包；Token/RefreshToken 只经 JS 注入页面。
type Credentials struct {
	Token        string         // cube.token（Auth_Tk）
	RefreshToken string         // cube.refreshToken（Refresh_Tk，可空）
	LoginUser    map[string]any // cube.loginUser 对象（可空）
	Generation   int            // 凭证代数：调用方在令牌刷新后递增；用于日志/诊断区分批次
}

// Options 渲染工作进程的启动参数与各等待时长。
type Options struct {
	// BrowserPath 公司浏览器可执行文件路径（必填，由调用方传入/用户手选，不在目标机下载）。
	BrowserPath string
	// WorkDir 工作目录：浏览器 profile（.chrome-official-tmp）、浏览器日志（official_chrome.log）
	// 与登录失败诊断图（official_login_diag.png）都落在此目录下。
	WorkDir string

	Width  int // 浏览器窗口宽（默认 DefaultWidth）
	Height int // 浏览器窗口高（默认 DefaultHeight）

	// 各等待时长：零值取 Default*（MinWait/PCDWait 例外——0 即「不等待」，需显式给值，
	// 用 DefaultOptions 可一次填齐）。
	WaitDialog time.Duration // 轨迹弹窗出现等待上限
	WaitTiles  time.Duration // 地图网络静默检测的兜底上限
	WaitLogin  time.Duration // 页面自动登录等待上限
	IdleQuiet  time.Duration // 网络静默判定时长
	MinWait    time.Duration // 轨迹弹窗出现后的固定等待下限（0=不等待）
	PCDWait    time.Duration // 派车单打印区/包装页样式就绪的固定等待（0=不等待）

	// SPKIPins 证书 SPKI pin（base64），映射 Chrome 的
	// --ignore-certificate-errors-spki-list（分号分隔）。内网自签证书的正规信任路径。
	SPKIPins []string

	// BrowserArgs 传给浏览器可执行文件的额外位置参数（仅历史 Electron 回退：源码应用
	// 目录与 --uvmp-cdp-worker，或集成测试使用的 cdp-worker.cjs 路径），插在全部 --开关
	// 之后、about:blank 之前。宿主浏览器（默认）无需位置参数。
	BrowserArgs []string

	// NoHeadless 为 true 时走历史 Electron 回退（chromedp ExecAllocator 拉起
	// browser-runtime，不传 --headless=new）；false 为宿主浏览器（本包直接拉起、
	// 读 DevToolsActivePort 连 CDP、带 --headless=new）。
	NoHeadless bool

	// 兼容开关（默认关，仅供国密浏览器等特殊环境回退）：
	// 全局忽略证书错误 / 禁用同源策略。
	IgnoreCertificateErrors bool
	DisableWebSecurity      bool

	// Log 日志回调（每行一条，已脱敏）；nil 则丢弃。
	Log func(line string)
}

// DefaultOptions 返回一份填好默认值的 Options，再按需覆盖。
func DefaultOptions(browserPath, workDir string) Options {
	return Options{
		BrowserPath: browserPath,
		WorkDir:     workDir,
		Width:       DefaultWidth,
		Height:      DefaultHeight,
		WaitDialog:  DefaultWaitDialog,
		WaitTiles:   DefaultWaitTiles,
		WaitLogin:   DefaultWaitLogin,
		IdleQuiet:   DefaultIdleQuiet,
		MinWait:     DefaultMinWait,
		PCDWait:     DefaultPCDWait,
	}
}

// normalize 校验必填项并补零值默认（MinWait/PCDWait 不补：0 即关闭）。
func (o *Options) normalize() error {
	if strings.TrimSpace(o.BrowserPath) == "" {
		return fmt.Errorf("render: BrowserPath 必填（公司浏览器路径，由调用方传入）")
	}
	if strings.TrimSpace(o.WorkDir) == "" {
		return fmt.Errorf("render: WorkDir 必填（浏览器 profile 与日志目录）")
	}
	if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	if o.Height <= 0 {
		o.Height = DefaultHeight
	}
	if o.WaitDialog <= 0 {
		o.WaitDialog = DefaultWaitDialog
	}
	if o.WaitTiles <= 0 {
		o.WaitTiles = DefaultWaitTiles
	}
	if o.WaitLogin <= 0 {
		o.WaitLogin = DefaultWaitLogin
	}
	if o.IdleQuiet <= 0 {
		o.IdleQuiet = DefaultIdleQuiet
	}
	if o.MinWait < 0 {
		o.MinWait = 0
	}
	if o.PCDWait < 0 {
		o.PCDWait = 0
	}
	return nil
}

// chromeSwitch 是浏览器命令行开关的中间形态：value 为 string 时输出 --name=value，
// 为 true 时输出 --name（与 chromedp.Flag 的取值约定一致）。
type chromeSwitch struct {
	name  string
	value any
}

// switches 组装 Chromium 启动开关（纯函数，便于单测断言安全默认值）。
// profileDir 为独立 profile 目录；与 official_track.py 的 CdpBrowser 命令行保持一致，
// 但默认不带 --ignore-certificate-errors / --disable-web-security；NoHeadless 时
// 不带 --headless=new（Electron 等宿主不支持）。
func (o Options) switches(profileDir string) []chromeSwitch {
	sw := make([]chromeSwitch, 0, 12)
	if !o.NoHeadless {
		sw = append(sw, chromeSwitch{"headless", "new"})
	}
	sw = append(sw,
		chromeSwitch{"no-sandbox", true},
		chromeSwitch{"disable-dev-shm-usage", true},
		chromeSwitch{"remote-debugging-port", "0"}, // 随机调试端口（DevToolsActivePort）
		chromeSwitch{"remote-allow-origins", "*"},
		chromeSwitch{"enable-unsafe-swiftshader", true},
		chromeSwitch{"use-angle", "swiftshader"},
		chromeSwitch{"user-data-dir", profileDir},
		chromeSwitch{"window-size", fmt.Sprintf("%d,%d", o.Width, o.Height)},
	)
	if len(o.SPKIPins) > 0 {
		sw = append(sw, chromeSwitch{"ignore-certificate-errors-spki-list", strings.Join(o.SPKIPins, ";")})
	}
	if o.IgnoreCertificateErrors {
		sw = append(sw, chromeSwitch{"ignore-certificate-errors", true})
	}
	if o.DisableWebSecurity {
		sw = append(sw, chromeSwitch{"disable-web-security", true})
	}
	return sw
}

// formatSwitches 把开关渲染成命令行参数（测试与诊断用）。
func formatSwitches(sw []chromeSwitch) []string {
	args := make([]string, 0, len(sw))
	for _, s := range sw {
		switch v := s.value.(type) {
		case string:
			args = append(args, "--"+s.name+"="+v)
		case bool:
			if v {
				args = append(args, "--"+s.name)
			}
		}
	}
	return args
}

// insertBrowserArgs 把 extra 位置参数插到 args 末尾的 about:blank 之前
// （chromedp 固定在命令行末尾追加 about:blank 兜底首屏；Electron 的应用脚本等
// 位置参数必须排在它前面、所有 --开关 之后）。args 末尾不是 about:blank 时原样
// 追加到末尾。纯函数，便于单测。
func insertBrowserArgs(args, extra []string) []string {
	if len(extra) == 0 {
		return args
	}
	n := len(args)
	if n == 0 || args[n-1] != "about:blank" {
		out := make([]string, 0, n+len(extra))
		out = append(out, args...)
		return append(out, extra...)
	}
	out := make([]string, 0, n+len(extra))
	out = append(out, args[:n-1]...)
	out = append(out, extra...)
	return append(out, "about:blank")
}
