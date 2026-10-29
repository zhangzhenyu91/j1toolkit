// Package ui 定义桌面壳（Fyne）与业务核心之间的边界：
// Backend 接口、领域类型、任务事件接线（JobRunner）与 FakeBackend 冒烟实现。
// 本包为纯逻辑层，不依赖任何 GUI 框架，可在无显示环境下测试；
// Fyne 控件层见 internal/ui/appshell，平台壳层能力见 internal/platform。
package ui

import "time"

// JobStatus 任务状态机：pending → running → success / failed / cancelled。
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSuccess   JobStatus = "success"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Terminal 判断任务是否已到终态。
func (s JobStatus) Terminal() bool {
	return s == JobSuccess || s == JobFailed || s == JobCancelled
}

// CN 中文状态文案（result.paused=true 的用户暂停由展示层另行标注「已暂停」）。
func (s JobStatus) CN() string {
	switch s {
	case JobPending:
		return "排队中"
	case JobRunning:
		return "执行中"
	case JobSuccess:
		return "成功"
	case JobFailed:
		return "失败"
	case JobCancelled:
		return "已取消"
	default:
		return string(s)
	}
}

// Job 任务档案（对应核心 state/jobs/<id>.json）。
type Job struct {
	ID         string         `json:"job_id"`
	AppID      string         `json:"app_id"`
	Status     JobStatus      `json:"status"`
	Error      string         `json:"error,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	StartedAt  time.Time      `json:"started_at,omitempty"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
}

// Paused 结果里 result.paused=true 表示用户主动暂停（非失败，可续跑）。
func (j Job) Paused() bool {
	if j.Result == nil {
		return false
	}
	v, _ := j.Result["paused"].(bool)
	return v
}

// JobProgress 任务进度事件。
type JobProgress struct {
	JobID   string `json:"job_id"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Label   string `json:"label,omitempty"`
}

// Percent 计算百分比（total<=0 时为 0）。
func (p JobProgress) Percent() int {
	if p.Total <= 0 {
		return 0
	}
	pct := p.Current * 100 / p.Total
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// EventKind 核心→壳事件种类（对应 RPC 事件名）。
type EventKind string

const (
	EventJobLog      EventKind = "job.log"
	EventJobProgress EventKind = "job.progress"
	EventJobDone     EventKind = "job.done"
	EventCoreExit    EventKind = "core.exit"
)

// Event 核心→壳主动推送的事件。
type Event struct {
	Kind     EventKind
	JobID    string
	Line     string // job.log
	Progress *JobProgress
	Job      *Job // job.done 携带终态任务
	Code     int  // core.exit
}

// Subscription 事件订阅句柄；Close 幂等。
type Subscription interface {
	Events() <-chan Event
	Close() error
}

// AppInfo 应用声明（对应核心 getApps 一项）。
type AppInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Badge      string `json:"badge,omitempty"`
	Desc       string `json:"desc,omitempty"`
	Order      int    `json:"order"`
	HasBrowser bool   `json:"has_browser"`
}

// SystemInfo 运行环境信息（对应核心 getSystemInfo）。
type SystemInfo struct {
	Platform         string   `json:"platform"`
	Machine          string   `json:"machine"`
	CoreVersion      string   `json:"python"` // 核心版本文案（沿用 RPC 字段名）
	Chrome           string   `json:"chrome"` // 生效渲染浏览器（空=内置 Electron）
	ConfigPath       string   `json:"config_path"`
	StateDir         string   `json:"state_dir"`
	SuggestedOutDirs []string `json:"suggested_outdirs,omitempty"`
}

// SSOConfig 派车系统 SSO 凭据。
// Password 仅作为 SaveConfig 入参有效（空=不修改）；GetConfig 不回填密码，
// 只以 HasPassword/PasswordSource 供界面提示。
type SSOConfig struct {
	Username       string `json:"username"`
	Password       string `json:"password,omitempty"`
	HasPassword    bool   `json:"has_password"`
	PasswordSource string `json:"password_source,omitempty"`
}

// RenderConfig 渲染器选择。
type RenderConfig struct {
	Chrome                  string   `json:"chrome"` // 显式公司浏览器（国密/兼容回退），空=内置 Electron
	Builtin                 bool     `json:"builtin"`
	IgnoreCertificateErrors bool     `json:"ignore_certificate_errors"`
	CertificateSPKIPins     []string `json:"certificate_spki_pins"`
}

// OrderExportConfig 轨迹导出并发度。
type OrderExportConfig struct {
	QueryWorkers  int `json:"query_workers"`
	RenderWorkers int `json:"render_workers"`
}

// ScheduleConfig 驻留调度配置。
type ScheduleConfig struct {
	Enabled bool   `json:"enabled"`
	Time    string `json:"time"`   // HH:MM
	Driver  string `json:"driver"` // os=系统定时器 / app=客户端驻留执行
}

// UIConfig 壳层后台运行配置。
type UIConfig struct {
	CloseToTray bool `json:"close_to_tray"`
	Autostart   bool `json:"autostart"`
}

// DevConfig 开发调试开关。
type DevConfig struct {
	Mock bool `json:"mock"`
}

// USBConfig U 盘（KVM 虚拟 U 盘）发现配置。
type USBConfig struct {
	Label       string `json:"label"`
	FallbackDir string `json:"fallback_dir"`
}

// SyncAppConfig 树障/交跨统计表同步配置（契约同构，独立指令文件）。
type SyncAppConfig struct {
	LocalPath   string `json:"local_path"`
	FileName    string `json:"file_name"`
	NightlyTime string `json:"nightly_time"`
}

// BrowserAutomationConfig 受控浏览器（课程自动学习）配置。
type BrowserAutomationConfig struct {
	CDPURL      string `json:"cdp_url"`
	ProfileDir  string `json:"profile_dir"`
	StartURL    string `json:"start_url"`
	StepTimeout int    `json:"step_timeout"`
}

// Config 客户端配置视图（对应核心 config.json 的 GUI 可写部分）。
type Config struct {
	SSO               SSOConfig               `json:"sso"`
	Render            RenderConfig            `json:"render"`
	OrderExport       OrderExportConfig       `json:"order_export"`
	Schedule          ScheduleConfig          `json:"schedule"`
	UI                UIConfig                `json:"ui"`
	Dev               DevConfig               `json:"dev"`
	USB               USBConfig               `json:"usb"`
	TreeSync          SyncAppConfig           `json:"tree_sync"`
	CrossingSync      SyncAppConfig           `json:"crossing_sync"`
	BrowserAutomation BrowserAutomationConfig `json:"browser_automation"`
}

// DefaultConfig 壳层默认配置（与 toolkit/config.example.json 口径一致）。
func DefaultConfig() Config {
	return Config{
		Render:       RenderConfig{Builtin: true},
		OrderExport:  OrderExportConfig{QueryWorkers: 4, RenderWorkers: 2},
		Schedule:     ScheduleConfig{Enabled: true, Time: "09:15", Driver: "os"},
		UI:           UIConfig{CloseToTray: true},
		USB:          USBConfig{Label: "GLKVM"},
		TreeSync:     SyncAppConfig{FileName: "检修一班树木统计表.xlsx", NightlyTime: "23:55"},
		CrossingSync: SyncAppConfig{FileName: "检修一班交跨统计表.xlsx", NightlyTime: "23:55"},
		BrowserAutomation: BrowserAutomationConfig{
			CDPURL:      "http://127.0.0.1:9222",
			StepTimeout: 120,
		},
	}
}

// HistoryRecord 执行记录（对应核心 state/history/<app_id>.jsonl 一行）。
type HistoryRecord struct {
	JobID  string         `json:"job_id"`
	Time   string         `json:"time"`
	Status JobStatus      `json:"status"`
	Error  string         `json:"error,omitempty"`
	Result map[string]any `json:"result,omitempty"`
	Dir    string         `json:"dir,omitempty"` // result.dir 的快捷位（打开目录用）
}

// Paused 同 Job.Paused 口径。
func (r HistoryRecord) Paused() bool {
	if r.Result == nil {
		return false
	}
	v, _ := r.Result["paused"].(bool)
	return v
}

// DeleteResult deleteHistory 返回（删执行记录连带任务日志/档案）。
type DeleteResult struct {
	Records int `json:"records"`
	Files   int `json:"files"`
	Skipped int `json:"skipped"`
}

// ClearResult clearLogs 返回。
type ClearResult struct {
	Removed int `json:"removed"`
	Skipped int `json:"skipped"`
}

// LogStats 日志占用统计。
type LogStats struct {
	Dir   string `json:"dir"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// QuizBank 题库（xls/xlsx 导入后保存在本机，各启用库合并搜题）。
type QuizBank struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	SourceFile string    `json:"source_file"`
	Count      int       `json:"count"`
	ImportedAt time.Time `json:"imported_at"`
	Enabled    bool      `json:"enabled"`
}

// QuizQuestion 题目。
type QuizQuestion struct {
	Content  string            `json:"content"`
	Answer   string            `json:"answer"`
	Options  map[string]string `json:"options,omitempty"` // A~F
	Type     string            `json:"type,omitempty"`
	Analysis string            `json:"analysis,omitempty"`
	BankName string            `json:"bank_name,omitempty"`
}

// QuizMatch 搜题自测结果。
type QuizMatch struct {
	Matched  bool          `json:"matched"`
	Score    float64       `json:"score"`
	Question QuizQuestion  `json:"question"`
	Near     *QuizQuestion `json:"near,omitempty"` // 未匹配时最接近的题目
}

// QuizScanOptions 扫描可调选项（对齐 quizSearch:setOptions；nil 字段不修改）。
type QuizScanOptions struct {
	Interval  *int     `json:"interval,omitempty"`  // 扫描间隔 ms（钳制 500~3000）
	Threshold *float64 `json:"threshold,omitempty"` // 匹配命中阈值（钳制 0.3~0.9）
	Opacity   *float64 `json:"opacity,omitempty"`   // 结果窗不透明度（钳制 0.3~1.0）
}

// QuizBoxDebug 各框最近 OCR 摘要（运行诊断：captureError=截屏层 / 摘要=OCR 层 / engineError=引擎层）。
type QuizBoxDebug struct {
	Matched bool   `json:"matched"`
	Snippet string `json:"snippet"`
}

// QuizScanState 扫描服务状态（对齐 quizSearch:getState）。
type QuizScanState struct {
	Scanning      bool                     `json:"scanning"`
	EngineReady   bool                     `json:"engineReady"`
	EngineError   string                   `json:"engineError,omitempty"`
	CaptureError  string                   `json:"captureError,omitempty"`
	SessionType   string                   `json:"sessionType,omitempty"` // wayland 截屏不可用（需 X11 会话）
	OcrMs         int64                    `json:"ocrMs"`
	BoxDebug      map[string]*QuizBoxDebug `json:"boxDebug"`
	Interval      int                      `json:"interval"`
	Threshold     float64                  `json:"threshold"`
	Opacity       float64                  `json:"opacity"`
	BoxVisible    map[string]bool          `json:"boxVisible"`
	QuestionCount int                      `json:"questionCount"`
}

// QuizSelftestResult 扫描引擎自检（模型文件齐备性 + 引擎细节，对齐 quizSearch:selftest）。
type QuizSelftestResult struct {
	OK        bool           `json:"ok"`
	ModelsDir string         `json:"modelsDir"`
	Missing   []string       `json:"missing"`
	Engine    map[string]any `json:"engine,omitempty"`
}

// 扫描页事件类型（QuizEvent.Type，对齐 quizsearch:event）。
const (
	QuizEventScan      = "scan"      // 启停扫描
	QuizEventEngine    = "engine"    // 引擎就绪或失败
	QuizEventBoxHidden = "boxHidden" // 扫描框被隐藏（悬浮窗关闭钮）
)

// QuizEvent 扫描页事件（对齐 quizsearch:event IPC payload）。
type QuizEvent struct {
	Type     string `json:"type"`
	Scanning bool   `json:"scanning,omitempty"` // scan
	Ready    bool   `json:"ready"`              // scan / engine
	Error    string `json:"error,omitempty"`    // engine
	Box      string `json:"box,omitempty"`      // boxHidden
}

// QuizEventStream 扫描页事件订阅句柄；Close 幂等。
type QuizEventStream interface {
	Events() <-chan QuizEvent
	Close() error
}

// CourseOptions 课程自动学习执行选项。
type CourseOptions struct {
	ScanOnly    bool `json:"scan_only"`
	MaxAttempts int  `json:"max_attempts"`
	Timeout     int  `json:"timeout"`
}

// CourseStatus 受控浏览器/引擎状态（对应 getAppStatus(course_autoplay)）。
type CourseStatus struct {
	CDPURL      string `json:"cdp_url"`
	Browser     string `json:"browser"`
	Connected   bool   `json:"connected"`
	Pages       int    `json:"pages"`
	Playwright  bool   `json:"playwright"`
	ActiveLists int    `json:"active_lists"`
	StartURL    string `json:"start_url"`
}

// CourseResult 课程任务结果摘要（任务 result 常用段）。
type CourseResult struct {
	Total     int  `json:"total"`
	Completed int  `json:"completed"`
	Pending   int  `json:"pending"`
	Skipped   int  `json:"skipped"`
	Processed int  `json:"processed"`
	Paused    bool `json:"paused"`
}

// CourseCheckpoint 课程续刷检查点（按 trainid/列表独立持久化）。
type CourseCheckpoint struct {
	TrainID   string `json:"train_id"`
	ListName  string `json:"list_name"`
	Page      int    `json:"page"`
	Processed int    `json:"processed"`
	Failed    int    `json:"failed"`
	Skipped   int    `json:"skipped"`
	UpdatedAt string `json:"updated_at"`
}

// TickResult 驻留调度单次判定结果。
type TickResult struct {
	Started bool   `json:"started"`
	JobID   string `json:"job_id,omitempty"`
}

// DailyTickResult maybeRunDaily 返回：每日派车单到点判定，
// 树障/交跨夜间兜底同挂此调用。
type DailyTickResult struct {
	Started             bool        `json:"started"`
	Reason              string      `json:"reason,omitempty"`
	JobID               string      `json:"job_id,omitempty"`
	TreeSyncNightly     *TickResult `json:"tree_sync_nightly,omitempty"`
	CrossingSyncNightly *TickResult `json:"crossing_sync_nightly,omitempty"`
}
