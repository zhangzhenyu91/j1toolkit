package appshell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// Info 信息弹层。
func Info(win fyne.Window, title, msg string) {
	runUI(func() { newMessageDialog(win, title, msg, "info").Show() })
}

// Warn 警示弹层。
func Warn(win fyne.Window, title, msg string) {
	runUI(func() { newMessageDialog(win, title, msg, "warning").Show() })
}

func newMessageDialog(win fyne.Window, title, msg, iconName string) *dialog.CustomDialog {
	d := dialog.NewCustomWithoutButtons(title, dialogMessage(msg), win)
	d.SetIcon(lineIcon(iconName, BrandBlue))
	if iconName == "warning" {
		d.SetIcon(lineIcon(iconName, WarningOrange))
	}
	d.SetButtons([]fyne.CanvasObject{
		newAppButton("知道了", "check", widget.MediumImportance, func() { d.Hide() }),
	})
	return d
}

func dialogMessage(msg string) *widget.Label {
	label := widget.NewLabel(msg)
	label.Alignment = fyne.TextAlignCenter
	label.Wrapping = fyne.TextWrapWord
	return label
}

// Confirm 确认弹层（ok 为确认按钮文案）。
func Confirm(win fyne.Window, title, msg, ok string, onOk func()) {
	runUI(func() {
		newConfirmDialog(win, title, msg, ok, widget.HighImportance, onOk).Show()
	})
}

// ConfirmDanger 危险操作确认（删除/清除/退出中断任务）。
func ConfirmDanger(win fyne.Window, title, msg, ok string, onOk func()) {
	runUI(func() {
		newConfirmDialog(win, title, msg, ok, widget.DangerImportance, onOk).Show()
	})
}

func newConfirmDialog(win fyne.Window, title, msg, ok string, importance widget.Importance, onOk func()) *dialog.CustomDialog {
	d := dialog.NewCustomWithoutButtons(title, dialogMessage(msg), win)
	d.SetIcon(lineIcon("info", BrandBlue))
	if importance == widget.DangerImportance {
		d.SetIcon(lineIcon("warning", DangerRed))
	}
	d.SetButtons([]fyne.CanvasObject{
		newAppButton("取消", "cancel", widget.LowImportance, func() { d.Hide() }),
		newAppButton(ok, "check", importance, func() {
			d.Hide()
			if onOk != nil {
				onOk()
			}
		}),
	})
	return d
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
		d := dialog.NewCustomWithoutButtons(title, entry, win)
		d.SetButtons([]fyne.CanvasObject{
			newAppButton("取消", "cancel", widget.LowImportance, func() { d.Hide() }),
			newAppButton("确定", "check", widget.HighImportance, func() {
				d.Hide()
				if onSubmit != nil {
					onSubmit(entry.Text)
				}
			}),
		})
		d.Show()
	})
}
