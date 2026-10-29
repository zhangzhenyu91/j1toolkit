package appshell

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageSettings 设置：SSO / 浏览器 / 导出并发 / 环境自检 / 后台运行 / 高级 / 路径 / 日志管理。
type pageSettings struct {
	shell *Shell
	root  fyne.CanvasObject

	ssoUser   *widget.Entry
	ssoPass   *widget.Entry
	pwdHint   *widget.Label
	browser   *widget.Entry
	effective *widget.Label
	wQuery    *widget.Entry
	wRender   *widget.Entry
	testBtns  map[string]*widget.Button
	testLog   *LogView
	testInfo  *widget.Label
	testPaths *fyne.Container
	closeTray *widget.Check
	autoStart *widget.Check
	driverSel *widget.Select
	devMock   *widget.Check
	infoKV    *kvCard
	logKV     *kvCard
}

const (
	driverOS  = "系统定时器（默认，客户端不开也执行）"
	driverApp = "客户端驻留执行（需开机自启+托盘驻留）"
)

func newPageSettings(s *Shell) *pageSettings {
	p := &pageSettings{shell: s}

	// ① SSO 凭据
	p.ssoUser = widget.NewEntry()
	p.ssoUser.SetPlaceHolder("派车系统登录账号")
	p.ssoPass = widget.NewPasswordEntry()
	p.ssoPass.SetPlaceHolder("留空则不修改")
	pwdSave := widget.NewButton("保存", p.saveSSO)
	pwdSave.Importance = widget.HighImportance
	p.pwdHint = muted("")
	card1 := newCard("派车系统 SSO 凭据",
		muted("保存于本机配置文件（Linux 权限 600），不用于其他用途；密码留空则不修改。"),
		hrow(muted("账号"), p.ssoUser),
		hrow(muted("密码"), p.ssoPass, pwdSave),
		p.pwdHint,
	)

	// ② 浏览器设置
	p.browser = widget.NewEntry()
	p.browser.SetPlaceHolder("留空 = 使用内置 Electron")
	pickBtn := widget.NewButton("浏览…", p.pickBrowser)
	detectBtn := widget.NewButton("自动检测", p.autoDetectBrowser)
	bSave := widget.NewButton("保存", p.saveBrowser)
	bSave.Importance = widget.HighImportance
	p.effective = muted("")
	card2 := newCard("浏览器设置",
		muted("派车单/轨迹 PDF 渲染与「官方页面直出」所用的浏览器。默认使用安装包内置 Electron，不依赖或自动探测机器上的 Chrome；内网需公司指定的专用浏览器时在下方显式指定（国密/兼容回退通道）。"),
		hrowGrow(p.browser, pickBtn, detectBtn, bSave),
		p.effective,
	)

	// ③ 导出并发
	p.wQuery = widget.NewEntry()
	p.wQuery.SetText("4")
	p.wRender = widget.NewEntry()
	p.wRender.SetText("2")
	wSave := widget.NewButton("保存并发设置", p.saveWorkers)
	wSave.Importance = widget.HighImportance
	card3 := newCard("导出并发",
		muted("轨迹导出 Go 并行引擎的并发度（查询 1..8 / 渲染 1..4，超出由引擎钳制），改动对下次导出任务生效。"),
		hrow(muted("查询并发"), p.wQuery, muted("渲染并发"), p.wRender, wSave),
	)

	// ④ 环境自检
	p.testBtns = map[string]*widget.Button{}
	p.testLog = NewLogView()
	p.testLog.Root.Resize(fyne.NewSize(0, 140))
	p.testInfo = muted("")
	p.testPaths = container.NewVBox()
	selfRow := container.NewHBox()
	for _, kind := range []struct{ id, label string }{
		{"render", "PDF 渲染自检"},
		{"sso", "SSO 登录测试"},
		{"cdp", "内置 CDP 渲染自检"},
	} {
		kind := kind
		btn := widget.NewButton(kind.label, func() { p.runSelftest(kind.id) })
		p.testBtns[kind.id] = btn
		selfRow.Add(btn)
	}
	card4 := newCard("环境自检",
		selfRow,
		p.testInfo,
		p.testPaths,
		p.testLog.Root,
	)

	// ⑤ 后台运行
	p.closeTray = widget.NewCheck("关闭窗口时最小化到托盘（驻留后台；托盘「退出」才真退出）", nil)
	p.closeTray.SetChecked(true)
	p.autoStart = widget.NewCheck("开机自动启动（登录后驻留托盘）", nil)
	if s.opts.AutoStart == nil {
		p.autoStart.Disable()
	}
	p.driverSel = widget.NewSelect([]string{driverOS, driverApp}, nil)
	p.driverSel.SetSelected(driverOS)
	uiSave := widget.NewButton("保存后台运行设置", p.saveUI)
	uiSave.Importance = widget.HighImportance
	card5 := newCard("后台运行",
		p.closeTray,
		p.autoStart,
		hrow(muted("每日导出由谁执行："), p.driverSel),
		muted("驻留执行更实时、状态直出客户端；代价是机器注销/未登录时当天不执行（系统定时器模式无此限制）。两种模式互斥，不会双跑。"),
		uiSave,
	)

	// ⑥ 高级
	p.devMock = widget.NewCheck("开发模式：使用模拟派车数据（dev.mock；无派车系统网络时调试用，生产环境勿开）", nil)
	devSave := widget.NewButton("保存高级设置", p.saveDev)
	card6 := newCard("高级", hrowGrow(p.devMock, devSave))

	// ⑦ 路径
	p.infoKV = newKVCard("路径",
		[2]string{"配置文件", "-"},
		[2]string{"状态目录", "-"},
	)
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
	card7 := newCard("路径", p.infoKV.box, hrow(openCfg, openState))

	// ⑧ 日志管理
	p.logKV = newKVCard("日志管理",
		[2]string{"日志目录", "-"},
		[2]string{"占用", "-"},
	)
	clearBtn := widget.NewButton("一键清除", p.clearLogs)
	clearBtn.Importance = widget.DangerImportance
	openLog := widget.NewButton("打开目录", func() {
		p.shell.Async(func() error {
			st, err := p.shell.Backend.LogStats(context.Background())
			if err != nil {
				return err
			}
			return p.shell.Backend.OpenPath(context.Background(), st.Dir)
		})
	})
	card8 := newCard("日志管理",
		p.logKV.box,
		hrow(openLog),
		hrowGrow(muted("30 天前的任务日志在核心启动时自动清理"), clearBtn),
	)

	p.root = pageScroll(card1, card2, card3, card4, card5, card6, card7, card8)
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
			} else {
				p.pwdHint.SetText("密码：未配置，导出将无法登录")
			}
			p.browser.SetText(cfg.Render.Chrome)
			effective := cfg.Render.Chrome
			if effective == "" {
				effective = "内置 Electron（不依赖宿主浏览器）"
			} else if ierr == nil && info.Chrome != "" {
				effective = info.Chrome
			}
			p.effective.SetText("当前生效：" + effective)
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
					[2]string{"配置文件", info.ConfigPath},
					[2]string{"状态目录", info.StateDir},
				)
			}
		})
		return nil
	})
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

func (p *pageSettings) autoDetectBrowser() {
	p.shell.Async(func() error {
		info, err := p.shell.Backend.SystemInfo(context.Background())
		if err != nil {
			return err
		}
		if info.Chrome == "" {
			ShowToast(p.shell.Win, "未自动探测到浏览器，请手动选择（或留空用自带 Electron 兜底）", ToastWarning)
			return nil
		}
		runUI(func() {
			p.browser.SetText(info.Chrome)
			p.effective.SetText("当前生效：" + info.Chrome)
		})
		p.shell.Toast("检测到："+info.Chrome, ToastSuccess)
		return nil
	})
}

func (p *pageSettings) saveBrowser() {
	chrome := p.browser.Text
	p.shell.Async(func() error {
		cfg, err := p.shell.Backend.GetConfig(context.Background())
		if err != nil {
			return err
		}
		render := cfg.Render
		render.Chrome = chrome
		render.Builtin = chrome == ""
		if err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{Render: &render}); err != nil {
			return err
		}
		p.shell.Toast("已保存", ToastSuccess)
		p.loadConfig()
		return nil
	})
}

func (p *pageSettings) saveWorkers() {
	q := clampInt(atoiOr(p.wQuery.Text, 4), 1, 8)
	r := clampInt(atoiOr(p.wRender.Text, 2), 1, 4)
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
		p.testInfo.SetText("自检运行中…")
		p.testPaths.Objects = nil
	})
	p.shell.StartJob("selftest:"+kind, map[string]any{}, ui.JobHandler{
		OnLog: func(line string) { p.testLog.Append(line) },
		OnDone: func(job ui.Job) {
			runUI(func() {
				for _, b := range p.testBtns {
					b.Enable()
				}
			})
			ok := job.Status == ui.JobSuccess
			text := job.Error
			if ok {
				text = selftestSummary(kind, job.Result)
			}
			status := "失败"
			if ok {
				status = "通过"
			}
			runUI(func() {
				p.testInfo.SetText(status + "：" + text)
				for _, path := range selftestResultPaths(job.Result) {
					path := path
					p.testPaths.Add(hrow(muted(path), widget.NewButton("打开", func() {
						p.shell.Async(func() error {
							return p.shell.Backend.ShowItem(context.Background(), path)
						})
					})))
				}
				p.testPaths.Refresh()
			})
		},
		OnError: func(err error) {
			runUI(func() {
				for _, b := range p.testBtns {
					b.Enable()
				}
				p.testInfo.SetText("失败：" + err.Error())
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
