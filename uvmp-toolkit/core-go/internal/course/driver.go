package course

import (
	"context"
	"time"
)

// TabInfo 标签页识别信息（find_list_page 阶段）。
type TabInfo struct {
	ID      string
	URL     string
	Title   string
	Visible bool
}

// Browser 一次运行的受控浏览器会话（chromedp/CDP 实现见 driver_chromedp.go；测试 fake 同契约）。
// 只连接本机 CDP（回环地址），不接管用户自行打开的窗口。
type Browser interface {
	// Tabs 当前全部标签页快照（识别课程列表用）。
	Tabs(ctx context.Context) ([]TabInfo, error)
	// BodyText 取标签页正文文本（页面内容识别用；失败返回错误）。
	BodyText(ctx context.Context, id string) (string, error)
	// AttachListPage 接管标签页为课程列表页并置前台。
	AttachListPage(ctx context.Context, id string) (ListPage, error)
	// Monitor 响应监视器（全程共享，驱动层喂入）。
	Monitor() *ResponseMonitor
	// Close 释放本会话连接（不关闭受控浏览器）。
	Close()
}

// ListPage 课程设置列表页（筛选/分页/进入学习）。
type ListPage interface {
	// ID 页面标识（响应监视器 scope）。
	ID() string
	URL() string
	Closed() bool
	// Close 关闭本页（引擎只关自己接管的学习弹窗；列表页一般不关）。
	Close() error
	// Screenshot 诊断截图落盘 PNG。
	Screenshot(path string) error

	// ReloadList 点「课程设置」页签（未激活时）或整页刷新，等 course/page 响应并解析。
	ReloadList(ctx context.Context) (*CoursePage, error)
	// ResetFilters 点「重置」按钮（缺失报「未找到课程设置区的「重置」按钮，无法保证扫描全部课程」），
	// 等 course/page 响应并解析。
	ResetFilters(ctx context.Context) (*CoursePage, error)
	// JumpToPage 页码输入框跳转到第 page 页（缺失报「未找到分页器的页码输入框，无法跳回续刷页」），
	// 等响应并等分页器高亮到位后解析。
	JumpToPage(ctx context.Context, page int) (*CoursePage, error)
	// NextPage 点分页器下一页（不可用报「课程总数显示仍有下一页，但分页器下一页不可用」），
	// 等响应并等分页器高亮到位后解析。page 为新页码。
	NextPage(ctx context.Context, page int) (*CoursePage, error)

	// WaitRow 等课程表格就绪并返回第 index 行文本（行数与接口不一致报
	// 「课程表格行数与接口不一致：DOM %d，接口 %d」；表格缺失报
	// 「未找到包含课程名称/学习进度/进入学习的课程表格」）。expectedName 参与就绪判定。
	WaitRow(ctx context.Context, index, pageSize int, expectedName string) (rowText string, err error)
	// EnterLearning 点第 index 行「进入学习」并接管弹出的学习页（入口缺失或不可见报
	// 「课程行没有可用的「进入学习」入口」；弹窗超时随 ctx/超时错误）。
	EnterLearning(ctx context.Context, index int) (LearnPage, error)
}

// LearnPage 「进入学习」弹出的学习页（课件树/视频/其他课件流程）。
type LearnPage interface {
	ID() string
	URL() string
	Closed() bool
	// Close 只关闭本学习弹窗（trial.py close_popup 口径）。
	Close() error
	Screenshot(path string) error

	// WaitWareTree 等课件目录树 DOM 数量与 n 一致（不一致报
	// 「课件目录数量与接口不一致：DOM %d，接口 %d」）。n≤1 不做数量强制（trial.py 同口径）。
	WaitWareTree(ctx context.Context, n int) error
	// ClickTreeItem 点目录第 index 项（切课件）。
	ClickTreeItem(ctx context.Context, index int) error

	// WaitVideoReady 等可见 video 元素可用（readyState≥1 且 duration 有限正数）。
	WaitVideoReady(ctx context.Context) error
	// InstallEndedHook 注入真实 ended 事件标记（__courseAutoplayEnded，幂等）。
	InstallEndedHook(ctx context.Context) error
	// VideoState 读取当前 video 播放状态。
	VideoState(ctx context.Context) (VideoState, error)
	// DragProgress 在进度条上按比例拖动（fromRatio→toRatio，12 步鼠标拖拽；
	// 进度条尺寸异常报「视频进度条尺寸异常，无法拖动」）。
	DragProgress(ctx context.Context, fromRatio, toRatio float64) error
	// Play 恢复播放（video.play() 失败回退点 .vjs-play-control；
	// 两者皆失败报「视频暂停且无法恢复播放」）。
	Play(ctx context.Context) error
	// WaitVideoEnded 等任一可见 video 真实 ended（含 ended 钩子标记）。
	WaitVideoEnded(ctx context.Context, timeout time.Duration) error

	// WaitAudioReady 等可见 audio 元素 readyState≥1（wareType=1 非 mp4）。
	WaitAudioReady(ctx context.Context) error
	// WaitIframeLoaded 等 selector 匹配的 iframe 预览加载出内容
	// （trial.py _wait_frame_loaded：帧建立 + 图片/画布/文本就绪；超时随错误）。
	WaitIframeLoaded(ctx context.Context, selector string, timeout time.Duration) error
}

// VideoState video 元素播放状态（Ended 含真实 ended 与 ended 钩子标记，
// 与 trial.py video.evaluate("ended || __courseAutoplayEnded") 同口径）。
type VideoState struct {
	Current  float64
	Duration float64
	Ended    bool
}
