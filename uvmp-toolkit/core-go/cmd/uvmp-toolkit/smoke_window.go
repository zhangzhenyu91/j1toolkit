package main

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"time"

	"fyne.io/fyne/v2"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui/appshell"
)

// exerciseWindowMaximize 显式验证原生最大化与还原，不依赖桌面鼠标输入。
// 仅 UVMP_FYNE_SMOKE_MAXIMIZE=1 时启用，正常启动与默认 GUI 冒烟不改变行为。
func exerciseWindowMaximize(shell *appshell.Shell) error {
	if os.Getenv("UVMP_FYNE_SMOKE_MAXIMIZE") != "1" {
		return nil
	}
	fyne.DoAndWait(shell.ToggleMaximize)
	time.Sleep(450 * time.Millisecond)
	if !shell.WindowMaximized() {
		return errors.New("窗口未进入最大化状态")
	}
	if path := os.Getenv("UVMP_FYNE_SMOKE_MAX_CAPTURE"); path != "" {
		if err := writeWindowCapture(shell, path); err != nil {
			return fmt.Errorf("最大化截图失败：%w", err)
		}
	}
	fyne.DoAndWait(shell.ToggleMaximize)
	time.Sleep(450 * time.Millisecond)
	if shell.WindowMaximized() {
		return errors.New("窗口未能从最大化还原")
	}
	return nil
}

func writeWindowCapture(shell *appshell.Shell, path string) error {
	var img image.Image
	fyne.DoAndWait(func() { img = captureFrame(shell.Win.Canvas()) })
	if img == nil {
		return errors.New("没有取得真实帧")
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err = png.Encode(file, img); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
