package appshell

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageSettings 设置：按「账号 / 宿主浏览器 / 导出与后台 / 开发诊断」分组，
// 主流程只露常用项，路径、日志、自检等排障信息收进可折叠的「开发诊断」。
type pageSettings struct {
	shell *Shell
	root  fyne.CanvasObject

	// 账号
	ssoUser *widget.Entry
	ssoPass *widget.Entry
	pwdHint *widget.Label

	// 宿主浏览器
	browser   *widget.Entry
	effective *widget.Label

	// 导出与后台
	wQuery    *widget.Entry
	wRender   *widget.Entry
	closeTray *widget.Check
	autoStart *widget.Check
	driverSel *widget.Select

	// 开发诊断
	devMock    *widget.Check
	testBtns   map[string]*widget.Button
	testLog    *LogView
	testInfo   *widget.Label
	testPaths  *fyne.Container
	logSection *collapsibleSection
	diag       *collapsibleSection
	infoKV     *kvCard
	logKV      *kvCard

	sections []*collapsibleSection
}

const (
	driverOS  = "系统定时器（默认，客户端不开也执行）"
	driverApp = "客户端驻留执行（需开机自启+托盘驻留）"
)

func newPageSettings(s *Shell) *pageSettings {
	p := &pageSettings{shell: s, testBtns: map[string]*widget.Button{}}

	// ① 账号（常用，默认展开）
	p.ssoUser = widget.NewEntry()
	p.ssoUser.SetPlaceHolder("派车系统登录账号")
	p.ssoPass = widget.NewPasswordEntry()
	p.ssoPass.SetPlaceHolder("留空则不修改")
	pwdSave := widget.NewButton("保存账号", p.saveSSO)
	pwdSave.Importance = widget.MediumImportance
	p.pwdHint = stateLabel("", widget.LowImportance)
	account := newCollapsibleSection("账号 · 派车系统", container.NewVBox(
		muted("派车系统的登录账号密码。只保存在本机配置文件，不用于其他用途；密码留空表示不修改。"),
		formRow("账号", p.ssoUser),
		formRow("密码", p.ssoPass, pwdSave),
		p.pwdHint,
	), true)

	// ② 宿主浏览器
	p.browser = widget.NewEntry()
	p.browser.SetPlaceHolder("留空 = 自动检测宿主浏览器")
	pickBtn := widget.NewButton("浏览…", p.pickBrowser)
	detectBtn := widget.NewButton("自动检测", p.autoDetectBrowser)
	bSave := widget.NewButton("保存浏览器", p.saveBrowser)
	bSave.Importance = widget.MediumImportance
	p.effective = stateLabel("", widget.LowImportance)
	browser := newCollapsibleSection("宿主浏览器", container.NewVBox(
		muted("用于派车单/轨迹 PDF 渲染。留空保存 = 自动检测本机已安装的宿主浏览器（360 企业浏览器 / 奇安信 / Chrome / Edge），检测不到时请点「自动检测」确认或手动指定；内网需公司专用浏览器（国密/兼容回退）时请显式填写路径。"),
		formRow("浏览器", p.browser, pickBtn, detectBtn, bSave),
		p.effective,
	), true)

	// ③ 导出与后台
	p.wQuery = widget.NewEntry()
	p.wQuery.SetText("4")
	p.wRender = widget.NewEntry()
	p.wRender.SetText("2")
	wSave := widget.NewButton("保存并发设置", p.saveWorkers)
	wSave.Importance = widget.MediumImportance
	workers := hrow(
		fixed(76, 34, formLabel("查询并发")), fixed(84, 36, p.wQuery),
		fixed(76, 34, formLabel("渲染并发")), fixed(84, 36, p.wRender),
		wSave,
	)

	p.closeTray = widget.NewCheck("关闭窗口时最小化到托盘（驻留后台；托盘「退出」才真退出）", nil)
	p.closeTray.SetChecked(true)
	p.autoStart = widget.NewCheck("开机自动启动（登录后驻留托盘）", nil)
	if s.opts.AutoStart == nil {
		p.autoStart.Disable()
	}
	p.driverSel = widget.NewSelect([]string{driverOS, driverApp}, nil)
	p.driverSel.SetSelected(driverOS)
	uiSave := widget.NewButton("保存后台设置", p.saveUI)
	uiSave.Importance = widget.MediumImportance
	exportBg := newCollapsibleSection("导出与后台", container.NewVBox(
		muted("轨迹导出并行引擎的并发度（查询 1..8 / 渲染 1..4，超出由引擎钳制），对下次导出任务生效。"),
		workers,
		widget.NewSeparator(),
		p.closeTray,
		p.autoStart,
		formRow("执行方式", p.driverSel),
		muted("驻留执行更实时、状态直出客户端；代价是机器注销/未登录时当天不执行。两种模式互斥，不会双跑。"),
		hrow(uiSave),
	), true)

	// ④ 开发诊断（默认收起：路径/自检/日志/开发开关全部收在此处）
	p.devMock = widget.NewCheck("开发模式：使用模拟派车数据（无派车系统网络时调试用，生产环境勿开）", nil)
	devSave := widget.NewButton("保存开发设置", p.saveDev)
	devSave.Importance = widget.MediumImportance

	p.testLog = NewLogView()
	p.testLog.Root.Resize(fyne.NewSize(0, 140))
	p.logSection = newCollapsibleSection("自检日志", p.testLog.Root, false)
	p.testInfo = stateLabel("尚未运行自检", widget.LowImportance)
	p.testPaths = container.NewVBox()
	selfRow := container.NewHBox()
	for _, kind := range []struct{ id, label string }{
		{"render", "检查 PDF 渲染"},
		{"sso", "检查派车系统登录"},
		{"cdp", "检查浏览器渲染"},
	} {
		kind := kind
		btn := widget.NewButton(kind.label, func() { p.runSelftest(kind.id) })
		p.testBtns[kind.id] = btn
		selfRow.Add(btn)
	}

	p.infoKV = newKVCard("运行环境", [2]string{"平台", "-"}, [2]string{"核心版本", "-"})
	openCfg := widget.NewButton("打开配置目录", func() {
		p.shell.Async(func() error {
			info, err := p.shell.Backend.SystemInfo(context.Background())
			if err != nil {
				return err
			}
			return p.shell.Backend.ShowItem(context.Background(), info.ConfigPath)
		})
	})
	openState := widget.NewButton("打开状态目录", func() {
		p.shell.Async(func() error {
			info, err := p.shell.Backend.SystemInfo(context.Background())
			if err != nil {
				return err
			}
			return p.shell.Backend.ShowItem(context.Background(), info.StateDir)
		})
	})

	p.logKV = newKVCard("日志", [2]string{"日志目录", "-"}, [2]string{"占用", "-"})
	openLog := widget.NewButton("打开日志目录", func() {
		p.shell.Async(func() error {
			st, err := p.shell.Backend.LogStats(context.Background())
			if err != nil {
				return err
			}
			return p.shell.Backend.OpenPath(context.Background(), st.Dir)
		})
	})
	clearBtn := widget.NewButton("一键清除", p.clearLogs)
	clearBtn.Importance = widget.DangerImportance

	diagContent := container.NewVBox(
		selfRow,
		p.testInfo,
		p.testPaths,
		p.logSection.Root,
		widget.NewSeparator(),
		hrowGrow(p.devMock, devSave),
		widget.NewSeparator(),
		muted("运行环境与路径（排障用；正常使用无需关心）"),
		p.infoKV.box,
		hrow(openCfg, openState),
		widget.NewSeparator(),
		muted("日志占用与清理"),
		p.logKV.box,
		hrow(openLog),
		hrowGrow(muted("30 天前的任务日志在启动时自动清理"), clearBtn),
	)
	p.diag = newCollapsibleSection("开发诊断", diagContent, false)

	p.sections = []*collapsibleSection{account, browser, exportBg, p.diag}
	p.root = pageScroll(account.Root, browser.Root, exportBg.Root, p.diag.Root)
	return p
}

func (p *pageSettings) Object() fyne.CanvasObject { return p.root }

func (p *pageSettings) OnShow() {
	p.loadConfig()
	p.loadLogStats()
}

func (p *pageSettings) loadConfig() {
	p.shell.Async(func() error {
		cfg, err := p.shell.Backend.GetConfig(context.Background())
		if err != nil {
			return nil
		}
		info, ierr := p.shell.Backend.SystemInfo(context.Background())
		runUI(func() {
			p.ssoUser.SetText(cfg.SSO.Username)
			if cfg.SSO.HasPassword {
				p.pwdHint.SetText(fmt.Sprintf("密码：已配置（来源 %s）", cfg.SSO.PasswordSource))
				p.pwdHint.Importance = widget.LowImportance
			} else {
				p.pwdHint.SetText("密码：未配置，导出将无法登录")
				p.pwdHint.Importance = widget.WarningImportance
			}
			p.pwdHint.Refresh()
			p.browser.SetText(cfg.Render.Chrome)
			p.effective.SetText("当前生效：" + browserModeText(cfg.Render, info, ierr == nil))
			p.wQuery.SetText(fmt.Sprintf("%d", cfg.OrderExport.QueryWorkers))
			p.wRender.SetText(fmt.Sprintf("%d", cfg.OrderExport.RenderWorkers))
			p.closeTray.SetChecked(cfg.UI.CloseToTray)
			p.autoStart.SetChecked(cfg.UI.Autostart)
			if cfg.Schedule.Driver == "app" {
				p.driverSel.SetSelected(driverApp)
			} else {
				p.driverSel.SetSelected(driverOS)
			}
			p.devMock.SetChecked(cfg.Dev.Mock)
			if ierr == nil {
				p.infoKV.SetValues(
					[2]string{"平台", info.Platform + " / " + info.Machine},
					[2]string{"核心版本", info.CoreVersion},
					[2]string{"生效浏览器", browserModeText(cfg.Render, info, true)},
					[2]string{"配置文件", info.ConfigPath},
					[2]string{"状态目录", info.StateDir},
				)
			}
		})
		return nil
	})
}

// browserModeText 浏览器生效口径文案：显式指定 > 核心检测到的实际浏览器。
// 历史 render.builtin=true 但未注入 UVMP_BROWSER_BIN 时，核心会按宿主检测（升级兼容），
// 此处必须同样显示宿主，不能谎称安装包内置 Electron；只有核心确实返回 Electron 运行时
// 路径时才显示内置浏览器。
func browserModeText(rc ui.RenderConfig, info ui.SystemInfo, infoOK bool) string {
	if chrome := strings.TrimSpace(rc.Chrome); chrome != "" {
		return chrome
	}
	detected := ""
	if infoOK {
		detected = strings.TrimSpace(info.Chrome)
	}
	if detected == "" {
		if rc.Builtin {
			return "未检测到可用浏览器（请点「自动检测」或手动指定）"
		}
		return "自动检测宿主浏览器（未检测到，请点「自动检测」或手动指定）"
	}
	if rc.Builtin && isBundledBrowserPath(detected) {
		return "安装包内置浏览器（Electron）"
	}
	return "自动检测宿主浏览器：" + detected
}

// isBundledBrowserPath 核心检测结果是否为随包 Electron 运行时（而非宿主浏览器）。
func isBundledBrowserPath(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "electron", "electron.exe":
		return true
	default:
		return false
	}
}

func (p *pageSettings) loadLogStats() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.LogStats(context.Background())
		if err != nil {
			return err
		}
		p.logKV.SetValues(
			[2]string{"日志目录", st.Dir},
			[2]string{"占用", fmt.Sprintf("%d 个文件 / %s", st.Files, humanBytes(st.Bytes))},
		)
		return nil
	})
}

func (p *pageSettings) saveSSO() {
	user := strings.TrimSpace(p.ssoUser.Text)
	pass := p.ssoPass.Text // 空 = 不修改
	p.shell.Async(func() error {
		err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			SSO: &ui.SSOConfig{Username: user, Password: pass},
		})
		if err == nil {
			runUI(func() { p.ssoPass.SetText("") })
			p.shell.Toast("已保存", ToastSuccess)
			p.loadConfig()
		}
		return err
	})
}

func (p *pageSettings) pickBrowser() {
	PickFile(p.shell.Win, "选择可执行文件", nil, func(path string) {
		if path != "" {
			p.browser.SetText(path)
		}
	})
}

// autoDetectBrowser 自动检测宿主浏览器：临时切到自动探测口径取回路径，随后还原原配置，
// 只把检测结果填进输入框（保存由用户确认）。
func (p *pageSettings) autoDetectBrowser() {
	p.shell.Async(func() error {
		ctx := context.Background()
		cfg, err := p.shell.Backend.GetConfig(ctx)
		if err != nil {
			return err
		}
		prev := cfg.Render
		probe := prev
		probe.Chrome = ""
		probe.Builtin = false
		if err := p.shell.Backend.SaveConfig(ctx, ui.ConfigPatch{Render: &probe}); err != nil {
			return err
		}
		info, ierr := p.shell.Backend.SystemInfo(ctx)
		// 探测只读：无论成功与否都还原用户原配置。
		if err := p.shell.Backend.SaveConfig(ctx, ui.ConfigPatch{Render: &prev}); err != nil {
			return err
		}
		if ierr != nil {
			return ierr
		}
		detected := strings.TrimSpace(info.Chrome)
		if detected == "" {
			p.shell.Toast("未检测到宿主浏览器：请点「浏览…」手动选择（如公司专用浏览器）", ToastWarning)
			return nil
		}
		runUI(func() {
			p.browser.SetText(detected)
			p.effective.SetText("当前生效：" + detected)
		})
		p.shell.Toast("检测到："+detected, ToastSuccess)
		return nil
	})
}

// saveBrowser 保存浏览器设置：留空 = 自动检测宿主浏览器（每次渲染按宿主探测），
// 显式路径 = 固定使用该浏览器（公司国密/兼容回退）。
func (p *pageSettings) saveBrowser() {
	chrome := strings.TrimSpace(p.browser.Text)
	p.shell.Async(func() error {
		ctx := context.Background()
		cfg, err := p.shell.Backend.GetConfig(ctx)
		if err != nil {
			return err
		}
		render := cfg.Render
		render.Chrome = chrome
		render.Builtin = false // 留空走宿主自动探测；显式路径优先于 builtin
		if err := p.shell.Backend.SaveConfig(ctx, ui.ConfigPatch{Render: &render}); err != nil {
			return err
		}
		if chrome == "" {
			p.shell.Toast("已保存：留空时将自动检测宿主浏览器", ToastSuccess)
		} else {
			p.shell.Toast("已保存，使用指定浏览器", ToastSuccess)
		}
		p.loadConfig()
		return nil
	})
}

func (p *pageSettings) saveWorkers() {
	q := settingsClamp(settingsIntOr(p.wQuery.Text, 4), 1, 8)
	r := settingsClamp(settingsIntOr(p.wRender.Text, 2), 1, 4)
	p.shell.Async(func() error {
		err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			OrderExport: &ui.OrderExportConfig{QueryWorkers: q, RenderWorkers: r},
		})
		if err == nil {
			p.shell.Toast("已保存", ToastSuccess)
		}
		return err
	})
}

// runSelftest 环境自检（selftest:render / selftest:sso / selftest:cdp 任务）。
func (p *pageSettings) runSelftest(kind string) {
	runUI(func() {
		for _, b := range p.testBtns {
			b.Disable()
		}
		p.testLog.Clear()
		setStateLabel(p.testInfo, "自检运行中…", widget.MediumImportance)
		p.testPaths.Objects = nil
		p.testPaths.Refresh()
		reflowInScroll(p.testPaths)
		p.logSection.SetExpanded(true) // 运行中展开日志，跑完可自行收起
	})
	p.shell.StartJob("selftest:"+kind, map[string]any{}, ui.JobHandler{
		OnLog: func(line string) { p.testLog.Append(line) },
		OnDone: func(job ui.Job) {
			ok := job.Status == ui.JobSuccess
			text := job.Error
			if ok {
				text = selftestSummary(kind, job.Result)
			}
			runUI(func() {
				for _, b := range p.testBtns {
					b.Enable()
				}
				if ok {
					setStateLabel(p.testInfo, "通过："+text, widget.SuccessImportance)
				} else {
					setStateLabel(p.testInfo, "失败："+text, widget.DangerImportance)
				}
				for _, path := range selftestResultPaths(job.Result) {
					path := path
					openBtn := widget.NewButton("打开", func() {
						p.shell.Async(func() error {
							return p.shell.Backend.ShowItem(context.Background(), path)
						})
					})
					p.testPaths.Add(container.NewBorder(nil, nil, nil, openBtn, formLabel(path)))
				}
				p.testPaths.Refresh()
				reflowInScroll(p.testPaths)
			})
		},
		OnError: func(err error) {
			runUI(func() {
				for _, b := range p.testBtns {
					b.Enable()
				}
				setStateLabel(p.testInfo, "失败："+err.Error(), widget.DangerImportance)
			})
		},
	})
}

func (p *pageSettings) saveUI() {
	closeTray := p.closeTray.Checked
	autoStart := p.autoStart.Checked
	driver := "os"
	if p.driverSel.Selected == driverApp {
		driver = "app"
	}
	p.shell.Async(func() error {
		cfg, err := p.shell.Backend.GetConfig(context.Background())
		if err != nil {
			return err
		}
		sch := cfg.Schedule
		sch.Driver = driver
		err = p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			UI:       &ui.UIConfig{CloseToTray: closeTray, Autostart: autoStart},
			Schedule: &sch,
		})
		if err != nil {
			return err
		}
		p.shell.SetCloseToTray(closeTray) // 即时生效，无需重启
		if a := p.shell.opts.AutoStart; a != nil {
			if autoStart {
				err = a.Enable()
			} else {
				err = a.Disable()
			}
			if err != nil {
				return fmt.Errorf("开机自启设置失败：%w", err)
			}
		}
		p.shell.Toast("已保存", ToastSuccess)
		p.loadConfig()
		return nil
	})
}

func (p *pageSettings) saveDev() {
	mock := p.devMock.Checked
	p.shell.Async(func() error {
		err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			Dev: &ui.DevConfig{Mock: mock},
		})
		if err == nil {
			p.shell.Toast("已保存", ToastSuccess)
		}
		return err
	})
}

func (p *pageSettings) clearLogs() {
	ConfirmDanger(p.shell.Win, "一键清除日志",
		"将删除全部任务日志与任务档案（不影响执行记录）；正在运行任务的日志会被跳过。确定继续？",
		"清除", func() {
			p.shell.Async(func() error {
				r, err := p.shell.Backend.ClearLogs(context.Background())
				if err != nil {
					return err
				}
				msg := fmt.Sprintf("已清除 %d 个日志文件", r.Removed)
				if r.Skipped > 0 {
					msg += fmt.Sprintf("，%d 个正在使用被跳过", r.Skipped)
				}
				p.shell.Toast(msg, ToastSuccess)
				p.loadLogStats()
				return nil
			})
		})
}

// selftestResultPaths 自检产物路径（cdp 自检产出 png/pdf 文件；兼容 paths 列表形状）。
func selftestResultPaths(result map[string]any) []string {
	out := []string{}
	for _, k := range []string{"png", "pdf"} {
		if s, _ := result[k].(string); s != "" {
			out = append(out, s)
		}
	}
	if paths, ok := result["paths"].([]any); ok {
		for _, v := range paths {
			if s, _ := v.(string); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// selftestSummary 自检结果摘要（对应各 lanes/文案口径）。
func selftestSummary(kind string, result map[string]any) string {
	switch kind {
	case "render":
		lanes, _ := result["lanes"].([]any)
		parts := []string{}
		for _, l := range lanes {
			m, _ := l.(map[string]any)
			name := "浏览器"
			if lane, _ := m["lane"].(string); lane == "electron" {
				name = "自带Electron"
			}
			state := "失败"
			if ok, _ := m["ok"].(bool); ok {
				state = "OK"
			}
			parts = append(parts, name+"："+state)
		}
		if len(parts) > 0 {
			return joinSemi(parts)
		}
		return "渲染自检完成"
	case "sso":
		if mock, _ := result["mock"].(bool); mock {
			return "登录成功（模拟数据模式）"
		}
		return "SSO 登录成功，凭证已就绪"
	case "cdp":
		browser, _ := result["browser"].(string)
		return fmt.Sprintf("内置 CDP 渲染：导航/eval/新tab/截图/printToPDF 全部通过（%s）", browser)
	default:
		return "自检完成"
	}
}

func joinSemi(parts []string) string {
	out := ""
	for i, s := range parts {
		if i > 0 {
			out += "；"
		}
		out += s
	}
	return out
}

// humanBytes 字节数人性化。
func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", int64(v), units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// settingsIntOr 解析正整数，空值/非法/非正数时返回默认值（设置页本地助手，避免跨文件耦合）。
func settingsIntOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// settingsClamp 把 n 钳制到 [lo, hi]。
func settingsClamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
