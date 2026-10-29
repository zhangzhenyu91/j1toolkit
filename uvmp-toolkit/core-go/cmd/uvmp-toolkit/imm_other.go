//go:build !windows

package main

// disableIMEForSmoke 非 Windows 无 IME 注入问题（冒烟不需要该处理）。
func disableIMEForSmoke() {}
