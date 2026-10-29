//go:build !windows && !(linux && x11)

package appshell

import "fyne.io/fyne/v2"

func newNativeWindowOps(fyne.Window) nativeWindowOps { return unsupportedWindowOps{} }

func updateNativeFrame(fyne.Window) {}
