package render

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Electron 宿主（desktop/electron/cdp-worker.cjs）的 CDP 兜底桥：
//   - Page.printToPDF：Electron CDP 不实现（-32601 wasn't found），cdp-worker.cjs
//     用 webContents.printToPDF 出 PDF；
//   - Target.createTarget：Electron CDP 不支持（-32000 Not supported），cdp-worker.cjs
//     开不可见新窗口，Go 侧等 Target 事件后 WithTargetID 附加。
//
// cdp-worker.cjs 在 profile 目录写 cdp-worker-bridge.json（port + token）并起只听
// 127.0.0.1 的 HTTP 服务。普通 Chromium 实现了这些 CDP 方法，不会走到本文件的路径，
// 行为不变。

// bridgeInfo cdp-worker.cjs 写入 profile 目录的兜底服务信息。
type bridgeInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// isPrintToPDFMissing 判断错误是否为「宿主未实现 Page.printToPDF」
// （Electron 等 CDP dispatcher 级缺失，-32601 / wasn't found / not implemented）。
func isPrintToPDFMissing(err error) bool {
	if err == nil {
		return false
	}
	var ce *cdproto.Error
	if errors.As(err, &ce) && ce.Code == -32601 {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "wasn't found") || strings.Contains(s, "not implemented")
}

// loadBridge 读取 cdp-worker.cjs 写下的兜底服务信息。
func (b *cdpBrowser) loadBridge() (*bridgeInfo, error) {
	raw, err := os.ReadFile(filepath.Join(b.profileDir(), "cdp-worker-bridge.json"))
	if err != nil {
		return nil, err
	}
	var bi bridgeInfo
	if err := json.Unmarshal(raw, &bi); err != nil {
		return nil, fmt.Errorf("render: 解析 cdp-worker-bridge.json 失败: %w", err)
	}
	if bi.Port <= 0 || bi.Token == "" {
		return nil, fmt.Errorf("render: cdp-worker-bridge.json 内容不完整")
	}
	return &bi, nil
}

// printToPDFViaWorker 走 cdp-worker.cjs 的 HTTP 兜底出 PDF（口径与 tabPrintPDF 一致）。
func (b *cdpBrowser) printToPDFViaWorker(jobCtx, tabCtx context.Context) ([]byte, error) {
	bi, err := b.loadBridge()
	if err != nil {
		return nil, fmt.Errorf("render: Page.printToPDF 不可用且无 cdp-worker 兜底: %w", err)
	}
	// 往目标 tab 打随机标记，兜底服务按标记找对应 webContents
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	marker := hex.EncodeToString(buf)
	if _, err := evalOn(jobCtx, tabCtx, `window.__cdpWorkerPdfMarker = "`+marker+`"`, evalTimeout); err != nil {
		return nil, fmt.Errorf("render: 写入 PDF 标记失败: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"marker":            marker,
		"printBackground":   true,
		"preferCSSPageSize": true,
	})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 2 * evalTimeout}
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/print-to-pdf", bi.Port), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CDP-Worker-Token", bi.Token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("render: 调用 cdp-worker printToPDF 兜底失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("render: 读取 cdp-worker printToPDF 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("render: cdp-worker printToPDF 兜底返回 %d: %s",
			resp.StatusCode, trimForDiag(string(raw), 200))
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("render: 解析 cdp-worker printToPDF 响应失败: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("render: cdp-worker printToPDF 兜底无数据")
	}
	return data, nil
}

// isCreateTargetMissing 判断错误是否为「宿主未实现 Target.createTarget」
// （Electron 等返回 -32000 Not supported）。
func isCreateTargetMissing(err error) bool {
	if err == nil {
		return false
	}
	var ce *cdproto.Error
	if errors.As(err, &ce) && ce.Code == -32000 {
		return strings.Contains(strings.ToLower(ce.Message), "not supported")
	}
	return strings.Contains(strings.ToLower(err.Error()), "not supported")
}

// hasBridge 宿主是否为带 printToPDF/新窗口兜底的 cdp-worker.cjs（Electron）。
func (b *cdpBrowser) hasBridge() bool {
	_, err := os.Stat(filepath.Join(b.profileDir(), "cdp-worker-bridge.json"))
	return err == nil
}

// workerNewWindow 让 cdp-worker.cjs 开一个对用户不可见的新窗口（Target.createTarget
// 的 Electron 替代）；新窗口的 page target 由调用方从 Target 事件里捕获。
func (b *cdpBrowser) workerNewWindow(jobCtx context.Context, bi *bridgeInfo) error {
	client := &http.Client{Timeout: evalTimeout}
	req, err := http.NewRequestWithContext(jobCtx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/new-window", bi.Port), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-CDP-Worker-Token", bi.Token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("render: 调用 cdp-worker 开新窗口失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("render: cdp-worker 开新窗口返回 %d: %s",
			resp.StatusCode, trimForDiag(string(raw), 200))
	}
	return nil
}

// newTabViaBridge 经 cdp-worker.cjs 开不可见新窗口并附加为目标标签页
// （Electron 不支持 Target.createTarget）。先订阅 Target 事件再开窗，防止错过。
func (b *cdpBrowser) newTabViaBridge(jobCtx context.Context) (context.Context, context.CancelFunc, error) {
	bi, err := b.loadBridge()
	if err != nil {
		return nil, nil, fmt.Errorf("render: Target.createTarget 不可用且无 cdp-worker 兜底: %w", err)
	}
	type created struct {
		id  target.ID
		err error
	}
	ch := make(chan created, 1)
	// 订阅在 b.ctx 上（携带 chromedp Context）；捕获到匹配 target 即结束订阅
	events := chromedp.BrowserEvents(b.ctx, target.TargetCreated)
	go func() {
		for ev, err := range events {
			if err != nil {
				ch <- created{err: err}
				return
			}
			if ev.TargetInfo != nil && ev.TargetInfo.Type == "page" && !ev.TargetInfo.Attached {
				ch <- created{id: ev.TargetInfo.TargetID}
				return
			}
		}
		ch <- created{err: fmt.Errorf("render: target 事件流提前结束")}
	}()
	if err := b.workerNewWindow(jobCtx, bi); err != nil {
		return nil, nil, err
	}
	var id target.ID
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, nil, r.err
		}
		id = r.id
	case <-jobCtx.Done():
		return nil, nil, jobCtx.Err()
	case <-time.After(evalTimeout):
		return nil, nil, fmt.Errorf("render: 等待新窗口 page target 超时")
	}
	tabCtx, cancel := chromedp.NewContext(b.ctx, chromedp.WithTargetID(id))
	if err := b.initTab(jobCtx, tabCtx); err != nil {
		cancel()
		return nil, nil, err
	}
	return tabCtx, cancel, nil
}
