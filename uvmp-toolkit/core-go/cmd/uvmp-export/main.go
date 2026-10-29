// uvmp-export 派车单/轨迹并行导出工作进程（Python goexport.py 的固定协议）：
//
//	启动  uvmp-export run
//	stdin 首行请求  {"config":<config.load dict>,"params":<pcd_export 参数>,
//	                 "codes":[{seq,code,date_hint}]}
//	      之后可收  {"command":"cancel"}
//	stdout 仅 NDJSON 事件：log / progress / done / error
//
// 另有内置 CDP 浏览器自检子命令（设置页「内置 CDP 渲染自检」，同套协议）：
//
//	启动  uvmp-export selftest
//	stdin 首行请求  {"config":<config.load dict>}（codes 不用）
//	      之后可收  {"command":"cancel"}
//	stdout 仅 NDJSON 事件，成功 done.result = {ok,png,pdf,...}
//
// 密码只经 stdin 请求体传入，不进命令行、不落日志；真实路径每批 Auth Manager
// 登录一次，渲染 worker 各自独立 WorkDir（状态目录 browser-workers/<批次>）。
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/exporter"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runExportIO(os.Stdin, os.Stdout))
	case "selftest":
		os.Exit(runSelftestIO(os.Stdin, os.Stdout))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: uvmp-export run|selftest（stdin 首行请求，stdout NDJSON 事件）")
}

// injectBrowserEnv 统一浏览器运行时自发现：ParseRunConfig / ParseSelftestConfig 之前
// 自动注入内置浏览器环境（UVMP_BROWSER_BIN/WORKER/APP，显式 render.chrome/显式 env
// 优先不覆盖），任务结束调用 restore 恢复，避免污染后续任务。无 Python goexport
// 注入时 run/selftest 也能独立工作。
func injectBrowserEnv(cfg map[string]any) (restore func()) {
	return browser.EnsureExportEnv(browser.ExplicitChrome(cfg))
}

// runExportIO 派车单/轨迹并行导出（原唯一子命令，行为不变；返回进程退出码）。
func runExportIO(in io.Reader, out io.Writer) int {
	r := bufio.NewReader(in)
	em := exporter.NewEmitter(out)
	req, err := exporter.ReadRequest(r)
	if err != nil {
		em.Error(err.Error())
		return 1
	}
	restore := injectBrowserEnv(req.Config)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go exporter.PumpCancel(r, cancel)

	if err := exporter.Run(ctx, req, em, buildDeps(em)); err != nil {
		return 1
	}
	return 0
}

// runSelftestIO 内置 CDP 浏览器自检：只打开 data: 本地页（零网络），验证导航 /
// eval / 新 tab / 截图 PNG / printToPDF，真实产物写 UVMP_SELFTEST_DIR
// （Python 侧传 state.logs_dir()；独立使用未设时落临时目录），成功
// done.result = {ok,png,pdf,...}。浏览器口径与导出一致：显式 render.chrome 优先，
// 否则内置 UVMP_BROWSER_BIN 配 UVMP_BROWSER_WORKER/APP（自动注入，显式 env 不覆盖）；
// 无浏览器/失败走明确 error 事件。返回进程退出码。
func runSelftestIO(in io.Reader, out io.Writer) int {
	r := bufio.NewReader(in)
	em := exporter.NewEmitter(out)
	req, err := exporter.ReadRequest(r)
	if err != nil {
		em.Error(err.Error())
		return 1
	}
	restore := injectBrowserEnv(req.Config)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go exporter.PumpCancel(r, cancel)

	outDir := os.Getenv("UVMP_SELFTEST_DIR")
	if strings.TrimSpace(outDir) == "" {
		// 独立使用（无 Python 侧传 state.logs_dir()）：产物默认落临时目录
		if d, err := os.MkdirTemp("", "uvmp-selftest-"); err == nil {
			outDir = d
		}
	}
	cfg, err := render.ParseSelftestConfig(req.Config, os.Getenv, outDir)
	if err != nil {
		em.Error(err.Error())
		return 1
	}
	cfg.Log = func(line string) { em.Log("%s", line) }
	cfg.Progress = func(step, total int, label string) { em.Progress(step, total, label) }
	em.Log("内置 CDP 浏览器自检：%s", cfg.BrowserPath)
	em.Log("自检产物目录：%s", cfg.OutDir)
	res, err := render.Selftest(ctx, cfg)
	if err != nil {
		em.Error(err.Error())
		return 1
	}
	em.Done(res)
	return 0
}

// buildDeps 校验通过后构建运行依赖：mock 走模拟数据；真实路径每批
// Auth Manager 登录一次（登录走 job ctx，取消覆盖登录；token 模式直接用
// 手动三件套），渲染 worker 工厂按需创建独立浏览器 worker。
func buildDeps(em *exporter.Emitter) func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
	logf := func(format string, args ...any) { em.Log(format, args...) }
	return func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
		if cfg.Mock {
			return exporter.Deps{API: exporter.NewMockAPI()}, nil
		}
		acfg, err := auth.ParseConfig(req.Config)
		if err != nil {
			return exporter.Deps{}, err
		}
		mgr := auth.NewManager(acfg, auth.WithLogger(auth.LogFunc(logf)))
		if _, err := mgr.Login(ctx); err != nil {
			return exporter.Deps{}, err
		}
		em.Log("登录成功，凭证已就绪")
		em.Log("浏览器（官方直出/CDP）：%s", cfg.BrowserPath)
		client := uvmpapi.NewClient(mgr, uvmpapi.WithLogger(uvmpapi.LogFunc(logf)))
		factory := func(ctx context.Context, idx int, creds render.Credentials) (exporter.Renderer, error) {
			opts := render.DefaultOptions(cfg.BrowserPath, cfg.WorkerDir(idx))
			opts.WaitTiles = cfg.WaitTiles
			opts.IdleQuiet = cfg.IdleQuiet
			opts.MinWait = cfg.MinWait
			opts.PCDWait = cfg.PCDWait
			opts.IgnoreCertificateErrors = cfg.IgnoreCertificateErrors
			opts.SPKIPins = cfg.SPKIPins
			// 内置 Electron（UVMP_BROWSER_WORKER）时带 worker 脚本位置参数并去掉
			// --headless=new；普通 Chromium 保持零值（旧行为不变）。
			opts.BrowserArgs = cfg.BrowserArgs
			opts.NoHeadless = cfg.NoHeadless
			opts.Log = func(line string) { em.Log("%s", line) }
			em.Log("渲染 Worker %d：启动（WorkDir %s）", idx, cfg.WorkerDir(idx))
			w, err := render.New(opts)
			if err != nil {
				return nil, err
			}
			if err := w.Start(ctx, creds); err != nil {
				_ = w.Close()
				return nil, err
			}
			em.Log("渲染 Worker %d：官方页面渲染器就绪（登录态注入成功）", idx)
			return w, nil
		}
		return exporter.Deps{API: client, Creds: credsSource{mgr}, NewRenderer: factory}, nil
	}
}

// credsSource auth.Manager → render.Credentials 快照（含 Generation）。
type credsSource struct {
	mgr *auth.Manager
}

func (c credsSource) Snapshot() render.Credentials {
	s := c.mgr.Snapshot()
	return render.Credentials{
		Token:        s.Token,
		RefreshToken: s.RefreshToken,
		LoginUser:    s.LoginUser,
		Generation:   int(s.Generation),
	}
}
