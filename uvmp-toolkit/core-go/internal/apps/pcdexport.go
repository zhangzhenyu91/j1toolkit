package apps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/exporter"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/uvmpapi"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/xlsx"
)

// pcdExportBackend 按派车单号清单按序导出派车单/轨迹 PDF
// （移植自 toolkit/apps/pcd_export/backend.py 的编排入口）。
//
// 编排（解析单号 → 逐号查找 → 派车单渲染 → 轨迹渲染 → 清单/未找到报告）内嵌
// core-go/internal/exporter（原 Go 并行导出工作进程的核心）承担，事件经
// exporter.Emitter 桥接到 job（log/progress/done/error 一一对应）。
// dev.mock 全流程由 exporter 自带 MockAPI 覆盖。
type pcdExportBackend struct {
	// 可注入依赖（测试 fake）；nil 用真实实现。
	StateSt  *state.Store
	LoadConf func() config.Dict
	// BuildDeps 覆盖 exporter 依赖装配（测试注入 fake API/Renderer）；nil 用真实装配。
	BuildDeps func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error)
}

const pcdExportAppID = "pcd_export"

func (b *pcdExportBackend) load() config.Dict {
	if b.LoadConf != nil {
		return b.LoadConf()
	}
	return config.Load()
}

func (b *pcdExportBackend) store() *state.Store {
	if b.StateSt != nil {
		return b.StateSt
	}
	return state.Default()
}

// saveUpload 前端 base64 上传的 xlsx 落盘到状态目录，返回路径
// （Python backend.save_upload 同口径）。
func (b *pcdExportBackend) saveUpload(xlsxB64, filename string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(xlsxB64)
	if err != nil {
		return "", fmt.Errorf("清单文件解码失败: %s", err)
	}
	return xlsx.SaveUpload(b.store().UploadsDir(), filename, data)
}

// resolveCodes 解析单号来源（codes / xlsx_b64 / xlsx_path / codes_text）
// （Python backend._resolve_codes 同口径）。
func (b *pcdExportBackend) resolveCodes(params map[string]any) ([]xlsx.CodeSeq, error) {
	if raw, ok := params["codes"].([]any); ok && len(raw) > 0 {
		out := []xlsx.CodeSeq{}
		for i, c := range raw {
			m, _ := c.(map[string]any)
			seq := xlsx.CodeSeq{
				Seq:  i + 1, // Python dict(c, seq=i+1)：以枚举序为准
				Code: config.StrField(m, "code"),
			}
			if hint := config.StrField(m, "date_hint"); hint != "" {
				h := hint
				seq.DateHint = &h
			}
			out = append(out, seq)
		}
		return out, nil
	}
	if b64 := config.StrField(params, "xlsx_b64"); b64 != "" {
		path, err := b.saveUpload(b64, config.StrField(params, "filename"))
		if err != nil {
			return nil, err
		}
		return xlsx.ExtractRunCodes(path)
	}
	if p := config.StrField(params, "xlsx_path"); p != "" {
		return xlsx.ExtractRunCodes(p)
	}
	if text := config.StrField(params, "codes_text"); text != "" {
		return xlsx.ParseCodesText(text), nil
	}
	return nil, fmt.Errorf("未提供派车单号来源（xlsx 文件或粘贴文本）")
}

// Preview 实现 PreviewBackend（Python backend.preview 同口径）。
func (b *pcdExportBackend) Preview(params map[string]any) (map[string]any, error) {
	codes, err := b.resolveCodes(params)
	if err != nil {
		return nil, err
	}
	preview := codes
	if len(preview) > 100 {
		preview = preview[:100]
	}
	return map[string]any{
		"total":     len(codes),
		"codes":     preview,
		"truncated": len(codes) > 100,
	}, nil
}

// Run 实现 Backend（Python backend.run 同口径）：解析单号后交内嵌 exporter 编排。
func (b *pcdExportBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	cfg := b.load()
	// 统一浏览器运行时自发现：在 exporter.ParseRunConfig 之前自动注入内置浏览器环境
	// （UVMP_BROWSER_BIN/WORKER/APP，显式 render.chrome/显式 env 优先不覆盖），
	// 任务结束 restore 恢复，避免污染长期进程的其他任务。
	restore := browser.EnsureExportEnv(browser.ExplicitChrome(cfg))
	defer restore()
	codes, err := b.resolveCodes(params)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("未解析到任何派车单号")
	}
	if !config.BoolField(params, "export_pcd", true) && !config.BoolField(params, "export_track", true) {
		return nil, fmt.Errorf("派车单 / 轨迹至少勾选一项")
	}

	req := exporter.Request{Config: cfg, Params: params}
	for _, c := range codes {
		req.Codes = append(req.Codes, exporter.CodeItem{
			Seq:      c.Seq,
			Code:     c.Code,
			DateHint: dateHintStr(c),
		})
	}

	secrets := cfgSecrets(cfg)
	bridge := newEventBridge(j, secrets)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j.AddCancelHook(cancel) // 取消即中断 exporter（登录/查询/渲染全部走 job ctx）

	build := b.BuildDeps
	if build == nil {
		build = realBuildDeps(bridge.logf)
	}
	err = exporter.Run(ctx, req, bridge.emitter(), build)
	if bridge.result != nil {
		return bridge.result, nil
	}
	if j.Cancelled() {
		return nil, job.ErrCancelled
	}
	if bridge.errMsg != "" {
		return nil, fmt.Errorf("%s", bridge.errMsg)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// dateHintStr date_hint 空串约定（Python goexport 协议同口径）。
func dateHintStr(c xlsx.CodeSeq) string {
	if c.DateHint == nil {
		return ""
	}
	return *c.DateHint
}

// ---------------------------------------------------------------- 事件桥

// eventBridge 把 exporter.Emitter 的 NDJSON 事件流转到 job（log/progress/done/error）。
type eventBridge struct {
	j       *job.Job
	secrets []string
	mu      sync.Mutex
	buf     []byte
	result  map[string]any
	errMsg  string
	em      *exporter.Emitter
}

func newEventBridge(j *job.Job, secrets []string) *eventBridge {
	b := &eventBridge{j: j, secrets: secrets}
	b.em = exporter.NewEmitter(&bridgeWriter{b: b})
	return b
}

func (b *eventBridge) emitter() *exporter.Emitter { return b.em }

func (b *eventBridge) logf(format string, args ...any) {
	b.j.Log(b.scrub(fmt.Sprintf(format, args...)))
}

// scrub 日志脱敏（命中密码替换为 ******，同 goexport.py _scrub）。
func (b *eventBridge) scrub(s string) string {
	for _, pw := range b.secrets {
		if pw != "" {
			s = strings.ReplaceAll(s, pw, "******")
		}
	}
	return s
}

// handleLine 处理一行 NDJSON 事件。
func (b *eventBridge) handleLine(line string) {
	var ev map[string]any
	if json.Unmarshal([]byte(line), &ev) != nil {
		return // 协议规定 stdout 仅事件，杂音忽略
	}
	switch ev["event"] {
	case "log":
		b.j.Log(b.scrub(config.StrField(ev, "line")))
	case "progress":
		b.j.SetProgress(
			config.IntField(ev, "current", 0),
			config.IntField(ev, "total", 0),
			b.scrub(config.StrField(ev, "label")))
	case "done":
		if r, ok := ev["result"].(map[string]any); ok {
			b.result = r
		} else {
			b.result = map[string]any{}
		}
	case "error":
		b.errMsg = b.scrub(config.StrField(ev, "error"))
	}
}

// bridgeWriter io.Writer：按行切分交给 handleLine。
type bridgeWriter struct{ b *eventBridge }

func (w *bridgeWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	w.b.buf = append(w.b.buf, p...)
	for {
		i := indexByte(w.b.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.b.buf[:i]))
		w.b.buf = w.b.buf[i+1:]
		if line != "" {
			w.b.handleLine(line)
		}
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// cfgSecrets 配置中可能出现的密码明文（config.json sso 与 config.ini [auth]），供日志脱敏。
func cfgSecrets(cfg config.Dict) []string {
	out := []string{}
	add := func(pw string) {
		if pw == "" {
			return
		}
		for _, s := range out {
			if s == pw {
				return
			}
		}
		out = append(out, pw)
	}
	add(config.StrField(config.MapField(cfg, "sso"), "password"))
	add(config.StrField(config.MapField(config.MapField(cfg, "_ini"), "auth"), "password"))
	return out
}

// ---------------------------------------------------------------- 依赖装配

// realBuildDeps exporter 依赖装配（cmd/uvmp-export buildDeps 同口径）：mock 走模拟数据；
// 真实路径每批 Auth Manager 登录一次（登录走 job ctx，取消覆盖登录），渲染 worker
// 工厂按需创建独立浏览器 worker。
func realBuildDeps(logf func(format string, args ...any)) func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
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
		logf("登录成功，凭证已就绪")
		logf("浏览器（官方直出/CDP）：%s", cfg.BrowserPath)
		client := uvmpapi.NewClient(mgr, uvmpapi.WithLogger(uvmpapi.LogFunc(logf)))
		factory := func(ctx context.Context, idx int, creds render.Credentials) (exporter.Renderer, error) {
			opts := render.DefaultOptions(cfg.BrowserPath, cfg.WorkerDir(idx))
			opts.WaitTiles = cfg.WaitTiles
			opts.IdleQuiet = cfg.IdleQuiet
			opts.MinWait = cfg.MinWait
			opts.PCDWait = cfg.PCDWait
			opts.IgnoreCertificateErrors = cfg.IgnoreCertificateErrors
			opts.SPKIPins = cfg.SPKIPins
			opts.BrowserArgs = cfg.BrowserArgs
			opts.NoHeadless = cfg.NoHeadless
			opts.Log = func(line string) { logf("%s", line) }
			logf("渲染 Worker %d：启动（WorkDir %s）", idx, cfg.WorkerDir(idx))
			w, err := render.New(opts)
			if err != nil {
				return nil, err
			}
			if err := w.Start(ctx, creds); err != nil {
				_ = w.Close()
				return nil, err
			}
			logf("渲染 Worker %d：官方页面渲染器就绪（登录态注入成功）", idx)
			return w, nil
		}
		return exporter.Deps{API: client, Creds: credsSource{mgr}, NewRenderer: factory}, nil
	}
}

// credsSource auth.Manager → render.Credentials 快照（含 Generation）。
type credsSource struct{ mgr *auth.Manager }

// Snapshot 实现 exporter.CredsSource。
func (c credsSource) Snapshot() render.Credentials {
	s := c.mgr.Snapshot()
	return render.Credentials{
		Token:        s.Token,
		RefreshToken: s.RefreshToken,
		LoginUser:    s.LoginUser,
		Generation:   int(s.Generation),
	}
}
