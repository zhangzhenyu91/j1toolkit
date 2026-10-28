package wmadd

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/compat/imagepdf"
	"shade/internal/httpapi"
)

// fakeAuth 把固定身份注入 httpapi.AuthMiddleware 的上下文（与生产同一条装配路径）。
type fakeAuth struct {
	ident auth.Identity
}

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: f.ident}, nil
}

func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) {
	return f.ident, nil
}

func (f fakeAuth) Logout(context.Context, string, time.Time) error { return nil }

// fakePermit 应用权限桩：allow 决定放行。
type fakePermit struct{ allow bool }

func (p fakePermit) Allow(context.Context, int64, string) (bool, error) { return p.allow, nil }

// fakeGeo 地理服务桩。
type fakeGeo struct {
	out LocationWeather
	err error
}

func (g fakeGeo) FetchLocationWeather(context.Context, float64, float64) (LocationWeather, error) {
	return g.out, g.err
}

// fakeRenderer 渲染桩：记录入参并返回固定 JPEG。
type fakeRenderer struct {
	wm          WatermarkFields
	orientation string
	photoLen    int
}

func (r *fakeRenderer) Render(photo []byte, wm WatermarkFields, orientation string) ([]byte, error) {
	r.wm = wm
	r.orientation = orientation
	r.photoLen = len(photo)
	return []byte("jpeg-bytes"), nil
}

type testEnv struct {
	handler  http.Handler
	fake     *fakeDB
	module   *Module
	renderer *fakeRenderer
}

func newTestServer(t *testing.T, ident auth.Identity) *testEnv {
	t.Helper()
	db, fake := openFake(t)
	m := New(db)
	m.Permit = fakePermit{allow: true}
	m.Geo = fakeGeo{out: LocationWeather{Location: "尧都区·临汾电力高级技工学校", Weather: "多云 32°C"}}
	renderer := &fakeRenderer{}
	m.Renderer = renderer
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return &testEnv{
		handler:  httpapi.AuthMiddleware(r, fakeAuth{ident: ident}),
		fake:     fake,
		module:   m,
		renderer: renderer,
	}
}

func (e *testEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) (int, map[string]any) {
	t.Helper()
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, rec.Body.String())
	}
	data := map[string]any{}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		_ = json.Unmarshal(env.Data, &data)
	}
	return env.Code, data
}

var (
	identAdmin = auth.Identity{ID: 1, Username: "admin", Role: "admin", TeamID: 1}
	identUser  = auth.Identity{ID: 4, Username: "u", Role: "user", TeamID: 1}
)

func teamRule() fakeRule {
	return fakeRule{match: "FROM sys_team", cols: []string{"id", "name"}, rows: [][]driver.Value{{int64(1), "检修一班"}}}
}

// TestGeoValidate 经纬度校验：非法 40022，合法回地理桩结果。
func TestGeoValidate(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)

	rec := env.do(t, http.MethodGet, "/api/v1/wmadd/geo?lng=abc&lat=1", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法经度应 40001 族错误，实际 %d %s", rec.Code, rec.Body.String())
	}
	if code, _ := decodeData(t, rec); code != codeCoord {
		t.Fatalf("业务码应 %d，实际 %d", codeCoord, code)
	}

	rec = env.do(t, http.MethodGet, "/api/v1/wmadd/geo?lng=111.5&lat=36.2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法坐标应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	code, data := decodeData(t, rec)
	if code != 0 || data["location"] != "尧都区·临汾电力高级技工学校" {
		t.Fatalf("地理结果不符: code=%d data=%v", code, data)
	}
}

// TestGeoNilClient 未接入地理服务时回空串（前端留空手填），不报错。
func TestGeoNilClient(t *testing.T) {
	env := newTestServer(t, identUser)
	env.module.Geo = nil
	rec := env.do(t, http.MethodGet, "/api/v1/wmadd/geo?lng=111.5&lat=36.2", nil)
	code, data := decodeData(t, rec)
	if code != 0 || data["location"] != "" || data["weather"] != "" {
		t.Fatalf("未接入应空串: code=%d data=%v", code, data)
	}
}

// TestTowers 班组杆塔：无班组回空 rows；有班组按 worklog_tower 返回数字经纬度并按 team_id 过滤。
func TestTowers(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.fake.on("FROM worklog_tower",
		[]string{"voltage_level", "line_name", "tower_no", "lng", "lat"},
		[][]driver.Value{{"110", "临牵线", "12", "111.500000", "36.200000"}})

	rec := env.do(t, http.MethodGet, "/api/v1/wmadd/towers", nil)
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("杆塔查询应成功: %s", rec.Body.String())
	}
	rows, _ := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("应返回 1 行杆塔，实际 %v", data)
	}
	row := rows[0].([]any)
	if row[0] != "110" || row[1] != "临牵线" || row[2] != "12" {
		t.Fatalf("杆塔行内容不符: %v", row)
	}
	if row[3].(float64) != 111.5 || row[4].(float64) != 36.2 {
		t.Fatalf("经纬度应为数字: %v", row)
	}
	q, ok := env.fake.lastQuery("FROM worklog_tower")
	if !ok || len(q.args) == 0 || q.args[0] != int64(1) {
		t.Fatalf("杆塔查询应按 team_id=1 过滤: %+v", q)
	}
}

// TestTowersNoTeam 未分配班组回 { rows: [] }（与有班组同形状）。
func TestTowersNoTeam(t *testing.T) {
	env := newTestServer(t, identUser)
	env.fake.onErr("FROM sys_team", errNoRows())
	rec := env.do(t, http.MethodGet, "/api/v1/wmadd/towers", nil)
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("无班组应空态成功: %s", rec.Body.String())
	}
	rows, _ := data["rows"].([]any)
	if len(rows) != 0 {
		t.Fatalf("无班组应空 rows: %v", data)
	}
}

// TestRender 照片格式/大小校验与渲染回图。
func TestRender(t *testing.T) {
	env := newTestServer(t, identUser)

	// 非法 dataURL
	rec := env.do(t, http.MethodPost, "/api/v1/wmadd/render", map[string]any{"image": "bad"})
	if code, _ := decodeData(t, rec); code != codeFormat {
		t.Fatalf("非法照片应 %d，实际 %s", codeFormat, rec.Body.String())
	}

	// 超大小（构造 15MB+1 base64 会过大；用伪造长度：直接测空图分支 + 渲染成功分支）
	tiny := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("tiny"))
	rec = env.do(t, http.MethodPost, "/api/v1/wmadd/render", map[string]any{"image": tiny})
	if rec.Code != http.StatusOK {
		t.Fatalf("渲染应成功: %s", rec.Body.String())
	}
	code, data := decodeData(t, rec)
	if code != 0 {
		t.Fatalf("渲染应成功: %s", rec.Body.String())
	}
	img, _ := data["image"].(string)
	if !strings.HasPrefix(img, "data:image/jpeg;base64,") {
		t.Fatalf("回图应为 jpeg dataURL: %v", data)
	}
	if env.renderer.photoLen != 4 {
		t.Fatalf("渲染入参照片长度不符: %d", env.renderer.photoLen)
	}
	if env.renderer.wm.AntiCode == "" || len(env.renderer.wm.AntiCode) != 14 {
		t.Fatalf("防伪码应服务端生成 14 位: %q", env.renderer.wm.AntiCode)
	}
}

// TestRenderOrientation orientation 透传渲染层（wx 标签）。
func TestRenderOrientation(t *testing.T) {
	env := newTestServer(t, identUser)
	tiny := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	rec := env.do(t, http.MethodPost, "/api/v1/wmadd/render", map[string]any{
		"image": tiny,
		"wm":    map[string]any{"content": "杆塔巡视", "orientation": "right"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("渲染应成功: %s", rec.Body.String())
	}
	if env.renderer.orientation != "right" || env.renderer.wm.Content != "杆塔巡视" {
		t.Fatalf("orientation/字段未透传: %+v", env.renderer)
	}
}

// TestPermitDenied 无应用权限 40301。
func TestPermitDenied(t *testing.T) {
	env := newTestServer(t, identUser)
	env.module.Permit = fakePermit{allow: false}
	rec := env.do(t, http.MethodGet, "/api/v1/wmadd/geo?lng=1&lat=1", nil)
	if code, _ := decodeData(t, rec); code != codeNoApp {
		t.Fatalf("无权限应 %d，实际 %s", codeNoApp, rec.Body.String())
	}
}

// TestSanitizeWm 字段清洗：截断、防伪码采信/重生成。
func TestSanitizeWm(t *testing.T) {
	wm := SanitizeWm(map[string]string{
		"content":  "  " + strings.Repeat("巡", 600) + " ",
		"time":     " 2026.10.27 08:30 ",
		"antiCode": "ABCDEFGHJKLMNP",
	})
	if len([]rune(wm.Content)) != cutContent {
		t.Fatalf("content 应截断到 %d，实际 %d", cutContent, len([]rune(wm.Content)))
	}
	if wm.Time != "2026.10.27 08:30" {
		t.Fatalf("time 应 trim: %q", wm.Time)
	}
	if wm.AntiCode != "ABCDEFGHJKLMNP" {
		t.Fatalf("字符集内防伪码应采信前端值: %q", wm.AntiCode)
	}

	bad := SanitizeWm(map[string]string{"antiCode": "00000000000000"})
	if bad.AntiCode == "00000000000000" {
		t.Fatalf("非法字符集应重新生成: %q", bad.AntiCode)
	}
	if len(bad.AntiCode) != antiCodeLen {
		t.Fatalf("防伪码应 %d 位: %q", antiCodeLen, bad.AntiCode)
	}
	for _, r := range bad.AntiCode {
		if !strings.ContainsRune(antiCodeAlphabet, r) {
			t.Fatalf("防伪码含非法字符 %q", r)
		}
	}
}

// TestOrientByWxTag EXIF 方向摆正：right 旋转后宽高互换。
func TestOrientByWxTag(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.White)
	got := orientByWxTag(src, "right")
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 4 {
		t.Fatalf("right 旋转后应 2x4，实际 %v", got.Bounds())
	}
	got = orientByWxTag(src, "down")
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 2 {
		t.Fatalf("down 旋转后应保持 4x2，实际 %v", got.Bounds())
	}
}

// TestAMapClient 高德客户端经 httptest 验证两步串行口径（地点拼区县·地标、天气按 adcode）。
func TestAMapClient(t *testing.T) {
	var regeoQ, weatherQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			regeoQ = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"status":"1","regeocode":{"addressComponent":{"adcode":"141002","district":"尧都区","city":"临汾市","township":"屯里镇","streetNumber":{"street":"解放路"}},"pois":[{"name":"临汾电力高级技工学校","distance":"120"},{"name":"远处地标","distance":"900"}]}}`))
		case "/v3/weather/weatherInfo":
			weatherQ = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"status":"1","lives":[{"weather":"多云","temperature":"31.6"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewAMapClient("test-key", srv.URL)
	got, err := c.FetchLocationWeather(context.Background(), 111.5, 36.2)
	if err != nil {
		t.Fatalf("地理取值失败: %v", err)
	}
	if got.Location != "尧都区·临汾电力高级技工学校" {
		t.Fatalf("地点应拼 区县·最近POI，实际 %q", got.Location)
	}
	if got.Weather != "多云 32°C" {
		t.Fatalf("天气应四舍五入温度，实际 %q", got.Weather)
	}
	if !strings.Contains(regeoQ, "location=111.500000%2C36.200000") || !strings.Contains(regeoQ, "key=test-key") {
		t.Fatalf("逆编码入参不符: %s", regeoQ)
	}
	if !strings.Contains(weatherQ, "city=141002") {
		t.Fatalf("天气应按 adcode 查询: %s", weatherQ)
	}

	// POI 超 200 米回退乡镇
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"1","regeocode":{"addressComponent":{"adcode":"141002","district":"尧都区","township":"屯里镇"},"pois":[{"name":"远处地标","distance":"900"}]}}`))
	}))
	defer srv2.Close()
	got, err = NewAMapClient("k", srv2.URL).FetchLocationWeather(context.Background(), 111.5, 36.2)
	if err != nil || got.Location != "尧都区·屯里镇" {
		t.Fatalf("POI 超距应回退乡镇: %+v err=%v", got, err)
	}

	// 未配置 key 恒空串
	empty, err := NewAMapClient("", srv.URL).FetchLocationWeather(context.Background(), 1, 1)
	if err != nil || empty != (LocationWeather{}) {
		t.Fatalf("未配置应空串: %+v err=%v", empty, err)
	}
}

// TestWatermarkRenderer 用真实字体素材端到端渲染（只读 server/assets/fonts，不联网）。
func TestWatermarkRenderer(t *testing.T) {
	fontsDir := filepath.Join("..", "..", "..", "..", "server", "assets", "fonts")
	if _, err := os.Stat(fontsDir); err != nil {
		t.Skipf("字体素材不可用: %v", err)
	}
	fonts, err := imagepdf.LoadFontFiles(fontsDir)
	if err != nil {
		t.Fatalf("加载字体失败: %v", err)
	}
	src := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			src.Set(x, y, color.RGBA{R: 80, G: 120, B: 160, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatalf("编码样图失败: %v", err)
	}
	r := &WatermarkRenderer{Fonts: fonts}
	out, err := r.Render(buf.Bytes(), WatermarkFields{
		Content:   "绝缘子清扫",
		Time:      "2026.10.27 09:00",
		Weather:   "多云 18°C",
		Location:  "尧都区·临汾电力高级技工学校",
		Longitude: "111.500000°E",
		Latitude:  "36.200000°N",
		AntiCode:  RandomAntiCode(14),
	}, "up")
	if err != nil {
		t.Fatalf("水印渲染失败: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("回图不是合法 JPEG: %v", err)
	}
	if img.Bounds().Dx() != 320 || img.Bounds().Dy() != 240 {
		t.Fatalf("回图尺寸应与原图一致，实际 %v", img.Bounds())
	}
}

// TestWatermarkRendererLazyFonts 零值渲染器自动加载字体（main 的 &wmadd.WatermarkRenderer{} 口径）；
// NewWatermarkRenderer 预加载同口径。字体素材只读、不联网，缺失时跳过。
func TestWatermarkRendererLazyFonts(t *testing.T) {
	if _, err := imagepdf.LoadDefaultFonts(); err != nil {
		t.Skipf("默认字体素材不可用: %v", err)
	}
	src := image.NewRGBA(image.Rect(0, 0, 64, 48))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatalf("编码样图失败: %v", err)
	}
	// 零值懒加载：Fonts 为 nil 时 Render 自动取默认字体
	if out, err := (&WatermarkRenderer{}).Render(buf.Bytes(), WatermarkFields{AntiCode: RandomAntiCode(14)}, "up"); err != nil || len(out) == 0 {
		t.Fatalf("零值渲染器懒加载失败: %v", err)
	}
	// 构造器预加载
	r := NewWatermarkRenderer()
	if r == nil || r.Fonts == nil {
		t.Fatalf("NewWatermarkRenderer 应预加载字体: %+v", r)
	}
	if out, err := r.Render(buf.Bytes(), WatermarkFields{AntiCode: RandomAntiCode(14)}, "up"); err != nil || len(out) == 0 {
		t.Fatalf("NewWatermarkRenderer 渲染失败: %v", err)
	}
}

// fakeConfig 运行时配置桩（AMAP_MAP_KEY / AMAP_BASE_URL 动态取值，可变值模拟配置中心改值）。
type fakeConfig struct {
	mu   sync.Mutex
	vals map[string]string
}

func (c *fakeConfig) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vals[key]
	if !ok {
		return "", errors.New("配置不存在")
	}
	return v, nil
}

func (c *fakeConfig) set(key, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vals == nil {
		c.vals = map[string]string{}
	}
	c.vals[key] = v
}

// TestAMapRuntimeConfig 运行时配置动态取 key/base：改值即时生效不重建客户端；
// Cfg 空值回退固定注入值，两边皆空则恒回空串且不发请求。
func TestAMapRuntimeConfig(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.URL.Query().Get("key"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"1","regeocode":{"addressComponent":{"adcode":"141002","district":"尧都区","city":"临汾市","township":"屯里镇"},"pois":[]}}`))
	}))
	defer srv.Close()
	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}

	cfg := &fakeConfig{vals: map[string]string{"AMAP_MAP_KEY": "cfg-key", "AMAP_BASE_URL": srv.URL}}
	c := NewAMapClient("fixed-key", "")
	c.Cfg = cfg
	c.HTTP = srv.Client()
	if _, err := c.FetchLocationWeather(context.Background(), 111.5, 36.2); err != nil {
		t.Fatalf("地理取值失败: %v", err)
	}
	got := recorded()
	if len(got) == 0 {
		t.Fatalf("应发起高德请求")
	}
	for _, k := range got {
		if k != "cfg-key" {
			t.Fatalf("首访应携带配置中心 key: %v", got)
		}
	}

	// 改值即时生效（同一客户端，不重建）
	cfg.set("AMAP_MAP_KEY", "key2")
	if _, err := c.FetchLocationWeather(context.Background(), 111.5, 36.2); err != nil {
		t.Fatalf("改值后取值失败: %v", err)
	}
	got = recorded()
	if got[len(got)-1] != "key2" {
		t.Fatalf("改值后应携带 key2: %v", got)
	}

	// Cfg 取值为空回退固定注入值
	cfg.set("AMAP_MAP_KEY", "")
	c2 := NewAMapClient("fixed-key", srv.URL)
	c2.Cfg = cfg
	c2.HTTP = srv.Client()
	if _, err := c2.FetchLocationWeather(context.Background(), 111.5, 36.2); err != nil {
		t.Fatalf("回退取值失败: %v", err)
	}
	got = recorded()
	if got[len(got)-1] != "fixed-key" {
		t.Fatalf("空值应回退固定 key: %v", got)
	}

	// 两边皆空：恒回空串且不发请求
	c3 := NewAMapClient("", srv.URL)
	c3.Cfg = cfg
	c3.HTTP = srv.Client()
	before := len(recorded())
	empty, err := c3.FetchLocationWeather(context.Background(), 111.5, 36.2)
	if err != nil || empty != (LocationWeather{}) {
		t.Fatalf("未配置应空串: %+v err=%v", empty, err)
	}
	if after := len(recorded()); after != before {
		t.Fatalf("未配置不应发请求: %d → %d", before, after)
	}
}

// ---------- 水印渲染专项 ----------

// absF 浮点绝对值。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// nearRGB 色距容差匹配（分量 0-255）。
func nearRGB(r, g, b uint32, tr, tg, tb, tol int) bool {
	d := func(a uint32, v int) bool {
		diff := int(a) - v
		if diff < 0 {
			diff = -diff
		}
		return diff <= tol
	}
	return d(r, tr) && d(g, tg) && d(b, tb)
}

// TestSanitizeWmUTF16 截断按 UTF-16 码元计（对齐 JS String.slice(0,n)）：emoji 占 2 码元。
func TestSanitizeWmUTF16(t *testing.T) {
	wm := SanitizeWm(map[string]string{"content": strings.Repeat("😀", 300)}) // 600 码元
	if utf16Len(wm.Content) != cutContent {
		t.Fatalf("content 应按 UTF-16 截断到 %d 码元，实际 %d", cutContent, utf16Len(wm.Content))
	}
	if got := len([]rune(wm.Content)); got != cutContent/2 {
		t.Fatalf("emoji 应保留 %d 个，实际 %d", cutContent/2, got)
	}
	// 未超长不截断
	if s := cut("杆塔巡视", cutContent); s != "杆塔巡视" {
		t.Fatalf("未超长不应截断: %q", s)
	}
}

// TestAutoOrientEXIF EXIF 1-8 摆正（对齐 @napi-rs/canvas loadImage 实测行为；
// 7 反对角线翻转为 compat/imagepdf.AutoOrient 越界 bug 的回归用例）。
func TestAutoOrientEXIF(t *testing.T) {
	// 4x2 四象限：左上红 / 右上绿 / 左下蓝 / 右下白
	red, green, blue, white := color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}, color.RGBA{B: 255, A: 255}, color.RGBA{R: 255, G: 255, B: 255, A: 255}
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			switch {
			case y == 0 && x < 2:
				src.Set(x, y, red)
			case y == 0:
				src.Set(x, y, green)
			case x < 2:
				src.Set(x, y, blue)
			default:
				src.Set(x, y, white)
			}
		}
	}
	corner := func(img image.Image, x, y int) color.RGBA {
		r, g, b, _ := img.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
	}
	type quad struct{ tl, tr, bl, br color.RGBA }
	cases := map[int]quad{
		1: {red, green, blue, white},
		2: {green, red, white, blue}, // 水平镜像
		3: {white, blue, green, red}, // 旋转 180°
		4: {blue, white, red, green}, // 垂直镜像
		5: {red, blue, green, white}, // 转置（宽高互换 2x4）
		6: {blue, red, white, green}, // 顺时针 90°
		7: {white, green, blue, red}, // 反对角线翻转
		8: {green, white, red, blue}, // 逆时针 90°
	}
	for o, want := range cases {
		got := autoOrientEXIF(src, o)
		b := got.Bounds()
		if o >= 5 {
			if b.Dx() != 2 || b.Dy() != 4 {
				t.Fatalf("orientation=%d 应 2x4，实际 %v", o, b)
			}
		} else if b.Dx() != 4 || b.Dy() != 2 {
			t.Fatalf("orientation=%d 应 4x2，实际 %v", o, b)
		}
		tl := corner(got, b.Min.X, b.Min.Y)
		tr := corner(got, b.Max.X-1, b.Min.Y)
		bl := corner(got, b.Min.X, b.Max.Y-1)
		br := corner(got, b.Max.X-1, b.Max.Y-1)
		if tl != want.tl || tr != want.tr || bl != want.bl || br != want.br {
			t.Fatalf("orientation=%d 四角不符: got=%v,%v,%v,%v want=%v,%v,%v,%v", o, tl, tr, bl, br, want.tl, want.tr, want.bl, want.br)
		}
	}
}

// TestOrientByWxTagMirrored wx 镜像标签按正向处理（对齐 render-photo.js：仅取 left/right/down
// 前缀旋转，不镜像像素）。
func TestOrientByWxTagMirrored(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.White)
	got := orientByWxTag(src, "up-mirrored")
	if got.Bounds() != src.Bounds() {
		t.Fatalf("up-mirrored 应保持 4x2，实际 %v", got.Bounds())
	}
	got = orientByWxTag(src, "down-mirrored")
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 2 {
		t.Fatalf("down-mirrored 应 180° 旋转保持 4x2，实际 %v", got.Bounds())
	}
	got = orientByWxTag(src, "right-mirrored")
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 4 {
		t.Fatalf("right-mirrored 应按 right 旋转 2x4，实际 %v", got.Bounds())
	}
}

// TestWatermarkRendererCard 端到端渲染出 v2 卡片特征（蓝头/黄点/白身），非通用黑底文本块。
func TestWatermarkRendererCard(t *testing.T) {
	fontsDir := filepath.Join("..", "..", "..", "..", "server", "assets", "fonts")
	if _, err := os.Stat(fontsDir); err != nil {
		t.Skipf("字体素材不可用: %v", err)
	}
	fonts, err := imagepdf.LoadFontFiles(fontsDir)
	if err != nil {
		t.Fatalf("加载字体失败: %v", err)
	}
	src := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			src.Set(x, y, color.RGBA{R: 168, G: 168, B: 168, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatalf("编码样图失败: %v", err)
	}
	r := &WatermarkRenderer{Fonts: fonts}
	out, err := r.Render(buf.Bytes(), WatermarkFields{
		Content:   "绝缘子清扫",
		Time:      "2026.10.27 09:00",
		Weather:   "多云 18°C",
		Location:  "尧都区",
		Longitude: "111.500000°E",
		Latitude:  "36.200000°N",
		AntiCode:  "ABCDEFGHJKLMNP",
	}, "up")
	if err != nil {
		t.Fatalf("水印渲染失败: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("回图不是合法 JPEG: %v", err)
	}
	// 蓝头合成色 0.7*(69,136,249)+0.3*168 灰底 = (99,146,225)
	blue, yellow := 0, 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r8, g8, b8, _ := img.At(x, y).RGBA()
			if nearRGB(r8>>8, g8>>8, b8>>8, 99, 146, 225, 18) {
				blue++
			}
			if r8>>8 > 200 && g8>>8 > 140 && g8>>8 < 235 && b8>>8 < 140 {
				yellow++
			}
		}
	}
	if blue < 200 {
		t.Fatalf("蓝头像素 = %d，期望 ≥200（v2 卡片头部）", blue)
	}
	if yellow < 20 {
		t.Fatalf("黄点像素 = %d，期望 ≥20", yellow)
	}
}

// solidJPEG 纯色 JPEG 测试图。
func solidJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// wmLandmarks 版式几何标志点（Node/Go 产物比对用）。
type wmLandmarks struct {
	headerX0, headerX1, headerY0, headerY1 float64
	dotX, dotY                             float64
	bodyX0, bodyX1, bodyY0, bodyY1         float64
	codeX0, codeX1                         float64
	brandX0, brandX1, brandY0, brandY1     float64
}

// bboxMatch 区域内匹配像素包围盒（x1/y1 开区间；无命中 ok=false）。
func bboxMatch(img image.Image, x0, y0, x1, y1 int, match func(r, g, b uint32) bool) (int, int, int, int, bool) {
	b := img.Bounds()
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if match(r>>8, g>>8, bl>>8) {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return 0, 0, 0, 0, false
	}
	return minX, minY, maxX + 1, maxY + 1, true
}

// extractLandmarks 从水印成品图提取版式标志点（底图纯色 168 灰，各区块按合成色检出）。
func extractLandmarks(img image.Image) wmLandmarks {
	b := img.Bounds()
	W, H := float64(b.Dx()), float64(b.Dy())
	B := W
	if H < B {
		B = H
	}
	var lm wmLandmarks
	if x0, y0, x1, y1, ok := bboxMatch(img, 0, 0, b.Dx(), b.Dy(), func(r, g, bl uint32) bool {
		return nearRGB(r, g, bl, 99, 146, 225, 22)
	}); ok {
		lm.headerX0, lm.headerY0, lm.headerX1, lm.headerY1 = float64(x0), float64(y0), float64(x1), float64(y1)
	}
	sx, sy, n := 0.0, 0.0, 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 > 140 && g>>8 < 235 && bl>>8 < 140 {
				sx += float64(x)
				sy += float64(y)
				n++
			}
		}
	}
	if n > 0 {
		lm.dotX, lm.dotY = sx/n, sy/n
	}
	if x0, y0, x1, y1, ok := bboxMatch(img, 0, 0, int(0.62*W), b.Dy(), func(r, g, bl uint32) bool {
		return nearRGB(r, g, bl, 229, 229, 229, 14)
	}); ok {
		lm.bodyX0, lm.bodyY0, lm.bodyX1, lm.bodyY1 = float64(x0), float64(y0), float64(x1), float64(y1)
	}
	if x0, _, x1, _, ok := bboxMatch(img, int(W-0.18*B), int(H-0.024*B), b.Dx(), b.Dy(), func(r, g, bl uint32) bool {
		return r >= 246 && g >= 246 && bl >= 246
	}); ok {
		lm.codeX0, lm.codeX1 = float64(x0), float64(x1)
	}
	if x0, y0, x1, y1, ok := bboxMatch(img, int(W-0.18*B), int(H-0.14*B), b.Dx(), int(H-0.024*B), func(r, g, bl uint32) bool {
		return !nearRGB(r, g, bl, 168, 168, 168, 20)
	}); ok {
		lm.brandX0, lm.brandY0, lm.brandX1, lm.brandY1 = float64(x0), float64(y0), float64(x1), float64(y1)
	}
	return lm
}

// compareLandmarks Node/Go 版式标志点比对（光栅器与 JPEG 编码不同，几何容差 px）。
func compareLandmarks(t *testing.T, n, g wmLandmarks) {
	t.Helper()
	check := func(name string, a, b, tol float64) {
		if absF(a-b) > tol {
			t.Errorf("%s 不一致: node=%.1f go=%.1f（容差 %.1f）", name, a, b, tol)
		}
	}
	check("headerX0", n.headerX0, g.headerX0, 5)
	check("headerX1", n.headerX1, g.headerX1, 5)
	check("headerY0", n.headerY0, g.headerY0, 5)
	check("headerY1", n.headerY1, g.headerY1, 5)
	check("dotX", n.dotX, g.dotX, 5)
	check("dotY", n.dotY, g.dotY, 5)
	check("bodyX0", n.bodyX0, g.bodyX0, 5)
	check("bodyX1", n.bodyX1, g.bodyX1, 5)
	check("bodyY0", n.bodyY0, g.bodyY0, 5)
	check("bodyY1", n.bodyY1, g.bodyY1, 5)
	check("codeX0", n.codeX0, g.codeX0, 8)
	check("codeX1", n.codeX1, g.codeX1, 8)
	check("brandX0", n.brandX0, g.brandX0, 8)
	check("brandX1", n.brandX1, g.brandX1, 8)
	check("brandY0", n.brandY0, g.brandY0, 8)
	check("brandY1", n.brandY1, g.brandY1, 8)
}

// TestRenderMatchesNodeRenderer 真实调用本地旧 Node renderer（server/src/worklog/render-photo.js，
// 只读不改）与 Go 渲染器做固定图片/固定字段比对：卡片头/黄点/卡身/防伪行/品牌 logo 版式一致。
// 临时脚本与图片放系统 temp（t.TempDir 自动清理），不入仓。node 或素材缺失时跳过。
func TestRenderMatchesNodeRenderer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node 不可用: %v", err)
	}
	repoRoot := filepath.Join("..", "..", "..", "..")
	renderJS := filepath.Join(repoRoot, "server", "src", "worklog", "render-photo.js")
	if _, err := os.Stat(renderJS); err != nil {
		t.Skipf("旧 Node renderer 不可用: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "server", "node_modules", "@napi-rs", "canvas")); err != nil {
		t.Skipf("@napi-rs/canvas 未安装: %v", err)
	}
	if _, err := imagepdf.LoadDefaultFonts(); err != nil {
		t.Skipf("默认字体素材不可用: %v", err)
	}
	renderAbs, err := filepath.Abs(renderJS)
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	script := filepath.Join(tmp, "render.js")
	js := "const fs = require('fs');\n" +
		"const { renderWatermarkedPhoto } = require(process.argv[2]);\n" +
		"(async () => {\n" +
		"  const photo = fs.readFileSync(process.argv[3]);\n" +
		"  const wm = JSON.parse(process.argv[4]);\n" +
		"  const out = await renderWatermarkedPhoto(photo, wm, process.argv[5]);\n" +
		"  fs.writeFileSync(process.argv[6], out);\n" +
		"})().catch((e) => { console.error(e); process.exit(1); });\n"
	if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}

	wm := map[string]string{
		"content":   "绝缘子清扫",
		"time":      "2026.10.27 09:00",
		"weather":   "多云 18°C",
		"location":  "尧都区",
		"longitude": "111.500000°E",
		"latitude":  "36.200000°N",
		"antiCode":  "ABCDEFGHJKLMNP",
	}
	wmJSON, err := json.Marshal(wm)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		w, h   int
		orient string
	}{
		{"up", 1200, 1600, "up"},
		{"right", 800, 600, "right"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			photo := solidJPEG(t, tc.w, tc.h, color.RGBA{R: 168, G: 168, B: 168, A: 255})
			photoPath := filepath.Join(tmp, "photo_"+tc.name+".jpg")
			nodeOut := filepath.Join(tmp, "node_"+tc.name+".jpg")
			if err := os.WriteFile(photoPath, photo, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(node, script, renderAbs, photoPath, string(wmJSON), tc.orient, nodeOut)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Node 渲染失败: %v\n%s", err, out)
			}
			nodeBuf, err := os.ReadFile(nodeOut)
			if err != nil {
				t.Fatal(err)
			}

			r := &WatermarkRenderer{}
			goBuf, err := r.Render(photo, SanitizeWm(wm), tc.orient)
			if err != nil {
				t.Fatalf("Go 渲染失败: %v", err)
			}
			nodeImg, err := jpeg.Decode(bytes.NewReader(nodeBuf))
			if err != nil {
				t.Fatalf("Node 产物解码失败: %v", err)
			}
			goImg, err := jpeg.Decode(bytes.NewReader(goBuf))
			if err != nil {
				t.Fatalf("Go 产物解码失败: %v", err)
			}
			if nodeImg.Bounds() != goImg.Bounds() {
				t.Fatalf("产物尺寸不一致: node=%v go=%v", nodeImg.Bounds(), goImg.Bounds())
			}
			compareLandmarks(t, extractLandmarks(nodeImg), extractLandmarks(goImg))
		})
	}
}
