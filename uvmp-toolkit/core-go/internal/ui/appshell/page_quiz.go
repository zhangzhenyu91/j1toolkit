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

// pageQuiz 题库搜题：准备题库 + 开始搜题 + 查看答案（A/B 双区域文字识别实时搜题）。
// OCR 截屏/悬浮窗由后端扫描服务（quizsearch.Scanner）承担，本页接线启停、
// 显隐/布局重置、interval/threshold/opacity、题图诊断、引擎自检，
// 并订阅 scan/engine/boxHidden 事件实时刷新状态。
// 高级识别选项与技术诊断默认收起，诊断出错时自动展开。
type pageQuiz struct {
	shell *Shell
	root  fyne.CanvasObject

	bankList   *fyne.Container
	bankScope  *appTooltipScope
	bankHint   *widget.Label
	bankCount  *widget.Label
	testText   *widget.Entry
	testResult *fyne.Container

	// 搜题控制
	scanBtn      *appButton
	scanStatus   *widget.Label
	scanDiag     *widget.Label
	optionsSec   *collapsibleSection
	diagSec      *collapsibleSection
	boxACheck    *enterpriseCheck
	boxBCheck    *enterpriseCheck
	intervalSl   *widget.Slider
	intervalVal  *widget.Label
	thresholdSl  *widget.Slider
	thresholdVal *widget.Label
	opacitySl    *widget.Slider
	opacityVal   *widget.Label
	debugBtn     *appButton
	selfBtn      *appButton
	selfResult   *widget.Label
	optTimer     *time.Timer // 选项防抖落盘
	updating     bool        // 程序化回填控件时抑制回调
	lastScan     ui.QuizScanState
	diagOpen     bool          // 诊断区是否因异常自动展开过（避免轮询每次都强制展开）
	pollStop     chan struct{} // 扫描期诊断轮询停止口（仅 UI 线程读写）

	layout    *pageSplitLayout
	cardBox   *fyne.Container
	bankCard  fyne.CanvasObject
	startCard fyne.CanvasObject
	testCard  fyne.CanvasObject
	answerSec *collapsibleSection
	maximized bool
}

func newPageQuiz(s *Shell) *pageQuiz {
	p := &pageQuiz{shell: s}
	// 构建期抑制控件回调（SetChecked 等会触发回调，异步回填不得与控件构造竞态）
	p.updating = true
	defer func() { p.updating = false }()

	importBtn := newAppButton("导入 Excel 题库…", "upload", widget.MediumImportance, p.importBank)
	tplBtn := newAppButton("下载导入模板", "download", widget.LowImportance, p.saveTemplate)
	p.bankHint = muted("支持 xls/xlsx（表头：题干、答案、选项 A～F），多个启用题库合并搜题。")
	p.bankCount = stateLabel("", widget.LowImportance)
	p.bankList = container.NewVBox()
	p.bankScope = newAppTooltipScope(p.bankList)
	card1 := newCard("准备题库",
		hrow(importBtn, tplBtn),
		p.bankHint,
		p.bankScope,
		p.bankCount,
	)

	// 开始搜题：主界面只保留启停、区域显隐与位置重置。
	p.scanBtn = newAppButton("开始搜题", "play", widget.HighImportance, p.toggleScan)
	p.scanBtn.tooltip = newAppTooltip(p.scanBtn, "开始搜题")
	p.scanBtn.Disable() // 无启用题库不可开搜（状态回填后按题库数放行）
	p.scanStatus = hbLabel("尚未开始", widget.LowImportance)
	p.boxACheck = newEnterpriseCheck("搜题区域 A", func(on bool) { p.setBoxVisible("A", on) })
	p.boxACheck.SetChecked(true)
	p.boxBCheck = newEnterpriseCheck("搜题区域 B", func(on bool) { p.setBoxVisible("B", on) })
	p.boxBCheck.SetChecked(true)
	resetBtn := newAppButton("重置区域位置", "reset", widget.LowImportance, p.resetBoxes)

	p.intervalSl = widget.NewSlider(500, 3000)
	p.intervalSl.Step = 50
	p.intervalSl.Value = 1000
	p.intervalVal = hbLabel(quizIntervalText(1000), widget.LowImportance)
	p.intervalSl.OnChanged = func(v float64) {
		p.intervalVal.SetText(quizIntervalText(int(v)))
		p.scheduleOptionsApply()
	}

	p.thresholdSl = widget.NewSlider(0.3, 0.9)
	p.thresholdSl.Step = 0.05
	p.thresholdSl.Value = 0.6
	p.thresholdVal = hbLabel(quizThresholdText(0.6), widget.LowImportance)
	p.thresholdSl.OnChanged = func(v float64) {
		p.thresholdVal.SetText(quizThresholdText(v))
		p.scheduleOptionsApply()
	}

	p.opacitySl = widget.NewSlider(0.3, 1)
	p.opacitySl.Step = 0.05
	p.opacitySl.Value = 0.9
	p.opacityVal = hbLabel(quizOpacityText(0.9), widget.LowImportance)
	p.opacitySl.OnChanged = func(v float64) {
		p.opacityVal.SetText(quizOpacityText(v))
		p.scheduleOptionsApply()
	}

	optionsBody := container.NewVBox(
		hrowGrow(taskFieldLabel("文字识别间隔（毫秒）"), p.intervalVal),
		p.intervalSl,
		hrowGrow(taskFieldLabel("匹配阈值（越高越严格）"), p.thresholdVal),
		p.thresholdSl,
		hrowGrow(taskFieldLabel("答案窗透明度"), p.opacityVal),
		p.opacitySl,
	)
	p.optionsSec = newCollapsibleSection("高级设置", optionsBody, false)

	p.scanDiag = muted("开始搜题后，这里显示文字识别引擎、截屏与搜题区域的技术摘要。")
	p.scanDiag.Wrapping = fyne.TextWrapWord
	p.debugBtn = newAppButton("保存题图", "save", widget.LowImportance, p.debugCapture)
	p.selfBtn = newAppButton("引擎自检", "flask", widget.LowImportance, p.runSelftest)
	p.selfResult = muted("")
	diagBody := container.NewVBox(
		p.scanDiag,
		hrow(p.debugBtn, p.selfBtn),
		p.selfResult,
	)
	p.diagSec = newCollapsibleSection("技术诊断（供技术支持）", diagBody, false)

	card2 := newCard("开始搜题",
		p.scanBtn,
		p.scanStatus,
		muted("开始后主窗口会隐藏，请拖动搜题区域分别框住题目。"),
		hrow(p.boxACheck, p.boxBCheck, resetBtn),
	)

	p.testText = widget.NewEntry()
	p.testText.SetPlaceHolder("粘贴题干，自测题库并查看答案")
	p.testText.OnSubmitted = func(string) { p.testMatch() }
	matchBtn := newAppButton("题干自测", "search", widget.LowImportance, p.testMatch)
	p.testResult = container.NewVBox()
	// 题干自测/查看答案作为次级内容：默认收起的折叠入口，常规与最大化都保留入口，
	// 最大化只参与列布局，不改写用户展开状态。
	p.answerSec = newCollapsibleSection("查看答案", container.NewVBox(
		hrowGrow(p.testText, matchBtn),
		muted("实时答案显示在答案窗口；题干自测可提前确认题库是否可用。"),
		p.testResult,
	), false)

	// 常规单列：题库 → 启停 → 查看答案折叠入口 → 高级设置 → 技术诊断；
	// 最大化分两列：左侧题库与启停，右侧查看答案/高级设置/诊断。
	p.bankCard = card1
	p.startCard = card2
	p.testCard = p.answerSec.Root
	p.layout = &pageSplitLayout{gap: cardGap, normalSplit: 0, maxSplit: 2}
	p.cardBox = container.New(p.layout,
		p.bankCard, p.startCard, p.testCard, p.optionsSec.Root, p.diagSec.Root)
	p.root = container.NewVScroll(p.cardBox)
	go p.watchEvents()
	return p
}

func (p *pageQuiz) Object() fyne.CanvasObject { return p.root }

// SetMaximized 只切换列布局；折叠状态、扫描事件与题库存量保持原样。
func (p *pageQuiz) SetMaximized(maximized bool) {
	if p == nil || p.maximized == maximized {
		return
	}
	runUI(func() {
		p.maximized = maximized
		p.layout.maximized = maximized
		p.cardBox.Refresh()
		p.root.Refresh()
	})
}

var _ windowModePage = (*pageQuiz)(nil)

func (p *pageQuiz) OnShow() {
	p.reloadBanks()
	p.reloadScanState()
}

// quizIntervalText 扫描间隔显示值。
func quizIntervalText(v int) string { return fmt.Sprintf("%d 毫秒", v) }

// quizThresholdText 匹配阈值显示值。
func quizThresholdText(v float64) string { return fmt.Sprintf("%.2f", v) }

// quizOpacityText 结果窗透明度显示值。
func quizOpacityText(v float64) string { return fmt.Sprintf("%d%%", int(v*100+0.5)) }

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
			if p.shell.current == "quiz_search" {
				p.bankScope.resetForShell(p.shell)
			}
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
			// 题库行动态增删后刷新外层滚动体，否则卡片高度不增长导致行重叠/消失。
			reflowInScroll(p.bankList)
			// 题库变化联动「开始搜题」可用态（无启用题库不可开搜）
			p.reloadScanState()
		})
		return nil
	})
}

func (p *pageQuiz) bankRow(b ui.QuizBank) fyne.CanvasObject {
	enable := newEnterpriseCheck("", func(on bool) {
		if p.updating {
			return
		}
		p.shell.Async(func() error {
			err := p.shell.Backend.QuizSetBankEnabled(context.Background(), b.ID, on)
			if err == nil {
				p.reloadBanks()
			}
			return err
		})
	})
	updating := p.updating
	p.updating = true
	enable.SetChecked(b.Enabled)
	p.updating = updating
	name := widget.NewLabel(b.Name)
	name.TextStyle = fyne.TextStyle{Bold: true}
	count := muted(fmt.Sprintf("%d 题 · %s", b.Count, b.ImportedAt.Format("2006-01-02 15:04")))
	renameBtn := newAppIconButton("重命名", "edit", widget.LowImportance, func() {
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
	delBtn := newAppIconButton("删除", "trash", widget.LowImportance, func() {
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
	delBtn.Icon = lineIcon("trash", DangerRed)
	left := container.NewVBox(name, count)
	right := container.NewCenter(container.NewHBox(enable, renameBtn, delBtn))
	// 左侧题库信息占满剩余宽度，操作按钮只占右侧固有宽度；
	// 右侧必须先包 Center：Border 会给右槽满行高，直接放按钮会被拉成一条竖长的红块。
	row := container.NewBorder(nil, nil, nil, right, left)
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

// ---------------------------------------------------------------- 匹配自测（③）

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
					row.Add(hbLabel("最接近："+near, widget.LowImportance))
				}
				p.testResult.Add(row)
			}
			p.testResult.Refresh()
			reflowInScroll(p.testResult) // 自测结果动态增高后同步卡片高度
		})
		return nil
	})
}

// ---------------------------------------------------------------- 搜题控制

// toggleScan 开始/停止搜题（需已有题库；开始后创建 A/B 搜题区域与答案窗）。
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
			p.shell.Toast("已开始搜题：拖动搜题区域框住题目，答案会实时显示", ToastSuccess)
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
			p.shell.Toast("已恢复搜题区域默认位置", ToastSuccess)
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
			runUI(func() {
				p.selfResult.SetText("自检失败：" + err.Error())
				p.diagSec.SetExpanded(true)
			})
			return err
		}
		runUI(func() {
			text := "自检通过（模型齐备）"
			if !r.OK {
				text = "自检未通过"
				if len(r.Missing) > 0 {
					text += "，缺少：" + fmt.Sprint(r.Missing)
				}
				p.diagSec.SetExpanded(true)
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
			runUI(func() {
				p.scanDiag.SetText("扫描服务不可用：" + err.Error())
				p.diagSec.SetExpanded(true)
			})
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
	p.intervalVal.SetText(quizIntervalText(st.Interval))
	p.thresholdVal.SetText(quizThresholdText(st.Threshold))
	p.opacityVal.SetText(quizOpacityText(st.Opacity))

	p.scanDiag.SetText(scanDiagText(st))
	alert := st.EngineError != "" || st.CaptureError != "" || st.SessionType == "wayland"
	// 异常只在技术诊断中保留细节，不在普通用户流程中自动展开。
	p.diagOpen = alert
	p.syncPolling()
}

// updateScanBtn 按扫描态/启用题库数刷新启停按钮（无启用题库不可开搜，对齐桌面壳）。
func (p *pageQuiz) updateScanBtn() {
	if p.lastScan.Scanning {
		p.setScanButton("停止搜题", "stop")
		p.scanBtn.Enable()
		p.scanStatus.SetText("搜题中（主窗口已隐藏，答案实时显示）")
		return
	}
	p.setScanButton("开始搜题", "play")
	if p.lastScan.QuestionCount > 0 {
		p.scanStatus.SetText(fmt.Sprintf("已停止（启用题库 %d 题）", p.lastScan.QuestionCount))
		p.scanBtn.Enable()
	} else {
		p.scanStatus.SetText("已停止（尚未导入可用题库）")
		p.scanBtn.Disable()
	}
}

func (p *pageQuiz) setScanButton(label, iconName string) {
	p.scanBtn.SetAction(label, iconName)
}

// syncPolling 扫描期间每 2s 拉一次状态喂技术诊断区（对齐桌面壳轮询）；
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

// scanDiagText 技术诊断文案（captureError=截屏层 / 各区域摘要=OCR 层 / engineError=引擎层）。
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
			out += fmt.Sprintf("；区域 %s 尚未出帧", box)
			continue
		}
		switch {
		case d.Matched:
			out += fmt.Sprintf("；区域 %s 已命中", box)
		case d.Snippet != "":
			out += fmt.Sprintf("；区域 %s 未命中（识别：%s）", box, d.Snippet)
		default:
			out += fmt.Sprintf("；区域 %s 未命中（未识别到文字：区域内无字或截屏为空）", box)
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
		runUI(func() {
			p.scanDiag.SetText("事件订阅失败：" + err.Error())
		})
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
			p.shell.Toast("文字识别暂不可用，请打开技术诊断查看原因", ToastError)
		}
		p.reloadScanState()
	case ui.QuizEventBoxHidden:
		p.shell.Toast("搜题区域 "+ev.Box+" 已隐藏（可勾选重新显示）", ToastInfo)
		p.reloadScanState()
	}
}
