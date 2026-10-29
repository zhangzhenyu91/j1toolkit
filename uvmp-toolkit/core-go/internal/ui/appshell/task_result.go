package appshell

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 本文件：五个业务页共用的「运行状态 + 完成摘要 + 产物入口 + 折叠运行日志」结果区，
// 以及把任务 result 字典翻成用户可读结论的纯函数（便于 fake 测试）。
// 技术日志默认收起，运行状态/进度/最终结果优先；失败时自动展开运行日志。

// taskArtifact 任务产物入口（目录或文件，Path 为空则不显示按钮）。
type taskArtifact struct {
	Label string
	Path  string
}

// taskOutcome 任务终态展示契约（纯数据，页面与测试共用）。
type taskOutcome struct {
	Status   string // 状态标题：成功 / 失败 / 已取消 / 已暂停 / 启动失败
	Summary  string // 完成摘要（面向用户，不含技术日志）
	Artifact taskArtifact
	Failed   bool // 是否失败（失败时自动展开运行日志）
}

// taskResult 任务结果区：状态行 + 摘要 + 产物按钮 + 「运行日志」折叠区。
type taskResult struct {
	Root        fyne.CanvasObject
	status      *widget.Label
	summary     *widget.Label
	artifactBtn *widget.Button
	logSec      *collapsibleSection
	log         *LogView

	// LogRoot 运行日志折叠卡（独立白卡，页面放在执行卡之后，避免卡片套卡片）。
	LogRoot fyne.CanvasObject

	open     func(path string)
	artifact taskArtifact
}

// newTaskResult 构造结果区；open 为打开产物的回调（页面注入 Backend.OpenPath）。
func newTaskResult(open func(path string)) *taskResult {
	t := &taskResult{open: open}

	t.status = bodyText("尚未开始")
	t.status.TextStyle = fyne.TextStyle{Bold: true}
	t.status.Importance = widget.LowImportance

	t.summary = bodyText("")
	t.summary.Hide()

	t.artifactBtn = widget.NewButtonWithIcon("打开产物目录", theme.FolderOpenIcon(), t.openArtifact)
	t.artifactBtn.Importance = widget.LowImportance
	t.artifactBtn.Hide()

	t.log = NewLogView()
	t.logSec = newCollapsibleSection("运行日志", t.log.Root, false)
	t.LogRoot = t.logSec.Root

	t.Root = container.NewVBox(
		hrowGrow(t.status, t.artifactBtn),
		t.summary,
	)
	return t
}

// Begin 进入新一轮执行：清空上一次结果与日志，日志折叠收起。
func (t *taskResult) Begin(summary string) {
	runUI(func() {
		t.artifact = taskArtifact{}
		t.artifactBtn.Hide()
		t.log.Clear()
		t.logSec.SetExpanded(false)
		t.setStatus("执行中", widget.HighImportance)
		t.summary.SetText(summary)
		t.summary.Show()
	})
}

// Apply 回填任务终态。
func (t *taskResult) Apply(o taskOutcome) {
	runUI(func() {
		imp := widget.MediumImportance
		switch o.Status {
		case "成功", "完成":
			imp = widget.SuccessImportance
		case "失败", "启动失败":
			imp = widget.DangerImportance
		case "已取消":
			imp = widget.LowImportance
		}
		t.setStatus(o.Status, imp)
		text := o.Summary
		if text == "" {
			text = o.Status
			if o.Failed {
				text = "执行失败，请展开运行日志查看详情。"
			}
		}
		t.summary.SetText(text)
		t.summary.Show()

		t.artifact = o.Artifact
		if o.Artifact.Path != "" {
			label := o.Artifact.Label
			if label == "" {
				label = "打开产物目录"
			}
			t.artifactBtn.SetText(label)
			t.artifactBtn.Show()
		} else {
			t.artifactBtn.Hide()
		}
		// 成功/取消默认收起；失败自动展开，方便直接看到出错环节
		t.logSec.SetExpanded(o.Failed)
	})
}

// FailStart 启动阶段失败：错误同时写入运行日志并自动展开。
func (t *taskResult) FailStart(err error) {
	msg := "启动失败"
	if err != nil {
		msg += "：" + err.Error()
	}
	t.Log(msg)
	t.Apply(taskOutcome{Status: "启动失败", Summary: msg, Failed: true})
}

// Log 追加一行运行日志（折叠区内的 LogView 自身串行化）。
func (t *taskResult) Log(line string) { t.log.Append(line) }

func (t *taskResult) setStatus(text string, imp widget.Importance) {
	t.status.Importance = imp
	t.status.SetText(text)
	t.status.Refresh()
}

func (t *taskResult) openArtifact() {
	if t.artifact.Path == "" || t.open == nil {
		return
	}
	t.open(t.artifact.Path)
}

// ---------------------------------------------------------------- 结果摘要纯函数

// baseOutcome 状态标题与错误文案（各应用在其上补摘要/产物）。
func baseOutcome(job ui.Job) taskOutcome {
	o := taskOutcome{Status: job.Status.CN()}
	if job.Paused() {
		o.Status = "已暂停"
	}
	if job.Status == ui.JobFailed || (job.Error != "" && !job.Paused()) {
		o.Failed = true
	}
	if job.Error != "" {
		o.Summary = job.Error
	}
	return o
}

// dailyOutcome 派车同步：明确条数/去向，并给出产物目录入口。
func dailyOutcome(job ui.Job) taskOutcome {
	o := baseOutcome(job)
	r := ui.DailyResultFrom(job.Result)
	if job.Status != ui.JobSuccess || job.Error != "" {
		if o.Summary == "" {
			o.Summary = ui.DailySummary(r)
		}
		return o
	}
	if r.Count == 0 {
		o.Summary = "执行完成：当日无派车数据"
		return o
	}
	dest := ""
	if r.Dest != "" {
		dest = fmt.Sprintf("（写入 %s）", r.Dest)
	}
	o.Summary = fmt.Sprintf("导出完成：%d 条派车单 → %s%s", r.Count, r.File, dest)
	if r.Dir != "" {
		o.Artifact = taskArtifact{Label: "打开产物目录", Path: r.Dir}
	}
	return o
}

// syncOutcome 树障/交跨同步：说明本次动作与去向；snapshot 打开 U 盘目录，
// replace 打开内网表所在目录（备份为 .bak）。
func syncOutcome(job ui.Job, usbMount, localPath string) taskOutcome {
	o := baseOutcome(job)
	r := ui.SyncRunResultFrom(job.Result)
	if job.Status != ui.JobSuccess || job.Error != "" {
		if o.Summary == "" {
			o.Summary = ui.SyncSummary(r)
		}
		return o
	}
	switch r.Skipped {
	case "usb_absent":
		o.Status = "完成"
		o.Summary = "本次未执行：U 盘未挂载（已跳过）" + nightlySuffix(r)
		return o
	case "no_command":
		o.Status = "完成"
		o.Summary = "本次未执行：U 盘无同步指令（已跳过）" + nightlySuffix(r)
		return o
	case "unknown_kind":
		o.Status = "完成"
		o.Summary = fmt.Sprintf("本次未执行：未知指令 %q（已跳过）", r.Kind) + nightlySuffix(r)
		return o
	case "bad_file":
		o.Status = "完成"
		o.Summary = "本次未执行：指令中的文件名非法（已跳过）" + nightlySuffix(r)
		return o
	}
	file := ""
	if r.File != "" {
		file = " → " + r.File
	}
	switch r.Kind {
	case "snapshot":
		o.Summary = "同步完成：内网表已拷贝到 U 盘" + file
		if usbMount != "" {
			o.Artifact = taskArtifact{Label: "打开 U 盘目录", Path: usbMount}
		}
	case "replace":
		o.Summary = "同步完成：U 盘新表已覆盖内网表（原文件备份为 .bak）" + file
		if localPath != "" {
			o.Artifact = taskArtifact{Label: "打开内网表目录", Path: filepath.Dir(localPath)}
		}
	default:
		o.Summary = "同步完成：" + ui.SyncSummary(r)
	}
	o.Summary += nightlySuffix(r)
	return o
}

// nightlySuffix 夜间兜底标注。
func nightlySuffix(r ui.SyncRunResult) string {
	if r.Nightly {
		return "（夜间兜底）"
	}
	return ""
}

// pcdOutcome 轨迹导出：完成摘要保留在结果区，产物目录可直接打开。
func pcdOutcome(job ui.Job) taskOutcome {
	o := baseOutcome(job)
	r := ui.PcdResultFrom(job.Result)
	if job.Status != ui.JobSuccess || job.Error != "" {
		if o.Summary == "" {
			o.Summary = ui.PcdSummary(r)
		}
		return o
	}
	parts := []string{
		fmt.Sprintf("共处理 %d 单", r.Total),
		fmt.Sprintf("派车单成功 %d / 失败 %d", r.PCDOk, r.PCDFail),
		fmt.Sprintf("轨迹成功 %d / 失败 %d", r.TrackOk, r.TrackFail),
	}
	if len(r.NotFound) > 0 {
		parts = append(parts, fmt.Sprintf("未找到 %d 单", len(r.NotFound)))
	}
	o.Summary = "导出完成：" + strings.Join(parts, "；")
	if r.Dir != "" {
		o.Artifact = taskArtifact{Label: "打开产物目录", Path: r.Dir}
	}
	return o
}

// courseOutcome 学堂刷课：阶段（扫描/学习）+ 数量 + 最终结果。
func courseOutcome(job ui.Job, scanOnly bool) taskOutcome {
	o := baseOutcome(job)
	r := ui.CourseResultFrom(job.Result)
	counts := ui.CourseCountText(r)
	switch {
	case job.Paused():
		o.Status = "已暂停"
		o.Summary = "已暂停：浏览器与登录态保留，可稍后继续。" + counts
	case job.Status == ui.JobSuccess && r.Pending > 0:
		o.Summary = "本轮结束，仍有未完成课程。" + counts
	case job.Status == ui.JobSuccess:
		label := "扫描完成"
		if !scanOnly {
			label = "学习完成（全部达到 100%）"
		}
		o.Summary = label + "：" + counts
	case job.Status == ui.JobCancelled:
		o.Summary = "已取消：" + counts
	default:
		if o.Summary == "" {
			o.Summary = "执行失败：" + counts
		}
	}
	if r.Skipped > 0 && job.Status == ui.JobSuccess {
		o.Summary += fmt.Sprintf("（跳过 %d 门）", r.Skipped)
	}
	return o
}

// ---------------------------------------------------------------- 表单字段标签

// taskFieldLabel 表单字段名（加粗，与滑杆值区分）。
func taskFieldLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Bold: true}
	return l
}

// openPathVia 打开目录/文件的后台回调（统一错误转轻提示）。
func openPathVia(s *Shell) func(string) {
	return func(path string) {
		if path == "" {
			return
		}
		s.Async(func() error {
			return s.Backend.OpenPath(context.Background(), path)
		})
	}
}
