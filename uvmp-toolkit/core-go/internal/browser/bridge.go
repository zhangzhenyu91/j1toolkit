package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// browser-runtime 回环桥契约（与 render/bridge.go、browser-runtime/main.cjs 一致）：
// 运行时在 profile 目录写 cdp-worker-bridge.json（port + token [+ mode/pid 诊断字段]），
// 起只听 127.0.0.1 的 HTTP 服务；调用方带 X-CDP-Worker-Token 头访问：
//   - POST /print-to-pdf  Page.printToPDF 的 Electron 兜底（render 导出链路用）
//   - POST /new-window    Target.createTarget 不支持的 Electron 兜底（render 新 tab 用）
//   - POST /open-url      打开网址（课程模式复用已有进程时打开 start_url）
//   - POST /shutdown      优雅退出（(*Instance).Stop / Stop 用）
// 本包只消费后两个；前两个由 render 消费，契约在此登记便于对照。

// bridgeFileName 回环桥信息文件名（落在 profile 目录）。
const bridgeFileName = "cdp-worker-bridge.json"

// bridgeInfo 运行时写入 profile 目录的回环桥信息（mode/pid 为附加诊断字段）。
type bridgeInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	Mode  string `json:"mode"`
	PID   int    `json:"pid"`
}

// loadBridge 读取 profile 目录的回环桥信息；无文件/内容不完整返回错误。
func loadBridge(profileDir string) (*bridgeInfo, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, bridgeFileName))
	if err != nil {
		return nil, err
	}
	var bi bridgeInfo
	if err := json.Unmarshal(raw, &bi); err != nil {
		return nil, fmt.Errorf("browser: 解析 %s 失败: %w", bridgeFileName, err)
	}
	if bi.Port <= 0 || bi.Token == "" {
		return nil, fmt.Errorf("browser: %s 内容不完整", bridgeFileName)
	}
	return &bi, nil
}

// post 向回环桥 POST JSON 请求（地址固定 127.0.0.1，天然仅回环）。
func (bi *bridgeInfo) post(ctx context.Context, path string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	// 回环桥只听 127.0.0.1，这里也钉死 127.0.0.1（不接受任何外部主机）
	u := fmt.Sprintf("http://127.0.0.1:%d%s", bi.Port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CDP-Worker-Token", bi.Token)
	client := &http.Client{Timeout: statusTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("browser: 回环桥 %s 返回 %d: %s", path, resp.StatusCode, trimForDiag(string(raw), 200))
	}
	return raw, nil
}

// bridgeOpenURL 经回环桥打开网址（课程模式复用已有进程时的 start_url 通道）。
// 无桥文件视为「非 browser-runtime 实例」，原样报错交调用方决策。
func bridgeOpenURL(ctx context.Context, profileDir, rawURL string) error {
	if err := validateStartURL(rawURL); err != nil {
		return err
	}
	bi, err := loadBridge(profileDir)
	if err != nil {
		return fmt.Errorf("browser: 目标浏览器非 browser-runtime 实例（无回环桥），无法打开启动网址: %w", err)
	}
	_, err = bi.post(ctx, "/open-url", map[string]string{"url": strings.TrimSpace(rawURL)})
	return err
}

// bridgeShutdown 经回环桥优雅退出运行时。返回是否找到并调用了回环桥。
func bridgeShutdown(ctx context.Context, profileDir string) (bool, error) {
	bi, err := loadBridge(profileDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := bi.post(ctx, "/shutdown", nil); err != nil {
		return true, err
	}
	return true, nil
}

// trimForDiag 截断诊断文本（与 render 口径一致）。
func trimForDiag(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
