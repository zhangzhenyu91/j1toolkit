package appshell

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pagePcd 轨迹导出（pcd_export）：派车单号清单 / 输出设置 / 执行 / 执行记录。
// 终态结果摘要与产物目录入口常驻，运行日志收进「运行日志」折叠（失败自动展开）。
type pagePcd struct {
	shell *Shell
	root  fyne.CanvasObject

	fileName   *widget.Label
	codesText  *widget.Entry
	codesView  *LogView
	codesCount *widget.Label
	outdir     *widget.Entry
	optBatch   *enterpriseCheck
	optPcd     *enterpriseCheck
	optTrack   *enterpriseCheck
	optCalib   *enterpriseCheck
	startBtn   *widget.Button
	startHint  *widget.Label
	cancelBtn  *widget.Button
	progress   *progressRow
	log        *LogView // 结果区运行日志（历史用例/外部断言共用）
	result     *taskResult
	history    *HistoryList

	codes      []map[string]any
	batchName  string
	currentJob *ui.JobHandle
}

func newPagePcd(s *Shell) *pagePcd {
	p := &pagePcd{shell: s, batchName: "派车单"}

	p.fileName = muted("未选择文件")
	pickBtn := newMotionButton("选择 xlsx 文件…", p.pickXlsx)
	pickBtn.Importance = widget.LowImportance
	p.codesText = widget.NewMultiLineEntry()
	p.codesText.SetPlaceHolder("也可以直接粘贴派车单号，每行一个（或用逗号/空格分隔）")
	p.codesText.Wrapping = fyne.TextWrapOff
	parseBtn := newMotionButton("解析粘贴的单号", p.parseText)
	parseBtn.Importance = widget.LowImportance
	p.codesCount = hbLabel("", widget.LowImportance)
	p.codesView = NewLogView()
	p.codesView.SetMinHeight(110) // 单号预览区按需压缩，避免解析后留大片空白
	card1 := newCard("① 派车单号清单",
		hrowGrow(p.fileName, pickBtn),
		muted("或直接粘贴派车单号后解析："),
		p.codesText,
		hrow(parseBtn, p.codesCount),
		p.codesView.Root,
	)

	p.outdir = widget.NewEntry()
	p.outdir.SetPlaceHolder("选择或输入目录路径")
	browseBtn := newMotionButton("浏览…", p.browseOutdir)
	browseBtn.Importance = widget.LowImportance
	p.optBatch = newEnterpriseCheck("建批次子目录（防覆盖）", nil)
	p.optBatch.SetChecked(true)
	p.optPcd = newEnterpriseCheck("导出派车单 PDF", nil)
	p.optPcd.SetChecked(true)
	p.optTrack = newEnterpriseCheck("导出轨迹 PDF + 轨迹点 CSV", nil)
	p.optTrack.SetChecked(true)
	p.optCalib = newEnterpriseCheck("自动校准行程结束时间", nil)
	card2 := newCard("② 输出设置",
		hrow(hbLabel("输出目录", widget.LowImportance), newMinBox(fyne.NewSize(320, 0), p.outdir), browseBtn),
		hrow(p.optBatch, p.optPcd),
		hrow(p.optTrack, p.optCalib),
		muted("逐单文件在「逐单/」子目录；全部导出后按序号合并出 _合并_派车单.pdf / _合并_轨迹.pdf；批次内附 _清单.csv、_12点前结束行程.xlsx 与 _未找到.txt；同批次重跑自动跳过已导出单据。勾选「自动校准行程结束时间」后：行程结束时间早于 12 点的轨迹按 1 小时逐次叠加校准至 12 点后，_12点前结束行程.xlsx 附「校准后结束时间/校准后行驶时长」两列。"),
	)

	p.startBtn = newMotionButtonWithIcon("开始导出", nil, p.startRun)
	p.startBtn.Importance = widget.HighImportance
	p.startBtn.Disable() // 未解析到单号前不可导出（预览后按解析结果放行）
	p.startHint = muted("请先选择 xlsx 文件或粘贴单号并解析")
	p.cancelBtn = newMotionButtonWithIcon("取消导出", nil, p.cancel)
	p.cancelBtn.Importance = widget.DangerImportance
	p.cancelBtn.Hide()
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s))
	p.log = p.result.log
	card3 := newCard("③ 执行",
		hrow(p.startBtn, p.cancelBtn),
		p.startHint,
		p.progress.Root,
		p.result.Root,
	)

	p.history = NewHistoryList("最近执行记录", true, func(r map[string]any) string {
		return ui.PcdSummary(ui.PcdResultFrom(r))
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnOpenDir = func(rec ui.HistoryRecord) {
		if rec.Dir != "" {
			s.Async(func() error { return s.Backend.OpenPath(context.Background(), rec.Dir) })
		}
	}
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), "pcd_export", rec.JobID)
					if err == nil {
						p.reloadHistory()
						s.Toast("已删除", ToastSuccess)
					}
					return err
				})
			})
	}
	p.history.OnClear = func() {
		ConfirmDanger(s.Win, "清空执行记录",
			"将删除该应用全部执行记录及其任务日志。确定继续？", "清空", func() {
				s.Async(func() error {
					r, err := s.Backend.DeleteHistory(context.Background(), "pcd_export", "")
					if err == nil {
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	p.root = pageScroll(card1, card2, card3, p.result.LogRoot, p.history.Root)
	return p
}

func (p *pagePcd) Object() fyne.CanvasObject { return p.root }

func (p *pagePcd) OnShow() {
	// 输出目录建议只在首次进页填充，切回不覆盖用户输入
	if p.outdir.Text == "" {
		p.shell.Async(func() error {
			info, err := p.shell.Backend.SystemInfo(context.Background())
			if err == nil && len(info.SuggestedOutDirs) > 0 {
				runUI(func() { p.outdir.SetText(info.SuggestedOutDirs[0]) })
			}
			return nil
		})
	}
	p.reloadHistory()
}

func (p *pagePcd) reloadHistory() {
	reloadHistory(p.shell, "pcd_export", p.history)
}

func (p *pagePcd) pickXlsx() {
	PickFile(p.shell.Win, "选择 Excel 文件", []string{".xlsx"}, func(path string) {
		if path == "" {
			return
		}
		p.loadXlsx(path)
	})
}

// loadXlsx 解析 xlsx 单号清单并回填（批次名取文件名去 .xlsx 后缀）。
func (p *pagePcd) loadXlsx(path string) {
	p.shell.Async(func() error {
		r, err := p.shell.Backend.Preview(context.Background(), "pcd_export", map[string]any{"xlsx_path": path})
		if err != nil {
			return fmt.Errorf("解析失败：%w", err)
		}
		// 批次名取文件名去 .xlsx 后缀（与桌面壳批次口径一致）；旧粘贴文本清掉防混淆
		name := baseName(path)
		runUI(func() { p.codesText.SetText("") })
		p.applyPreview(r, strings.TrimSuffix(name, filepath.Ext(name)), name)
		return nil
	})
}

func (p *pagePcd) parseText() {
	text := strings.TrimSpace(p.codesText.Text)
	if text == "" {
		p.shell.Toast("请先粘贴派车单号", ToastWarning)
		return
	}
	p.shell.Async(func() error {
		r, err := p.shell.Backend.Preview(context.Background(), "pcd_export", map[string]any{"codes_text": text})
		if err != nil {
			return fmt.Errorf("解析失败：%w", err)
		}
		p.applyPreview(r, "粘贴文本", "（粘贴文本）")
		return nil
	})
}

// applyPreview 回填解析结果：batchName 进任务批次参数，displayName 仅展示。
func (p *pagePcd) applyPreview(r map[string]any, batchName, displayName string) {
	codes := toCodeList(r["codes"])
	runUI(func() {
		p.codes = codes
		p.batchName = batchName
		p.fileName.SetText(displayName)
		view := ""
		for i, c := range codes {
			if i >= 100 {
				break
			}
			line := fmt.Sprintf("%d. %s", c["seq"], c["code"])
			if dh, _ := c["date_hint"].(string); dh != "" {
				line += "　（" + dh + "）"
			}
			view += line + "\n"
		}
		p.codesView.SetText(view)
		if len(codes) == 0 {
			p.codesCount.SetText("未解析到有效单号")
			p.startBtn.Disable()
			p.startHint.Show()
		} else {
			extra := ""
			if len(codes) > 100 {
				extra = "（仅预览前 100 个）"
			}
			p.codesCount.SetText(fmt.Sprintf("共 %d 个单号%s", len(codes), extra))
			p.startBtn.Enable()
			p.startHint.Hide()
		}
	})
}

func (p *pagePcd) browseOutdir() {
	PickDirectory(p.shell.Win, "选择输出目录", p.outdir.Text, func(path string) {
		if path != "" {
			p.outdir.SetText(path)
		}
	})
}

func (p *pagePcd) startRun() {
	outdir := strings.TrimSpace(p.outdir.Text)
	if len(p.codes) == 0 {
		p.shell.Toast("请先选择 xlsx 文件或粘贴单号并解析", ToastWarning)
		return
	}
	if outdir == "" {
		p.shell.Toast("请填写输出目录", ToastWarning)
		return
	}
	if !p.optPcd.Checked && !p.optTrack.Checked {
		p.shell.Toast("派车单 / 轨迹至少勾选一项", ToastWarning)
		return
	}
	params := map[string]any{
		"codes":          p.codes,
		"outdir":         outdir,
		"batch_name":     p.batchName,
		"batch_subdir":   p.optBatch.Checked,
		"export_pcd":     p.optPcd.Checked,
		"export_track":   p.optTrack.Checked,
		"calibrate_noon": p.optCalib.Checked,
	}
	p.shell.Async(func() error {
		ok, err := ssoReady(p.shell)
		if err == nil && !ok {
			ShowToast(p.shell.Win, "尚未配置 SSO 账号密码——请先到「设置」页填写（或开启开发模式试用）", ToastWarning)
			return nil
		}
		runUI(func() {
			p.result.Begin(fmt.Sprintf("正在导出 %d 个派车单，请稍候…", len(p.codes)))
			p.progress.Reset()
			p.startBtn.Disable()
			p.cancelBtn.Show()
			var h *ui.JobHandle
			h = p.shell.StartJob("pcd_export", params, ui.JobHandler{
				OnLog: func(line string) {
					if p.currentJob != h {
						return // 过期任务日志：不污染新一轮
					}
					p.result.Log(line)
				},
				OnProgress: func(pr ui.JobProgress) {
					if p.currentJob != h {
						return
					}
					setProgress(p.progress, pr)
				},
				OnDone: func(job ui.Job) {
					if p.currentJob != h {
						return // 过期任务终态：不覆盖新一轮状态
					}
					p.currentJob = nil
					p.cancelBtn.Hide()
					// 终态保留本次摘要与产物入口（不再只剩执行记录）；成功后清理本批输入与临时日志，
					// 避免下一批混淆，完整日志仍可在执行记录中查看。失败/取消保留现场便于排查与重试。
					p.result.Apply(pcdOutcome(job))
					if job.Status == ui.JobSuccess && job.Error == "" {
						p.codes = nil
						p.batchName = "派车单"
						p.codesText.SetText("")
						p.codesView.Clear()
						p.codesCount.SetText("")
						p.fileName.SetText("未选择文件")
						p.result.log.Clear()
						p.startBtn.Disable()
					} else if len(p.codes) > 0 {
						p.startBtn.Enable()
					}
					p.reloadHistory()
				},
				OnError: func(err error) {
					if p.currentJob != h {
						return
					}
					p.currentJob = nil
					p.startBtn.Enable()
					p.cancelBtn.Hide()
					p.result.FailStart(err)
				},
			})
			p.currentJob = h
		})
		return nil
	})
}

func (p *pagePcd) cancel() {
	if p.currentJob != nil {
		p.currentJob.Cancel()
	}
}

// toCodeList 解析 preview.codes 为序号/单号/日期提示列表。
func toCodeList(v any) []map[string]any {
	out := []map[string]any{}
	switch list := v.(type) {
	case []any:
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// baseName 取路径文件名。
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
