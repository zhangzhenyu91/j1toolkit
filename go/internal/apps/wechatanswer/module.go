// Package wechatanswer 迁移 crane-answer 合并核心：微信群智能应答（监听群 + location/treeWatch 规则 +
// WeChatPadPro 消息通道 + Dify 树障/交跨提取 + 群内提问 WF3 + 聊天总结归档 WF4 + 更正/撤回/辅证 +
// 失败补推队列 + 未消费聊天归档）。
//
// 与 crane-answer 的关键差异（合并约定）：
//   - 业务记录不落本地 JSON 台账：提取确认后组装 tree/crossing ingest 载荷交 CaptureSink
//     （主代理接 treehazard/crossing 模块），失败进 MySQL 技术队列 wa_push_queue 补推；
//   - 不做汇总 xlsx、不做群内「报送」指令（严禁）；
//   - 全局 WeChatPadPro / Dify 连接配置由 config.Store 读取（WECHATPADPRO_* / DIFY_*），
//     规则只保留各自独立的 Dify API Key，不再重复配 API URL；
//   - 群内提问 WF3 直调 callme ingest 逻辑（AskIngest 由 bridge 接，不走 HTTP 自循环），
//     WF4 聊天总结经 ChatlogSink 推 chatlog ingest 写 WeKnora（同样由 bridge 接）；
//   - 成员显示名解析：sys_user.wxid 唯一启用账号优先，其次群成员接口，最后 wxid 兜底。
//
// 网络默认禁用：WeChatPadPro Token / Dify 地址未配置时不建立任何连接；复杂依赖均可注入假实现。
package wechatanswer

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/config"
)

// AppKey 本模块对应的 sys_app 应用标识（对外契约键，与网页端一致）。
const AppKey = "wechat-answer"

// CaptureKind 载荷分派类型（CaptureSink.Process 的 kind）。
const (
	KindTree     = "tree"
	KindCrossing = "crossing"
)

// CaptureSink 捕获出口：主代理接 tree/crossing 模块（如直调各模块 ingest 或转发 HTTP）。
// payload 为 treehazard/crossing ingest 契约 JSON（mode=direct upsert 或 deletes）。
// 成功 = err 为 nil 且 status 为 2xx；其余情况由本模块入补推队列。
type CaptureSink interface {
	Process(ctx context.Context, kind string, payload []byte) (status int, body []byte, err error)
}

// ChatMessage 未消费群文本消息归档项。
type ChatMessage struct {
	Day        string // 北京时间自然日 yyyy-MM-dd
	GroupID    string
	GroupName  string
	SvrID      string
	SenderID   string
	SenderName string
	Content    string
	Time       int64
}

// ChatDocument 未消费群文档类文件归档项。
type ChatDocument struct {
	Day         string
	GroupID     string
	GroupName   string
	SvrID       string
	SenderID    string
	SenderName  string
	FileName    string
	Time        int64
	Unsupported bool // 扩展名不在入库白名单（仅留痕不上传）
}

// ChatArchiver 未消费聊天归档出口（默认 SQLChatArchiver 写 MySQL；可注入 WeKnora 推送等实现）。
type ChatArchiver interface {
	ArchiveMessage(ctx context.Context, rec ChatMessage) error
	ArchiveDocument(ctx context.Context, rec ChatDocument) error
}

// QuoteRef 引用回执锚点（被引用消息）。
type QuoteRef struct {
	SvrID        string
	Content      string
	CreateTime   string
	FromUserID   string
	ReplyContext map[string]any // 入站消息原始 reply_context，群聊引用须原样透传
}

// MessageSender 群内回执发送（已记录/已删除等）；nil 表示未接入，静默跳过。
// 需要发文件（WF3 回答 md）时实现可选的 FileSender。
type MessageSender interface {
	SendText(ctx context.Context, groupID, text string) error
	SendQuote(ctx context.Context, groupID, text string, quote QuoteRef) error
}

// DifyClient Dify workflow 调用（blocking）。apiKey 为规则独立密钥；
// 全局 API URL/User/Timeout 由实现侧从 config.Store 读取。未配置地址时返回错误，不发起网络请求。
type DifyClient interface {
	RunWorkflow(ctx context.Context, apiKey string, inputs map[string]string) (map[string]any, error)
}

// MemberSource 群成员名单来源（WeChatPadPro GetChatRoomMemberDetail）。
type MemberSource interface {
	GroupMembers(ctx context.Context, groupID string) (map[string]string, error) // wxid → 显示名
}

// ImageDownloader 图片本体下载（WeChatPadPro CdnDownloadImage 网关）。
type ImageDownloader interface {
	DownloadImage(ctx context.Context, fileNo, aesKey string) ([]byte, error)
}

// SyncSource WeChatPadPro 实时消息通道；Run 阻塞至 ctx 取消或连接终止。
// onMessage 收到单条已解析 JSON 报文（map 形态，键名兼容 svr_id/new_msg_id 等）。
type SyncSource interface {
	Run(ctx context.Context, onMessage func(raw map[string]any)) error
}

// NameResolver 成员显示名解析（sys_user.wxid 唯一启用账号优先）。
type NameResolver interface {
	DisplayName(ctx context.Context, groupID, wxid string) (string, error)
	WxidByName(ctx context.Context, groupID, name string) (string, error)
}

// ConfigGet 运行时配置读取（*config.Store 天然满足；测试可注入假实现）。
type ConfigGet interface {
	Get(ctx context.Context, key string) (string, error)
}

// AppPermit 校验用户是否拥有指定应用的使用权限（nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// 编译期确认 *config.Store 满足 ConfigGet。
var _ ConfigGet = (*config.Store)(nil)

// Module 微信群智能应答模块。除 DB/Cfg 外均为可注入依赖；nil 时对应能力禁用（不连网）。
type Module struct {
	DB          *sql.DB
	Cfg         ConfigGet
	Permit      AppPermit // 应用权限检查（可注入；nil 不拦截）
	Sink        CaptureSink
	Archiver    ChatArchiver
	Sender      MessageSender
	Dify        DifyClient
	Members     MemberSource
	Images      ImageDownloader
	Files       FileDownloader // 文件/视频/语音下载（WF3/文档归档）
	Names       NameResolver
	Sync        SyncSource
	Rules       RuleProvider
	State       RecordStore
	Queue       PushQueue
	Ask         AskIngest    // 群内提问出口（bridge 接 callme ingest 逻辑）
	Chatlog     ChatlogSink  // 聊天归档出口（bridge 接 chatlog ingest 逻辑）
	Lines       LineSource   // 线路表（默认 team_line）
	Teams       TeamSource   // 班组名（默认 sys_team）
	Events      EventLog     // 消息幂等登记（默认 wa_event）
	MediaCache  MediaStore   // 媒体下载参数缓存（默认 wa_media）
	RecallStore RecallLog    // 撤回暂存（默认 wa_recall）
	ChatDays    ChatDayStore // 聊天日归档态（默认 wa_chat_* 表）
	AppLog      AppLogger    // 应用日志（bridge 接 logging.Logger）
	// ServerDir 数据目录根（辅证照片落盘 {ServerDir}/data/wechatanswer/）；空 = 当前目录。
	ServerDir string
	// Now 时间源（测试可注入）；nil = time.Now。
	Now func() time.Time
	// QueuePoll 补推轮询间隔；0 = 30s。
	QueuePoll time.Duration
	// ImageTTL 图片/媒体参数缓存保留时长；0 = 7 天（媒体固定 3 天）。
	ImageTTL time.Duration
	// AskSync 群内提问同步执行（测试可注入 true）；默认异步不阻塞消息管线。
	AskSync bool

	mu          sync.Mutex
	started     bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	syncCancel  context.CancelFunc // 当前消息通道运行（Reconnect 热重连取消用）
	reconnectCh chan struct{}      // 热重连踢醒（跳过退避）
	wsConnected bool
	lastReadyAt time.Time

	// 无 DB 时的内存回退存储（事件/媒体/撤回/聊天日）
	memEvents  *memEventLog
	memMedia   *memMediaStore
	memRecalls *memRecallLog
	memDays    *memChatDayStore
}

// New 构造模块；Sink / Archiver / Dify 等依赖可在装配后注入。
func New(db *sql.DB, cfg ConfigGet) *Module {
	return &Module{
		DB:          db,
		Cfg:         cfg,
		reconnectCh: make(chan struct{}, 1),
	}
}

// now 当前时间（可注入）。
func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// ensureMem 无 DB 回退存储懒初始化（并发安全）。
func (m *Module) ensureMem() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.memEvents == nil {
		m.memEvents = newMemEventLog()
	}
	if m.memMedia == nil {
		m.memMedia = newMemMediaStore()
	}
	if m.memRecalls == nil {
		m.memRecalls = newMemRecallLog()
	}
	if m.memDays == nil {
		m.memDays = newMemChatDayStore()
	}
	if m.reconnectCh == nil {
		m.reconnectCh = make(chan struct{}, 1)
	}
}

// eventLog / mediaCache / recallLog / chatDays 存取器（注入优先；有 DB 走 MySQL；否则内存）。
func (m *Module) eventLog() EventLog {
	if m.Events != nil {
		return m.Events
	}
	if m.DB != nil {
		return &SQLEventLog{DB: m.DB}
	}
	m.ensureMem()
	return m.memEvents
}

func (m *Module) mediaCache() MediaStore {
	if m.MediaCache != nil {
		return m.MediaCache
	}
	if m.DB != nil {
		return &SQLMediaStore{DB: m.DB}
	}
	m.ensureMem()
	return m.memMedia
}

func (m *Module) recallLog() RecallLog {
	if m.RecallStore != nil {
		return m.RecallStore
	}
	if m.DB != nil {
		return &SQLRecallLog{DB: m.DB}
	}
	m.ensureMem()
	return m.memRecalls
}

func (m *Module) chatDays() ChatDayStore {
	if m.ChatDays != nil {
		return m.ChatDays
	}
	if m.DB != nil {
		return &SQLChatDayStore{DB: m.DB}
	}
	m.ensureMem()
	return m.memDays
}

// EnsureSchema 建立本模块依赖的表（幂等；既有库补列保留存量数据）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	if m.DB == nil {
		return errors.New("wechatanswer: DB 未注入")
	}
	for _, ddl := range schemaDDL {
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return m.ensureColumns(ctx)
}

// RegisterRoutes 注册 /api/v1/wechat-answer 下全部路由（挂载在 /api/v1 之下）。
// 兼容旧路径前缀 /wechatanswer。监听群/规则 CRUD 需登录 + wechat-answer 应用权限；
// 写操作限管理员（超管/班组管理员）。
func (m *Module) RegisterRoutes(r chi.Router) {
	for _, prefix := range []string{"/wechat-answer", "/wechatanswer"} {
		prefix := prefix
		r.Route(prefix, func(r chi.Router) {
			r.Get("/groups", m.handleGroupList)
			r.Post("/groups", m.handleGroupCreate)
			r.Put("/groups/{id}", m.handleGroupUpdate)
			r.Delete("/groups/{id}", m.handleGroupDelete)
			r.Get("/groups/{id}/rules", m.handleGroupRuleList)
			r.Post("/groups/{id}/rules", m.handleRuleUpsert)
			r.Get("/rules/{id}", m.handleRuleGet)
			r.Put("/rules/{id}", m.handleRuleUpdate)
			r.Delete("/rules/{id}", m.handleRuleDelete)
			r.Get("/status", m.handleStatus)
			r.Post("/status/reconnect", m.handleReconnect)
			r.Post("/reconnect", m.handleReconnect) // 旧路径兼容
		})
	}
}

// Start 启动后台任务（WeChatPadPro 同步通道 + 补推队列轮询 + 聊天归档巡检 + 图片清理排程）。
// 网络默认禁用：Sync 未注入或 Token 未配置时不建立连接；Sink 未注入时不跑补推。
func (m *Module) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("wechatanswer: 已启动")
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	if m.Sync != nil {
		if m.cfgValue(runCtx, "WECHATPADPRO_TOKEN", "") == "" {
			log.Printf("[微信智能应答] 未配置 WECHATPADPRO_TOKEN，消息通道等待配置")
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runSyncLoop(runCtx)
		}()
	}
	if m.Queue != nil && m.Sink != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runQueueLoop(runCtx)
		}()
	}
	if m.Chatlog != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runDigestLoop(runCtx)
		}()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runCleanupLoop(runCtx)
	}()
	return nil
}

// Close 停止后台任务并等待退出。
func (m *Module) Close() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = false
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	return nil
}

// Reconnect 热重连实时通道：取消当前连接并跳过退避立即重建（不改任何配置）。
func (m *Module) Reconnect(ctx context.Context) {
	m.mu.Lock()
	cancel := m.syncCancel
	ch := m.reconnectCh
	m.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	if cancel != nil {
		cancel()
	}
	m.appLog(ctx, "info", "business", "wechat.sync.reconnect", "手动热重连实时通道", "", nil)
}

// setSyncState 记录通道连接状态（status 快照用；不含任何敏感值）。
func (m *Module) setSyncState(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsConnected = connected
	if connected {
		m.lastReadyAt = m.now()
	}
}

func (m *Module) markReady() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastReadyAt = m.now()
	m.wsConnected = true
}

// statusSnapshot 接入状态（禁止暴露 Token/URL 等敏感值）。
func (m *Module) statusSnapshot(ctx context.Context) map[string]any {
	tokenConfigured := m.cfgValue(ctx, "WECHATPADPRO_TOKEN", "") != ""
	difyConfigured := m.cfgValue(ctx, "DIFY_API_URL", "") != ""
	m.mu.Lock()
	ws := m.wsConnected
	last := m.lastReadyAt
	m.mu.Unlock()
	lastStr := ""
	if !last.IsZero() {
		lastStr = last.Format("2006-01-02 15:04:05")
	}
	return map[string]any{
		"configured":       tokenConfigured,
		"token_configured": tokenConfigured,
		"ws_connected":     ws,
		"auto_reconnect":   tokenConfigured && m.Sync != nil,
		"last_ready_at":    lastStr,
		// 兼容旧键
		"dify_configured": difyConfigured,
		"has_sink":        m.Sink != nil,
		"has_archiver":    m.Archiver != nil,
		"has_sync":        m.Sync != nil,
	}
}

// runSyncLoop 消息同步循环：断线按指数退避重连（上限 30s）；Reconnect 热重连跳过退避。
func (m *Module) runSyncLoop(ctx context.Context) {
	attempts := 0
	for ctx.Err() == nil {
		m.mu.Lock()
		if m.reconnectCh == nil {
			m.reconnectCh = make(chan struct{}, 1)
		}
		ch := m.reconnectCh
		m.mu.Unlock()

		if m.cfgValue(ctx, "WECHATPADPRO_TOKEN", "") == "" {
			select {
			case <-ctx.Done():
				return
			case <-ch:
			case <-time.After(5 * time.Second):
			}
			attempts = 0
			continue
		}
		err := m.runSyncOnce(ctx, ch)
		if ctx.Err() != nil {
			return
		}
		attempts++
		delay := time.Second << (attempts - 1)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		if err != nil {
			log.Printf("[微信智能应答] 消息通道断开（第 %d 次重连前）：%v", attempts, err)
			m.appLog(ctx, "warn", "business", "wechat.sync.disconnected", "消息通道断开，准备重连", err.Error(), map[string]any{"attempts": attempts})
		}
		select {
		case <-ctx.Done():
			return
		case <-ch:
			attempts = 0 // 手动热重连：立即重建
		case <-time.After(delay):
		}
	}
}

// runSyncOnce 单次通道连接（Reconnect 时经 cancel 热断重建）。
func (m *Module) runSyncOnce(ctx context.Context, kick chan struct{}) error {
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.syncCancel = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.setSyncState(false)
	}()
	m.setSyncState(true)
	err := m.Sync.Run(runCtx, func(raw map[string]any) {
		m.markReady()
		msg := ParseMessage(raw)
		if msg == nil {
			return
		}
		m.HandleMessage(context.WithoutCancel(runCtx), msg)
	})
	if runCtx.Err() != nil {
		return nil // 热重连/关停主动断开
	}
	return err
}
