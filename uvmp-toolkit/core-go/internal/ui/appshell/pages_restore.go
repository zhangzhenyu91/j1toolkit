package appshell

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2/widget"
)

// 参数恢复只修改页面输入，不自动再次执行；数字经 JSON 快照后按 float64 处理。
func restoreString(params map[string]any, key string) string {
	if value, ok := params[key].(string); ok {
		return value
	}
	return ""
}

func restoreBool(params map[string]any, key string, fallback bool) bool {
	if value, ok := params[key].(bool); ok {
		return value
	}
	return fallback
}

func restoreInt(params map[string]any, key string, fallback int) int {
	switch value := params[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

func (p *pagePcd) RestoreParams(params map[string]any) {
	codes := toCodeList(params["codes"])
	batch := restoreString(params, "batch_name")
	if batch == "" {
		batch = "派车单"
	}
	p.applyPreview(map[string]any{"codes": params["codes"]}, batch, "（历史参数回填）")
	p.outdir.SetText(restoreString(params, "outdir"))
	p.optBatch.SetChecked(restoreBool(params, "batch_subdir", true))
	p.optPcd.SetChecked(restoreBool(params, "export_pcd", true))
	p.optTrack.SetChecked(restoreBool(params, "export_track", true))
	p.optCalib.SetChecked(restoreBool(params, "calibrate_noon", false))
	p.result.Begin(fmt.Sprintf("已回填 %d 个单号，确认参数后点击开始导出", len(codes)))
	p.result.Apply(taskOutcome{Status: "待执行", Summary: "参数已恢复，尚未再次执行。"})
}

func (p *pageDaily) RestoreParams(params map[string]any) {
	p.runDate.SetText(restoreString(params, "date"))
	p.result.Apply(taskOutcome{Status: "待执行", Summary: "执行日期已回填，确认后点击立即执行。"})
}

func (p *pageSync) RestoreParams(params map[string]any) {
	nightly := restoreBool(params, "nightly", false)
	if nightly {
		p.runBtn.Importance = widget.LowImportance
		p.nightlyBtn.Importance = widget.HighImportance
		p.result.Apply(taskOutcome{Status: "待执行", Summary: "已选择夜间兜底，确认后点击对应执行按钮。"})
		return
	}
	p.runBtn.Importance = widget.HighImportance
	p.nightlyBtn.Importance = widget.LowImportance
	p.result.Apply(taskOutcome{Status: "待执行", Summary: "已选择按 U 盘指令执行，确认后点击对应执行按钮。"})
}

func (p *pageCourse) RestoreParams(params map[string]any) {
	p.scanOnly.SetChecked(restoreBool(params, "scan_only", false))
	p.maxTry.SetText(strconv.Itoa(restoreInt(params, "max_attempts", 3)))
	p.timeout.SetText(strconv.Itoa(restoreInt(params, "timeout", 120)))
	p.result.Apply(taskOutcome{Status: "待执行", Summary: "执行选项已回填，确认后点击开始学习。"})
}
