package appshell

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageHome 首页：派车同步状态、运行环境、应用一览（对应 HomePage.vue）。
type pageHome struct {
	shell *Shell
	root  fyne.CanvasObject

	syncKV  *kvCard
	envKV   *kvCard
	envErr  *widget.Label
	appList *fyne.Container
}

func newPageHome(s *Shell) *pageHome {
	p := &pageHome{shell: s}
	p.syncKV = newKVCard("派车同步", [2]string{"上次成功", "-"}, [2]string{"下次定时", "-"}, [2]string{"U盘", "-"})
	goBtn := widget.NewButtonWithIcon("前往", nil, func() { s.Navigate("daily_sync") })
	syncCard := newCard("派车同步",
		p.syncKV.box,
		hrow(goBtn),
	)
	p.envErr = muted("")
	p.envErr.Hide()
	p.envKV = newKVCard("运行环境", [2]string{"平台", "-"}, [2]string{"核心", "-"})
	envCard := newCard("运行环境", p.envErr, p.envKV.box)
	p.appList = container.NewVBox()
	appCard := newCard("应用一览", p.appList)

	p.root = pageScroll(syncCard, envCard, appCard)
	return p
}

func (p *pageHome) Object() fyne.CanvasObject { return p.root }

func (p *pageHome) OnShow() {
	p.reloadSync()
	p.reloadEnv()
	p.reloadApps()
}

func (p *pageHome) reloadSync() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.AppStatus(context.Background(), "daily_sync")
		if err != nil {
			return nil // 首页状态失败不打断
		}
		ds := ui.DailyStatusFrom(st)
		last := "从未执行"
		if ds.Stamp.Time != "" {
			last = ds.Stamp.Time
			if ds.Stamp.Count > 0 {
				last += fmt.Sprintf("（%d 条）", ds.Stamp.Count)
			}
		}
		usb := ds.USB.Mount
		if usb == "" {
			usb = fmt.Sprintf("未检测到（卷标 %s）", ds.USB.Label)
		}
		p.syncKV.SetValues(
			[2]string{"上次成功", last},
			[2]string{"下次定时", ds.Schedule.NextRun},
			[2]string{"U盘", usb},
		)
		return nil
	})
}

func (p *pageHome) reloadEnv() {
	p.shell.Async(func() error {
		info, err := p.shell.Backend.SystemInfo(context.Background())
		runUI(func() {
			if err != nil {
				p.envErr.SetText("核心调用失败：" + err.Error())
				p.envErr.Show()
				return
			}
			p.envErr.Hide()
			chrome := info.Chrome
			if chrome == "" {
				chrome = "内置 Electron（不依赖宿主浏览器）"
			}
			p.envKV.SetValues(
				[2]string{"平台", info.Platform + " / " + info.Machine},
				[2]string{"核心", info.CoreVersion},
				[2]string{"浏览器", chrome},
				[2]string{"配置文件", info.ConfigPath},
				[2]string{"状态目录", info.StateDir},
			)
		})
		return nil
	})
}

func (p *pageHome) reloadApps() {
	p.shell.Async(func() error {
		apps, err := p.shell.Backend.ListApps(context.Background())
		if err != nil {
			return err
		}
		runUI(func() {
			p.appList.Objects = nil
			for _, a := range apps {
				a := a
				desc := a.Desc
				if desc == "" {
					desc = a.Name
				}
				nameL := widget.NewLabel(a.Name)
				nameL.TextStyle = fyne.TextStyle{Bold: true}
				btn := widget.NewButton("进入应用", func() {
					if isAppPage(a.ID) {
						p.shell.Navigate(a.ID)
					} else {
						p.shell.Toast("应用 "+a.ID+" 暂无独立页面", ToastWarning)
					}
				})
				btn.Importance = widget.LowImportance
				row := container.NewBorder(nil, nil,
					container.NewVBox(nameL, muted(desc)),
					btn)
				p.appList.Add(row)
			}
			p.appList.Refresh()
		})
		return nil
	})
}

// isAppPage 应用 id 是否有对应常驻页面。
func isAppPage(id string) bool {
	switch id {
	case "daily_sync", "tree_sync", "crossing_sync", "pcd_export", "quiz_search", "course_autoplay":
		return true
	default:
		return false
	}
}
