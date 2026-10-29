package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/course"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// courseAutoplayBackend 学堂刷课应用（移植自 toolkit/apps/course_autoplay/backend.py）：
// Run 按培训班检查点续刷/暂停恢复，Status 状态卡（受控浏览器连接 + active_lists），
// LaunchBrowser 启动/复用受控浏览器（internal/browser 课程模式，不创建 Job）。
type courseAutoplayBackend struct{}

// courseSettings browser_automation + course_autoplay 配置（Python browser_automation.settings
// 与 backend.run 参数合并口径）。
type courseSettings struct {
	CDPURL       string
	ProfileDir   string
	StepTimeout  time.Duration
	StartURL     string
	MaxAttempts  int
	ElectronPath string
	RuntimeDir   string
}

// loadCourseSettings 解析并校验配置（错误文案与 Python 同口径）。
func loadCourseSettings(cfg config.Dict) (courseSettings, error) {
	s := courseSettings{}
	ba := config.MapField(cfg, "browser_automation")
	raw := config.StrFieldTrim(ba, "cdp_url")
	if raw == "" {
		raw = browser.DefaultCourseCDPURL
	}
	norm, _, err := browser.ValidateLoopbackURL(raw)
	if err != nil {
		return s, fmt.Errorf("CDP 地址仅允许本机 HTTP 地址，例如 http://127.0.0.1:9222（%s）", err)
	}
	s.CDPURL = norm

	profile := config.StrFieldTrim(ba, "profile_dir")
	if profile == "" {
		profile = filepath.Join(state.StateDir(), "browser-automation", "profile")
	}
	s.ProfileDir = profile

	timeoutSec := config.IntField(ba, "step_timeout", 120)
	if timeoutSec <= 0 {
		return s, fmt.Errorf("browser_automation.step_timeout 必须大于 0")
	}
	s.StepTimeout = time.Duration(timeoutSec) * time.Second

	s.StartURL = config.StrFieldTrim(ba, "start_url")
	if s.StartURL != "" {
		u, err := url.Parse(s.StartURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return s, fmt.Errorf("启动网址必须是完整的 http/https 地址")
		}
	}

	ca := config.MapField(cfg, "course_autoplay")
	s.MaxAttempts = config.IntField(ca, "max_attempts", 3)

	s.ElectronPath = config.StrFieldTrim(ba, "electron_path")
	s.RuntimeDir = config.StrFieldTrim(ba, "runtime_dir")
	return s, nil
}

// resolveBrowserHost 定位受控浏览器宿主（Electron + browser-runtime 应用目录）：
// 配置 electron_path/runtime_dir → 环境变量 UVMP_ELECTRON/UVMP_BROWSER_RUNTIME_DIR →
// internal/browser 统一探测（开发树与 fyne 安装布局同一口径，与导出/自检 env 注入
// 共用 browser.ScanRuntime：应用目录为 browser-runtime/app 或源码 browser-runtime）。
// 找不到给出可操作错误。
func resolveBrowserHost(s courseSettings) (electron, runtimeDir string, err error) {
	electron, runtimeDir = s.ElectronPath, s.RuntimeDir
	if electron == "" {
		electron = strings.TrimSpace(os.Getenv("UVMP_ELECTRON"))
	}
	if runtimeDir == "" {
		runtimeDir = strings.TrimSpace(os.Getenv("UVMP_BROWSER_RUNTIME_DIR"))
	}
	// 显式 runtime 目录旁的宿主（历史口径优先）
	if electron == "" && runtimeDir != "" {
		name := "electron"
		if runtime.GOOS == "windows" {
			name = "electron.exe"
		}
		if p := filepath.Join(runtimeDir, name); fileExists(p) {
			electron = p
		}
	}
	if electron == "" || runtimeDir == "" {
		rt := browser.ScanRuntime()
		if electron == "" {
			electron = rt.Electron
		}
		if runtimeDir == "" {
			runtimeDir = rt.AppDir
		}
	}
	if runtimeDir == "" {
		return "", "", fmt.Errorf("未找到 browser-runtime 应用目录，请在配置 browser_automation.runtime_dir 指定")
	}
	if electron == "" {
		return "", "", fmt.Errorf("未找到 Electron 宿主，请在配置 browser_automation.electron_path 指定")
	}
	if !fileExists(electron) {
		return "", "", fmt.Errorf("指定的浏览器宿主不存在: %s", electron)
	}
	if !fileExists(filepath.Join(runtimeDir, "package.json")) {
		return "", "", fmt.Errorf("browser-runtime 目录无效（缺 package.json）: %s", runtimeDir)
	}
	return electron, runtimeDir, nil
}

// courseEngineAvailable 课程引擎可用性（chromedp 随构建恒编入可用）。
// 旧 UI（desktop CourseAutoplayPage.vue）以 status.playwright 布尔门控「开始学习」，
// 兼容字段取值即本判定。
func courseEngineAvailable() bool { return true }

// courseStatus 受控浏览器状态卡（Python browser_automation.status + backend.status）：
// cdp_url/browser/profile_dir/start_url/connected/pages + active_lists。
// playwright 为旧 UI 解码兼容字段（值=引擎可用），engine 保留 chromedp 口径，
// 两者缺一会造成旧 UI 解码回归（按钮永久禁用/状态不显示）。
func courseStatus(cfg config.Dict) (map[string]any, error) {
	s, err := loadCourseSettings(cfg)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"cdp_url":     s.CDPURL,
		"browser":     "",
		"profile_dir": s.ProfileDir,
		"start_url":   s.StartURL,
		"connected":   false,
		"pages":       0,
		"engine":      "chromedp",
		"playwright":  courseEngineAvailable(),
	}
	if electron, _, err := resolveBrowserHost(s); err == nil {
		result["browser"] = electron
	}
	result["active_lists"] = course.NewCheckpointsStore().ActiveCount()
	if pages, ok := probeCoursePages(s.CDPURL); ok {
		result["connected"] = true
		result["pages"] = pages
	}
	return result, nil
}

// probeCoursePages 探测受控浏览器调试端口（GET /json/list），返回 page 目标数。
func probeCoursePages(cdpURL string) (int, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(cdpURL + "/json/list")
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var targets []struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return 0, false
	}
	pages := 0
	for _, t := range targets {
		if t.Type == "page" {
			pages++
		}
	}
	return pages, true
}

// Run 实现 Backend（Python backend.run）：接管课程列表→断点续刷/暂停恢复→结果摘要。
// 取消=暂停：保留受控浏览器与登录态，返回 paused=true 结果（任务历史 cancelled 显示「已暂停」）。
func (b *courseAutoplayBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	cfg := config.Load()
	s, err := loadCourseSettings(cfg)
	if err != nil {
		return nil, err
	}
	timeout := s.StepTimeout
	if v, ok := toInt(params["timeout"]); ok {
		if v <= 0 {
			return nil, fmt.Errorf("单步超时和单课尝试次数必须大于 0")
		}
		timeout = time.Duration(v) * time.Second
	}
	maxAttempts := s.MaxAttempts
	if v, ok := toInt(params["max_attempts"]); ok {
		if v <= 0 {
			return nil, fmt.Errorf("单步超时和单课尝试次数必须大于 0")
		}
		maxAttempts = v
	}
	scanOnly := toBoolParam(params["scan_only"])

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j.AddCancelHook(cancel)

	cb, err := course.Connect(ctx, course.ConnectOptions{
		CDPURL:  s.CDPURL,
		Timeout: timeout,
		Log:     j.Log,
	})
	if err != nil {
		return nil, fmt.Errorf("无法连接 %s。请先「打开浏览器」进入培训班课程列表，或确认浏览器已开启远程调试：%w", s.CDPURL, err)
	}
	return course.RunJob(ctx, cb, course.RunJobOptions{
		Options: course.Options{
			Timeout:          timeout,
			MaxAttempts:      maxAttempts,
			OutDir:           state.LogsDir(),
			DiagnosticPrefix: j.ID + "_failure",
			Log:              j.Log,
			Progress:         j.SetProgress,
		},
		ScanOnly: scanOnly,
		Dir:      state.LogsDir(),
		Store:    course.NewCheckpointsStore(),
	})
}

// Status 实现 StatusBackend（Python backend.status）。
func (b *courseAutoplayBackend) Status(cfg map[string]any) (map[string]any, error) {
	return courseStatus(config.Dict(cfg))
}

// LaunchBrowser 实现 BrowserBackend（Python backend.launch_browser）：
// 启动或复用受控浏览器（internal/browser 课程模式，持久 profile + start_url 自动访问）。
// 打开浏览器不是 Job、不产生任务档案；已有受控浏览器时直接复用，不重复启动。
func (b *courseAutoplayBackend) LaunchBrowser(cfg map[string]any) (map[string]any, error) {
	c := config.Dict(cfg)
	s, err := loadCourseSettings(c)
	if err != nil {
		return nil, err
	}
	electron, runtimeDir, err := resolveBrowserHost(s)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inst, err := browser.Start(ctx, browser.NewCourseConfig(electron, runtimeDir, s.ProfileDir, s.CDPURL, s.StartURL))
	if err != nil {
		return nil, err
	}
	result, err := courseStatus(c)
	if err != nil {
		return nil, err
	}
	result["launched"] = !inst.Status().Reused
	result["browser"] = electron
	if pages, ok := probeCoursePages(s.CDPURL); ok {
		result["connected"] = true
		result["pages"] = pages
	}
	return result, nil
}

// toInt 参数取整数（缺失/不可解析返回 ok=false；浮点截断，宽接口入口）。
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// toBoolParam 宽松布尔参数（bool/数值/字符串）。
func toBoolParam(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}
