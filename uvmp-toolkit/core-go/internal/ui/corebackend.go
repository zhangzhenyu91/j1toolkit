package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/schedule"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
)

// CoreBackend 是 Backend 的生产实现：直连 Go 核心（job/state/config/apps/usb/schedule/quizsearch），
// 口径与 toolkit stdio RPC（internal/rpc 方法表）一致，不经过进程间通道。
// FakeBackend 仅测试/冒烟用；桌面壳（appshell）与后续复用方一律走本实现。
type CoreBackend struct {
	mgr   *job.JobManager
	store *state.Store
	quiz  *quizsearch.BankStore

	// quizDir 题库数据目录（ResolveDataDir：旧 Electron 目录原地可读优先，BankStore 与扫描服务同址）。
	quizDir string

	// scanner 扫描服务（装配见 quizruntime.go）：CoreBackend 装配并持有生命周期——
	// 首次扫描控制调用时按生产装配（NewQuizScanner），AttachQuizScanner 可注入（测试）；
	// 挂接后题库操作走扫描服务以保持在途匹配索引同步；未挂接时题库方法直连 BankStore。
	scanner       QuizScanner
	scanAssembled bool // 已尝试装配（成功或失败）；失败原因见 scanErr
	scanErr       error
	quizPumpStop  chan struct{} // 当前扫描事件泵停止口（换挂/退出时收口）

	now     func() time.Time
	usbOpts *usb.Options

	mu       sync.Mutex
	subs     map[*coreSub]struct{}
	quizSubs map[*quizSub]struct{}

	quitCh      chan struct{}
	disposeOnce sync.Once
}

// NewCoreBackend 构造生产 Backend。状态目录/配置与 CLI、RPC 同源（state.Default / config.Load），
// 测试可用 state.SetRoot 与 UVMP_TOOLKIT_CONFIG 隔离。内置应用注册幂等（apps.Builtin）。
func NewCoreBackend() *CoreBackend {
	apps.Builtin()
	st := state.Default()
	b := &CoreBackend{
		mgr:      job.NewManager(st),
		store:    st,
		now:      time.Now,
		subs:     map[*coreSub]struct{}{},
		quizSubs: map[*quizSub]struct{}{},
		quitCh:   make(chan struct{}),
	}
	// 题库目录 = ResolveDataDir（旧 Electron userData/quiz-search 旧 banks/settings 原地可读，
	// 与扫描服务同址；全新装与旧实现同目录双向兼容）
	b.quizDir = quizsearch.ResolveDataDir(st.RootDir())
	if qs, err := quizsearch.NewBankStore(b.quizDir); err == nil {
		b.quiz = qs
	}
	return b
}

var _ Backend = (*CoreBackend)(nil)
var _ Disposer = (*CoreBackend)(nil)

// AttachQuizScanner 挂接扫描服务（测试注入或自定义装配），使题库操作与在途匹配索引同步。
// 已有扫描服务时先释放旧实例；启动事件泵把 scan/engine/boxHidden 扇出到订阅者。
func (b *CoreBackend) AttachQuizScanner(s QuizScanner) {
	b.mu.Lock()
	old := b.scanner
	b.scanner = s
	b.scanAssembled = true
	b.scanErr = nil
	b.mu.Unlock()
	if old != nil && old != s {
		old.Dispose()
	}
	b.hookQuizPump(s)
}

// scannerOrAssemble 取扫描服务：未注入且未装配时按生产路径装配一次（NewQuizScanner），
// 失败原因缓存复用（不冒充可用）；装配成功即启动事件泵。
func (b *CoreBackend) scannerOrAssemble() (QuizScanner, error) {
	b.mu.Lock()
	if b.scanner != nil {
		s := b.scanner
		b.mu.Unlock()
		return s, nil
	}
	if b.scanAssembled {
		err := b.scanErr
		b.mu.Unlock()
		if err == nil {
			err = errors.New("扫描服务不可用")
		}
		return nil, err
	}
	b.scanAssembled = true
	b.mu.Unlock()

	s, err := NewQuizScanner(QuizScannerOptions{DataDir: b.quizDir})
	b.mu.Lock()
	if err != nil {
		b.scanErr = err
		b.mu.Unlock()
		return nil, err
	}
	if b.scanner != nil { // 装配期间被 AttachQuizScanner 抢先注入：保留注入实例
		injected := b.scanner
		b.mu.Unlock()
		s.Dispose()
		return injected, nil
	}
	b.scanner = s
	b.mu.Unlock()
	b.hookQuizPump(s)
	return s, nil
}

// hookQuizPump 启动扫描事件泵：把 scan/engine/boxHidden 扇出到 QuizSubscribe 订阅者。
func (b *CoreBackend) hookQuizPump(s QuizScanner) {
	b.mu.Lock()
	if b.quizPumpStop != nil {
		close(b.quizPumpStop)
	}
	stop := make(chan struct{})
	b.quizPumpStop = stop
	quit := b.quitCh
	b.mu.Unlock()
	go func() {
		for {
			select {
			case <-quit:
				return
			case <-stop:
				return
			case ev, ok := <-s.Events():
				if !ok {
					return
				}
				b.emitQuiz(toUIQuizEvent(ev))
			}
		}
	}()
}

// emitQuiz 扫描事件扇出（订阅通道满则丢最旧保最新）。
func (b *CoreBackend) emitQuiz(ev QuizEvent) {
	b.mu.Lock()
	subs := make([]*quizSub, 0, len(b.quizSubs))
	for s := range b.quizSubs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		s.send(ev)
	}
}

// Dispose 释放扫描服务等底层资源（UI 退出时调用；幂等）。
func (b *CoreBackend) Dispose() {
	b.disposeOnce.Do(func() {
		b.mu.Lock()
		close(b.quitCh)
		if b.quizPumpStop != nil {
			close(b.quizPumpStop)
			b.quizPumpStop = nil
		}
		s := b.scanner
		b.scanner = nil
		b.scanAssembled = true
		if b.scanErr == nil {
			b.scanErr = errors.New("核心已释放")
		}
		b.mu.Unlock()
		if s != nil {
			s.Dispose()
		}
	})
}

// quizSub 扫描事件订阅句柄（Close 幂等）。
type quizSub struct {
	b      *CoreBackend
	ch     chan QuizEvent
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func (s *quizSub) Events() <-chan QuizEvent { return s.ch }

func (s *quizSub) Close() error {
	s.once.Do(func() {
		s.b.mu.Lock()
		delete(s.b.quizSubs, s)
		s.b.mu.Unlock()
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
	return nil
}

// send 投递事件（满则丢最旧保最新；订阅者关闭后丢弃）。
func (s *quizSub) send(ev QuizEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- ev:
			return
		default:
			select {
			case <-s.ch:
			default:
			}
		}
	}
}

// load 读取当前配置（config.Load：config.ini + config.json 合并）。
func (b *CoreBackend) load() config.Dict { return config.Load() }

// ---------------------------------------------------------------- SystemAPI

func (b *CoreBackend) Ping(context.Context) error {
	if b.mgr == nil {
		return errors.New("核心未初始化")
	}
	return nil
}

func (b *CoreBackend) SystemInfo(context.Context) (SystemInfo, error) {
	cfg := b.load()
	sum := usb.PlatformSummary()
	info := SystemInfo{
		Platform:    asString(sum["platform"]),
		Machine:     asString(sum["machine"]),
		CoreVersion: asString(sum["core"]),
		ConfigPath:  config.ConfigPath(),
		StateDir:    b.store.RootDir(),
	}
	// 生效渲染浏览器（与 exporter/render 同一口径）：显式 render.chrome 优先；
	// 否则 UVMP_BROWSER_BIN → 宿主自动检测（360ent / qaxbrowser / Chrome / Edge）。
	// 历史 render.builtin=true 仅在注入 UVMP_BROWSER_BIN 时才是 Electron 回退，未注入时
	// 按宿主检测（旧配置升级兼容）；不再因 builtin 缺省而返回空浏览器。
	rcfg := config.RenderCfg(cfg)
	chrome := config.StrFieldTrim(rcfg, "chrome")
	if c, err := apps.DetectBrowser(chrome); err == nil {
		info.Chrome = c
	}
	oe := config.MapField(cfg, "order_export")
	info.SuggestedOutDirs = usb.SuggestedOutdirs(
		config.StrField(config.MapField(cfg, "usb"), "label"),
		config.StrField(oe, "default_outdir"),
		exeDir(), b.usbOpts)
	return info, nil
}

func (b *CoreBackend) OpenPath(_ context.Context, path string) error {
	if path == "" {
		return errors.New("路径不存在")
	}
	if !pathExists(path) {
		return errors.New("路径不存在")
	}
	if !usb.OpenPath(path) {
		return fmt.Errorf("打开失败：%s", path)
	}
	return nil
}

func (b *CoreBackend) ShowItem(_ context.Context, path string) error {
	if path == "" {
		return errors.New("路径不能为空")
	}
	if runtime.GOOS == "windows" {
		// explorer /select 定位到文件；explorer 常以非零码退出，启动成功即视为已请求
		cmd := exec.Command("explorer", "/select,"+filepath.FromSlash(path))
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("定位失败：%s", path)
		}
		return nil
	}
	// Linux/X11 无统一「定位到文件」协议：打开所在目录
	dir := path
	if !dirExists(path) {
		dir = filepath.Dir(path)
	}
	if !usb.OpenPath(dir) {
		return fmt.Errorf("打开失败：%s", dir)
	}
	return nil
}

// ---------------------------------------------------------------- ConfigAPI

func (b *CoreBackend) GetConfig(context.Context) (Config, error) {
	return configFromDict(b.load()), nil
}

// SaveConfig 嵌套局部更新（patch.Map 已剥除空密码=不修改）。
func (b *CoreBackend) SaveConfig(_ context.Context, patch ConfigPatch) error {
	_, err := config.Save(patch.Map())
	return err
}

// configFromDict 配置字典 → 界面配置视图（密码不回填，仅 HasPassword/PasswordSource 提示）。
func configFromDict(cfg config.Dict) Config {
	c := DefaultConfig()
	sso := config.MapField(cfg, "sso")
	iniAuth := config.MapField(config.MapField(cfg, "_ini"), "auth")
	c.SSO.Username = config.StrField(sso, "username")
	pw := config.StrField(sso, "password")
	iniPw := config.StrField(iniAuth, "password")
	c.SSO.HasPassword = pw != "" || iniPw != ""
	switch {
	case pw != "":
		c.SSO.PasswordSource = "config.json"
	case iniPw != "":
		c.SSO.PasswordSource = "config.ini"
	}
	r := config.MapField(cfg, "render")
	c.Render = RenderConfig{
		Chrome:                  config.StrField(r, "chrome"),
		Builtin:                 config.BoolField(r, "builtin", false),
		IgnoreCertificateErrors: config.BoolField(r, "ignore_certificate_errors", false),
		CertificateSPKIPins:     stringList(config.ListField(r, "certificate_spki_pins")),
	}
	oe := config.MapField(cfg, "order_export")
	c.OrderExport = OrderExportConfig{
		QueryWorkers:  config.IntField(oe, "query_workers", 4),
		RenderWorkers: config.IntField(oe, "render_workers", 2),
	}
	sch := config.MapField(cfg, "schedule")
	c.Schedule = ScheduleConfig{
		Enabled: config.BoolField(sch, "enabled", true),
		Time:    config.StrFieldDefault(sch, "time", "09:15"),
		Driver:  config.StrFieldDefault(sch, "driver", "os"),
	}
	u := config.MapField(cfg, "ui")
	c.UI = UIConfig{
		CloseToTray: config.BoolField(u, "close_to_tray", true),
		Autostart:   config.BoolField(u, "autostart", false),
	}
	c.Dev = DevConfig{Mock: config.BoolField(config.MapField(cfg, "dev"), "mock", false)}
	uc := config.MapField(cfg, "usb")
	c.USB = USBConfig{
		Label:       config.StrFieldDefault(uc, "label", "GLKVM"),
		FallbackDir: config.StrField(uc, "fallback_dir"),
	}
	c.TreeSync = syncCfgFrom(config.MapField(cfg, "tree_sync"), "检修一班树木统计表.xlsx")
	c.CrossingSync = syncCfgFrom(config.MapField(cfg, "crossing_sync"), "检修一班交跨统计表.xlsx")
	ba := config.MapField(cfg, "browser_automation")
	c.BrowserAutomation = BrowserAutomationConfig{
		CDPURL:      config.StrFieldDefault(ba, "cdp_url", "http://127.0.0.1:9222"),
		ProfileDir:  config.StrField(ba, "profile_dir"),
		StartURL:    config.StrField(ba, "start_url"),
		StepTimeout: config.IntField(ba, "step_timeout", 120),
	}
	return c
}

func syncCfgFrom(m config.Dict, defaultName string) SyncAppConfig {
	return SyncAppConfig{
		LocalPath:   config.StrField(m, "local_path"),
		FileName:    config.StrFieldDefault(m, "file_name", defaultName),
		NightlyTime: config.StrFieldDefault(m, "nightly_time", "23:55"),
	}
}

// ---------------------------------------------------------------- AppAPI

func (b *CoreBackend) ListApps(context.Context) ([]AppInfo, error) {
	all := apps.All()
	out := make([]AppInfo, 0, len(all))
	for _, a := range all {
		_, hasBrowser := a.Backend.(apps.BrowserBackend)
		out = append(out, AppInfo{
			ID: a.ID, Name: a.Name, Badge: a.Badge, Desc: a.Desc,
			Order: a.Order, HasBrowser: hasBrowser,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

// AppStatus 应用状态卡：通用段（schedule/usb/last_run）+ 应用 StatusBackend 段（后者覆盖同名键）。
func (b *CoreBackend) AppStatus(_ context.Context, appID string) (map[string]any, error) {
	cfg := b.load()
	base := b.baseStatus(appID, cfg)
	if a := apps.Get(appID); a != nil {
		if sb, ok := a.Backend.(apps.StatusBackend); ok {
			m, err := sb.Status(cfg)
			if err != nil {
				return nil, err
			}
			for k, v := range m {
				base[k] = v
			}
		}
	}
	return base, nil
}

// baseStatus 各应用通用状态段（FakeBackend 同形状，真实取值）。
func (b *CoreBackend) baseStatus(appID string, cfg config.Dict) map[string]any {
	sch := config.MapField(cfg, "schedule")
	usbCfg := config.MapField(cfg, "usb")
	out := map[string]any{
		"schedule": map[string]any{
			"enabled":  config.BoolField(sch, "enabled", true),
			"time":     config.StrFieldDefault(sch, "time", "09:15"),
			"driver":   config.StrFieldDefault(sch, "driver", "os"),
			"next_run": schedule.NextRunText(cfg, b.now()),
		},
		"usb": map[string]any{
			"label":        config.StrField(usbCfg, "label"),
			"mount":        usb.FindUsbMount(config.StrField(usbCfg, "label"), nil, b.usbOpts),
			"fallback_dir": config.StrField(usbCfg, "fallback_dir"),
		},
	}
	if last := b.store.LastRun(appID); last != nil {
		out["last_run"] = map[string]any{
			"time":   asString(last["time"]),
			"status": asString(last["status"]),
			"error":  asString(last["error"]),
		}
	}
	return out
}

func (b *CoreBackend) LaunchAppBrowser(_ context.Context, appID string) error {
	a := apps.Get(appID)
	if a == nil {
		return fmt.Errorf("应用不存在: %s", appID)
	}
	bb, ok := a.Backend.(apps.BrowserBackend)
	if !ok {
		return fmt.Errorf("应用 %s 不支持受控浏览器", appID)
	}
	_, err := bb.LaunchBrowser(b.load())
	return err
}

// ---------------------------------------------------------------- 事件订阅

type coreSub struct {
	b      *CoreBackend
	ch     chan Event
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func (s *coreSub) Events() <-chan Event { return s.ch }

func (s *coreSub) Close() error {
	s.once.Do(func() {
		s.b.mu.Lock()
		delete(s.b.subs, s)
		s.b.mu.Unlock()
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
	return nil
}

// send 投递事件（满则丢最旧保最新；订阅者关闭后丢弃）。
func (s *coreSub) send(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- ev:
			return
		default:
			select {
			case <-s.ch:
			default:
			}
		}
	}
}

func (b *CoreBackend) Subscribe(context.Context) (Subscription, error) {
	s := &coreSub{b: b, ch: make(chan Event, 256)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s, nil
}

func (b *CoreBackend) emit(ev Event) {
	b.mu.Lock()
	subs := make([]*coreSub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		s.send(ev)
	}
}

// ---------------------------------------------------------------- 小工具

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return filepath.Dir(p)
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func stringList(l []any) []string {
	out := make([]string, 0, len(l))
	for _, v := range l {
		if s := asString(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
