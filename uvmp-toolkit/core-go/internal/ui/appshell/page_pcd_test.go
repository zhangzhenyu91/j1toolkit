package appshell

import (
	"context"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pcdDelayedSystemInfoBackend 延迟推荐保存位置返回，模拟用户等待期间已经输入。
type pcdDelayedSystemInfoBackend struct {
	ui.Backend
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (b *pcdDelayedSystemInfoBackend) SystemInfo(context.Context) (ui.SystemInfo, error) {
	close(b.started)
	<-b.release
	defer close(b.returned)
	return ui.SystemInfo{SuggestedOutDirs: []string{"D:/推荐导出"}}, nil
}

func TestPagePcdSuggestedOutdirDoesNotOverwriteLateInput(t *testing.T) {
	backend := &pcdDelayedSystemInfoBackend{
		Backend:  ui.NewFakeBackend(),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)

	s.Navigate("pcd_export")
	waitShellCond(t, "推荐保存位置请求开始", func() bool {
		select {
		case <-backend.started:
			return true
		default:
			return false
		}
	})
	runUI(func() { p.outdir.SetText("D:/用户已填写") })
	close(backend.release)
	waitShellCond(t, "推荐保存位置请求返回", func() bool {
		select {
		case <-backend.returned:
			return true
		default:
			return false
		}
	})

	var outdir string
	runUI(func() { outdir = p.outdir.Text })
	if outdir != "D:/用户已填写" {
		t.Fatalf("异步推荐保存位置覆盖了用户输入: %q", outdir)
	}
}
