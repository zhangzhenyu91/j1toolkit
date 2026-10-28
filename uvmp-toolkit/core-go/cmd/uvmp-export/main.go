// uvmp-export 派车单/轨迹并行导出工作进程（Python goexport.py 的固定协议）：
//
//	启动  uvmp-export run
//	stdin 首行请求  {"config":<config.load dict>,"params":<pcd_export 参数>,
//	                 "codes":[{seq,code,date_hint}]}
//	      之后可收  {"command":"cancel"}
//	stdout 仅 NDJSON 事件：log / progress / done / error
//
// 密码只经 stdin 请求体传入，不进命令行、不落日志；真实路径每批 Auth Manager
// 登录一次，渲染 worker 各自独立 WorkDir（状态目录 browser-workers/job）。
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/exporter"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "用法: uvmp-export run（stdin 首行请求，stdout NDJSON 事件）")
		os.Exit(2)
	}
	in := bufio.NewReader(os.Stdin)
	em := exporter.NewEmitter(os.Stdout)
	req, err := exporter.ReadRequest(in)
	if err != nil {
		em.Error(err.Error())
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go exporter.PumpCancel(in, cancel)

	if err := exporter.Run(ctx, req, em, buildDeps(em)); err != nil {
		os.Exit(1)
	}
}

// buildDeps 校验通过后构建运行依赖：mock 走模拟数据；真实路径每批
// Auth Manager 登录一次（token 模式直接用手动三件套），渲染 worker 工厂
// 按需创建独立浏览器 worker。
func buildDeps(em *exporter.Emitter) func(req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
	logf := func(format string, args ...any) { em.Log(format, args...) }
	return func(req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
		if cfg.Mock {
			return exporter.Deps{API: exporter.NewMockAPI()}, nil
		}
		acfg, err := auth.ParseConfig(req.Config)
		if err != nil {
			return exporter.Deps{}, err
		}
		mgr := auth.NewManager(acfg, auth.WithLogger(auth.LogFunc(logf)))
		if _, err := mgr.Login(context.Background()); err != nil {
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
