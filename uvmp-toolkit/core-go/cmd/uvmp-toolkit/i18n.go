package main

import (
	"log"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
)

// setupTranslations 补齐 Fyne 内置控件缺失的 zh-Hans 词条，消除启动日志噪音。
//
// 背景：Fyne 的 Select 控件占位符走 lang.L("(Select one)")（fyne.io/fyne/v2/widget/select.go），
// 内置 zh-Hans 语言包没有该词条，每创建一个 Select 就打一条
//
//	Fyne error: Translation failure
//	  Cause: message "(Select one)" not found in language "zh-Hans"
//
// 到 stderr（门禁与真机日志都会出现）。这里只补框架内置字符串，不改任何业务文案。
func setupTranslations() {
	const zhHansBundle = `{"(Select one)":"请选择"}`
	if err := lang.AddTranslationsForLocale([]byte(zhHansBundle), fyne.Locale("zh-Hans")); err != nil {
		log.Printf("加载 zh-Hans 补充翻译失败（不影响主功能）: %v", err)
	}
}
