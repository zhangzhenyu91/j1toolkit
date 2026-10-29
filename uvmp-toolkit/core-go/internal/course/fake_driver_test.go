package course

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// fakeBrowser 测试用受控浏览器假件：页面语义操作直连 mockStation（真实 HTTP JSON），
// 响应经 ResponseMonitor 喂入——等待/解析/状态机全走引擎真实代码，仅浏览器机械动作被模拟。
type fakeBrowser struct {
	station *mockStation
	monitor *ResponseMonitor
	train   string

	// extraTabs 附加标签页（多候选识别测试）。
	extraTabs []TabInfo
	// bodyText 页面内容识别文本（缺省为课程列表标记）。
	bodyText string
	// listTabURL 列表标签页地址覆盖（无候选识别测试）。
	listTabURL string
	// rowTextOverride 防误点模拟：返回行文本与是否就绪。
	rowTextOverride func(index int, items []map[string]any) (string, bool)
	// noSeek 视频拖不动（3 次拖动失败路径）。
	noSeek bool
	// noEnd 视频拖到尾但永不 ended（3 轮失败路径）。
	noEnd bool
	// badCourseID 课件清单 courseId 与当前课程不一致。
	badCourseID bool
	// waresOverride 课件清单覆盖（多课件/非法 wareType 测试）。
	waresOverride []any
	// failReset 缺「重置」按钮。
	failReset bool
	// failEnter 无「进入学习」入口。
	failEnter bool
	// totalDriftNext 下一页返回漂移 total（分页期间总数变化）。
	totalDriftNext bool

	shots []string
}

func newFakeBrowser(station *mockStation, train string) *fakeBrowser {
	return &fakeBrowser{station: station, monitor: NewResponseMonitor(), train: train}
}

func (b *fakeBrowser) Monitor() *ResponseMonitor { return b.monitor }
func (b *fakeBrowser) Close()                    {}

func (b *fakeBrowser) Tabs(ctx context.Context) ([]TabInfo, error) {
	listURL := b.station.ListURL(b.train)
	if b.listTabURL != "" {
		listURL = b.listTabURL
	}
	tabs := []TabInfo{{
		ID:      "list",
		URL:     listURL,
		Title:   "课程设置",
		Visible: true,
	}}
	return append(tabs, b.extraTabs...), nil
}

func (b *fakeBrowser) BodyText(ctx context.Context, id string) (string, error) {
	if b.bodyText != "" {
		return b.bodyText, nil
	}
	return "课程名称 学习进度 进入学习", nil
}

func (b *fakeBrowser) AttachListPage(ctx context.Context, id string) (ListPage, error) {
	return &fakeListPage{browser: b, id: id, train: b.train}, nil
}

// --------------------------------------------------------------- 列表页

type fakeListPage struct {
	browser *fakeBrowser
	id      string
	train   string
	page    int
	items   []map[string]any
	closed  bool
}

func (p *fakeListPage) ID() string   { return p.id }
func (p *fakeListPage) URL() string  { return p.browser.station.ListURL(p.train) }
func (p *fakeListPage) Closed() bool { return p.closed }
func (p *fakeListPage) Close() error {
	p.closed = true
	return nil
}

func (p *fakeListPage) Screenshot(path string) error {
	p.browser.shots = append(p.browser.shots, path)
	return os.WriteFile(path, []byte("png"), 0o644)
}

// fetchPage 真实 HTTP 拉课程分页并喂监视器（等待/解析走引擎真实代码）。
func (p *fakeListPage) fetchPage(ctx context.Context, page int) (*CoursePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	api := p.browser.station.BaseURL() + "/rest/gwxt-learning-c-s" + CoursePageAPI +
		"?trainid=" + p.train + "&page=" + fmt.Sprintf("%d", page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	p.browser.monitor.Add(MonitoredResponse{Scope: p.id, URL: api, Status: resp.StatusCode, Body: body})
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	data, err := ParseCoursePage(payload)
	if err != nil {
		return nil, err
	}
	if p.browser.totalDriftNext && page > 1 {
		data.Total++ // 模拟翻页期间总数变化
	}
	p.page = page
	p.items = data.Items
	return data, nil
}

func (p *fakeListPage) ReloadList(ctx context.Context) (*CoursePage, error) {
	return p.fetchPage(ctx, 1)
}

func (p *fakeListPage) ResetFilters(ctx context.Context) (*CoursePage, error) {
	if p.browser.failReset {
		return nil, fmt.Errorf("未找到课程设置区的「重置」按钮，无法保证扫描全部课程")
	}
	return p.fetchPage(ctx, 1)
}

func (p *fakeListPage) JumpToPage(ctx context.Context, page int) (*CoursePage, error) {
	return p.fetchPage(ctx, page)
}

func (p *fakeListPage) NextPage(ctx context.Context, page int) (*CoursePage, error) {
	return p.fetchPage(ctx, page)
}

// WaitRow 返回课程行文本（模拟 DOM 行；防误点覆盖可返回错误行）。
func (p *fakeListPage) WaitRow(ctx context.Context, index, pageSize int, expectedName string) (string, error) {
	if len(p.items) != pageSize {
		return "", fmt.Errorf("课程表格行数与接口不一致：DOM %d，接口 %d", len(p.items), pageSize)
	}
	if index >= len(p.items) {
		return "", fmt.Errorf("课程表格行数与接口不一致：DOM %d，接口 %d", len(p.items), pageSize)
	}
	if p.browser.rowTextOverride != nil {
		if text, ready := p.browser.rowTextOverride(index, p.items); ready {
			return text, nil
		}
		return "", fmt.Errorf("等待页面条件超时（%.0f 秒）", 120.0)
	}
	item := p.items[index]
	return fmt.Sprintf("%s %g%% 进入学习", item["coursename"], mustFloat(item["studyRate"])), nil
}

func (p *fakeListPage) EnterLearning(ctx context.Context, index int) (LearnPage, error) {
	if p.browser.failEnter {
		return nil, fmt.Errorf("课程行没有可用的「进入学习」入口")
	}
	if index >= len(p.items) {
		return nil, fmt.Errorf("课程行没有可用的「进入学习」入口")
	}
	item := p.items[index]
	id := fmt.Sprintf("%v", item["resourceid"])
	lp := &fakeLearnPage{browser: p.browser, id: "popup-" + id, courseID: id}
	// 学习页加载即请求课件清单（模拟站 play_html 行为），响应喂监视器
	wares, err := p.browser.fetchWares(ctx, lp.id, id)
	if err != nil {
		return nil, err
	}
	lp.wares = wares
	return lp, nil
}

// fetchWares 拉课件清单（可被 badCourseID/waresOverride 扭曲）。
func (b *fakeBrowser) fetchWares(ctx context.Context, scope, courseID string) (map[string]any, error) {
	api := b.station.BaseURL() + "/rest/gwxt-learning-c-s" + WareListAPI + "?id=" + courseID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if b.badCourseID || b.waresOverride != nil {
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if b.waresOverride != nil {
			payload["resultValue"] = b.waresOverride
		}
		if b.badCourseID {
			payload["resultValue"] = []any{map[string]any{
				"id": "catalog",
				"courseWareCatalogRelationDTOS": []any{
					map[string]any{"wareType": float64(2), "courseId": "OTHER", "wareName": "错课件", "suffixInfo": ".pdf"},
				},
			}}
		}
		body, _ = json.Marshal(payload)
	}
	b.monitor.Add(MonitoredResponse{Scope: scope, URL: api, Status: resp.StatusCode, Body: body})
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// postPassWare 学习上报（模拟页面 fetch getPassWare）。
func (b *fakeBrowser) postPassWare(ctx context.Context, scope, courseID string) {
	api := b.station.BaseURL() + "/rest/gwxt-learning-c-s" + SaveStudyAPI
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, strings.NewReader(`{"id":"`+courseID+`"}`))
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.monitor.Add(MonitoredResponse{Scope: scope, URL: api, Status: resp.StatusCode, Body: body})
}

// fetchLastStudy 课件切换响应（模拟页面 fetch lastStudyHour）。
func (b *fakeBrowser) fetchLastStudy(ctx context.Context, scope string) {
	api := b.station.BaseURL() + "/rest/gwxt-learning-c-s" + LastStudyAPI + "?x=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.monitor.Add(MonitoredResponse{Scope: scope, URL: api, Status: resp.StatusCode, Body: body})
}

// --------------------------------------------------------------- 学习弹窗

type fakeLearnPage struct {
	browser  *fakeBrowser
	id       string
	courseID string
	wares    map[string]any
	video    VideoState
	videoOn  bool
	closed   bool
}

func (p *fakeLearnPage) ID() string { return p.id }
func (p *fakeLearnPage) URL() string {
	return p.browser.station.BaseURL() + "/learning-c/course/courseplay?id=" + p.courseID
}
func (p *fakeLearnPage) Closed() bool { return p.closed }

func (p *fakeLearnPage) Close() error {
	p.closed = true
	p.browser.monitor.MarkClosed(p.id)
	return nil
}

func (p *fakeLearnPage) Screenshot(path string) error {
	p.browser.shots = append(p.browser.shots, path)
	return os.WriteFile(path, []byte("png"), 0o644)
}

// flattened 叶子课件列表。
func (p *fakeLearnPage) flattened() []map[string]any {
	wares, err := FlattenWares(p.wares)
	if err != nil {
		return nil
	}
	return wares
}

func (p *fakeLearnPage) WaitWareTree(ctx context.Context, n int) error {
	if got := len(p.flattened()); got != n {
		return fmt.Errorf("课件目录数量与接口不一致：DOM %d，接口 %d", got, n)
	}
	return nil
}

func (p *fakeLearnPage) ClickTreeItem(ctx context.Context, index int) error {
	wares := p.flattened()
	if index >= len(wares) {
		return fmt.Errorf("课件目录第 %d 项不存在", index+1)
	}
	p.browser.fetchLastStudy(ctx, p.id)
	p.videoOn = fmt.Sprintf("%v", wares[index]["wareType"]) == "1"
	return nil
}

func (p *fakeLearnPage) WaitVideoReady(ctx context.Context) error {
	p.videoOn = true
	p.video = VideoState{Duration: 10}
	return ctx.Err()
}

func (p *fakeLearnPage) InstallEndedHook(ctx context.Context) error { return ctx.Err() }

func (p *fakeLearnPage) VideoState(ctx context.Context) (VideoState, error) {
	if err := ctx.Err(); err != nil {
		return VideoState{}, err
	}
	return p.video, nil
}

func (p *fakeLearnPage) DragProgress(ctx context.Context, fromRatio, toRatio float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.browser.noSeek {
		return nil
	}
	p.video.Current = toRatio * p.video.Duration
	return nil
}

func (p *fakeLearnPage) Play(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.browser.noEnd {
		return nil
	}
	p.video.Current = p.video.Duration
	p.video.Ended = true
	p.browser.postPassWare(ctx, p.id, p.courseID)
	return nil
}

func (p *fakeLearnPage) WaitVideoEnded(ctx context.Context, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.video.Ended {
		return nil
	}
	if p.browser.noEnd {
		return fmt.Errorf("等待页面条件超时（%.0f 秒）", timeout.Seconds())
	}
	// 模拟播放自然结束并上报
	p.video.Current = p.video.Duration
	p.video.Ended = true
	p.browser.postPassWare(ctx, p.id, p.courseID)
	return nil
}

func (p *fakeLearnPage) WaitAudioReady(ctx context.Context) error { return ctx.Err() }

func (p *fakeLearnPage) WaitIframeLoaded(ctx context.Context, selector string, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 文档预览加载完成即触发学习上报（模拟站 play_html 的 setTimeout fetch）
	p.browser.postPassWare(ctx, p.id, p.courseID)
	return nil
}

// mustFloat 宽松取数值（测试断言用）。
func mustFloat(v any) float64 {
	f, _ := toFloat(v)
	return f
}
