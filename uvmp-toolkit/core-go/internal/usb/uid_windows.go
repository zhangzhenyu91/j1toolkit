//go:build windows

package usb

import "os"

// fileUID Windows 无 Unix 属主概念（desktopUser 仅 linux 使用，返回 -1 表示不可用）。
func fileUID(st os.FileInfo) int { return -1 }
