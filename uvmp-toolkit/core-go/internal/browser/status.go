package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cdpTarget CDP /json/list 的目标概要（closeCDPTabs 用）。
type cdpTarget struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// listCDPTargets 拉取 CDP 目标列表（仅回环）。
func listCDPTargets(ctx context.Context, cdpURL string) ([]cdpTarget, error) {
	client := &http.Client{Timeout: statusTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdpURL+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browser: /json/list 返回 %d", resp.StatusCode)
	}
	var targets []cdpTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// cdpCloseTarget 关闭单个标签页：Chromium 111+ 要求 PUT /json/close/<id>，旧版 GET 亦可。
func cdpCloseTarget(ctx context.Context, cdpURL, id string) error {
	var last error
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, cdpURL+"/json/close/"+id, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: statusTimeout}).Do(req)
		if err != nil {
			last = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		last = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return last
}

// closeCDPTabs 关闭全部 page target（最后一个窗口关闭后宿主浏览器自行退出）。
func closeCDPTabs(ctx context.Context, cdpURL string) error {
	if cdpURL == "" {
		return nil
	}
	targets, err := listCDPTargets(ctx, cdpURL)
	if err != nil {
		return err
	}
	var firstErr error
	for _, t := range targets {
		if t.Type != "page" || t.ID == "" {
			continue
		}
		if err := cdpCloseTarget(ctx, cdpURL, t.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// openURLViaCDP 在已运行的宿主浏览器打开新标签页（课程模式复用实例时打开 start_url）：
// 优先 PUT /json/new?<url>（Chromium 111+），旧版回退 GET。网址原样拼在查询串后
// （DevTools HTTP 端点按原文解析，URL 编码会破坏 data:/https: 地址）。
func openURLViaCDP(ctx context.Context, cdpURL, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if err := validateStartURL(rawURL); err != nil {
		return err
	}
	var last error
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, cdpURL+"/json/new?"+rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: statusTimeout}).Do(req)
		if err != nil {
			last = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		last = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return last
}

// probeCDP 探测 CDP 调试端口（仅回环地址，调用方已过 ValidateLoopbackURL）：
// GET /json/list，返回是否存活与 page target 数。连接失败/超时一律视为未存活。
func probeCDP(ctx context.Context, cdpURL string) (alive bool, pages int) {
	client := &http.Client{Timeout: statusTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdpURL+"/json/list", nil)
	if err != nil {
		return false, 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, 0
	}
	var targets []struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return true, 0 // 端口在响应但结构异常：仍算存活（/json/list 形状随宿主变化）
	}
	for _, t := range targets {
		if t.Type == "page" {
			pages++
		}
	}
	return true, pages
}

// readDevToolsPort 读取 profile 目录的 DevToolsActivePort（Chromium 调试端口通告文件：
// 首行端口、次行 browser WS 路径），返回端口。
func readDevToolsPort(profileDir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort"))
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("browser: DevToolsActivePort 端口无效: %q", line)
	}
	return port, nil
}

// Status 独立浏览器运行时状态（GUI 状态卡 / 课程轮询数据源）。
type Status struct {
	Mode       Mode   `json:"mode"`                  // 查询/实例模式
	Running    bool   `json:"running"`               // CDP 调试端口是否在响应
	Reused     bool   `json:"reused"`                // Start 时命中已有进程（仅课程模式可复用）
	CDPURL     string `json:"cdp_url"`               // 调试地址（仅回环；导出模式就绪后为实际随机端口）
	ProfileDir string `json:"profile_dir"`           // 浏览器 profile 目录
	StartURL   string `json:"start_url,omitempty"`   // 课程模式启动网址（空省略）
	PID        int    `json:"pid,omitempty"`         // 运行时进程 PID：本包启动即子进程 PID；复用实例回填回环桥自报 PID（未知为 0）
	BridgePort int    `json:"bridge_port,omitempty"` // 回环桥端口（0=未发现）
	Pages      int    `json:"pages"`                 // page target 数（/json/list）
}

// refreshRunning 探测 CDP 刷新 Running/Pages 与（导出模式）实际 CDPURL。
func (s *Status) refreshRunning(ctx context.Context, profileDir string) {
	if s.CDPURL == "" && s.Mode == ModeExport {
		if port, err := readDevToolsPort(profileDir); err == nil {
			s.CDPURL = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
	}
	if s.CDPURL != "" {
		if alive, pages := probeCDP(ctx, s.CDPURL); alive {
			s.Running = true
			s.Pages = pages
			return
		}
	}
	s.Running = false
	s.Pages = 0
}

// refreshBridge 刷新回环桥端口（有桥文件才算 browser-runtime 实例）。
func (s *Status) refreshBridge(profileDir string) {
	if bi, err := loadBridge(profileDir); err == nil {
		s.BridgePort = bi.Port
		if s.PID == 0 && bi.PID > 0 {
			s.PID = bi.PID
		}
	} else {
		s.BridgePort = 0
	}
}

// QueryStatus 查询独立浏览器运行时状态（不启动、不改动任何状态）：课程模式按 CDPURL
// 探活，导出模式按 profile 的 DevToolsActivePort 通告端口探活。未在运行返回 Running=false。
func QueryStatus(ctx context.Context, cfg Config) (Status, error) {
	c, err := cfg.Validate()
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Mode:       c.Mode,
		ProfileDir: c.ProfileDir,
		StartURL:   strings.TrimSpace(c.StartURL),
	}
	if c.Mode == ModeCourse {
		st.CDPURL = c.CDPURL
	}
	st.refreshRunning(ctx, c.ProfileDir)
	st.refreshBridge(c.ProfileDir)
	return st, nil
}
