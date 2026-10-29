package platform

// CloseAction 主窗口关闭 / 托盘退出等请求的处置动作。
type CloseAction int

const (
	// ActionHide 隐藏主窗口，进程驻留（托盘常驻）。
	ActionHide CloseAction = iota
	// ActionExit 直接退出。
	ActionExit
	// ActionConfirmExit 需要用户确认后才退出（有任务进行中）。
	ActionConfirmExit
)

// String 诊断文案。
func (a CloseAction) String() string {
	switch a {
	case ActionHide:
		return "hide"
	case ActionExit:
		return "exit"
	case ActionConfirmExit:
		return "confirm-exit"
	default:
		return "unknown"
	}
}

// ClosePolicy 关窗驻留与退出确认策略（镜像 electron/main.cjs 语义）：
//   - 关窗：close_to_tray 且托盘可用 → 隐藏驻留；否则直接退出（窗口全关即退出）；
//   - 托盘「退出」：有任务进行中 → 先确认（「仍要退出」会中断任务）；否则直接退出；
//   - forceQuit（托盘确认后 / before-quit）路径由调用方直接 ActionExit，不走策略。
type ClosePolicy struct {
	CloseToTray   bool // 关闭窗口时最小化到托盘
	TrayAvailable bool // 托盘图标已建立（托盘缺失时无法驻留）
	JobRunning    bool // 有导出任务正在进行（退出确认用）
}

// OnWindowClose 主窗口被关闭时的处置。
func (p ClosePolicy) OnWindowClose() CloseAction {
	if p.CloseToTray && p.TrayAvailable {
		return ActionHide
	}
	return ActionExit
}

// OnTrayExit 托盘菜单「退出」时的处置。
func (p ClosePolicy) OnTrayExit() CloseAction {
	if p.JobRunning {
		return ActionConfirmExit
	}
	return ActionExit
}

// ExitConfirmMessage 退出确认文案（与 electron/main.cjs 对话框一致）。
func (p ClosePolicy) ExitConfirmMessage() string {
	return "有导出任务正在进行，退出将中断任务。确定退出吗？"
}
