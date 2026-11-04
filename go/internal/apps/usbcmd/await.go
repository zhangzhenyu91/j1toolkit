package usbcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Link 服务端操作 U 盘的最小面（treehazard/crossing/bridge 的 UsbLink 均满足）。
type Link interface {
	MountToHost(ctx context.Context) error
	ListFiles(ctx context.Context) ([]string, error)
	DownloadFile(ctx context.Context, name string) ([]byte, error)
	DeleteFiles(ctx context.Context, names []string) error
}

// Policy 结果等待策略：每轮先等 Wait，再断开共享 collect 一次；无结果则重挂回共享。
// 轮次不宜多：每次 collect 都会让被控机看到一次 USB 拔出/插入。
type Policy struct {
	Waits []time.Duration
	Total time.Duration
}

// DefaultPolicy 树障/交跨：snapshot/replace 都是秒级文件操作，首轮 60s 足够。
var DefaultPolicy = Policy{
	Waits: []time.Duration{60 * time.Second, 3 * time.Minute, 10 * time.Minute},
	Total: 30 * time.Minute,
}

// DailyPolicy 派车导出要登录/查询/生成 xlsx，首轮给 2 分钟。
var DailyPolicy = Policy{
	Waits: []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute},
	Total: 30 * time.Minute,
}

// Await 等待并取回指定 id 的结果；超时返回错误。调用方必须持有全局 U 盘会话锁。
func Await(ctx context.Context, link Link, app, id string, policy Policy) (Result, error) {
	if link == nil {
		return Result{}, fmt.Errorf("U 盘链路未接入")
	}
	if len(policy.Waits) == 0 {
		policy = DefaultPolicy
	}
	if policy.Total <= 0 {
		policy.Total = 30 * time.Minute
	}
	deadline := time.Now().Add(policy.Total)
	var lastErr error
	for _, wait := range policy.Waits {
		if err := sleepCtx(ctx, wait); err != nil {
			recordAuditTimeout(app, id, "等待结果被取消："+err.Error())
			return Result{}, err
		}
		if time.Now().After(deadline) {
			break
		}
		res, found, err := collect(ctx, link, app, id)
		if err != nil {
			lastErr = err
			_ = link.MountToHost(ctx)
			continue
		}
		if found {
			recordAudit(res)
			return res, nil
		}
		// 没读到结果：把 U 盘重新共享给被控机，让客户端继续执行/落结果。
		if err := link.MountToHost(ctx); err != nil {
			lastErr = err
		}
	}
	note := clientStatusNote(ctx, link)
	if lastErr != nil {
		err := fmt.Errorf("等待指令 %s 结果超时: %w（%s）", id, lastErr, note)
		recordAuditTimeout(app, id, err.Error())
		return Result{}, err
	}
	err := fmt.Errorf("等待指令 %s 结果超时（%s）", id, note)
	recordAuditTimeout(app, id, err.Error())
	return Result{}, err
}

// agentStatus 被控机 usb_agent_status.json 的最小解析面（字段与客户端 usb_protocol 一致）。
type agentStatus struct {
	LastSeen    string `json:"last_seen"`
	State       string `json:"state"`
	LastError   string `json:"last_error"`
	LastCommand *struct {
		ID    string `json:"id"`
		App   string `json:"app"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	} `json:"last_command"`
	USB struct {
		Mount     string `json:"mount"`
		MountedAt string `json:"mounted_at"`
		Readonly  bool   `json:"readonly"`
	} `json:"usb"`
}

// clientStatusNote 超时时的可读判因：把“被控机是否在跑 / U 盘是否挂载 / 最近一条指令结果”
// 带进失败原因（通知与审计都可见），不再只有一句“等待超时”。
func clientStatusNote(ctx context.Context, link Link) string {
	if link == nil {
		return "U 盘链路未接入"
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := link.DownloadFile(cctx, StatusName)
	if err != nil {
		return "读取被控机状态失败：" + err.Error()
	}
	if len(raw) == 0 {
		return "被控机未上报状态（usb-agent 可能未运行，或 U 盘从未挂载）"
	}
	var st agentStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return "被控机状态文件损坏：" + err.Error()
	}
	if st.LastSeen == "" {
		return "被控机状态缺少 last_seen"
	}
	mount := st.USB.Mount
	if mount == "" {
		mount = "未挂载"
	} else if st.USB.Readonly {
		mount += "（只读）"
	}
	note := fmt.Sprintf("被控机 agent 最后上报 %s，U 盘 %s", st.LastSeen, mount)
	if st.LastCommand != nil && st.LastCommand.ID != "" {
		note += fmt.Sprintf("；最近指令 %s/%s ok=%t", st.LastCommand.App, st.LastCommand.ID, st.LastCommand.OK)
		if !st.LastCommand.OK && st.LastCommand.Error != "" {
			note += "（" + st.LastCommand.Error + "）"
		}
	}
	if st.LastError != "" {
		note += "；最近错误：" + st.LastError
	}
	return note
}

// collect 断开共享一次，读结果并删除（结果先写后删指令，读到结果说明客户端已完成）。
func collect(ctx context.Context, link Link, app, id string) (Result, bool, error) {
	names, err := link.ListFiles(ctx)
	if err != nil {
		return Result{}, false, fmt.Errorf("列 U 盘文件失败: %w", err)
	}
	// 扫全部本应用结果文件：按 id 命中本轮，顺带清掉旧轮次/损坏结果。
	prefix := app + "_result_"
	candidates := make([]string, 0, 2)
	for _, n := range names {
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".json") {
			candidates = append(candidates, n)
		}
	}
	var matched *Result
	stale := make([]string, 0, len(candidates))
	var lastErr error
	for _, name := range candidates {
		raw, err := link.DownloadFile(ctx, name)
		if err != nil {
			lastErr = err
			continue
		}
		if raw == nil {
			continue
		}
		res, err := ParseResult(raw)
		if err != nil {
			// 损坏结果不反复重试：删掉，继续等客户端重写。
			stale = append(stale, name)
			lastErr = err
			continue
		}
		stale = append(stale, name)
		if res.ID == id && res.App == app {
			r := res
			matched = &r
		}
	}
	if matched != nil {
		// 本轮结果与可能残留的指令一起消费掉。
		_ = link.DeleteFiles(ctx, append(stale, CmdName(app)))
		return *matched, true, nil
	}
	if len(stale) > 0 {
		_ = link.DeleteFiles(ctx, stale)
	}
	if lastErr != nil {
		return Result{}, false, lastErr
	}
	return Result{}, false, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Session 进程级 U 盘会话锁：树障/交跨/派车共用，任何时刻只有一个轮次操作 U 盘。
type Session struct {
	ch chan struct{}
}

// NewSession 构造会话锁（容量 1）。
func NewSession() *Session { return &Session{ch: make(chan struct{}, 1)} }

// DefaultSession 服务端单例（单进程部署；测试可各自 NewSession）。
var DefaultSession = NewSession()

// Acquire 阻塞等待会话锁（排程轮次用；ctx 取消返回错误）。
func (s *Session) Acquire(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryAcquire 非阻塞抢锁（手动入口用；占用中返回 false）。
func (s *Session) TryAcquire() bool {
	if s == nil {
		return true
	}
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release 释放会话锁（未持有时安全返回）。
func (s *Session) Release() {
	if s == nil {
		return
	}
	select {
	case <-s.ch:
	default:
	}
}
