//go:build !windows

package usb

import (
	"os"
	"syscall"
)

// fileUID 取文件属主 uid（linux/macos；desktopUser 镜像回退用）。
func fileUID(st os.FileInfo) int {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid)
	}
	return -1
}
