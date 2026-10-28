package render

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// jsTimeArray 把时间渲染成 [Y,M,D,h,m,s] 的 JSON 数组字面量。
func jsTimeArray(t time.Time) string {
	return fmt.Sprintf("[%d,%d,%d,%d,%d,%d]",
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// BuildPopupRewriteJS 生成「自动校准行程结束时间」的弹窗时间改写 JS。
// oldDT/newDT 为行程结束时间（改写前/后），oldDurSeconds/newDurSeconds 为行驶时长（秒，
// 行程开始→结束）。命中数随截图日志透出；未命中不视为失败（保留官方原始时间）。
// 行为与 official_track.py build_popup_rewrite_js 一致。
func BuildPopupRewriteJS(oldDT, newDT time.Time, oldDurSeconds, newDurSeconds int64) string {
	return strings.NewReplacer(
		"__OLDC__", jsTimeArray(oldDT),
		"__NEWC__", jsTimeArray(newDT),
		"__OLDDUR__", strconv.FormatInt(oldDurSeconds, 10),
		"__NEWDUR__", strconv.FormatInt(newDurSeconds, 10),
	).Replace(popupRewriteJSTpl)
}

// RewriteResult 弹窗时间改写的执行结果。
type RewriteResult struct {
	// Enabled 为 false 表示未注入改写脚本（preShotJS 为空）。
	Enabled bool
	// Replaced 改写命中处数（Enabled 且为 0 = 未命中，保留官方原始时间）。
	Replaced int
}
