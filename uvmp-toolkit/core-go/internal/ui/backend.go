package ui

import "context"

// Backend 是桌面壳与业务核心之间的唯一边界。
// 界面交互只调用本接口；核心（toolkit RPC / Go 服务）后续以实现本接口的方式适配，
// 不把任何业务语义写进 UI 层。FakeBackend（fake.go）为冒烟/演示实现。
//
// 方法族与 RPC 方法表（内网工具箱开发指南 §4.4）一一对应，
// 题库（quiz_search 文本侧）与课程（course_autoplay）为壳内高频场景，单独成族便于强类型使用。
type Backend interface {
	SystemAPI
	ConfigAPI
	AppAPI
	TaskAPI
	HistoryAPI
	QuizAPI
	CourseAPI
	ScheduleAPI
	EventAPI
}

// SystemAPI 系统信息与本机路径操作。
type SystemAPI interface {
	// Ping 核心存活探测。
	Ping(ctx context.Context) error
	// SystemInfo 运行环境信息。
	SystemInfo(ctx context.Context) (SystemInfo, error)
	// OpenPath 在文件管理器中打开路径（目录或文件）。
	OpenPath(ctx context.Context, path string) error
	// ShowItem 在文件管理器中定位该路径。
	ShowItem(ctx context.Context, path string) error
}

// ConfigAPI 配置读写。SaveConfig 为嵌套局部更新语义（与 RPC saveConfig 一致），
// 空密码表示不修改（由实现层剥除）。
type ConfigAPI interface {
	GetConfig(ctx context.Context) (Config, error)
	SaveConfig(ctx context.Context, patch ConfigPatch) error
}

// AppAPI 应用列表与状态。
type AppAPI interface {
	// ListApps 已登记应用（按 order 升序）。
	ListApps(ctx context.Context) ([]AppInfo, error)
	// AppStatus 应用状态卡数据（原样状态字典，展示层用 decode.go 的解码器取型）。
	AppStatus(ctx context.Context, appID string) (map[string]any, error)
	// LaunchAppBrowser 启动/复用受控浏览器（仅 HasBrowser 应用；非 Job）。
	LaunchAppBrowser(ctx context.Context, appID string) error
}

// TaskAPI 任务通道（runJob 契约：启动即执行，事件经 EventAPI 推送）。
type TaskAPI interface {
	// RunJob 启动任务，返回 job_id；快速失败的任务可能在返回前就发完事件（订阅侧须做回放缓冲）。
	RunJob(ctx context.Context, appID string, params map[string]any) (string, error)
	// CancelJob 取消任务（课程任务语义为「暂停」：保留浏览器与登录态）。
	CancelJob(ctx context.Context, jobID string) error
	// GetJob 查询任务档案。
	GetJob(ctx context.Context, jobID string) (Job, error)
	// JobLog 任务完整日志全文。
	JobLog(ctx context.Context, jobID string) (string, error)
	// Preview 输入预览（如 pcd_export 解析单号清单）。
	Preview(ctx context.Context, appID string, params map[string]any) (map[string]any, error)
}

// HistoryAPI 执行记录与日志管理。
type HistoryAPI interface {
	// History 应用执行记录（新→旧）。
	History(ctx context.Context, appID string) ([]HistoryRecord, error)
	// DeleteHistory 删执行记录：jobID 非空删单条，空清空该应用；连带任务日志/档案。
	DeleteHistory(ctx context.Context, appID string, jobID string) (DeleteResult, error)
	// ClearLogs 一键清除全部任务日志与档案（运行中任务的占用文件跳过）。
	ClearLogs(ctx context.Context) (ClearResult, error)
	// LogStats 日志占用统计。
	LogStats(ctx context.Context) (LogStats, error)
}

// QuizAPI 题库搜题：题库文本侧 + 扫描控制（对齐旧壳 quizSearch:* IPC）。
// OCR 截屏/悬浮窗由扫描服务（quizsearch.Scanner）承担，经本接口统一编排。
type QuizAPI interface {
	// QuizBanks 题库列表（含启用开关）。
	QuizBanks(ctx context.Context) ([]QuizBank, error)
	// QuizImportBank 从 xls/xlsx 导入题库（表头：题干、答案、选项A~F，可选题型/解析）。
	QuizImportBank(ctx context.Context, path string) (QuizBank, error)
	// QuizSetBankEnabled 启用/停用题库（仅启用库参与搜题）。
	QuizSetBankEnabled(ctx context.Context, bankID string, enabled bool) error
	// QuizRenameBank 重命名题库。
	QuizRenameBank(ctx context.Context, bankID string, name string) error
	// QuizRemoveBank 删除题库。
	QuizRemoveBank(ctx context.Context, bankID string) error
	// QuizMatch 题干文本匹配（搜题自测）。
	QuizMatch(ctx context.Context, text string) (QuizMatch, error)
	// QuizSaveTemplate 保存空白题库模板到指定路径。
	QuizSaveTemplate(ctx context.Context, path string) error

	// QuizScanState 扫描服务状态（启停/引擎/截屏/各框摘要/选项）。
	QuizScanState(ctx context.Context) (QuizScanState, error)
	// QuizScanStart 开始扫描（需已有题库；创建引擎与 A/B 扫描框、结果窗）。
	QuizScanStart(ctx context.Context) (QuizScanState, error)
	// QuizScanStop 停止扫描（作废在途 OCR、销毁引擎与悬浮窗）。
	QuizScanStop(ctx context.Context) (QuizScanState, error)
	// QuizSetBoxVisible 扫描框 A/B 显隐（box="A"/"B"）。
	QuizSetBoxVisible(ctx context.Context, box string, visible bool) (QuizScanState, error)
	// QuizResetBoxes 恢复双框默认布局。
	QuizResetBoxes(ctx context.Context) (QuizScanState, error)
	// QuizSetOptions 热更 interval/threshold/opacity（nil 字段不修改，实现层钳制后落盘）。
	QuizSetOptions(ctx context.Context, opt QuizScanOptions) (QuizScanState, error)
	// QuizDebugCapture 把扫描框当前所见画面存成 PNG（排查截屏/OCR/匹配问题），返回文件路径；dir 空=题库数据目录。
	QuizDebugCapture(ctx context.Context, dir string) (string, error)
	// QuizSelftest 扫描引擎自检（模型文件齐备性 + 引擎细节）。
	QuizSelftest(ctx context.Context) (QuizSelftestResult, error)
	// QuizSubscribe 扫描页事件流（scan/engine/boxHidden）；每次调用返回独立订阅句柄。
	QuizSubscribe(ctx context.Context) (QuizEventStream, error)
}

// CourseAPI 学堂刷课（course_autoplay）强类型通道；
// 开始/暂停底层仍是任务通道（RunJob/CancelJob），事件与历史与其他应用同轨。
type CourseAPI interface {
	// CourseStatus 受控浏览器/引擎状态。
	CourseStatus(ctx context.Context) (CourseStatus, error)
	// CourseOpenBrowser 保存启动网址并启动/复用受控浏览器。
	CourseOpenBrowser(ctx context.Context, startURL string) error
	// CourseStart 开始学习（scanOnly=仅扫描不进入课程），返回 job_id。
	CourseStart(ctx context.Context, opt CourseOptions) (string, error)
	// CoursePause 暂停学习（等价 CancelJob：保留浏览器与登录态，可续刷）。
	CoursePause(ctx context.Context, jobID string) error
	// CourseCheckpoints 各培训班/列表的续刷检查点。
	CourseCheckpoints(ctx context.Context) ([]CourseCheckpoint, error)
}

// ScheduleAPI 驻留调度（schedule.driver=app 时壳每 60s 调用；
// 到点判定在核心侧 due_check 门闸内完成，接口只回报是否启动了任务）。
type ScheduleAPI interface {
	// MaybeRunDaily 每日派车单同步到点判定（树障/交跨夜间兜底同挂此调用）。
	MaybeRunDaily(ctx context.Context) (DailyTickResult, error)
	// MaybeRunTreeSyncTick 树障统计表 U 盘指令轮询（U 盘不在秒退）。
	MaybeRunTreeSyncTick(ctx context.Context) (TickResult, error)
	// MaybeRunCrossingSyncTick 交跨统计表 U 盘指令轮询。
	MaybeRunCrossingSyncTick(ctx context.Context) (TickResult, error)
}

// EventAPI 核心→壳事件订阅。
type EventAPI interface {
	// Subscribe 订阅任务/核心事件；每次调用返回独立订阅句柄。
	Subscribe(ctx context.Context) (Subscription, error)
}

// ConfigPatch 配置嵌套局部更新：指针非 nil 的段参与更新。
type ConfigPatch struct {
	SSO               *SSOConfig               `json:"sso,omitempty"`
	Render            *RenderConfig            `json:"render,omitempty"`
	OrderExport       *OrderExportConfig       `json:"order_export,omitempty"`
	Schedule          *ScheduleConfig          `json:"schedule,omitempty"`
	UI                *UIConfig                `json:"ui,omitempty"`
	Dev               *DevConfig               `json:"dev,omitempty"`
	USB               *USBConfig               `json:"usb,omitempty"`
	TreeSync          *SyncAppConfig           `json:"tree_sync,omitempty"`
	CrossingSync      *SyncAppConfig           `json:"crossing_sync,omitempty"`
	BrowserAutomation *BrowserAutomationConfig `json:"browser_automation,omitempty"`
}

// HasSSOPassword patch 是否携带新密码（空密码=不修改，由实现层剥除）。
func (p ConfigPatch) HasSSOPassword() bool {
	return p.SSO != nil && p.SSO.Password != ""
}

// Disposer 可释放底层资源的后端（扫描服务/悬浮窗等）；UI 退出时调用（幂等）。
type Disposer interface {
	Dispose()
}
