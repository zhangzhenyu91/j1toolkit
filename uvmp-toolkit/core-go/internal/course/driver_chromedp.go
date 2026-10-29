package course

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
)

// ConnectOptions 受控浏览器连接参数（课程引擎入口）。
type ConnectOptions struct {
	// CDPURL 本机浏览器调试地址（仅回环，browser.ValidateLoopbackURL 同口径）。
	CDPURL string
	// Timeout 单步页面等待超时（≤0 取 120s）。
	Timeout time.Duration
	// Log 连接期日志回调。
	Log func(msg string)
}

// CDPBrowser chromedp 驱动的受控浏览器会话：chromedp 管页面驱动（评估/鼠标/截图，
// 与 internal/render 同套 chromedp+cdproto），cdpHub 管响应监视与目标探测。
// 只连接本机 CDP；Close 只断开连接并关闭自己打开的学习弹窗，不关课程列表/浏览器。
type CDPBrowser struct {
	opts    ConnectOptions
	alloc   context.Context
	cancel  context.CancelFunc
	hub     *cdpHub
	monitor *ResponseMonitor
}

// Connect 连接本机受控浏览器（internal/browser ModeCourse 启动的实例或已开调试端口的浏览器）。
func Connect(ctx context.Context, opts ConnectOptions) (*CDPBrowser, error) {
	norm, _, err := browser.ValidateLoopbackURL(opts.CDPURL)
	if err != nil {
		return nil, err
	}
	opts.CDPURL = norm
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	monitor := NewResponseMonitor()
	hub, err := newCDPHub(ctx, opts.CDPURL, monitor, opts.Log)
	if err != nil {
		return nil, err
	}
	// 会话生命周期与 Close 绑定，不随调用方 ctx 起落（传入 ctx 只约束「建立连接」这一步；
	// 否则调用方的限时 ctx 到期会连带拆掉 CDP 会话，运行中途全部挂死——实测踩坑）
	alloc, cancel := remote.NewAllocator(context.WithoutCancel(ctx), opts.CDPURL)
	return &CDPBrowser{opts: opts, alloc: alloc, cancel: cancel, hub: hub, monitor: monitor}, nil
}

// Monitor 响应监视器。
func (b *CDPBrowser) Monitor() *ResponseMonitor { return b.monitor }

// Close 断开会话（释放弹窗上下文；不关闭受控浏览器）。
func (b *CDPBrowser) Close() {
	b.hub.Close()
	b.cancel()
}

// listPageTarget /json/list 一条 page 目标。
type listPageTarget struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// listTargets 拉取浏览器当前目标（/json/list）。
func (b *CDPBrowser) listTargets(ctx context.Context) ([]listPageTarget, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.opts.CDPURL+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("course: 读取浏览器目标失败: %w", err)
	}
	defer resp.Body.Close()
	var out []listPageTarget
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("course: 解析浏览器目标失败: %w", err)
	}
	return out, nil
}

// Tabs 目标快照（find_list_page 识别用；可见性经 CDP 探测，失败视为不可见）。
func (b *CDPBrowser) Tabs(ctx context.Context) ([]TabInfo, error) {
	raw, err := b.listTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TabInfo, 0, len(raw))
	for _, t := range raw {
		if t.Type != "page" {
			continue
		}
		visible := false
		if s, err := b.hub.evalString(ctx, t.ID, "document.visibilityState"); err == nil {
			visible = s == "visible"
		}
		title := t.Title
		if title == "" {
			title = "(无法读取标题)"
		}
		out = append(out, TabInfo{ID: t.ID, URL: t.URL, Title: title, Visible: visible})
	}
	return out, nil
}

// BodyText 页面正文文本（页面内容识别用）。
func (b *CDPBrowser) BodyText(ctx context.Context, id string) (string, error) {
	return b.hub.evalString(ctx, id, "document.body ? (document.body.innerText || '') : ''")
}

// AttachListPage 接管标签页为课程列表页（置前台；取消时只分离不关页）。
func (b *CDPBrowser) AttachListPage(ctx context.Context, id string) (ListPage, error) {
	tabCtx, tabCancel := chromedp.NewContext(b.alloc,
		chromedp.WithTargetID(target.ID(id)), chromedp.WithDetachOnCancel())
	if err := initTarget(ctx, tabCtx); err != nil {
		tabCancel()
		return nil, err
	}
	if err := runOn(ctx, tabCtx, b.opts.Timeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(cctx, t, page.BringToFront, cdp.Empty{})
		return err
	})); err != nil {
		// 置前台失败不阻断接管（后台窗口也允许跑）
		if b.opts.Log != nil {
			b.opts.Log(fmt.Sprintf("置前台失败（继续接管）：%s", trimErr(err)))
		}
	}
	return &cdpListPage{browser: b, id: id, ctx: tabCtx, cancel: tabCancel}, nil
}

// initTarget 首次 Do 触发目标附加（用与标签页同生命周期的 tabCtx 直接跑，
// 口径同 internal/render initTab：不套临时超时 ctx，避免事件循环随超时退出）。
// 附加后做前台化三件套（置前 + 焦点模拟 + 活跃生命周期，Playwright 同口径）：
// Electron 弹窗默认后台节流，不激活时输入事件与截图每步要等约 5 秒（实测）。
func initTarget(jobCtx, tabCtx context.Context) error {
	done := make(chan error, 1)
	go func() {
		done <- chromedp.Do(tabCtx, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
			if _, err := cdp.Call(cctx, t, page.Enable, page.EnableParams{}); err != nil {
				return err
			}
			if _, err := cdp.Call(cctx, t, emulation.SetFocusEmulationEnabled, emulation.SetFocusEmulationEnabledParams{Enabled: true}); err != nil {
				return err
			}
			if _, err := cdp.Call(cctx, t, page.SetWebLifecycleState, page.SetWebLifecycleStateParams{State: page.SetWebLifecycleStateStateActive}); err != nil {
				return err
			}
			_, err := cdp.Call(cctx, t, page.BringToFront, cdp.Empty{})
			return err
		}))
	}()
	select {
	case err := <-done:
		return err
	case <-jobCtx.Done():
		return jobCtx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("course: 初始化标签页超时")
	}
}

// evalOn 在标签页上下文求值（超时/取消只中止本次调用）。
func evalOn(jobCtx, tabCtx context.Context, expr string, timeout time.Duration) (any, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(jobCtx, cancel)
	defer stop()
	return chromedp.Run(tctx, chromedp.Evaluate[any](expr))
}

// evalJSONSimple 执行表达式并取值：
//   - 结构化结果：表达式返回 JSON.stringify(...) 文本，解到 out；
//   - 纯字符串动作（ok/missing 等）：out 为 *string 时直接取值。
func evalJSONSimple(jobCtx, tabCtx context.Context, expr string, out any) error {
	raw, err := evalOn(jobCtx, tabCtx, expr, 30*time.Second)
	if err != nil {
		return err
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("course: 页面脚本未返回字符串")
	}
	if out == nil {
		return nil
	}
	if str, isStr := out.(*string); isStr {
		*str = strings.Trim(s, `"`)
		return nil
	}
	if s == "" {
		return fmt.Errorf("course: 页面脚本无返回")
	}
	return json.Unmarshal([]byte(s), out)
}

// waitExprOn 轮询直到表达式为 true（trial.py _wait_for_function 口径）。
func waitExprOn(jobCtx, tabCtx context.Context, expr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		v, err := evalOn(jobCtx, tabCtx, expr, 30*time.Second)
		if err == nil {
			if ok, isBool := v.(bool); isBool && ok {
				return nil
			}
		}
		if err2 := jobCtx.Err(); err2 != nil {
			return err2
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待页面条件超时（%.0f 秒）", timeout.Seconds())
		}
		select {
		case <-jobCtx.Done():
			return jobCtx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------- 列表页

type cdpListPage struct {
	browser *CDPBrowser
	id      string
	ctx     context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	closed bool
}

func (p *cdpListPage) ID() string  { return p.id }
func (p *cdpListPage) URL() string { return currentURL(p.ctx, p.id) }

func (p *cdpListPage) Closed() bool {
	select {
	case <-p.ctx.Done():
		return true
	default:
		return false
	}
}

func (p *cdpListPage) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.cancel() // WithDetachOnCancel：只分离不关用户列表页
	return nil
}

func (p *cdpListPage) Screenshot(path string) error {
	return captureShot(p.ctx, path)
}

// reloadListJS 识别「课程设置」页签：未激活点页签（返回 clicked），否则整页刷新（reload）。
const reloadListJS = `(() => {
  const tabs = Array.from(document.querySelectorAll('.el-tabs__item')).filter(e => e.offsetParent !== null);
  const tab = tabs.find(e => /^\s*课程设置\s*$/.test(e.textContent || ''));
  if (tab && !String(tab.className || '').includes('is-active')) { tab.click(); return 'clicked'; }
  return 'reload';
})()`

func (p *cdpListPage) ReloadList(ctx context.Context) (*CoursePage, error) {
	mark := p.browser.monitor.Seq()
	var action string
	if err := evalJSONSimple(ctx, p.ctx, reloadListJS, &action); err != nil {
		return nil, err
	}
	if action == "clicked" {
		return p.waitCoursePage(ctx, mark)
	}
	if err := runOn(ctx, p.ctx, p.browser.opts.Timeout, chromedp.Reload()); err != nil {
		return nil, err
	}
	return p.waitCoursePage(ctx, mark)
}

// waitCoursePage 等 course/page 响应并解析（trial.py _expect_response + parse_course_page）。
func (p *cdpListPage) waitCoursePage(ctx context.Context, mark int) (*CoursePage, error) {
	payload, err := p.browser.monitor.WaitSuccess(ctx, p.id, CoursePageAPI, mark, p.browser.opts.Timeout, "课程列表")
	if err != nil {
		return nil, err
	}
	return ParseCoursePage(payload)
}

const resetBtnJS = `(() => {
  const btns = Array.from(document.querySelectorAll('button')).filter(e => e.offsetParent !== null);
  const btn = btns.find(e => /^\s*重置\s*$/.test(e.textContent || ''));
  if (!btn) return 'missing';
  btn.click();
  return 'ok';
})()`

func (p *cdpListPage) ResetFilters(ctx context.Context) (*CoursePage, error) {
	mark := p.browser.monitor.Seq()
	var action string
	if err := evalJSONSimple(ctx, p.ctx, resetBtnJS, &action); err != nil {
		return nil, err
	}
	if action != "ok" {
		return nil, fmt.Errorf("未找到课程设置区的「重置」按钮，无法保证扫描全部课程")
	}
	return p.waitCoursePage(ctx, mark)
}

const jumpPageJSFmt = `(() => {
  const jump = Array.from(document.querySelectorAll('.el-pagination'))
    .filter(pager => pager.offsetParent !== null)
    .map(pager => pager.querySelector('.el-pagination__jump input'))
    .find(Boolean);
  if (!jump) return 'missing';
  jump.focus();
  jump.value = '%d';
  jump.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', bubbles: true}));
  jump.dispatchEvent(new KeyboardEvent('keyup', {key: 'Enter', bubbles: true}));
  return 'ok';
})()`

func (p *cdpListPage) JumpToPage(ctx context.Context, page int) (*CoursePage, error) {
	mark := p.browser.monitor.Seq()
	var action string
	js := fmt.Sprintf(jumpPageJSFmt, page)
	if err := evalJSONSimple(ctx, p.ctx, js, &action); err != nil {
		return nil, err
	}
	if action != "ok" {
		return nil, fmt.Errorf("未找到分页器的页码输入框，无法跳回续刷页")
	}
	data, err := p.waitCoursePage(ctx, mark)
	if err != nil {
		return nil, err
	}
	if err := p.WaitPageNumber(ctx, page); err != nil {
		return nil, err
	}
	return data, nil
}

const nextPageJS = `(() => {
  const btn = Array.from(document.querySelectorAll('.el-pagination'))
    .filter(pager => pager.offsetParent !== null)
    .map(pager => pager.querySelector('.btn-next'))
    .find(Boolean);
  if (!btn) return 'missing';
  if (btn.disabled) return 'disabled';
  btn.click();
  return 'ok';
})()`

func (p *cdpListPage) NextPage(ctx context.Context, page int) (*CoursePage, error) {
	mark := p.browser.monitor.Seq()
	var action string
	if err := evalJSONSimple(ctx, p.ctx, nextPageJS, &action); err != nil {
		return nil, err
	}
	if action != "ok" {
		return nil, fmt.Errorf("课程总数显示仍有下一页，但分页器下一页不可用")
	}
	data, err := p.waitCoursePage(ctx, mark)
	if err != nil {
		return nil, err
	}
	if err := p.WaitPageNumber(ctx, page); err != nil {
		return nil, err
	}
	return data, nil
}

// WaitPageNumber 等分页器高亮页码（trial.py _wait_page_number）。
func (p *cdpListPage) WaitPageNumber(ctx context.Context, n int) error {
	js := fmt.Sprintf(`(() => {
	  const target = %d;
	  return Array.from(document.querySelectorAll('.el-pagination')).some(pager => {
	    if (pager.offsetParent === null) return false;
	    const active = pager.querySelector('.el-pager li.active');
	    return Boolean(active) && Number(active.textContent.trim()) === target;
	  });
	})()`, n)
	return waitExprOn(ctx, p.ctx, js, p.browser.opts.Timeout)
}

// rowWaitJS 表格行就绪判定（trial.py _course_row 的 wait_for_function）。
const rowWaitJSFmt = `(() => {
  const expectedCount = %d, expectedIndex = %d, expectedName = %s;
  const tables = Array.from(document.querySelectorAll('.el-table')).filter(t => t.offsetParent !== null);
  const table = tables.find(candidate => {
    const header = candidate.querySelector('.el-table__header');
    const text = header ? header.textContent : '';
    return text.includes('课程名称') && text.includes('学习进度') && text.includes('进入学习');
  });
  if (!table) return false;
  const body = table.querySelector('.el-table__body-wrapper');
  if (!body) return false;
  const rows = body.querySelectorAll('tbody tr.el-table__row');
  if (rows.length !== expectedCount || !rows[expectedIndex]) return false;
  const actual = (rows[expectedIndex].innerText || '').replace(/\s+/g, ' ').trim();
  return !expectedName || actual.includes(expectedName);
})()`

// rowFetchJS 取行文本/定位错误（行数不一致或缺表格时报结构化错误）。
const rowFetchJSFmt = `(() => {
  const expectedCount = %d, expectedIndex = %d;
  const tables = Array.from(document.querySelectorAll('.el-table')).filter(t => t.offsetParent !== null);
  const table = tables.find(candidate => {
    const header = candidate.querySelector('.el-table__header');
    const text = header ? header.textContent : '';
    return text.includes('课程名称') && text.includes('学习进度') && text.includes('进入学习');
  });
  if (!table) return JSON.stringify({error: 'no_table'});
  const body = table.querySelector('.el-table__body-wrapper');
  const rows = body ? body.querySelectorAll('tbody tr.el-table__row') : [];
  if (rows.length !== expectedCount) return JSON.stringify({error: 'count_mismatch', dom: rows.length});
  if (!rows[expectedIndex]) return JSON.stringify({error: 'no_row'});
  return JSON.stringify({rowText: rows[expectedIndex].innerText || ''});
})()`

func (p *cdpListPage) WaitRow(ctx context.Context, index, pageSize int, expectedName string) (string, error) {
	waitJS := fmt.Sprintf(rowWaitJSFmt, pageSize, index, jsString(expectedName))
	if err := waitExprOn(ctx, p.ctx, waitJS, p.browser.opts.Timeout); err != nil {
		return "", err
	}
	var res struct {
		Error string `json:"error"`
		Dom   int    `json:"dom"`
		Row   string `json:"rowText"`
	}
	js := fmt.Sprintf(rowFetchJSFmt, pageSize, index)
	if err := evalJSONSimple(ctx, p.ctx, js, &res); err != nil {
		return "", err
	}
	switch res.Error {
	case "no_table":
		return "", fmt.Errorf("未找到包含课程名称/学习进度/进入学习的课程表格")
	case "count_mismatch":
		return "", fmt.Errorf("课程表格行数与接口不一致：DOM %d，接口 %d", res.Dom, pageSize)
	case "no_row":
		return "", fmt.Errorf("课程表格行数与接口不一致：DOM %d，接口 %d", res.Dom, pageSize)
	}
	return res.Row, nil
}

const enterLearningJSFmt = `(() => {
  const rowIndex = %d;
  const tables = Array.from(document.querySelectorAll('.el-table')).filter(t => t.offsetParent !== null);
  const table = tables.find(candidate => {
    const header = candidate.querySelector('.el-table__header');
    const text = header ? header.textContent : '';
    return text.includes('课程名称') && text.includes('学习进度') && text.includes('进入学习');
  });
  if (!table) return 'no_table';
  const body = table.querySelector('.el-table__body-wrapper');
  const rows = body ? body.querySelectorAll('tbody tr.el-table__row') : [];
  if (!rows[rowIndex]) return 'no_row';
  const row = rows[rowIndex];
  const hits = Array.from(row.querySelectorAll('*'))
    .filter(e => e.children.length === 0 && (e.textContent || '').trim() === '进入学习');
  const btn = hits.find(e => e.offsetParent !== null);
  if (!btn) return 'no_button';
  btn.click();
  return 'ok';
})()`

func (p *cdpListPage) EnterLearning(ctx context.Context, index int) (LearnPage, error) {
	// 弹窗基线：点击前目标集（弹窗=点击后新增 page 目标，trial.py _expect_popup 同口径）
	raw, err := p.browser.listTargets(ctx)
	if err != nil {
		return nil, err
	}
	baseline := map[string]bool{}
	for _, t := range raw {
		if t.Type == "page" {
			baseline[t.ID] = true
		}
	}
	var action string
	js := fmt.Sprintf(enterLearningJSFmt, index)
	if err := evalJSONSimple(ctx, p.ctx, js, &action); err != nil {
		return nil, err
	}
	if action != "ok" {
		return nil, fmt.Errorf("课程行没有可用的「进入学习」入口")
	}
	newID, err := p.browser.hub.waitNewPage(ctx, baseline, p.browser.opts.Timeout)
	if err != nil {
		return nil, err
	}
	tabCtx, tabCancel := chromedp.NewContext(p.browser.alloc, chromedp.WithTargetID(target.ID(newID)))
	if err := initTarget(ctx, tabCtx); err != nil {
		tabCancel()
		return nil, err
	}
	return &cdpLearnPage{browser: p.browser, id: newID, ctx: tabCtx, cancel: tabCancel}, nil
}

// ---------------------------------------------------------------- 学习弹窗

type cdpLearnPage struct {
	browser *CDPBrowser
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
}

func (p *cdpLearnPage) ID() string  { return p.id }
func (p *cdpLearnPage) URL() string { return currentURL(p.ctx, p.id) }

func (p *cdpLearnPage) Closed() bool {
	select {
	case <-p.ctx.Done():
		return true
	default:
		return false
	}
}

// Close 只关闭本学习弹窗（chromedp 取消即关目标；trial.py close_popup 同口径）。
func (p *cdpLearnPage) Close() error {
	p.cancel()
	return nil
}

func (p *cdpLearnPage) Screenshot(path string) error {
	return captureShot(p.ctx, path)
}

func (p *cdpLearnPage) WaitWareTree(ctx context.Context, n int) error {
	js := fmt.Sprintf(`document.querySelectorAll('.el-tree .nameClass').length === %d`, n)
	if err := waitExprOn(ctx, p.ctx, js, p.browser.opts.Timeout); err != nil {
		var got struct {
			Count int `json:"count"`
		}
		_ = evalJSONSimple(ctx, p.ctx, `JSON.stringify({count: document.querySelectorAll('.el-tree .nameClass').length})`, &got)
		return fmt.Errorf("课件目录数量与接口不一致：DOM %d，接口 %d", got.Count, n)
	}
	return nil
}

const clickTreeJSFmt = `(() => {
  const items = document.querySelectorAll('.el-tree .nameClass');
  if (!items[%d]) return 'missing';
  items[%d].click();
  return 'ok';
})()`

func (p *cdpLearnPage) ClickTreeItem(ctx context.Context, index int) error {
	var action string
	js := fmt.Sprintf(clickTreeJSFmt, index, index)
	if err := evalJSONSimple(ctx, p.ctx, js, &action); err != nil {
		return err
	}
	if action != "ok" {
		return fmt.Errorf("课件目录第 %d 项不存在", index+1)
	}
	return nil
}

const videoReadyJS = `Array.from(document.querySelectorAll('video')).some(video =>
  video.offsetParent !== null && video.readyState >= 1 &&
  Number.isFinite(video.duration) && video.duration > 0)`

func (p *cdpLearnPage) WaitVideoReady(ctx context.Context) error {
	return waitExprOn(ctx, p.ctx, videoReadyJS, p.browser.opts.Timeout)
}

const installEndedHookJS = `(() => {
  const video = Array.from(document.querySelectorAll('video')).find(v => v.offsetParent !== null);
  if (!video) return 'missing';
  video.__courseAutoplayEnded = Boolean(video.ended);
  if (!video.__courseAutoplayWatched) {
    video.addEventListener('ended', () => { video.__courseAutoplayEnded = true; });
    video.__courseAutoplayWatched = true;
  }
  return 'ok';
})()`

func (p *cdpLearnPage) InstallEndedHook(ctx context.Context) error {
	var action string
	if err := evalJSONSimple(ctx, p.ctx, installEndedHookJS, &action); err != nil {
		return err
	}
	if action != "ok" {
		return fmt.Errorf("未找到可见 video 元素")
	}
	return nil
}

const videoStateJS = `JSON.stringify((() => {
  const video = Array.from(document.querySelectorAll('video')).find(v => v.offsetParent !== null);
  if (!video) return {error: 'missing'};
  return {
    current: video.currentTime,
    duration: video.duration,
    ended: Boolean(video.ended || video.__courseAutoplayEnded)
  };
})())`

func (p *cdpLearnPage) VideoState(ctx context.Context) (VideoState, error) {
	var res struct {
		Error    string  `json:"error"`
		Current  float64 `json:"current"`
		Duration float64 `json:"duration"`
		Ended    bool    `json:"ended"`
	}
	if err := evalJSONSimple(ctx, p.ctx, videoStateJS, &res); err != nil {
		return VideoState{}, err
	}
	if res.Error != "" {
		return VideoState{}, fmt.Errorf("未找到可见 video 元素")
	}
	return VideoState{Current: res.Current, Duration: res.Duration, Ended: res.Ended}, nil
}

const sliderBoxJS = `JSON.stringify((() => {
  let slider = document.querySelector('[role=slider][aria-label="进度小节"]');
  if (!slider || slider.offsetParent === null) {
    slider = Array.from(document.querySelectorAll('.vjs-progress-control [role="slider"]'))
      .find(e => e.offsetParent !== null);
  }
  if (!slider) return {error: 'missing'};
  const b = slider.getBoundingClientRect();
  return {x: b.x, y: b.y, width: b.width, height: b.height};
})())`

func (p *cdpLearnPage) DragProgress(ctx context.Context, fromRatio, toRatio float64) error {
	var box struct {
		Error  string  `json:"error"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := evalJSONSimple(ctx, p.ctx, sliderBoxJS, &box); err != nil {
		return err
	}
	if box.Error != "" || box.Width <= 2 || box.Height <= 0 {
		return fmt.Errorf("视频进度条尺寸异常，无法拖动")
	}
	if fromRatio < 0 {
		fromRatio = 0
	}
	if fromRatio > 0.99 {
		fromRatio = 0.99
	}
	if toRatio < 0 {
		toRatio = 0
	}
	if toRatio > 1 {
		toRatio = 1
	}
	startX := box.X + clampF(1.0, box.Width-2.0, box.Width*fromRatio)
	targetX := box.X + mathMin(box.Width-1.0, box.Width*toRatio)
	y := box.Y + box.Height/2
	return runOn(ctx, p.ctx, p.browser.opts.Timeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		// 协议口径：mouseMoved 不带 button/clickCount，仅反映按住的按钮位；press/release 才有 clickCount
		send := func(typ input.DispatchMouseEventType, x float64, button input.MouseButton, buttons, clickCount int64) error {
			ev := input.DispatchMouseEventParams{Type: typ, X: x, Y: y, Button: button, Buttons: buttons, ClickCount: clickCount}
			_, err := cdp.Call(cctx, t, input.DispatchMouseEvent, ev)
			return err
		}
		none := input.MouseButton("none")
		if err := send(input.DispatchMouseEventTypeMouseMoved, startX, none, 0, 0); err != nil {
			return err
		}
		if err := send(input.DispatchMouseEventTypeMousePressed, startX, input.MouseButtonLeft, 1, 1); err != nil {
			return err
		}
		const steps = 12
		for i := 1; i <= steps; i++ {
			x := startX + (targetX-startX)*float64(i)/steps
			if err := send(input.DispatchMouseEventTypeMouseMoved, x, none, 1, 0); err != nil {
				return err
			}
		}
		return send(input.DispatchMouseEventTypeMouseReleased, targetX, input.MouseButtonLeft, 0, 1)
	}))
}

const playJS = `(() => {
  const video = Array.from(document.querySelectorAll('video')).find(v => v.offsetParent !== null);
  if (video) {
    try {
      const p = video.play();
      if (p && typeof p.catch === 'function') p.catch(() => {});
      return 'ok';
    } catch (e) { /* 回退点击播放键 */ }
  }
  const play = Array.from(document.querySelectorAll('.vjs-play-control')).find(e => e.offsetParent !== null);
  if (play) { play.click(); return 'ok'; }
  return 'fail';
})()`

func (p *cdpLearnPage) Play(ctx context.Context) error {
	var action string
	if err := evalJSONSimple(ctx, p.ctx, playJS, &action); err != nil {
		return err
	}
	if action != "ok" {
		return fmt.Errorf("视频暂停且无法恢复播放")
	}
	return nil
}

const videoEndedJS = `Array.from(document.querySelectorAll('video')).some(video =>
  video.offsetParent !== null && Boolean(video.ended || video.__courseAutoplayEnded))`

func (p *cdpLearnPage) WaitVideoEnded(ctx context.Context, timeout time.Duration) error {
	return waitExprOn(ctx, p.ctx, videoEndedJS, timeout)
}

const audioReadyJS = `Array.from(document.querySelectorAll('audio')).some(audio =>
  audio.offsetParent !== null && audio.readyState >= 1)`

func (p *cdpLearnPage) WaitAudioReady(ctx context.Context) error {
	return waitExprOn(ctx, p.ctx, audioReadyJS, p.browser.opts.Timeout)
}

const frameSrcJSFmt = `JSON.stringify((() => {
  const el = document.querySelector('%s');
  if (!el) return {error: 'missing'};
  return {src: el.getAttribute('src') || ''};
})())`

const frameReadyJS = `(() => {
  const body = document.body;
  if (!body) return false;
  const images = Array.from(document.images);
  if (images.some(img => img.complete && img.naturalWidth > 0)) return true;
  if (document.querySelector('canvas,embed,object')) return true;
  const text = (body.innerText || '').trim();
  return images.length === 0 && text.length > 0 && !/^(加载中|loading)/i.test(text);
})()`

func (p *cdpLearnPage) WaitIframeLoaded(ctx context.Context, selector string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = p.browser.opts.Timeout
	}
	var src struct {
		Error string `json:"error"`
		Src   string `json:"src"`
	}
	js := fmt.Sprintf(frameSrcJSFmt, selector)
	if err := waitExprOn(ctx, p.ctx, fmt.Sprintf(`document.querySelector('%s') !== null`, selector), timeout); err != nil {
		return err
	}
	if err := evalJSONSimple(ctx, p.ctx, js, &src); err != nil {
		return err
	}
	if src.Error != "" {
		return fmt.Errorf("未找到课件预览 iframe：%s", selector)
	}
	expected := ""
	if src.Src != "" {
		expected = joinURL(p.URL(), src.Src)
	}
	deadline := time.Now().Add(timeout)
	var frameID cdp.FrameID
	for {
		fid, err := p.findFrame(ctx, expected)
		if err == nil {
			frameID = fid
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("课件预览 iframe 尚未建立")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	// 帧内容就绪（trial.py _wait_frame_loaded 的内容判定）
	for {
		ok, err := p.frameContentReady(ctx, frameID)
		if err == nil && ok {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待课件预览 iframe 内容超时（%.0f 秒）", timeout.Seconds())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// findFrame 在帧树中找 URL 匹配的子帧（expected 空=第一个子帧）。
func (p *cdpLearnPage) findFrame(ctx context.Context, expected string) (cdp.FrameID, error) {
	var found cdp.FrameID
	err := runOn(ctx, p.ctx, 30*time.Second, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.GetFrameTree, cdp.Empty{})
		if err != nil {
			return err
		}
		var walk func(node *page.FrameTree)
		walk = func(node *page.FrameTree) {
			if node == nil || found != "" {
				return
			}
			for _, child := range node.ChildFrames {
				if child == nil || child.Frame == nil {
					continue
				}
				if expected == "" || child.Frame.URL == expected {
					found = child.Frame.ID
					return
				}
				walk(child)
			}
		}
		walk(res.FrameTree)
		if found == "" {
			return fmt.Errorf("frame not found")
		}
		return nil
	}))
	return found, err
}

// frameContentReady 在子帧隔离世界里执行内容就绪判定。
func (p *cdpLearnPage) frameContentReady(ctx context.Context, frameID cdp.FrameID) (bool, error) {
	var ok bool
	err := runOn(ctx, p.ctx, 30*time.Second, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		world, err := cdp.Call(cctx, t, page.CreateIsolatedWorld, page.CreateIsolatedWorldParams{
			FrameID:   frameID,
			WorldName: "uvmpCoursePreview",
		})
		if err != nil {
			return err
		}
		res, err := cdp.Call(cctx, t, runtime.Evaluate, runtime.EvaluateParams{
			Expression:    frameReadyJS,
			ContextID:     world.ExecutionContextID,
			ReturnByValue: boolPtr(true),
		})
		if err != nil {
			return err
		}
		if res.ExceptionDetails != nil {
			return fmt.Errorf("帧脚本异常")
		}
		var b bool
		if res.Result != nil {
			_ = json.Unmarshal(res.Result.Value, &b)
		}
		ok = b
		return nil
	}))
	return ok, err
}

// boolPtr 布尔指针字面量（cdproto 可选参数用，口径同 internal/render）。
func boolPtr(v bool) *bool { return &v }

// ---------------------------------------------------------------- 通用

// runOn 在标签页上下文执行动作（超时/取消只约束本次等待，口径同 render runOn）。
func runOn(jobCtx, tabCtx context.Context, timeout time.Duration, a chromedp.Action[chromedp.Void]) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(jobCtx, cancel)
	defer stop()
	return chromedp.Do(tctx, a)
}

// evalJSONSimple 定义见前文（唯一实现）。

// captureShot 整页截图落盘 PNG（诊断用）。
func captureShot(tabCtx context.Context, path string) error {
	var data []byte
	err := runOn(context.Background(), tabCtx, 30*time.Second, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format:      page.CaptureScreenshotFormatPng,
			FromSurface: boolPtr(false), // 诊断截图不等合成器出帧（遮挡窗口会挂到超时）
		})
		if err != nil {
			return err
		}
		data = res.Data
		return nil
	}))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("截图失败: 无数据")
	}
	return os.WriteFile(path, data, 0o644)
}

// currentURL 目标当前地址（快照）。
func currentURL(tabCtx context.Context, _ string) string {
	u, _ := evalOn(context.Background(), tabCtx, "location.href", 5*time.Second)
	s, _ := u.(string)
	return s
}

// jsString JS 字符串字面量（转义）。
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// clampF 夹取 [lo, hi]。
func clampF(lo, hi, v float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mathMin 两数取小。
func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
