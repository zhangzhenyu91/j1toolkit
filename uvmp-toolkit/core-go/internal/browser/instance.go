package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Instance 一个已就绪的独立浏览器运行时实例（Start 返回）。
// 跨方法安全；Stop 幂等。导出模式持有本包启动的进程（Stop 负责回收），课程模式可能
// 为复用实例（Reused=true，本包未启动该进程）。
type Instance struct {
	cfg    Config
	cdpURL string // 实际调试地址（仅回环）：course 为配置值，export 为就绪后通告的随机端口
	reused bool   // 课程模式命中已有进程（未新启动）
	pid    int    // 本包启动的进程 PID（复用为 0）
	proc   *procHandle

	mu      sync.Mutex
	stopped bool
}

// procHandle 本包启动的浏览器进程句柄：done 在进程退出后关闭（可多方等待），
// err 为 Wait 结果。
type procHandle struct {
	pid  int
	done chan struct{}
	mu   sync.Mutex
	err  error
}

// exited 进程是否已退出（非阻塞）。
func (h *procHandle) exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// waitExit 等进程退出，超时返回 false。
func (h *procHandle) waitExit(timeout time.Duration) bool {
	select {
	case <-h.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Start 启动（或复用）受控浏览器并等待就绪：
//   - ModeExport：启动独立不可见进程（随机调试端口），就绪条件为 DevToolsActivePort
//     通告 + CDP /json/list 响应（Electron 回退另需回环桥文件落盘）；不复用已有进程
//     （渲染 Worker 语义）；
//   - ModeCourse：同 CDPURL 已在响应时直接复用（Reused=true，不重复启动），StartURL
//     非空时在复用实例中打开（宿主浏览器走 CDP /json/new，Electron 回退走回环桥）；
//     否则启动可见进程（默认 9222），start_url 直接作为首窗地址（Electron 回退经
//     --uvmp-start-url 加载）。
//
// ctx 取消中止等待（已启动的进程按 Stop 语义回收）；就绪超时取 Config.ReadyTimeout。
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	c, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.ProfileDir, 0o755); err != nil {
		return nil, fmt.Errorf("browser: 创建 profile 目录失败: %w", err)
	}
	if c.Mode == ModeCourse {
		return startCourse(ctx, c)
	}
	return startExport(ctx, c)
}

// startCourse 课程模式：复用优先，否则启动可见浏览器。
func startCourse(ctx context.Context, c Config) (*Instance, error) {
	// 复用探测：同端口已有 CDP 即复用（与 Python browser_automation.launch 口径一致）
	if alive, pages := probeCDP(ctx, c.CDPURL); alive {
		c.logf("browser: 复用已有受控浏览器 %s（page target %d）", c.CDPURL, pages)
		if startURL := strings.TrimSpace(c.StartURL); startURL != "" {
			if err := openReuseURL(ctx, c, startURL); err != nil {
				return nil, err
			}
			c.logf("browser: 已在复用实例打开启动网址 %s", startURL)
		}
		return &Instance{cfg: c, cdpURL: c.CDPURL, reused: true}, nil
	}
	h, err := spawn(c)
	if err != nil {
		return nil, err
	}
	inst := &Instance{cfg: c, cdpURL: c.CDPURL, pid: h.pid, proc: h}
	err = waitReady(ctx, c, h, func() (bool, string) {
		if alive, _ := probeCDP(ctx, c.CDPURL); alive {
			return true, ""
		}
		return false, ""
	})
	if err != nil {
		inst.killFallback()
		return nil, fmt.Errorf("%w（进程日志见 %s）", err, logPath(c))
	}
	c.logf("browser: 课程浏览器就绪 %s（PID %d）", c.CDPURL, h.pid)
	return inst, nil
}

// openReuseURL 在复用实例中打开启动网址：Electron 回退走回环桥 /open-url；
// 宿主浏览器走 CDP /json/new（新标签页）。失败给出可操作错误（浏览器本身仍可用）。
func openReuseURL(ctx context.Context, c Config, startURL string) error {
	var err error
	if c.ElectronFallback() {
		err = bridgeOpenURL(ctx, c.ProfileDir, startURL)
	} else {
		err = openURLViaCDP(ctx, c.CDPURL, startURL)
	}
	if err != nil {
		return fmt.Errorf("浏览器: 复用已有受控浏览器，但打开启动网址失败: %w（可在浏览器窗口中手动打开）", err)
	}
	return nil
}

// startExport 导出模式：启动独立不可见浏览器（随机调试端口），等通告与（Electron 回退）
// 回环桥就绪。
func startExport(ctx context.Context, c Config) (*Instance, error) {
	// 清残留：上次崩溃留下的 DevToolsActivePort/回环桥文件会误导就绪判定与 render
	_ = os.Remove(filepath.Join(c.ProfileDir, "DevToolsActivePort"))
	_ = os.Remove(filepath.Join(c.ProfileDir, bridgeFileName))

	h, err := spawn(c)
	if err != nil {
		return nil, err
	}
	inst := &Instance{cfg: c, pid: h.pid, proc: h}
	var readyURL string
	err = waitReady(ctx, c, h, func() (bool, string) {
		port, err := readDevToolsPort(c.ProfileDir)
		if err != nil {
			return false, ""
		}
		u := fmt.Sprintf("http://127.0.0.1:%d", port)
		if alive, _ := probeCDP(ctx, u); !alive {
			return false, ""
		}
		// 回环桥只有 Electron 回退运行时才有；宿主浏览器只要求 CDP 就绪
		if c.ElectronFallback() {
			if _, err := loadBridge(c.ProfileDir); err != nil {
				return false, ""
			}
		}
		readyURL = u
		return true, ""
	})
	if err != nil {
		inst.killFallback()
		return nil, fmt.Errorf("%w（进程日志见 %s）", err, logPath(c))
	}
	inst.cdpURL = readyURL
	if bi, _ := loadBridge(c.ProfileDir); bi != nil {
		c.logf("browser: 导出浏览器就绪 %s（PID %d，回环桥 127.0.0.1:%d）", readyURL, h.pid, bi.Port)
	} else {
		c.logf("browser: 导出浏览器就绪 %s（PID %d）", readyURL, h.pid)
	}
	return inst, nil
}

// Mode 实例模式。
func (i *Instance) Mode() Mode { return i.cfg.Mode }

// CDPURL 实际调试地址（仅本机回环）：course 为配置值，export 为就绪后通告的随机端口。
func (i *Instance) CDPURL() string { return i.cdpURL }

// ProfileDir 浏览器 profile 目录（回环桥文件与 DevToolsActivePort 所在）。
func (i *Instance) ProfileDir() string { return i.cfg.ProfileDir }

// Status 查询实例当前状态（每轮实时探测 CDP 与回环桥）。
func (i *Instance) Status() Status {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	st := Status{
		Mode:       i.cfg.Mode,
		CDPURL:     i.cdpURL,
		ProfileDir: i.cfg.ProfileDir,
		StartURL:   strings.TrimSpace(i.cfg.StartURL),
		Reused:     i.reused,
		PID:        i.pid,
	}
	st.refreshRunning(ctx, i.cfg.ProfileDir)
	st.refreshBridge(i.cfg.ProfileDir)
	return st
}

// Stop 停止实例（幂等）：
//   - 宿主浏览器：逐个关闭标签页让其自行退出，等 CDP 断开；本包启动的进程超时强杀，
//     复用实例超时明确报错（不代杀他人进程）；
//   - Electron 回退：经回环桥 /shutdown 优雅退出；本包启动的进程超时强杀。
func (i *Instance) Stop(ctx context.Context) error {
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		return nil
	}
	i.stopped = true
	i.mu.Unlock()

	called, err := bridgeShutdown(ctx, i.cfg.ProfileDir)
	if err != nil {
		if i.proc != nil {
			// 桥在但调用失败：自有进程仍要回收，强杀兜底后回报失败原因
			i.killFallback()
		}
		return err
	}
	if !called {
		// 宿主浏览器（无回环桥）：逐个关闭标签页让浏览器自行退出，再按需强杀本包进程
		if err := i.stopHostBrowser(ctx); err != nil {
			return err
		}
		return nil
	}
	if i.proc != nil {
		if i.proc.waitExit(i.cfg.StopGrace) {
			i.cfg.logf("browser: 浏览器已优雅退出（PID %d）", i.pid)
			return nil
		}
		i.killFallback()
		i.cfg.logf("browser: 优雅退出超时，已强杀进程树（PID %d）", i.pid)
		return nil
	}
	// 复用实例：等 CDP 断开（窗口随 /shutdown 关闭）
	deadline := time.Now().Add(i.cfg.StopGrace)
	for {
		if alive, _ := probeCDP(ctx, i.cdpURL); !alive {
			i.cfg.logf("browser: 复用浏览器已退出 %s", i.cdpURL)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("browser: 已发送 /shutdown 但浏览器仍在响应，请稍后重试或手动关闭浏览器窗口")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// stopHostBrowser 停止宿主浏览器：关闭全部 page target（最后一个窗口关闭后浏览器退出），
// 等 CDP 断开；本包启动的进程超时后强杀，复用实例超时则提示手动关闭（不代杀他人进程）。
func (i *Instance) stopHostBrowser(ctx context.Context) error {
	_ = closeCDPTabs(ctx, i.cdpURL)
	deadline := time.Now().Add(i.cfg.StopGrace)
	for {
		if alive, _ := probeCDP(ctx, i.cdpURL); !alive {
			i.cfg.logf("browser: 宿主浏览器已退出 %s", i.cdpURL)
			return nil
		}
		if time.Now().After(deadline) {
			if i.proc != nil {
				i.killFallback()
				if alive, _ := probeCDP(ctx, i.cdpURL); !alive {
					return nil
				}
			}
			return errors.New("browser: 宿主浏览器仍在响应，无法远程关闭，请手动关闭浏览器窗口")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// killFallback 强杀本包启动的进程树（优雅退出失败兜底；复用实例不代杀）。
func (i *Instance) killFallback() {
	if i.proc == nil || i.pid <= 0 {
		return
	}
	killTree(i.pid)
	i.proc.waitExit(2 * time.Second)
}

// Stop 按配置停止已在运行的受控浏览器（GUI 停止按钮等不持有 Instance 句柄的场景）：
// Electron 回退经 profile 目录回环桥 /shutdown 优雅退出；宿主浏览器逐个关闭标签页让
// 浏览器自行退出。未在运行或无实例时返回 nil（幂等）；关闭后仍在响应时明确报错。
func Stop(ctx context.Context, cfg Config) error {
	c, err := cfg.Validate()
	if err != nil {
		return err
	}
	called, err := bridgeShutdown(ctx, c.ProfileDir)
	if err != nil {
		return err
	}
	cdpURL := c.CDPURL
	if c.Mode == ModeExport {
		if port, perr := readDevToolsPort(c.ProfileDir); perr == nil {
			cdpURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
	}
	alive := false
	if cdpURL != "" {
		alive, _ = probeCDP(ctx, cdpURL)
	}
	if !called {
		if !alive {
			return nil // 未在运行：幂等成功
		}
		// 宿主浏览器无回环桥：关闭全部标签页（最后一个窗口关闭后浏览器退出）
		_ = closeCDPTabs(ctx, cdpURL)
	}
	deadline := time.Now().Add(c.StopGrace)
	for {
		if cdpURL == "" {
			if _, err := loadBridge(c.ProfileDir); err != nil {
				return nil // 桥文件随退出删除即视为已停
			}
		} else if alive, _ := probeCDP(ctx, cdpURL); !alive {
			return nil
		}
		if time.Now().After(deadline) {
			if !called {
				return fmt.Errorf("browser: 宿主浏览器仍在响应（%s），无法远程关闭，请手动关闭浏览器窗口", cdpURL)
			}
			return errors.New("browser: 已发送 /shutdown 但浏览器仍在响应，请稍后重试或手动关闭浏览器窗口")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// spawn 启动浏览器进程（脱离进程组独立存活），日志落 profile 目录，返回进程句柄。
// 浏览器生命周期与调用方解耦（课程受控浏览器不随调用方退出被杀），退出走标签页
// 关闭/回环桥 /shutdown 或 killFallback。
func spawn(c Config) (*procHandle, error) {
	logF, err := os.Create(logPath(c))
	if err != nil {
		return nil, fmt.Errorf("browser: 创建进程日志失败: %w", err)
	}
	cmd := exec.Command(c.BrowserPath, c.buildArgs()...)
	detachProcess(cmd)
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return nil, fmt.Errorf("browser: 启动浏览器失败: %w", err)
	}
	h := &procHandle{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		h.mu.Lock()
		h.err = cmd.Wait()
		h.mu.Unlock()
		_ = logF.Close()
		close(h.done)
	}()
	return h, nil
}

// logPath 进程日志（stdout/stderr 合并落盘，失败排障用）。
func logPath(c Config) string {
	return filepath.Join(c.ProfileDir, "browser.log")
}

// waitReady 轮询等待就绪：ready 返回 (就绪, 最后状态说明)；进程退出/ctx 取消即中止。
func waitReady(ctx context.Context, c Config, h *procHandle, ready func() (bool, string)) error {
	deadline := time.Now().Add(c.ReadyTimeout)
	last := ""
	for {
		if ok, note := ready(); ok {
			return nil
		} else if note != "" {
			last = note
		}
		select {
		case <-h.done:
			h.mu.Lock()
			err := h.err
			h.mu.Unlock()
			if last != "" {
				return fmt.Errorf("browser: 浏览器进程提前退出: %s（exit: %v）", last, err)
			}
			return fmt.Errorf("browser: 浏览器进程提前退出（exit: %v）", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		if time.Now().After(deadline) {
			if last != "" {
				return fmt.Errorf("browser: 启动就绪等待超时: %s", last)
			}
			return fmt.Errorf("browser: 启动就绪等待超时（%s）", c.ReadyTimeout)
		}
	}
}
