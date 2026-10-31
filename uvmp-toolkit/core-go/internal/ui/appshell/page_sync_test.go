package appshell

import (
	"context"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestPageSyncReloadStatusPreservesUnsavedInputs(t *testing.T) {
	f := ui.NewFakeBackend()
	ctx := context.Background()
	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		TreeSync: &ui.SyncAppConfig{LocalPath: "D:/初始/树木.xlsx", FileName: "初始.xlsx", NightlyTime: "23:55"},
	}); err != nil {
		t.Fatal(err)
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["tree_sync"].(*pageSync)

	s.Navigate("tree_sync")
	waitShellCond(t, "树障同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	assertSyncForm(t, p, "D:/初始/树木.xlsx", "初始.xlsx", "23:55")

	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		TreeSync: &ui.SyncAppConfig{LocalPath: "D:/外部/树木.xlsx", FileName: "外部.xlsx", NightlyTime: "23:30"},
	}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.localPath.SetText("D:/未保存/树木.xlsx")
		p.fileName.SetText("未保存.xlsx")
		p.nightly.SetText("22:45")
	})

	s.Navigate("home")
	s.Navigate("tree_sync")
	waitShellCond(t, "树障同步后续状态刷新", func() bool {
		var path string
		runUI(func() { path = p.localPathTxt })
		return path == "D:/外部/树木.xlsx"
	})
	assertSyncForm(t, p, "D:/未保存/树木.xlsx", "未保存.xlsx", "22:45")

	runUI(func() { p.save() })
	waitShellCond(t, "保存同步设置后刷新状态", func() bool {
		var path string
		runUI(func() { path = p.localPathTxt })
		return path == "D:/未保存/树木.xlsx"
	})
	assertSyncForm(t, p, "D:/未保存/树木.xlsx", "未保存.xlsx", "22:45")
	cfg, err := f.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TreeSync.LocalPath != "D:/未保存/树木.xlsx" || cfg.TreeSync.FileName != "未保存.xlsx" || cfg.TreeSync.NightlyTime != "22:45" {
		t.Fatalf("保存后的同步配置不符: %+v", cfg.TreeSync)
	}
}

func assertSyncForm(t *testing.T, p *pageSync, localPath, fileName, nightly string) {
	t.Helper()
	var gotPath, gotName, gotNightly string
	runUI(func() {
		gotPath = p.localPath.Text
		gotName = p.fileName.Text
		gotNightly = p.nightly.Text
	})
	if gotPath != localPath || gotName != fileName || gotNightly != nightly {
		t.Fatalf("状态刷新覆盖了表单输入: path=%q name=%q nightly=%q", gotPath, gotName, gotNightly)
	}
}

// ---------------------------------------------------------------- Task7 常规/最大化布局

func TestPageSyncNormalModeShowsCompactFileAndUSBStatus(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	for _, appID := range []string{"tree_sync", "crossing_sync"} {
		appID := appID
		t.Run(appID, func(t *testing.T) {
			s.Navigate(appID)
			p := s.pages[appID].(*pageSync)
			waitShellCond(t, appID+" 首次配置回填", func() bool {
				var loaded bool
				runUI(func() { loaded = p.formLoaded })
				return loaded
			})
			task7RequireWindowMode(t, p)
			runUI(func() {
				if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("常规模式不应展开五行完整状态")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
					t.Fatal("常规模式应隐藏执行记录次级内容")
				}
				file := task7FindLabel(p.root, "文件检查：")
				usb := task7FindLabel(p.root, "U 盘：")
				if file == nil || usb == nil {
					t.Fatal("常规模式缺少文件检查与 U 盘紧凑状态")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, file) || !pageCanvasObjectEffectivelyVisible(p.root, usb) {
					t.Fatal("常规模式的文件检查与 U 盘紧凑状态不可见")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.localPath) {
					t.Fatal("常规模式缺少同步设置")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.runBtn) || !pageCanvasObjectEffectivelyVisible(p.root, p.nightlyBtn) {
					t.Fatal("常规模式缺少两种执行入口")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.result.Root) {
					t.Fatal("常规模式缺少结果区")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.result.LogRoot) {
					t.Fatal("常规模式缺少排错日志入口")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.fileName) || !pageCanvasObjectEffectivelyVisible(p.root, p.nightly) {
					t.Fatal("常规模式缺少 U 盘文件名或每日备份时间设置")
				}
			})

			// 结果区进入执行中（摘要可见），首屏必须容纳真实卡片高度而不是空闲 MinSize。
			runUI(func() {
				p.result.Begin("正在按 U 盘中的同步任务执行…")
				p.progress.Reset() // progressRow 改为 Reset 显示/默认隐藏后，仍按真实执行流展示进度
				p.progress.Set(1, 2, "检查 U 盘 1/2")
			})
			// 1160×780 实际内容区约 928×638：文件检查/U 盘状态、同步设置与主执行可达。
			// 新增顶部 12px 留白后执行卡略低于首屏，按真实整页滚动验证可完整到达，不放宽阈值。
			task7ResizePage(t, p.root, 928, 638)
			content := task7ScrollContent(t, p.root)
			runCard := content.Objects[2]
			if bottom := runCard.Position().Y + runCard.Size().Height; bottom > 638 {
				scroll := p.root.(*container.Scroll)
				target := bottom - 638
				runUI(func() { scroll.ScrollToOffset(fyne.NewPos(0, target)) })
				top := runCard.Position().Y - scroll.Offset.Y
				if top < 0 || top+runCard.Size().Height > 638 {
					t.Fatalf("执行卡无法经整页滚动完整到达：top=%.1f bottom=%.1f offset=%.1f", top, top+runCard.Size().Height, scroll.Offset.Y)
				}
				runUI(func() { scroll.ScrollToTop() })
			}
			runUI(func() {
				if !p.result.summary.Visible() || !strings.Contains(p.result.summary.Text, "正在按") {
					t.Fatalf("执行中摘要未展示：%q", p.result.summary.Text)
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.progress.Root) {
					t.Fatal("执行中进度区应可见")
				}
			})
			// 900×600：整页滚动仍能到达内容底部。
			task7ResizePage(t, p.root, 900, 600)
			scroll := p.root.(*container.Scroll)
			runUI(func() { scroll.ScrollToBottom() })
			wantOffset := fyne.Max(0, content.MinSize().Height-scroll.Size().Height)
			if scroll.Offset.Y != wantOffset {
				t.Fatalf("900×600 未滚动到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, wantOffset)
			}
			// 668×458 常规同样必须能滚到底。
			task7ResizePage(t, p.root, 668, 458)
			runUI(func() { scroll.ScrollToBottom() })
			if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
				t.Fatalf("668×458 常规未滚动到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
			}
			if scroll.Offset.Y <= 0 {
				t.Fatal("668×458 常规长内容应可滚动")
			}
		})
	}
}

func TestPageSyncMaximizedModeSharedSkeletonAndState(t *testing.T) {
	f := ui.NewFakeBackend()
	counter := &task7CountingBackend{Backend: f}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, counter, Options{DisableTray: true, DisableScheduler: true})

	for _, appID := range []string{"tree_sync", "crossing_sync"} {
		appID := appID
		t.Run(appID, func(t *testing.T) {
			s.Navigate(appID)
			p := s.pages[appID].(*pageSync)
			waitShellCond(t, appID+" 首次配置回填", func() bool {
				var loaded bool
				runUI(func() { loaded = p.formLoaded })
				return loaded
			})
			time.Sleep(120 * time.Millisecond)

			wantDef := "检修一班树木统计表.xlsx"
			if appID == "crossing_sync" {
				wantDef = "检修一班交跨统计表.xlsx"
			}
			if p.defFileName != wantDef {
				t.Fatalf("两种 appID 默认文件名分叉：%q want %q", p.defFileName, wantDef)
			}

			job := &ui.JobHandle{}
			runUI(func() {
				p.localPath.SetText("D:/未保存/统计表.xlsx")
				p.fileName.SetText("未保存.xlsx")
				p.nightly.SetText("22:45")
				p.currentJob = job
			})
			root := p.root
			content := task7ScrollContent(t, p.root)
			historyRoot := p.history.Root
			counter.calls.Store(0)

			task7ResizePage(t, root, 1200, 700)
			runUI(func() { s.setWindowMaximized(true) })
			task7AssertNoBackendCalls(t, counter, appID+" 最大化")

			if p.root != root || task7ScrollContent(t, p.root) != content || p.history.Root != historyRoot {
				t.Fatal("切换最大化重建了页面或内容容器")
			}
			runUI(func() {
				if !pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("最大化应显示五行完整状态")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
					t.Fatal("最大化应显示执行记录")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.result.LogRoot) {
					t.Fatal("最大化不得隐藏排错日志入口")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, task7FindLabel(p.root, "文件检查：")) {
					t.Fatal("最大化应由完整状态替代紧凑状态")
				}
			})
			rows, ok := p.statusKV.box.Objects[0].(*fyne.Container)
			if !ok || len(rows.Objects) != 5 {
				t.Fatalf("完整状态应为五行，实际 %T", p.statusKV.box.Objects[0])
			}

			task7ResizePage(t, root, 1200, 700)
			first := task7RequireOffset(t, content, content.Objects[0])
			right := task7RequireOffset(t, content, content.Objects[2])
			if first.Y != right.Y || first.X == right.X {
				t.Fatalf("最大化应为左状态/设置、右执行/日志双列：left=%v right=%v", first, right)
			}
			runUI(func() {
				if p.localPath.Text != "D:/未保存/统计表.xlsx" || p.fileName.Text != "未保存.xlsx" || p.nightly.Text != "22:45" {
					t.Fatal("最大化切换清空了未保存输入")
				}
				if p.currentJob != job {
					t.Fatal("最大化切换替换了进行中的任务")
				}
			})

			// 668×458：最大化窄宽必须退化为单列，全部预建内容可经整页滚动到达。
			task7ResizePage(t, root, 668, 458)
			for i := 1; i < len(content.Objects); i++ {
				if !content.Objects[i].Visible() {
					continue
				}
				pos := task7RequireOffset(t, content, content.Objects[i])
				if pos.X != 0 {
					t.Fatalf("668×458 最大化第 %d 张卡片未退化为单列：x=%.1f", i, pos.X)
				}
			}
			scroll := root.(*container.Scroll)
			runUI(func() { scroll.ScrollToBottom() })
			last := content.Objects[len(content.Objects)-1]
			if bottom := last.Position().Y + last.Size().Height; bottom > content.Size().Height {
				t.Fatalf("668×458 最大化末卡超出内容：bottom=%.1f content=%.1f", bottom, content.Size().Height)
			}
			if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
				t.Fatalf("668×458 最大化未滚到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
			}
			if scroll.Offset.Y <= 0 {
				t.Fatal("668×458 最大化长内容应可滚动")
			}

			counter.calls.Store(0)
			runUI(func() { s.setWindowMaximized(false) })
			task7AssertNoBackendCalls(t, counter, appID+" 还原")
			runUI(func() {
				if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("还原后应重新收起五行完整状态")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
					t.Fatal("还原后应重新隐藏执行记录")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, task7FindLabel(p.root, "文件检查：")) {
					t.Fatal("还原后应恢复紧凑状态")
				}
				if p.localPath.Text != "D:/未保存/统计表.xlsx" || p.currentJob != job {
					t.Fatal("还原后输入或任务状态丢失")
				}
			})
			task7ResizePage(t, root, 1200, 700)
			first = task7RequireOffset(t, content, content.Objects[0])
			restored := task7RequireOffset(t, content, content.Objects[1])
			if first.X != restored.X || first.Y == restored.Y {
				t.Fatalf("还原后应恢复单列：first=%v second=%v", first, restored)
			}
		})
	}
}

func TestPageSyncStatusCardModesAndTextGeometry(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	for _, appID := range []string{"tree_sync", "crossing_sync"} {
		appID := appID
		t.Run(appID, func(t *testing.T) {
			s.Navigate(appID)
			p := s.pages[appID].(*pageSync)
			waitShellCond(t, appID+" 首次配置回填", func() bool {
				var loaded bool
				runUI(func() { loaded = p.formLoaded })
				return loaded
			})
			runUI(func() {
				p.fileHint.SetText("文件检查：存在（D:/很深的目录/检修一班树木统计表-2026-10.xlsx）")
				p.usbHint.SetText("U 盘：/run/media/user/GLKVM-很长的挂载点名称")
			})
			task7ResizePage(t, p.root, 928, 638)

			runUI(func() {
				title := task7FindLabel(p.root, "运行状态")
				if title == nil || !pageCanvasObjectEffectivelyVisible(p.root, title) {
					t.Fatal("状态卡标题应为运行状态且可见")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.fileHint) || !pageCanvasObjectEffectivelyVisible(p.root, p.usbHint) {
					t.Fatal("常规应显示文件检查与 U 盘紧凑状态")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("常规只应显示紧凑状态，不显示完整 statusKV")
				}
				file := task7RectOf(t, p.root, p.fileHint)
				usb := task7RectOf(t, p.root, p.usbHint)
				if file.overlaps(usb) {
					t.Fatalf("常规紧凑状态文本重叠：file=%+v usb=%+v", file, usb)
				}
				titleRect := task7RectOf(t, p.root, title)
				if titleRect.overlaps(file) || titleRect.overlaps(usb) {
					t.Fatalf("常规状态标题与紧凑文本重叠：title=%+v file=%+v usb=%+v", titleRect, file, usb)
				}
				assertTask7TextFits(t, "常规文件检查", p.fileHint)
				assertTask7TextFits(t, "常规 U 盘状态", p.usbHint)
			})

			runUI(func() { s.setWindowMaximized(true) })
			task7ResizePage(t, p.root, 1200, 700)
			runUI(func() {
				title := task7FindLabel(p.root, "运行状态")
				if title == nil || !pageCanvasObjectEffectivelyVisible(p.root, title) {
					t.Fatal("最大化状态卡标题应为运行状态且可见")
				}
				if !pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("最大化应显示完整 statusKV")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, p.fileHint) || pageCanvasObjectEffectivelyVisible(p.root, p.usbHint) {
					t.Fatal("最大化只应显示完整 statusKV，不显示紧凑状态")
				}
				assertTask7TextFits(t, "最大化完整状态", p.statusKV.box)
			})

			runUI(func() { s.setWindowMaximized(false) })
			runUI(func() {
				if !pageCanvasObjectEffectivelyVisible(p.root, p.fileHint) {
					t.Fatal("还原后应恢复紧凑状态")
				}
				if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
					t.Fatal("还原后应隐藏完整 statusKV")
				}
			})
		})
	}
}
