package appshell

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// ToastKind 轻提示种类。
type ToastKind int

const (
	ToastInfo ToastKind = iota
	ToastSuccess
	ToastWarning
	ToastError
)

// ShowToast 底部轻提示（2.4s 自动消失；可在任意线程调用）。
func ShowToast(win fyne.Window, msg string, kind ToastKind) {
	runUI(func() {
		c := win.Canvas()
		if c == nil {
			return
		}
		var bg, fg color.NRGBA
		switch kind {
		case ToastSuccess:
			bg, fg = SuccessGreenBG, SuccessGreenTxt
		case ToastWarning:
			bg, fg = WarningOrangeBG, WarningOrangeTx
		case ToastError:
			bg, fg = DangerRedBG, DangerRed
		default:
			bg, fg = LightBlueBG, BrandBlue
		}
		text := canvas.NewText(msg, fg)
		text.TextSize = 13
		rect := canvas.NewRectangle(bg)
		rect.CornerRadius = 8
		pop := widget.NewPopUp(
			container.NewStack(rect, container.NewPadded(text)),
			c,
		)
		size := c.Size()
		pop.ShowAtPosition(fyne.NewPos(12, size.Height-72))
		time.AfterFunc(2400*time.Millisecond, func() {
			runUI(func() { pop.Hide() })
		})
	})
}

// toast 信息类轻提示。
func toast(win fyne.Window, msg string) { ShowToast(win, msg, ToastInfo) }
