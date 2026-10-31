package appshell

import (
	"io"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// newLogActions 为技术支持保留轻量复制/导出入口，不属于普通用户主操作。
func newLogActions(s *Shell, text func() string, fileName string) fyne.CanvasObject {
	copyBtn := newAppButton("复制", "copy", widget.LowImportance, func() {
		content := text()
		if strings.TrimSpace(content) == "" {
			s.Toast("当前没有可复制的日志", ToastWarning)
			return
		}
		s.App.Clipboard().SetContent(content)
		s.Toast("日志已复制", ToastSuccess)
	})
	exportBtn := newAppButton("导出", "download", widget.LowImportance, func() {
		content := text()
		if strings.TrimSpace(content) == "" {
			s.Toast("当前没有可导出的日志", ToastWarning)
			return
		}
		save := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
			if err != nil {
				s.Toast("导出日志失败："+err.Error(), ToastError)
				return
			}
			if writer == nil {
				return
			}
			_, writeErr := io.WriteString(writer, content)
			closeErr := writer.Close()
			if writeErr != nil {
				s.Toast("写入日志失败："+writeErr.Error(), ToastError)
				return
			}
			if closeErr != nil {
				s.Toast("保存日志失败："+closeErr.Error(), ToastError)
				return
			}
			s.Toast("日志已导出", ToastSuccess)
		}, s.Win)
		save.SetFileName(fileName)
		save.Show()
	})
	return container.NewHBox(copyBtn, exportBtn)
}
