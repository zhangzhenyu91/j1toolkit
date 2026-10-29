package appshell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// Info 信息弹层。
func Info(win fyne.Window, title, msg string) {
	runUI(func() { dialog.ShowInformation(title, msg, win) })
}

// Warn 警示弹层。
func Warn(win fyne.Window, title, msg string) {
	runUI(func() {
		d := dialog.NewInformation(title, msg, win)
		d.SetDismissText("知道了")
		d.Show()
	})
}

// Confirm 确认弹层（ok 为确认按钮文案）。
func Confirm(win fyne.Window, title, msg, ok string, onOk func()) {
	runUI(func() {
		d := dialog.NewConfirm(title, msg, func(yes bool) {
			if yes && onOk != nil {
				onOk()
			}
		}, win)
		d.SetConfirmText(ok)
		d.SetDismissText("取消")
		d.Show()
	})
}

// ConfirmDanger 危险操作确认（删除/清除/退出中断任务）。
func ConfirmDanger(win fyne.Window, title, msg, ok string, onOk func()) {
	Confirm(win, title, msg, ok, onOk)
}

// PickFile 文件选择（原生对话框风格；exts 为扩展名如 ".xlsx"，自动附「所有文件」兜底——
// 麒麟/UKUI 过滤不可靠已踩坑）。cb 在 UI 线程回调，取消时以空路径通知。
func PickFile(win fyne.Window, title string, exts []string, cb func(path string)) {
	runUI(func() {
		d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if rc == nil || err != nil {
				cb("")
				return
			}
			path := rc.URI().Path()
			_ = rc.Close()
			cb(path)
		}, win)
		_ = title // fyne FileDialog 无标题接口，保留形参便于统一调用面
		if len(exts) > 0 {
			d.SetFilter(storage.NewExtensionFileFilter(exts))
		}
		d.SetConfirmText("选择")
		d.SetDismissText("取消")
		d.Show()
	})
}

// PickDirectory 目录选择；cb 取消时以空路径通知。
func PickDirectory(win fyne.Window, title, defaultDir string, cb func(path string)) {
	runUI(func() {
		d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if uri == nil || err != nil {
				cb("")
				return
			}
			cb(uri.Path())
		}, win)
		_ = title
		if defaultDir != "" {
			if u, uerr := storage.ParseURI("file://" + defaultDir); uerr == nil {
				if lu, ok := u.(fyne.ListableURI); ok {
					d.SetLocation(lu)
				}
			}
		}
		d.SetConfirmText("选择")
		d.SetDismissText("取消")
		d.Show()
	})
}

// PromptText 单行文本输入弹层（重命名等）。
func PromptText(win fyne.Window, title, initial string, onSubmit func(string)) {
	runUI(func() {
		entry := widget.NewEntry()
		entry.SetText(initial)
		d := dialog.NewCustomConfirm(title, "确定", "取消", entry, func(yes bool) {
			if yes && onSubmit != nil {
				onSubmit(entry.Text)
			}
		}, win)
		d.Show()
	})
}
