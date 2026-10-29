//go:build windows

package main

import "golang.org/x/sys/windows"

// imm32 / ImmDisableIME：冒烟进程禁用输入法，避免第三方 IME（如搜狗）在窗口获得
// 前台焦点时注入被测进程——IME 自带的 libpng 会把它自己皮肤 PNG 的
// "iCCP: known incorrect sRGB profile" 警告写到本进程 stderr，污染门禁日志
// （已核实字符串只存在于 C:\Windows\System32\sogoupy.ime 等 IME 模块）。
// 只影响当前进程/线程，且仅冒烟模式调用：正常运行不禁用 IME（设置页等仍可中文输入）。
var procImmDisableIME = windows.NewLazySystemDLL("imm32.dll").NewProc("ImmDisableIME")

// disableIMEForSmoke 在当前线程禁用 IME（必须在创建窗口前调用）。
func disableIMEForSmoke() {
	// ImmDisableIME(DWORD idThread)：0 = 调用线程
	_, _, _ = procImmDisableIME.Call(0)
}
