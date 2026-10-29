// Package browser 是受控宿主浏览器的 Go 侧封装：双模式启动 / 状态 / 停止、CDP 地址
// 与 profile 查询、仅回环地址校验，供课程（course）与 GUI 调用。默认驱动宿主机上的
// 公司指定浏览器（360 企业浏览器 360ent 优先，见 detect.go）；旧 browser-runtime
// （Electron）仅在调用方显式给出 RuntimeDir 时作为回退宿主（见 Config.RuntimeDir），
// 不自动探测、不下载。
//
// 两种模式（Config.Mode）：
//   - ModeExport 不可见导出渲染：独立临时 profile、随机调试端口（DevToolsActivePort 通告），
//     每 Start 一个独立进程（渲染 Worker 语义），不复用。
//   - ModeCourse 可见课程模式：持久 profile（登录态跨次保留）、可见窗口、start_url 直接
//     作为首窗地址、调试端口默认 9222（仅本机回环）、同端口进程复用（已在跑则不重复启动）、
//     不做证书豁免。
//
// Electron 回退（RuntimeDir 非空）沿用 browser-runtime 适配器：--uvmp-browser-mode=
// export|course 双模式、回环桥（cdp-worker-bridge.json）、DevTools 通告与地图域名
// 证书豁免/SPKI pin 保持原契约。
//
// 安全边界：CDP/回环桥地址一律仅本机回环（ValidateLoopbackURL）；启动网址仅允许
// http/https/data/about；进程命令行不携带任何凭证（凭证由调用方经 CDP/JS 注入）。
package browser

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mode 独立浏览器运行时模式。
type Mode string

const (
	// ModeExport 不可见导出渲染模式（供 internal/render 导出链路与自检）。
	ModeExport Mode = "export"
	// ModeCourse 可见课程模式（供课程自动学习与 GUI 受控浏览器）。
	ModeCourse Mode = "course"
)

// 默认值（窗口尺寸与 render.DefaultWidth/Height 同值）。
const (
	// DefaultCourseCDPURL 课程模式默认调试地址（仅本机回环，端口 9222）。
	DefaultCourseCDPURL = "http://127.0.0.1:9222"
	// DefaultWidth / DefaultHeight 浏览器窗口默认宽高。
	DefaultWidth  = 1500
	DefaultHeight = 950
	// DefaultReadyTimeout 启动就绪等待默认上限（课程模式与浏览器冷启动同数量级）。
	DefaultReadyTimeout = 60 * time.Second
	// DefaultStopGrace 停止宽限期：/shutdown 优雅退出的最长等待。
	DefaultStopGrace = 5 * time.Second
	// pollInterval 就绪/退出轮询间隔。
	pollInterval = 300 * time.Millisecond
	// statusTimeout 单次状态探测（CDP /json/list、回环桥调用）超时。
	statusTimeout = 3 * time.Second
)

// Config 受控浏览器启动配置。构造便捷入口见 NewExportConfig / NewCourseConfig，
// 零值默认由 Validate 补齐；Start/QueryStatus/Stop 均先做同样校验。
type Config struct {
	// Mode 必填：ModeExport / ModeCourse。
	Mode Mode
	// BrowserPath 必填：浏览器可执行文件（默认宿主机公司的 360ent 等；显式 render.chrome
	// 或 browser.DetectHost 结果）。由调用方传入/用户手选，不在目标机下载。
	BrowserPath string
	// RuntimeDir 可选：仅显式 Electron 回退时给出（browser-runtime 应用目录，内含
	// package.json + main.cjs）。为空 = 宿主机浏览器，不需要运行时目录。
	RuntimeDir string
	// ProfileDir 必填：浏览器 profile 目录。课程模式为持久目录（登录态保留），导出模式
	// 为批次临时目录。Electron 回退的回环桥文件与 DevToolsActivePort 落于此。
	ProfileDir string

	// StartURL 课程模式启动网址（空=about:blank；仅 ModeCourse 可用）。允许
	// http/https/data/about（测试用 data: 本地页；生产为国网学堂 http/https 地址）。
	// 宿主浏览器直接作为首窗位置参数；Electron 回退经 --uvmp-start-url 传递。
	StartURL string
	// CDPURL 课程模式调试地址（仅本机回环，空取 DefaultCourseCDPURL；仅 ModeCourse 可用）。
	// 导出模式端口随机，就绪后经 (*Instance).CDPURL 查询。
	CDPURL string

	// Width / Height 窗口尺寸（零值取 DefaultWidth/DefaultHeight）。
	Width  int
	Height int

	// SPKIPins 证书 SPKI pin（base64），映射 Chromium 的
	// --ignore-certificate-errors-spki-list（分号分隔）。内网自签证书的正规信任路径；
	// 配置后导出模式不再使用地图域名豁免。两模式均可配。
	SPKIPins []string

	// ReadyTimeout 启动就绪等待上限（零值取 DefaultReadyTimeout）。
	ReadyTimeout time.Duration
	// StopGrace 停止宽限期（零值取 DefaultStopGrace）。
	StopGrace time.Duration

	// Log 日志回调（每行一条，不脱敏内容已由调用方保证）；nil 则丢弃。
	Log func(line string)
}

// NewExportConfig 返回导出不可见模式配置（再按需覆盖字段）。runtimeDir 为空 =
// 宿主浏览器；非空 = 显式 Electron 回退（browser-runtime 应用目录）。
func NewExportConfig(browserPath, runtimeDir, profileDir string) Config {
	return Config{
		Mode:        ModeExport,
		BrowserPath: browserPath,
		RuntimeDir:  runtimeDir,
		ProfileDir:  profileDir,
		Width:       DefaultWidth,
		Height:      DefaultHeight,
	}
}

// NewCourseConfig 返回课程可见模式配置（cdpURL/startURL 空取默认；再按需覆盖字段）。
// runtimeDir 为空 = 宿主浏览器（start_url 直接作为首窗地址）；非空 = 显式 Electron 回退。
func NewCourseConfig(browserPath, runtimeDir, profileDir, cdpURL, startURL string) Config {
	return Config{
		Mode:        ModeCourse,
		BrowserPath: browserPath,
		RuntimeDir:  runtimeDir,
		ProfileDir:  profileDir,
		CDPURL:      cdpURL,
		StartURL:    startURL,
		Width:       DefaultWidth,
		Height:      DefaultHeight,
	}
}

// ElectronFallback 是否显式 Electron 回退（browser-runtime 应用目录非空）。
func (c Config) ElectronFallback() bool { return strings.TrimSpace(c.RuntimeDir) != "" }

// Validate 校验配置并返回填好默认值的副本（不改接收者）。
func (c Config) Validate() (Config, error) {
	d := c
	if err := d.normalize(); err != nil {
		return Config{}, err
	}
	return d, nil
}

// normalize 校验必填项并补零值默认（详见各字段注释）。
func (c *Config) normalize() error {
	if c.Mode != ModeExport && c.Mode != ModeCourse {
		return fmt.Errorf("browser: Mode 必填且仅支持 %q/%q（当前 %q）", ModeExport, ModeCourse, c.Mode)
	}
	if strings.TrimSpace(c.BrowserPath) == "" {
		return errors.New("browser: BrowserPath 必填（宿主浏览器路径，由调用方传入）")
	}
	if _, err := os.Stat(c.BrowserPath); err != nil {
		return fmt.Errorf("browser: BrowserPath 不存在: %s", c.BrowserPath)
	}
	// RuntimeDir 仅显式 Electron 回退才需要：非空时必须是完整 browser-runtime 应用目录；
	// 宿主浏览器留空即可（不再要求安装运行时目录）。
	if c.ElectronFallback() {
		for _, name := range []string{"package.json", "main.cjs"} {
			if _, err := os.Stat(filepath.Join(c.RuntimeDir, name)); err != nil {
				return fmt.Errorf("browser: Electron 回退的 RuntimeDir 缺少 %s（%s）", name, c.RuntimeDir)
			}
		}
	}
	if strings.TrimSpace(c.ProfileDir) == "" {
		return errors.New("browser: ProfileDir 必填（浏览器 profile 目录）")
	}
	if c.Width <= 0 {
		c.Width = DefaultWidth
	}
	if c.Height <= 0 {
		c.Height = DefaultHeight
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = DefaultReadyTimeout
	}
	if c.StopGrace <= 0 {
		c.StopGrace = DefaultStopGrace
	}
	// SPKI pin 去空白项
	pins := make([]string, 0, len(c.SPKIPins))
	for _, p := range c.SPKIPins {
		if p = strings.TrimSpace(p); p != "" {
			pins = append(pins, p)
		}
	}
	c.SPKIPins = pins

	switch c.Mode {
	case ModeCourse:
		if strings.TrimSpace(c.CDPURL) == "" {
			c.CDPURL = DefaultCourseCDPURL
		}
		norm, _, err := ValidateLoopbackURL(c.CDPURL)
		if err != nil {
			return err
		}
		c.CDPURL = norm
		if err := validateStartURL(c.StartURL); err != nil {
			return err
		}
	case ModeExport:
		if c.StartURL != "" {
			return errors.New("browser: StartURL 仅课程模式可用（导出模式首屏固定 about:blank）")
		}
		if c.CDPURL != "" {
			return errors.New("browser: CDPURL 仅课程模式可用（导出模式调试端口随机）")
		}
	}
	return nil
}

// validateStartURL 校验启动网址：空表示 about:blank；仅允许 http/https/data/about
// （data/about 供本地测试页与空白页；生产课程为 http/https，与 Python
// browser_automation.settings 的口径一致）。
func validateStartURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("browser: StartURL 无效: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "data", "about":
		return nil
	default:
		return fmt.Errorf("browser: StartURL 协议不支持（仅 http/https/data/about）: %s", u.Scheme)
	}
}

// ValidateLoopbackURL 校验 CDP/回环调试地址：仅允许本机回环 HTTP 地址
// （http://127.0.0.1:9222 / http://localhost:9222 / http://[::1]:9222），禁止远程主机、
// https、用户信息、路径、查询与片段，端口必须明确给出。返回规范化地址（去尾斜杠）与端口。
// 与 Python toolkit/browser_automation.validate_cdp_url 同口径。
func ValidateLoopbackURL(raw string) (string, int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, errors.New("browser: CDP 地址不能为空（例如 http://127.0.0.1:9222）")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, fmt.Errorf("browser: CDP 地址无效: %w", err)
	}
	if u.Scheme != "http" {
		return "", 0, errors.New("browser: CDP 地址仅允许 http（仅本机回环，例如 http://127.0.0.1:9222）")
	}
	host := strings.ToLower(u.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return "", 0, fmt.Errorf("browser: CDP 地址仅允许本机回环主机（127.0.0.1/localhost/::1）: %s", u.Hostname())
	}
	if u.User != nil {
		return "", 0, errors.New("browser: CDP 地址不允许携带用户信息")
	}
	if u.Path != "" && u.Path != "/" {
		return "", 0, errors.New("browser: CDP 地址不允许带路径")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", 0, errors.New("browser: CDP 地址不允许带查询或片段")
	}
	portStr := u.Port()
	if portStr == "" {
		return "", 0, errors.New("browser: CDP 地址必须明确指定端口（例如 http://127.0.0.1:9222）")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("browser: CDP 地址端口无效: %s", portStr)
	}
	return strings.TrimSuffix(raw, "/"), port, nil
}

// logf 输出日志行（Log 为空则丢弃）。
func (c Config) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}
