package appshell

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageQuiz 题库搜题：题库管理 + 搜题自测 + 扫描控制（A/B 双框 OCR 实时搜题）。
// OCR 截屏/悬浮窗由后端扫描服务（quizsearch.Scanner）承担，本页接线启停、
// 显隐/布局重置、interval/threshold/opacity、题图诊断、引擎自检，
// 并订阅 scan/engine/boxHidden 事件实时刷新状态。
type pageQuiz struct {
	shell *Shell
	root  fyne.CanvasObject

	bankList   *fyne.Container
	bankHint   *widget.Label
	bankCount  *widget.Label
	testText   *widget.Entry
	testResult *fyne.Container

	// ③ 扫描控制
	scanBtn      *widget.Button
	scanStatus   *widget.Label
	scanDiag     *widget.Label
	boxACheck    *widget.Check
	boxBCheck    *widget.Check
	intervalSl   *widget.Slider
	intervalVal  *widget.Label
	thresholdSl  *widget.Slider
	thresholdVal *widget.Label
	opacitySl    *widget.Slider
	opacityVal   *widget.Label
	debugBtn     *widget.Button
	selfBtn      *widget.Button
	selfResult   *widget.Label

	optTimer *time.Timer // 选项防抖落盘
	updating bool        // 程序化回填控件时抑制回调
	lastScan ui.QuizScanState
	pollStop chan struct{} // 扫描期诊断轮询停止口（仅 UI 线程读写）
}

func newPageQuiz(s *Shell) *pageQuiz {
	p := &pageQuiz{shell: s}
	// 构建期抑制控件回调（SetChecked 等会触发回调，异步回填不得与控件构造竞态）
	p.updating = true
	defer func() { p.updating = false }()

	importBtn := widget.NewButton("导入 Excel 题库…", p.importBank)
	importBtn.Importance = widget.HighImportance
	tplBtn := widget.NewButton("下载模板", p.saveTemplate)
	p.bankHint = muted("支持 xls/xlsx，表头：题干、答案、选项A~F（可选：题型、解析）。导入后题库保存在本机（各启用库合并搜题），不再依赖原 Excel 文件。")
	p.bankCount = muted("")
	p.bankList = container.NewVBox()
	card1 := newCard("① 题库管理",
		hrow(tplBtn, importBtn),
		p.bankHint,
		p.bankList,
		p.bankCount,
	)

	p.testText = widget.NewEntry()
	p.testText.SetPlaceHolder("粘贴一段题干文字，验证题库是否可用")
	p.testText.OnSubmitted = func(string) { p.testMatch() }
	matchBtn := widget.NewButton("测试匹配", p.testMatch)
	matchBtn.Importance = widget.HighImportance
	p.testResult = container.NewVBox()
	card2 := newCard("② 搜题自测",
		hrowGrow(p.testText, matchBtn),
		p.testResult,
	)

	// ③ 扫描控制（真实扫描：开始/停止 + A/B 显隐 + 布局/选项 + 诊断）
	p.scanBtn = widget.NewButton("开始搜题", p.toggleScan)
	p.scanBtn.Importance = widget.HighImportance
	p.scanBtn.Disable() // 无启用题库不可开搜（状态回填后按题库数放行）
	p.scanStatus = muted("已停止")
	p.scanDiag = muted("开始搜题后：拖动扫描框覆盖题目区域，答案在结果窗直出；诊断信息（引擎/截屏/各框摘要）显示在此。")
	p.scanDiag.Wrapping = fyne.TextWrapWord

	p.boxACheck = widget.NewCheck("扫描框 A", func(on bool) { p.setBoxVisible("A", on) })
	p.boxACheck.SetChecked(true)
	p.boxBCheck = widget.NewCheck("扫描框 B", func(on bool) { p.setBoxVisible("B", on) })
	p.boxBCheck.SetChecked(true)
	resetBtn := widget.NewButton("重置布局", p.resetBoxes)

	p.intervalSl = widget.NewSlider(500, 3000)
	p.intervalSl.Step = 50
	p.intervalSl.Value = 1000
	p.intervalVal = muted("间隔 1000ms")
	p.intervalSl.OnChanged = func(v float64) {
		p.intervalVal.SetText(fmt.Sprintf("间隔 %dms", int(v)))
		p.scheduleOptionsApply()
	}

	p.thresholdSl = widget.NewSlider(0.3, 0.9)
	p.thresholdSl.Step = 0.05
	p.thresholdSl.Value = 0.6
	p.thresholdVal = muted("阈值 0.60")
	p.thresholdSl.OnChanged = func(v float64) {
		p.thresholdVal.SetText(fmt.Sprintf("阈值 %.2f", v))
		p.scheduleOptionsApply()
	}

	p.opacitySl = widget.NewSlider(0.3, 1)
	p.opacitySl.Step = 0.05
	p.opacitySl.Value = 0.9
	p.opacityVal = muted("透明度 0.90")
	p.opacitySl.OnChanged = func(v float64) {
		p.opacityVal.SetText(fmt.Sprintf("透明度 %.2f", v))
		p.scheduleOptionsApply()
	}

	p.debugBtn = widget.NewButton("保存题图", p.debugCapture)
	p.selfBtn = widget.NewButton("引擎自检", p.runSelftest)
	p.selfResult = muted("")

	card3 := newCard("③ 搜题控制",
		hrow(p.scanBtn, p.scanStatus),
		p.scanDiag,
		hrow(p.boxACheck, p.boxBCheck, resetBtn),
		hrow(p.intervalVal, p.thresholdVal, p.opacityVal),
		hrowGrow(p.intervalSl),
		hrowGrow(p.thresholdSl),
		hrowGrow(p.opacitySl),
		hrow(p.debugBtn, p.selfBtn),
		p.selfResult,
	)

	p.root = pageScroll(card1, card2, card3)
	go p.watchEvents()
	return p
}

func (p *pageQuiz) Object() fyne.CanvasObject { return p.root }

func (p *pageQuiz) OnShow() {
	p.reloadBanks()
	p.reloadScanState()
}

// ---------------------------------------------------------------- 题库管理（①）

func (p *pageQuiz) reloadBanks() {
	p.shell.Async(func() error {
		banks, err := p.shell.Backend.QuizBanks(context.Background())
		if err != nil {
			return err
		}
		count := 0
		for _, b := range banks {
			if b.Enabled {
				count += b.Count
			}
		}
		runUI(func() {
			p.bankList.Objects = nil
			if len(banks) == 0 {
				p.bankList.Add(statusBar("info", "尚未导入题库——搜题前请先导入至少一个题库。"))
			}
			for _, b := range banks {
				p.bankList.Add(p.bankRow(b))
			}
			p.bankList.Refresh()
			if len(banks) > 0 {
				p.bankCount.SetText(fmt.Sprintf("启用题库合计 %d 题参与搜题。", count))
			} else {
				p.bankCount.SetText("")
			}
			// 题库变化联动「开始搜题」可用态（无启用题库不可开搜）
			p.reloadScanState()
		})
		return nil
	})
}

func (p *pageQuiz) bankRow(b ui.QuizBank) fyne.CanvasObject {
	enable := widget.NewCheck("", func(on bool) {
		p.shell.Async(func() error {
			err := p.shell.Backend.QuizSetBankEnabled(context.Background(), b.ID, on)
			if err == nil {
				p.reloadBanks()
			}
			return err
		})
	})
	enable.SetChecked(b.Enabled)
	name := widget.NewLabel(b.Name)
	name.TextStyle = fyne.TextStyle{Bold: true}
	count := muted(fmt.Sprintf("%d 题 · %s", b.Count, b.ImportedAt.Format("2006-01-02 15:04")))
	renameBtn := widget.NewButton("重命名", func() {
		PromptText(p.shell.Win, "重命名题库", b.Name, func(newName string) {
			if newName == "" {
				return
			}
			p.shell.Async(func() error {
				err := p.shell.Backend.QuizRenameBank(context.Background(), b.ID, newName)
				if err == nil {
					p.reloadBanks()
				}
				return err
			})
		})
	})
	renameBtn.Importance = widget.LowImportance
	delBtn := widget.NewButton("删除", func() {
		ConfirmDanger(p.shell.Win, "删除题库", "确定删除该题库？", "删除", func() {
			p.shell.Async(func() error {
				err := p.shell.Backend.QuizRemoveBank(context.Background(), b.ID)
				if err == nil {
					p.shell.Toast("已删除", ToastSuccess)
					p.reloadBanks()
				}
				return err
			})
		})
	})
	delBtn.Importance = widget.DangerImportance
	left := container.NewVBox(name, count)
	right := container.NewHBox(enable, renameBtn, delBtn)
	row := container.NewBorder(nil, nil, left, right)
	bg := container.NewPadded(row)
	return bg
}

func (p *pageQuiz) importBank() {
	PickFile(p.shell.Win, "导入 Excel 题库", []string{".xlsx", ".xls"}, func(path string) {
		if path == "" {
			return
		}
		p.shell.Async(func() error {
			b, err := p.shell.Backend.QuizImportBank(context.Background(), path)
			if err != nil {
				return fmt.Errorf("导入失败：%w", err)
			}
			p.shell.Toast(fmt.Sprintf("已导入「%s」%d 题", b.Name, b.Count), ToastSuccess)
			p.reloadBanks()
			return nil
		})
	})
}

func (p *pageQuiz) saveTemplate() {
	PickDirectory(p.shell.Win, "选择模板保存目录", "", func(dir string) {
		if dir == "" {
			return
		}
		path := dir + "/题库模板.xlsx"
		p.shell.Async(func() error {
			err := p.shell.Backend.QuizSaveTemplate(context.Background(), path)
			if err == nil {
				p.shell.Toast("模板已保存："+path, ToastSuccess)
			}
			return err
		})
	})
}

// ---------------------------------------------------------------- 搜题自测（②）

func (p *pageQuiz) testMatch() {
	text := p.testText.Text
	if text == "" {
		p.shell.Toast("请先粘贴题干文字", ToastWarning)
		return
	}
	p.shell.Async(func() error {
		m, err := p.shell.Backend.QuizMatch(context.Background(), text)
		if err != nil {
			return err
		}
		runUI(func() {
			p.testResult.Objects = nil
			if m.Matched {
				ans := widget.NewLabel(m.Question.Answer)
				ans.TextStyle = fyne.TextStyle{Bold: true}
				ans.Importance = widget.HighImportance
				p.testResult.Add(container.NewHBox(ans, widget.NewLabel(m.Question.Content)))
				meta := m.Question.BankName
				if meta != "" {
					meta += " · "
				}
				p.testResult.Add(muted(fmt.Sprintf("%s匹配度 %d%%", meta, int(m.Score*100))))
				if m.Question.Analysis != "" {
					p.testResult.Add(muted("解析：" + m.Question.Analysis))
				}
			} else {
				row := container.NewHBox()
				warn := widget.NewLabel("未匹配到题目")
				warn.Importance = widget.WarningImportance
				row.Add(warn)
				if m.Near != nil {
					near := m.Near.Content
					if len([]rune(near)) > 40 {
						near = string([]rune(near)[:40]) + "…"
					}
					row.Add(muted("最接近：" + near))
				}
				p.testResult.Add(row)
			}
			p.testResult.Refresh()
		})
		return nil
	})
}

// ---------------------------------------------------------------- 扫描控制（③）

// toggleScan 开始/停止搜题（需已有题库；开始后创建 A/B 扫描框与结果窗）。
func (p *pageQuiz) toggleScan() {
	if p.lastScan.Scanning {
		p.shell.Async(func() error {
			st, err := p.shell.Backend.QuizScanStop(context.Background())
			if err != nil {
				return err
			}
			runUI(func() { p.applyScanState(st) })
			return nil
		})
		return
	}
	p.shell.Async(func() error {
		st, err := p.shell.Backend.QuizScanStart(context.Background())
		if err != nil {
			return fmt.Errorf("开始搜题失败：%w", err)
		}
		runUI(func() {
			p.applyScanState(st)
			p.shell.Toast("已开始搜题：拖动扫描框覆盖题目区域，答案在结果窗直出", ToastSuccess)
		})
		return nil
	})
}

func (p *pageQuiz) setBoxVisible(box string, on bool) {
	if p.updating {
		return
	}
	p.shell.Async(func() error {
		st, err := p.shell.Backend.QuizSetBoxVisible(context.Background(), box, on)
		if err != nil {
			return err
		}
		runUI(func() { p.applyScanState(st) })
		return nil
	})
}

func (p *pageQuiz) resetBoxes() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.QuizResetBoxes(context.Background())
		if err != nil {
			return err
		}
		runUI(func() {
			p.applyScanState(st)
			p.shell.Toast("已恢复双框默认布局", ToastSuccess)
		})
		return nil
	})
}

// scheduleOptionsApply 选项滑条防抖落盘（400ms 静默后整组提交，钳制在核心侧）。
func (p *pageQuiz) scheduleOptionsApply() {
	if p.updating {
		return
	}
	if p.optTimer != nil {
		p.optTimer.Stop()
	}
	p.optTimer = time.AfterFunc(400*time.Millisecond, func() {
		runUI(func() {
			interval := int(p.intervalSl.Value)
			threshold := p.thresholdSl.Value
			opacity := p.opacitySl.Value
			p.shell.Async(func() error {
				_, err := p.shell.Backend.QuizSetOptions(context.Background(), ui.QuizScanOptions{
					Interval:  &interval,
					Threshold: &threshold,
					Opacity:   &opacity,
				})
				return err
			})
		})
	})
}

// debugCapture 把扫描框当前所见画面存成 PNG（排查「截屏黑/空」「OCR 失败」「匹配未中」）。
func (p *pageQuiz) debugCapture() {
	p.shell.Async(func() error {
		file, err := p.shell.Backend.QuizDebugCapture(context.Background(), "")
		if err != nil {
			return err
		}
		p.shell.Toast("题图已保存："+file, ToastSuccess)
		// 保存后在文件管理器定位（与桌面壳 showItem 同交互）
		if err := p.shell.Backend.ShowItem(context.Background(), file); err != nil {
			return err
		}
		return nil
	})
}

// runSelftest 引擎自检（模型文件齐备性 + 引擎细节）。
func (p *pageQuiz) runSelftest() {
	p.selfResult.SetText("自检运行中…")
	p.shell.Async(func() error {
		r, err := p.shell.Backend.QuizSelftest(context.Background())
		if err != nil {
			runUI(func() { p.selfResult.SetText("自检失败：" + err.Error()) })
			return err
		}
		runUI(func() {
			text := "自检通过（模型齐备）"
			if !r.OK {
				text = "自检未通过"
				if len(r.Missing) > 0 {
					text += "，缺少：" + fmt.Sprint(r.Missing)
				}
			}
			if v, ok := r.Engine["ready"]; ok {
				text += fmt.Sprintf("，引擎就绪=%v", v)
			}
			if v, ok := r.Engine["ort_version"]; ok {
				text += "，ORT=" + fmt.Sprint(v)
			}
			p.selfResult.SetText(text)
		})
		return nil
	})
}

// reloadScanState 拉取扫描状态并回填控件（切回页面/启停后）。
func (p *pageQuiz) reloadScanState() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.QuizScanState(context.Background())
		if err != nil {
			runUI(func() { p.scanDiag.SetText("扫描服务不可用：" + err.Error()) })
			return nil // 状态不可用不算页面错误（提示已内联展示）
		}
		runUI(func() { p.applyScanState(st) })
		return nil
	})
}

// applyScanState 状态回填（UI 线程）。
func (p *pageQuiz) applyScanState(st ui.QuizScanState) {
	p.updating = true
	defer func() { p.updating = false }()
	p.lastScan = st

	p.updateScanBtn()
	if st.BoxVisible != nil {
		p.boxACheck.SetChecked(st.BoxVisible["A"])
		p.boxBCheck.SetChecked(st.BoxVisible["B"])
	}
	p.intervalSl.SetValue(float64(st.Interval))
	p.thresholdSl.SetValue(st.Threshold)
	p.opacitySl.SetValue(st.Opacity)
	p.intervalVal.SetText(fmt.Sprintf("间隔 %dms", st.Interval))
	p.thresholdVal.SetText(fmt.Sprintf("阈值 %.2f", st.Threshold))
	p.opacityVal.SetText(fmt.Sprintf("透明度 %.2f", st.Opacity))

	p.scanDiag.SetText(scanDiagText(st))
	p.syncPolling()
}

// updateScanBtn 按扫描态/启用题库数刷新启停按钮（无启用题库不可开搜，对齐桌面壳）。
func (p *pageQuiz) updateScanBtn() {
	if p.lastScan.Scanning {
		p.scanBtn.SetText("停止搜题")
		p.scanBtn.Enable()
		p.scanStatus.SetText("搜题中（主窗口已隐藏，框选区域对准题目）")
	} else {
		p.scanBtn.SetText("开始搜题")
		p.scanStatus.SetText("已停止")
		if p.lastScan.QuestionCount > 0 {
			p.scanBtn.Enable()
		} else {
			p.scanBtn.Disable()
		}
	}
}

// syncPolling 扫描期间每 2s 拉一次状态喂运行诊断区（对齐桌面壳轮询）；
// 停止扫描即收口轮询。仅 UI 线程调用。
func (p *pageQuiz) syncPolling() {
	if p.lastScan.Scanning {
		if p.pollStop == nil {
			stop := make(chan struct{})
			p.pollStop = stop
			go func() {
				t := time.NewTicker(2 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-stop:
						return
					case <-t.C:
						p.reloadScanState()
					}
				}
			}()
		}
	} else if p.pollStop != nil {
		close(p.pollStop)
		p.pollStop = nil
	}
}

// scanDiagText 运行诊断文案（captureError=截屏层 / 各框摘要=OCR 层 / engineError=引擎层）。
func scanDiagText(st ui.QuizScanState) string {
	out := fmt.Sprintf("引擎：%s", engineStatusText(st))
	if st.CaptureError != "" {
		out += "；截屏：" + st.CaptureError
	} else if st.Scanning {
		out += "；截屏：正常"
	}
	if st.OcrMs > 0 {
		out += fmt.Sprintf("；OCR %dms", st.OcrMs)
	}
	out += fmt.Sprintf("；启用题库 %d 题", st.QuestionCount)
	if st.SessionType != "" {
		out += "；会话 " + st.SessionType
	}
	for _, box := range []string{"A", "B"} {
		d := st.BoxDebug[box]
		if d == nil {
			out += fmt.Sprintf("；框%s 尚未出帧", box)
			continue
		}
		switch {
		case d.Matched:
			out += fmt.Sprintf("；框%s 已命中", box)
		case d.Snippet != "":
			out += fmt.Sprintf("；框%s 未命中（识别：%s）", box, d.Snippet)
		default:
			out += fmt.Sprintf("；框%s 未命中（未识别到文字：框内无字或截屏为空）", box)
		}
	}
	return out
}

func engineStatusText(st ui.QuizScanState) string {
	switch {
	case st.EngineError != "":
		return "异常（" + st.EngineError + "）"
	case st.Scanning && st.EngineReady:
		return "就绪"
	case st.Scanning:
		return "加载中"
	default:
		return "未启动"
	}
}

// watchEvents 订阅扫描页事件（scan/engine/boxHidden）并实时刷新状态区。
func (p *pageQuiz) watchEvents() {
	sub, err := p.shell.Backend.QuizSubscribe(context.Background())
	if err != nil {
		runUI(func() { p.scanDiag.SetText("事件订阅失败：" + err.Error()) })
		return
	}
	defer sub.Close()
	for ev := range sub.Events() {
		ev := ev
		runUI(func() { p.onQuizEvent(ev) })
	}
}

func (p *pageQuiz) onQuizEvent(ev ui.QuizEvent) {
	switch ev.Type {
	case ui.QuizEventScan:
		p.reloadScanState()
	case ui.QuizEventEngine:
		if !ev.Ready {
			p.shell.Toast("OCR 引擎异常："+ev.Error, ToastError)
		}
		p.reloadScanState()
	case ui.QuizEventBoxHidden:
		p.shell.Toast("扫描框 "+ev.Box+" 已隐藏（可勾选重新显示）", ToastInfo)
		p.reloadScanState()
	}
}
