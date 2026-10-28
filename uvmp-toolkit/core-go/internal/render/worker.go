package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNotStarted Worker 尚未 Start 就调用渲染方法。
var ErrNotStarted = errors.New("render: Worker 未启动（先调用 Start）")

// Worker 官方页面直出渲染工作进程（对应 official_track.py 的 OfficialTrackRenderer）：
// 每个 Worker 独占一个 Chromium 进程，一次只渲一单（方法间互斥），并行由调用方
// 开多个 Worker 编排；不做内部全局并行。
type Worker struct {
	opts  Options
	red   *redactor
	br    *cdpBrowser
	creds Credentials

	mu        sync.Mutex
	started   bool
	closeOnce sync.Once
}

// New 创建渲染工作进程（不启动浏览器）。浏览器路径必须由调用方传入。
func New(opts Options) (*Worker, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("render: 创建 WorkDir 失败: %w", err)
	}
	red := &redactor{}
	return &Worker{opts: opts, red: red, br: newCdpBrowser(opts, red)}, nil
}

// diagPath 登录失败诊断截图路径。
func (w *Worker) diagPath() string {
	return filepath.Join(w.opts.WorkDir, "official_login_diag.png")
}

// log 输出一条脱敏日志（无回调则丢弃）。
func (w *Worker) log(format string, args ...any) {
	if w.opts.Log == nil {
		return
	}
	w.opts.Log(w.red.apply(fmt.Sprintf(format, args...)))
}

// Start 启动浏览器并完成官方页面登录。流程（移植 OfficialTrackRenderer.start）：
// 等 cube 框架加载 → 优先等页面自己走完 #home?T= 自动登录（页面完整初始化依赖它：
// 弹窗消息包/地图 AccessToken 都是登录后才就位——凭证注入跳步曾致打印弹窗
// CUBE.self.msg 未初始化、轨迹地图空白）→ 超时再注入凭证兜底 → 失败给全量诊断。
// 令牌只进 URL fragment 与 JS 注入，不进命令行、不落日志。
func (w *Worker) Start(ctx context.Context, creds Credentials) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(creds.Token) == "" {
		return errors.New("render: Credentials.Token 不能为空")
	}
	w.creds = creds
	w.red.add(creds.Token)
	w.red.add(creds.RefreshToken)

	if err := w.br.start(ctx); err != nil {
		return err
	}
	if err := w.br.navigate(ctx, IndexURL+"#home?T="+creds.Token); err != nil {
		return err
	}

	// 1) 等 cube 框架本身加载
	if ok := w.br.waitExpr(ctx, "typeof cube!=='undefined'", 60*time.Second, 0); !ok {
		w.dumpFailure(ctx, "cube 框架未加载")
		return fmt.Errorf("cube 框架未加载（页面未打开成功），诊断见 %s", w.diagPath())
	}

	// 2) 等页面自动登录完成（完整初始化；每 15s 打一次页面环境便于定位卡点）
	deadline := time.Now().Add(w.opts.WaitLogin)
	var lastLog time.Time
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := w.br.eval(ctx, "typeof cube!=='undefined' && !!cube.loginUser && !!cube.token", evalTimeout)
		if err == nil {
			if ok, isBool := v.(bool); isBool && ok {
				w.log("官方页面渲染器：页面自动登录完成（完整初始化）")
				w.started = true
				return nil
			}
		}
		if time.Since(lastLog) >= 15*time.Second {
			lastLog = time.Now()
			probe, _ := w.br.evalString(ctx, probeJS, evalTimeout)
			w.log("  [渲染器] 等待页面自动登录… 环境 %s", probe)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	// 3) 兜底：注入凭证（页面初始化可能不完整，弹窗/地图渲染可能异常）
	w.log("  [渲染器] 页面自动登录 %ds 未完成，改为注入凭证兜底", int(w.opts.WaitLogin/time.Second))
	injectJS, err := buildInjectAuthJS(creds.Token, creds.RefreshToken, creds.LoginUser)
	if err != nil {
		return fmt.Errorf("render: 组装凭证注入脚本失败: %w", err)
	}
	r, evalErr := w.br.evalString(ctx, injectJS, evalTimeout)
	if evalErr != nil || r != "ok" {
		w.dumpFailure(ctx, "凭证注入返回异常")
		if evalErr != nil {
			return fmt.Errorf("凭证注入失败: %v", evalErr)
		}
		return fmt.Errorf("凭证注入失败: %s", r)
	}
	if ready := w.br.waitExpr(ctx, "!!cube.token && !!cube.loginUser", 30*time.Second, 0); !ready {
		w.dumpFailure(ctx, "凭证注入后仍未就绪")
		return fmt.Errorf("官方系统页面登录态注入后仍未就绪，诊断见 %s", w.diagPath())
	}
	w.started = true
	return nil
}

// ApplyCredentials 页面已登录前提下换入新凭证（令牌刷新后使用；Generation 同步更新）。
func (w *Worker) ApplyCredentials(ctx context.Context, creds Credentials) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return ErrNotStarted
	}
	if strings.TrimSpace(creds.Token) == "" {
		return errors.New("render: Credentials.Token 不能为空")
	}
	injectJS, err := buildInjectAuthJS(creds.Token, creds.RefreshToken, creds.LoginUser)
	if err != nil {
		return fmt.Errorf("render: 组装凭证注入脚本失败: %w", err)
	}
	r, evalErr := w.br.evalString(ctx, injectJS, evalTimeout)
	if evalErr != nil || r != "ok" {
		if evalErr != nil {
			return fmt.Errorf("凭证注入失败: %v", evalErr)
		}
		return fmt.Errorf("凭证注入失败: %s", r)
	}
	if ready := w.br.waitExpr(ctx, "!!cube.token && !!cube.loginUser", 30*time.Second, 0); !ready {
		return errors.New("官方系统页面登录态注入后仍未就绪")
	}
	w.creds = creds
	w.red.add(creds.Token)
	w.red.add(creds.RefreshToken)
	return nil
}

// Credentials 返回当前凭证（含 Generation），供调用方记录。
func (w *Worker) Credentials() Credentials {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.creds
}

// dumpFailure 登录失败现场：cube/token/loginUser 各自状态 + 整页截图
// （对应 OfficialTrackRenderer._dump_failure）。
func (w *Worker) dumpFailure(ctx context.Context, why string) {
	evalOr := func(expr string) any {
		v, err := w.br.eval(ctx, expr, evalTimeout)
		if err != nil {
			return nil
		}
		return v
	}
	p := struct {
		Why        string `json:"why"`
		TypeofCube any    `json:"typeof_cube"`
		HasToken   any    `json:"has_token"`
		HasLogin   any    `json:"has_loginUser"`
		ReadyState any    `json:"readyState"`
		Title      any    `json:"title"`
		URL        string `json:"url"`
	}{
		Why:        why,
		TypeofCube: evalOr("typeof cube"),
		HasToken:   evalOr("typeof cube!=='undefined' ? !!cube.token : null"),
		HasLogin:   evalOr("typeof cube!=='undefined' ? !!cube.loginUser : null"),
		ReadyState: evalOr("document.readyState"),
		Title:      evalOr("document.title"),
	}
	if u, err := w.br.evalString(ctx, "location.href", evalTimeout); err == nil {
		p.URL = trimForDiag(u, 160) // URL 可能带 #home?T=<token>，经 red.apply 再出日志
	}
	if b, err := json.Marshal(p); err == nil {
		w.log("  [渲染器诊断] %s", string(b))
	}
	data, err := w.br.capturePNG(ctx)
	if err == nil && len(data) > 0 {
		_ = os.WriteFile(w.diagPath(), data, 0o644)
	}
}

// RenderPCDPDF 逐单渲染官方「派车单批量打印」弹窗并出 PDF（移植 render_pcd_pdf_one）：
// 打印模板经 ko component 加载，先等「模板已注册或加载器就绪」再开弹窗；提取打印区 DOM
// 后在同浏览器新标签页 printToPDF。失败隔离到单，诊断包落 diagDir（空则随 PDF）。
func (w *Worker) RenderPCDPDF(ctx context.Context, item map[string]any, outPDF, tag, diagDir string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return ErrNotStarted
	}
	if diagDir == "" {
		diagDir = filepath.Dir(outPDF)
	}

	tplReady := w.br.waitExpr(ctx, tplReadyJS, 60*time.Second, 0)
	probe, _ := w.br.evalString(ctx, probeJS, evalTimeout)
	w.log("  [渲染诊断] 打印组件/加载器就绪=%v；页面环境 %s", tplReady, probe)

	openJS, err := buildOpenPCDPrintJS([]map[string]any{item})
	if err != nil {
		return fmt.Errorf("render: 组装打印弹窗脚本失败: %w", err)
	}
	r, evalErr := w.br.evalString(ctx, openJS, 60*time.Second)
	if evalErr != nil || r != "ok" {
		if err := sleepCtx(ctx, 3*time.Second); err != nil { // 加载竞态兜底：重试一次
			return err
		}
		r, evalErr = w.br.evalString(ctx, openJS, 60*time.Second)
	}
	if evalErr != nil || r != "ok" {
		w.br.dumpDiag(ctx, diagDir, tagOr(tag, "openfail"))
		if evalErr != nil {
			return fmt.Errorf("打开官方派车单打印弹窗失败: %v", evalErr)
		}
		return fmt.Errorf("打开官方派车单打印弹窗失败: %s", r)
	}

	if ok := w.br.waitExpr(ctx, section0ReadyJS, 120*time.Second, 0); !ok {
		state, _ := w.br.evalString(ctx, printAreaStateJS, evalTimeout)
		w.log("  [渲染诊断] 弹窗状态 %s", state)
		w.br.dumpDiag(ctx, diagDir, tagOr(tag, "section0"))
		w.br.eval(ctx, closeDialogJS, evalTimeout)
		return errors.New("派车单打印区渲染超时（Section0 未出）")
	}

	// 打印区数据填充/样式就位的固定等待（config.ini [export] pcd_render_wait 可调/置 0 关闭）
	if w.opts.PCDWait > 0 {
		if err := sleepCtx(ctx, w.opts.PCDWait); err != nil {
			return err
		}
	}
	dom, evalErr := w.br.evalString(ctx, extractPrintDomJS, evalTimeout)
	if evalErr != nil || dom == "" {
		w.br.dumpDiag(ctx, diagDir, tagOr(tag, "extract"))
		return errors.New("派车单打印区提取失败")
	}
	var parsed struct {
		Head string `json:"head"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(dom), &parsed); err != nil {
		w.br.dumpDiag(ctx, diagDir, tagOr(tag, "extract"))
		return errors.New("派车单打印区提取失败")
	}
	fullHTML := buildPrintFullHTML(parsed.Head, parsed.Body)

	tabCtx, closeTab, err := w.br.newTab(ctx)
	if err != nil {
		return err
	}
	defer closeTab()
	writeJS, err := buildDocumentWriteJS(fullHTML)
	if err != nil {
		return fmt.Errorf("render: 组装打印页脚本失败: %w", err)
	}
	if _, err := evalOn(ctx, tabCtx, writeJS, evalTimeout); err != nil {
		return fmt.Errorf("写入打印页失败: %w", err)
	}
	// 样式表/文档就绪检测，替代固定 sleep；上限与派车单固定等待同档
	w.br.waitExprOn(ctx, tabCtx, stylesReadyJS, stylesWait(w.opts.PCDWait), 300*time.Millisecond)
	data, err := w.br.tabPrintPDF(ctx, tabCtx)
	if err != nil {
		return fmt.Errorf("printToPDF 无数据: %w", err)
	}
	if err := os.WriteFile(outPDF, data, 0o644); err != nil {
		return err
	}
	closeTab()

	w.br.eval(ctx, closeDialogJS, evalTimeout)
	return sleepCtx(ctx, 500*time.Millisecond)
}

// RenderRoutePNG 行程详情弹窗截图（理想样式：行程编号/里程/时速/绑定的派车单等）。
// preShotJS 为截图前注入执行的改写脚本（自动校准行程结束时间用，空串不注入）。
func (w *Worker) RenderRoutePNG(ctx context.Context, routeID, outPNG, preShotJS string) (RewriteResult, error) {
	return w.renderTrackPNG(ctx, TemplateRoute, map[string]any{"routeId": routeID}, outPNG, preShotJS)
}

// RenderPCDPNG 按派车单弹窗截图（找不到行程段时的回退）；preShotJS 口径同 RenderRoutePNG。
func (w *Worker) RenderPCDPNG(ctx context.Context, order map[string]any, outPNG, preShotJS string) (RewriteResult, error) {
	return w.renderTrackPNG(ctx, TemplatePCD, map[string]any{"runData": order}, outPNG, preShotJS)
}

// renderTrackPNG 轨迹弹窗渲染共用流程（移植 OfficialTrackRenderer._render）：
// 开弹窗 → 等内容出现 → 等地图 canvas → 固定等待下限 → 地图网络静默检测 → 时间改写 →
// 内容区截图 → 空白告警 → 关弹窗。
func (w *Worker) renderTrackPNG(ctx context.Context, templateName string, params any, outPNG, preShotJS string) (RewriteResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var result RewriteResult
	if !w.started {
		return result, ErrNotStarted
	}

	openJS, err := buildOpenDialogJS(templateName, params)
	if err != nil {
		return result, fmt.Errorf("render: 组装轨迹弹窗脚本失败: %w", err)
	}
	r, evalErr := w.br.evalString(ctx, openJS, 60*time.Second)
	if evalErr != nil || r != "ok" {
		if evalErr != nil {
			return result, fmt.Errorf("打开官方轨迹弹窗失败: %v", evalErr)
		}
		return result, fmt.Errorf("打开官方轨迹弹窗失败: %s", r)
	}
	if ok := w.br.waitExpr(ctx, dialogBodyReadyJS, w.opts.WaitDialog, 0); !ok {
		return result, errors.New("轨迹弹窗未出现")
	}
	// 等地图 canvas 出现（无轨迹点时可能一直没有，容忍）
	w.br.waitExpr(ctx, dialogCanvasReadyJS, min(40*time.Second, w.opts.WaitDialog), 0)

	// 固定等待下限：实测多数轨迹完整渲染需 30s+，下限之内不启动静默检测（防截早）
	if w.opts.MinWait > 0 {
		if err := sleepCtx(ctx, w.opts.MinWait); err != nil {
			return result, err
		}
	}
	// 瓦片/轨迹渲染完成检测：盯地图域名在途请求，静默即完成；须先见到本弹窗自己的
	// 瓦片请求（minSeen=2）；wait_tiles_s 为兜底上限
	t0 := time.Now()
	idleOK := w.br.waitNetworkIdle(ctx, w.opts.IdleQuiet, max(15*time.Second, w.opts.WaitTiles), "map.sgcc.com.cn", 2)
	status := "兜底超时"
	if idleOK {
		status = "静默完成"
	}
	w.log("  [渲染] 地图加载检测：%s（%.1fs）", status, time.Since(t0).Seconds())
	if err := sleepCtx(ctx, time.Second); err != nil { // 静默后给最后一帧渲染留一瞬
		return result, err
	}

	// 「自动校准行程结束时间」：截图前注入改写 JS；未命中不视为失败（保留官方原始时间）
	if preShotJS != "" {
		result.Enabled = true
		hits := 0
		raw, err := w.br.evalString(ctx, preShotJS, 30*time.Second)
		if err == nil && raw != "" && !strings.HasPrefix(raw, "ERR") {
			var info struct {
				Replaced int `json:"replaced"`
			}
			if json.Unmarshal([]byte(raw), &info) == nil {
				hits = info.Replaced
			}
		}
		result.Replaced = hits
		suffix := ""
		if hits == 0 {
			suffix = "（保留官方原始时间）"
		}
		w.log("  [校准] 弹窗时间改写命中 %d 处%s", hits, suffix)
	}

	rectRaw, err := w.br.evalString(ctx, rectJS, evalTimeout)
	if err != nil || rectRaw == "" {
		return result, errors.New("弹窗内容区未找到")
	}
	var box struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := json.Unmarshal([]byte(rectRaw), &box); err != nil {
		return result, errors.New("弹窗内容区未找到")
	}
	clip := clipRect{
		X:      max(0, box.X),
		Y:      max(0, box.Y),
		Width:  box.Width,
		Height: box.Height,
	}
	if err := w.br.screenshotClip(ctx, clip, outPNG); err != nil {
		return result, err
	}

	// 空白告警：内容弹窗截图通常 ≥30KB，过小多半没渲完就拍了
	if st, err := os.Stat(outPNG); err == nil && st.Size() < 30*1024 {
		w.log("  [渲染告警] 轨迹截图仅 %.1f KB，疑似空白（企业浏览器偏慢，"+
			"可调大 config.ini [export] map_tiles_wait 后重试）", float64(st.Size())/1024)
		w.br.dumpDiag(ctx, filepath.Dir(outPNG), "blank_"+blankTag(outPNG))
	}

	w.br.eval(ctx, closeDialogJS, evalTimeout)
	if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
		return result, err
	}
	return result, nil
}

// PNGToPDF 把轨迹 PNG 用 PNGWrapHTML 包版成 PDF：同浏览器新标签页 document.write 后
// printToPDF（A4 横向、preferCSSPageSize），不再外起转换进程。
func (w *Worker) PNGToPDF(ctx context.Context, pngPath, outPDF string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return ErrNotStarted
	}
	data, err := os.ReadFile(pngPath)
	if err != nil {
		return fmt.Errorf("render: 读取轨迹 PNG 失败: %w", err)
	}
	html := BuildPNGWrapHTML(PNGDataURI(data))
	tabCtx, closeTab, err := w.br.newTab(ctx)
	if err != nil {
		return err
	}
	defer closeTab()
	writeJS, err := buildDocumentWriteJS(html)
	if err != nil {
		return fmt.Errorf("render: 组装包版页脚本失败: %w", err)
	}
	if _, err := evalOn(ctx, tabCtx, writeJS, evalTimeout); err != nil {
		return fmt.Errorf("写入包版页失败: %w", err)
	}
	w.br.waitExprOn(ctx, tabCtx, stylesReadyJS, stylesWait(w.opts.PCDWait), 300*time.Millisecond)
	pdf, err := w.br.tabPrintPDF(ctx, tabCtx)
	if err != nil {
		return fmt.Errorf("printToPDF 无数据: %w", err)
	}
	return os.WriteFile(outPDF, pdf, 0o644)
}

// Close 关闭浏览器并释放工作进程（可重复调用）。
func (w *Worker) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.br != nil {
			w.br.close()
		}
		w.started = false
	})
	return nil
}

// stylesWait 包版/打印页样式就绪检测的等待上限（与 Python max(1, int(pcd_wait_s)) 同档）。
func stylesWait(pcdWait time.Duration) time.Duration {
	if pcdWait < time.Second {
		return time.Second
	}
	return pcdWait
}

// tagOr 诊断文件名序号兜底（Python tag or "openfail" 口径）。
func tagOr(tag, fallback string) string {
	if tag != "" {
		return tag
	}
	return fallback
}

// blankTag 空白告警诊断文件名序号（PNG 主名前 24 字符）。
func blankTag(outPNG string) string {
	base := filepath.Base(outPNG)
	if e := filepath.Ext(base); e != "" {
		base = base[:len(base)-len(e)]
	}
	return trimForDiag(base, 24)
}

// sleepCtx 可取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
