package usbcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// Policy 结果等待策略：每轮先等 Wait，再断开共享取回一次；还有下一轮才重挂回共享。
// 每次取回都会让被控机看到一次 USB 拔出/插入，轮次越少越稳：
// 所有应用统一用 Once（下发后等满一个窗口再取回一次），不再多轮试探。
type Policy struct {
	Waits []time.Duration
	Total time.Duration
}

// DefaultResultWait 单条指令未单独指定等待时长时的默认值（10 分钟）。
const DefaultResultWait = 10 * time.Minute

// Once 只取一次结果的策略：等 wait 后断开共享取回一次（`collectRefs`），不再重挂。
// Total 留出 1 分钟余量，避免 sleep 抖动越过截止点而跳过唯一一次取回；
// 后续新指令可直接复用，需要更长/更短窗口时传自己的 wait。
// 合批时这条策略是整批共享的：本策略没有下一检查点，取回一次后缺失的指令直接超时。
func Once(wait time.Duration) Policy {
	if wait <= 0 {
		wait = DefaultResultWait
	}
	return Policy{Waits: []time.Duration{wait}, Total: wait + time.Minute}
}

// Await 等待并取回指定 id 的结果；超时返回错误。调用方必须持有全局 U 盘会话锁。
func Await(ctx context.Context, link Link, app, id string, policy Policy) (Result, error) {
	ref := CommandRef{App: app, ID: id}
	out := AwaitMany(ctx, link, []CommandRef{ref}, policy)
	o := out[ref]
	return o.Result, o.Err
}

// CommandRef 一条待取结果的指令（app + 指令 id）。
type CommandRef struct {
	App string
	ID  string
}

// AwaitOutcome 单条指令的取回结果：Err 非空即等待超时/取消/列盘失败。
type AwaitOutcome struct {
	Result Result
	Err    error
}

// AwaitMany 同一 policy 下批量等待并取回结果，供合批同步一次窗口收齐多应用 snapshot。
// 与 Await 共用同一等待/收集实现：每个检查点只列盘一次，命中的结果先返回并消费；
// 只有仍有缺失且存在下一检查点时才重挂一次，不为某个缺失指令单独再次满等。
// 因此传 Once 时只有一个等待窗口。调用方必须持有全局 U 盘会话锁。
func AwaitMany(ctx context.Context, link Link, refs []CommandRef, policy Policy) map[CommandRef]AwaitOutcome {
	out := make(map[CommandRef]AwaitOutcome, len(refs))
	if link == nil {
		for _, ref := range refs {
			out[ref] = AwaitOutcome{Err: fmt.Errorf("U 盘链路未接入")}
		}
		return out
	}
	if len(policy.Waits) == 0 {
		policy = Once(DefaultResultWait)
	}
	if policy.Total <= 0 {
		policy.Total = 30 * time.Minute
	}
	deadline := time.Now().Add(policy.Total)
	pending := dedupeRefs(refs)
	var lastErr error
	for i, wait := range policy.Waits {
		if len(pending) == 0 {
			break
		}
		if err := sleepCtx(ctx, wait); err != nil {
			for _, ref := range pending {
				recordAuditTimeout(ref.App, ref.ID, "等待结果被取消："+err.Error())
				out[ref] = AwaitOutcome{Err: err}
			}
			return out
		}
		if time.Now().After(deadline) {
			break
		}
		last := i == len(policy.Waits)-1
		found, err := collectRefs(ctx, link, pending)
		if err != nil {
			lastErr = err
		}
		for ref, res := range found {
			recordAudit(res)
			out[ref] = AwaitOutcome{Result: res}
		}
		pending = missingRefs(pending, out)
		if len(pending) == 0 || last {
			break
		}
		// 没读到全部结果且还有下一轮才重挂；末轮取回后不再把盘交还给被控机。
		if err := link.MountToHost(ctx); err != nil {
			lastErr = err
		}
	}
	if len(pending) == 0 {
		return out
	}
	// 状态说明只读取一次，供本轮所有缺失指令复用。
	note := clientStatusNote(ctx, link)
	for _, ref := range pending {
		var err error
		if lastErr != nil {
			err = fmt.Errorf("等待指令 %s 结果超时: %w（%s）", ref.ID, lastErr, note)
		} else {
			err = fmt.Errorf("等待指令 %s 结果超时（%s）", ref.ID, note)
		}
		recordAuditTimeout(ref.App, ref.ID, err.Error())
		out[ref] = AwaitOutcome{Err: err}
	}
	return out
}

// dedupeRefs 去重（同 app+id 只等一次），保留首次出现顺序。
func dedupeRefs(refs []CommandRef) []CommandRef {
	seen := make(map[CommandRef]bool, len(refs))
	out := make([]CommandRef, 0, len(refs))
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

func missingRefs(pending []CommandRef, out map[CommandRef]AwaitOutcome) []CommandRef {
	missing := make([]CommandRef, 0, len(pending))
	for _, ref := range pending {
		if _, done := out[ref]; !done {
			missing = append(missing, ref)
		}
	}
	return missing
}

// RestoreShare 轮次收尾把 U 盘重新挂回被控机（best-effort）。
// Await/AwaitMany 的末轮取回会断开共享，单次策略也不再重挂；不复挂会让被控机长时间看不到
// U 盘、残留指令在 TTL 内也拿不到。忽略 ctx 取消，独立 30s 超时；调用方须持有会话锁。
func RestoreShare(ctx context.Context, link Link) {
	if link == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := link.MountToHost(cctx); err != nil {
		log.Printf("[U盘同步] 轮次结束复挂失败：%v\n", err)
	}
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

// collectRefs 断开共享一次，按请求的 app 扫描结果并归位：命中的结果与其所属应用的
// 残留指令一起消费；未命中应用的结果文件按旧轮次/损坏处理（只清结果，保留指令）。
func collectRefs(ctx context.Context, link Link, refs []CommandRef) (map[CommandRef]Result, error) {
	names, err := link.ListFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("列 U 盘文件失败: %w", err)
	}
	wanted := make(map[string]map[string]CommandRef, len(refs))
	for _, ref := range refs {
		ids := wanted[ref.App]
		if ids == nil {
			ids = map[string]CommandRef{}
			wanted[ref.App] = ids
		}
		ids[ref.ID] = ref
	}
	found := make(map[CommandRef]Result, len(refs))
	consumedApps := make(map[string]bool, len(refs))
	stale := make([]string, 0, len(names))
	var lastErr error
	for _, name := range names {
		app, ok := resultAppOf(name)
		if !ok {
			continue
		}
		ids, asked := wanted[app]
		if !asked {
			continue
		}
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
		ref, hit := ids[res.ID]
		if !hit || res.App != app {
			continue
		}
		found[ref] = res
		consumedApps[app] = true
	}
	// 命中应用的本轮结果与残留指令一起消费掉；未命中应用只清旧结果。
	victims := stale
	for app := range consumedApps {
		victims = append(victims, CmdName(app))
	}
	if len(victims) > 0 {
		_ = link.DeleteFiles(ctx, victims)
	}
	if len(found) > 0 {
		return found, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// resultAppOf 从 `{app}_result_<id>.json` 取出应用名。
func resultAppOf(name string) (string, bool) {
	if !strings.HasSuffix(name, ".json") {
		return "", false
	}
	idx := strings.Index(name, "_result_")
	if idx <= 0 {
		return "", false
	}
	return name[:idx], true
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
