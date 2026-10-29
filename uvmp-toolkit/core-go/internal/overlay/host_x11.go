//go:build linux

// overlay：Linux X11 原生实现（纯 Go 线协议直连 X server，无 cgo——构建线全程 CGO_ENABLED=0）。
//
// 要点：
//   - 透明置顶悬浮窗 = 32 位 ARGB 可视区 + override-redirect（绕开 WM，免装饰免焦点），
//     内容逐像素预乘 BGRA 经 PutImage 送显（合成器按 alpha 混出透明区）；
//   - 全局不透明度烘进像素 alpha（与 Windows 实现同口径，不依赖 _NET_WM_WINDOW_OPACITY）；
//   - 截屏 GetImage(root 区域)；多屏 Xinerama（缺扩展回退单屏）；DPI 读 RESOURCE_MANAGER 的 Xft.dpi；
//   - 拖拽轮询 QueryPointer（root 坐标 + 按键掩码，不依赖 pointer capture）。
package overlay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// x11Conn X11 连接（握手 + 请求/回复分流 + 事件循环）。
type x11Conn struct {
	c        net.Conn
	info     *x11SetupInfo
	root     uint32
	ridBase  uint32
	ridMask  uint32
	nextRID  uint32
	maxReq   int // 字节
	seq      uint16
	wrMu     sync.Mutex
	pendMu   sync.Mutex
	pending  map[uint16]chan []byte
	winMu    sync.Mutex
	windows  map[uint32]*x11Window
	closed   chan struct{}
	closeOne sync.Once
	scale    float64
	workArea Rect
	heads    []Rect
}

// connectX11 连接 X server 并完成握手。
func connectX11() (*x11Conn, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		return nil, errors.New("未设置 DISPLAY（需要 X11 会话）")
	}
	host, dispNum, screenNum := parseDisplay(display)
	if host != "" && host != "unix" {
		return nil, fmt.Errorf("仅支持本地 X11 会话（DISPLAY=%q）", display)
	}
	conn, err := dialX11(dispNum)
	if err != nil {
		return nil, err
	}
	authName, authData := readXauth(dispNum)
	if _, err := conn.Write(x11ReqSetupRequest(authName, authData)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("X11 握手发送失败：%w", err)
	}
	hdr := make([]byte, 8)
	if err := readFull(conn, hdr); err != nil {
		conn.Close()
		return nil, fmt.Errorf("X11 握手读取失败：%w", err)
	}
	bodyLen := int(binary.LittleEndian.Uint16(hdr[6:8])) * 4
	body := make([]byte, bodyLen)
	if err := readFull(conn, body); err != nil {
		conn.Close()
		return nil, fmt.Errorf("X11 握手读取失败：%w", err)
	}
	info, err := x11ParseSetup(append(hdr, body...))
	if err != nil {
		conn.Close()
		return nil, err
	}
	if info.ImageByteOrder != 0 {
		conn.Close()
		return nil, errors.New("X11 大端字节序服务器暂不支持")
	}
	if screenNum >= len(info.Roots) {
		screenNum = 0
	}
	root := info.Roots[screenNum]
	cn := &x11Conn{
		c:       conn,
		info:    info,
		root:    root.Root,
		ridBase: info.RIDBase,
		ridMask: info.RIDMask,
		maxReq:  int(info.MaxRequestLen) * 4,
		pending: map[uint16]chan []byte{},
		windows: map[uint32]*x11Window{},
		closed:  make(chan struct{}),
		scale:   1,
	}
	if cn.maxReq <= 0 {
		cn.maxReq = 262144
	}
	go cn.readLoop()
	cn.initGeometry()
	return cn, nil
}

// parseDisplay 解析 DISPLAY（[host]:display[.screen]）。
func parseDisplay(s string) (host string, disp, screen int) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, 0
	}
	host = s[:i]
	rest := s[i+1:]
	if j := strings.Index(rest, "."); j >= 0 {
		screen, _ = strconv.Atoi(rest[j+1:])
		rest = rest[:j]
	}
	disp, _ = strconv.Atoi(rest)
	return host, disp, screen
}

// dialX11 连本地 X socket（文件系统路径优先，其次抽象命名空间）。
func dialX11(dispNum int) (net.Conn, error) {
	path := fmt.Sprintf("/tmp/.X11-unix/X%d", dispNum)
	if c, err := net.DialTimeout("unix", path, 3*time.Second); err == nil {
		return c, nil
	}
	if c, err := net.DialTimeout("unix", "\x00"+path, 3*time.Second); err == nil {
		return c, nil
	}
	return nil, fmt.Errorf("连接 X11 socket 失败（%s）", path)
}

// readFull 读满 n 字节。
func readFull(c net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := c.Read(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// readXauth 从 XAUTHORITY（缺省 ~/.Xauthority）取本显示的 MIT-MAGIC-COOKIE-1。
func readXauth(dispNum int) (name, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".Xauthority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	dispStr := strconv.Itoa(dispNum)
	for p := 0; p+2 <= len(raw); {
		family := binary.BigEndian.Uint16(raw[p : p+2])
		p += 2
		readField := func() []byte {
			if p+2 > len(raw) {
				return nil
			}
			n := int(binary.BigEndian.Uint16(raw[p : p+2]))
			p += 2
			if p+n > len(raw) {
				return nil
			}
			v := raw[p : p+n]
			p += n
			return v
		}
		addr := readField()
		display := readField()
		nm := readField()
		dt := readField()
		_ = addr
		_ = family
		if string(nm) != "MIT-MAGIC-COOKIE-1" {
			continue
		}
		if len(display) > 0 && string(display) != dispStr {
			continue
		}
		return nm, dt
	}
	return nil, nil
}

// readLoop 读循环：回复按序号回投，事件分发到窗口。
func (cn *x11Conn) readLoop() {
	hdr := make([]byte, 32)
	for {
		if err := readFull(cn.c, hdr); err != nil {
			cn.closeOne.Do(func() { close(cn.closed) })
			return
		}
		if hdr[0] == x11EvtReply {
			bodyLen := int(binary.LittleEndian.Uint32(hdr[4:8])) * 4
			pkt := make([]byte, 32+bodyLen)
			copy(pkt, hdr)
			if bodyLen > 0 {
				if err := readFull(cn.c, pkt[32:]); err != nil {
					cn.closeOne.Do(func() { close(cn.closed) })
					return
				}
			}
			seq := binary.LittleEndian.Uint16(hdr[2:4])
			cn.pendMu.Lock()
			ch := cn.pending[seq]
			delete(cn.pending, seq)
			cn.pendMu.Unlock()
			if ch != nil {
				ch <- pkt
			}
			continue
		}
		ev := x11ParseEvent(hdr)
		if ev.Kind == x11KindDestroy {
			cn.winMu.Lock()
			w := cn.windows[ev.Window]
			cn.winMu.Unlock()
			if w != nil {
				w.markClosed()
			}
			continue
		}
		if ev.Kind != x11KindButtonPress && ev.Kind != x11KindButtonRelease {
			continue // 其余事件（含错误）无需交互：BadWindow 于销毁竞态属正常
		}
		cn.winMu.Lock()
		w := cn.windows[ev.Window]
		cn.winMu.Unlock()
		if w == nil {
			continue
		}
		we := WindowEvent{
			Kind:   EventPointerPress,
			X:      int(ev.X),
			Y:      int(ev.Y),
			Button: int(ev.Button),
		}
		if ev.Kind == x11KindButtonRelease {
			we.Kind = EventPointerRelease
		}
		// 窗内坐标为物理像素 → DIP
		s := w.dpiScale()
		we.X = int(float64(we.X) / s)
		we.Y = int(float64(we.Y) / s)
		select {
		case w.events <- we:
		default:
		}
	}
}

// allocRID 分配资源 id。
func (cn *x11Conn) allocRID() uint32 {
	cn.wrMu.Lock()
	defer cn.wrMu.Unlock()
	id := cn.ridBase | (cn.nextRID & cn.ridMask)
	cn.nextRID++
	return id
}

// request 发请求；expectReply 时同步等待回复（5s 超时）。
func (cn *x11Conn) request(req []byte, expectReply bool) ([]byte, error) {
	cn.wrMu.Lock()
	cn.seq++
	seq := cn.seq
	var ch chan []byte
	if expectReply {
		ch = make(chan []byte, 1)
		cn.pendMu.Lock()
		cn.pending[seq] = ch
		cn.pendMu.Unlock()
	}
	_, err := cn.c.Write(req)
	cn.wrMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("X11 写入失败：%w", err)
	}
	if !expectReply {
		return nil, nil
	}
	select {
	case b := <-ch:
		if b[0] == x11EvtError {
			return nil, fmt.Errorf("X11 错误码 %d", b[1])
		}
		return b, nil
	case <-time.After(5 * time.Second):
		return nil, errors.New("X11 回复超时")
	case <-cn.closed:
		return nil, errors.New("X11 连接已关闭")
	}
}

// internAtom 查/建原子。
func (cn *x11Conn) internAtom(name string) (uint32, error) {
	b, err := cn.request(x11ReqInternAtom(name), true)
	if err != nil {
		return 0, err
	}
	return x11ParseInternAtomReply(b)
}

// initGeometry 初始化缩放/工作区/多屏头（Xinerama 缺失回退单屏）。
func (cn *x11Conn) initGeometry() {
	root := cn.info.Roots[0]
	for _, r := range cn.info.Roots {
		if r.Root == cn.root {
			root = r
		}
	}
	cn.scale = 1
	// Xft.dpi（RESOURCE_MANAGER 预定义原子 23 / STRING 31）
	if b, err := cn.request(x11ReqGetProperty(cn.root, 23, 31), true); err == nil {
		if _, _, value, err := x11ParseGetPropertyReply(b); err == nil {
			cn.scale = parseXftDpi(string(value))
		}
	}
	waPx := Rect{X: 0, Y: 0, W: int(root.Width), H: int(root.Height)}
	if atom, err := cn.internAtom("_NET_WORKAREA"); err == nil {
		if b, err := cn.request(x11ReqGetProperty(cn.root, atom, 6), true); err == nil { // CARDINAL=6
			if _, _, value, err := x11ParseGetPropertyReply(b); err == nil && len(value) >= 16 {
				x := int32(binary.LittleEndian.Uint32(value[0:4]))
				y := int32(binary.LittleEndian.Uint32(value[4:8]))
				w := int32(binary.LittleEndian.Uint32(value[8:12]))
				h := int32(binary.LittleEndian.Uint32(value[12:16]))
				if w > 0 && h > 0 {
					waPx = Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}
				}
			}
		}
	}
	cn.workArea = toDipRect(waPx, cn.scale)
	cn.heads = nil
	if op, err := cn.xineramaOpcode(); err == nil && op != 0 {
		req := []byte{op, 5, 0x00, 0x01} // XineramaQueryScreens(5)
		if b, err := cn.request(req, true); err == nil {
			for _, s := range x11ParseXineramaReply(b) {
				cn.heads = append(cn.heads, toDipRect(Rect{
					X: int(s[0]), Y: int(s[1]), W: int(s[2]), H: int(s[3]),
				}, cn.scale))
			}
		}
	}
	if len(cn.heads) == 0 {
		cn.heads = []Rect{toDipRect(Rect{X: 0, Y: 0, W: int(root.Width), H: int(root.Height)}, cn.scale)}
	}
}

// xineramaOpcode 查 Xinerama 扩展主操作码（0 = 不支持）。
func (cn *x11Conn) xineramaOpcode() (uint8, error) {
	b, err := cn.request(x11ReqQueryExtension("XINERAMA"), true)
	if err != nil {
		return 0, err
	}
	present, major, err := x11ParseQueryExtensionReply(b)
	if err != nil || !present {
		return 0, nil
	}
	return major, nil
}

// parseXftDpi 从 RESOURCE_MANAGER 串解析 Xft.dpi → 缩放系数（缺省 1.0）。
func parseXftDpi(res string) float64 {
	for _, line := range strings.Split(res, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Xft.dpi") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil && v > 0 {
			return v / 96.0
		}
	}
	return 1
}

// toDipRect 物理矩形 → DIP 矩形。
func toDipRect(r Rect, scale float64) Rect {
	if scale <= 0 {
		scale = 1
	}
	return Rect{
		X: int(float64(r.X) / scale),
		Y: int(float64(r.Y) / scale),
		W: int(float64(r.W) / scale),
		H: int(float64(r.H) / scale),
	}
}

// Close 关闭连接。
func (cn *x11Conn) Close() {
	cn.closeOne.Do(func() {
		close(cn.closed)
		cn.c.Close()
	})
}

// ---------------------------------------------------------------- 宿主

// x11Host X11 宿主。
type x11Host struct {
	cn     *x11Conn
	closed bool
	mu     sync.Mutex
}

// newX11Host 连接 X server 构建宿主。
func newX11Host() (Host, error) {
	cn, err := connectX11()
	if err != nil {
		return nil, err
	}
	return &x11Host{cn: cn}, nil
}

// Displays 全部显示器（DIP）。
func (h *x11Host) Displays() []Display {
	out := make([]Display, 0, len(h.cn.heads))
	for i, head := range h.cn.heads {
		out = append(out, Display{
			ID:       i + 1,
			Bounds:   head,
			WorkArea: head,
			Scale:    h.cn.scale,
		})
	}
	// 工作区（_NET_WORKAREA）仅在单头时有意义，多头逐头近似取自身
	if len(out) == 1 {
		out[0].WorkArea = h.cn.workArea
	}
	return out
}

// Primary 主显示器（第一个头）。
func (h *x11Host) Primary() Display {
	ds := h.Displays()
	return ds[0]
}

// DisplayFor 相交面积最大的显示器（无交集取主显示器）。
func (h *x11Host) DisplayFor(r Rect) Display {
	ds := h.Displays()
	best := ds[0]
	bestArea := 0
	for _, d := range ds {
		ix := minInt(r.X+r.W, d.Bounds.X+d.Bounds.W) - maxInt(r.X, d.Bounds.X)
		iy := minInt(r.Y+r.H, d.Bounds.Y+d.Bounds.H) - maxInt(r.Y, d.Bounds.Y)
		area := 0
		if ix > 0 && iy > 0 {
			area = ix * iy
		}
		if area > bestArea {
			bestArea = area
			best = d
		}
	}
	if bestArea == 0 {
		return ds[0]
	}
	return best
}

// PointerState 光标位置（DIP）与左键按下态（QueryPointer root 坐标 + 按键掩码）。
func (h *x11Host) PointerState() (Point, bool, error) {
	b, err := h.cn.request(x11ReqQueryPointer(h.cn.root), true)
	if err != nil {
		return Point{}, false, err
	}
	st, err := x11ParseQueryPointerReply(b)
	if err != nil {
		return Point{}, false, err
	}
	s := h.cn.scale
	if s <= 0 {
		s = 1
	}
	return Point{
		X: int(float64(st.RootX) / s),
		Y: int(float64(st.RootY) / s),
	}, st.Mask&x11Button1Mask != 0, nil
}

// CaptureDisplay GetImage 截取显示器区域（稠密 BGRA8888，物理像素；maxThumb 长边封顶）。
func (h *x11Host) CaptureDisplay(d Display, maxThumb int) (*Frame, error) {
	px := Rect{
		X: int(float64(d.Bounds.X) * d.Scale),
		Y: int(float64(d.Bounds.Y) * d.Scale),
		W: int(float64(d.Bounds.W) * d.Scale),
		H: int(float64(d.Bounds.H) * d.Scale),
	}
	if px.W <= 0 || px.H <= 0 {
		return nil, errors.New("显示器尺寸非法")
	}
	b, err := h.cn.request(x11ReqGetImage(h.cn.root, int16(px.X), int16(px.Y), uint16(px.W), uint16(px.H)), true)
	if err != nil {
		return nil, err
	}
	depth, pixels, err := x11ParseGetImageReply(b)
	if err != nil {
		return nil, err
	}
	if len(pixels) == 0 {
		return nil, nil // 截屏为空
	}
	bgra, w, hh := unpackX11Image(pixels, px.W, px.H, depth)
	if bgra == nil {
		return nil, fmt.Errorf("不支持的截屏像素格式（depth=%d）", depth)
	}
	if maxThumb > 0 {
		tw, th := ThumbSize(w, hh, maxThumb)
		if tw != w || th != hh {
			return scaleFrameBGRA(bgra, w, hh, tw, th, d.ID), nil
		}
	}
	return &Frame{Pix: bgra, Width: w, Height: hh, DisplayID: d.ID}, nil
}

// unpackX11Image 解包 GetImage 像素流为稠密 BGRA（支持 32bpp BGRX 与 24bpp 打包 BGR）。
func unpackX11Image(pixels []byte, w, h int, depth uint8) ([]byte, int, int) {
	if w <= 0 || h <= 0 {
		return nil, 0, 0
	}
	out := make([]byte, w*h*4)
	switch {
	case depth == 32 || depth == 24:
		// 常见：depth 24 占 32bpp（BGRX）
		if len(pixels) >= w*h*4 {
			for i := 0; i < w*h; i++ {
				out[i*4] = pixels[i*4]
				out[i*4+1] = pixels[i*4+1]
				out[i*4+2] = pixels[i*4+2]
				out[i*4+3] = 255
			}
			return out, w, h
		}
		// 罕见：depth 24 打包 24bpp（行距 4 对齐）
		stride := (w*3 + 3) &^ 3
		if len(pixels) < stride*h {
			return nil, 0, 0
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				si := y*stride + x*3
				di := (y*w + x) * 4
				out[di] = pixels[si]
				out[di+1] = pixels[si+1]
				out[di+2] = pixels[si+2]
				out[di+3] = 255
			}
		}
		return out, w, h
	}
	return nil, 0, 0
}

// NewWindow 创建透明置顶悬浮窗。
func (h *x11Host) NewWindow(opts WindowOpts) (Window, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("宿主已关闭")
	}
	h.mu.Unlock()
	if opts.Opacity <= 0 {
		opts.Opacity = 1
	}
	vis, depth, err := x11FindARGBVisual(h.cn.info)
	if err != nil {
		return nil, err
	}
	_ = depth
	scale := h.DisplayFor(opts.Bounds).Scale
	if scale <= 0 {
		scale = 1
	}
	w := &x11Window{
		host:    h,
		events:  make(chan WindowEvent, 32),
		closed:  make(chan struct{}),
		bounds:  opts.Bounds,
		opacity: opts.Opacity,
		visible: true,
		scale:   scale,
	}
	w.id = h.cn.allocRID()
	w.gc = h.cn.allocRID()
	colormap := h.cn.allocRID()
	pxX := int16(float64(opts.Bounds.X) * scale)
	pxY := int16(float64(opts.Bounds.Y) * scale)
	pxW := uint16(float64(opts.Bounds.W) * scale)
	pxH := uint16(float64(opts.Bounds.H) * scale)
	eventMask := uint32(x11EventButtonPressMask | x11EventButtonRelMask | x11EventPointerMotion |
		x11EventExposure | x11EventStructureNotify)
	if _, err := h.cn.request(x11ReqCreateColormap(colormap, h.cn.root, vis.ID), false); err != nil {
		return nil, err
	}
	if _, err := h.cn.request(x11ReqCreateWindow(w.id, h.cn.root, vis.ID, colormap, eventMask, pxX, pxY, pxW, pxH), false); err != nil {
		return nil, err
	}
	if _, err := h.cn.request(x11ReqCreateGC(w.gc, w.id), false); err != nil {
		return nil, err
	}
	h.cn.winMu.Lock()
	h.cn.windows[w.id] = w
	h.cn.winMu.Unlock()
	if _, err := h.cn.request(x11ReqMapWindow(w.id), false); err != nil {
		return nil, err
	}
	// 首帧空内容（全透明）占位
	_ = w.Paint(NewBuffer(maxInt(opts.Bounds.W, 1), maxInt(opts.Bounds.H, 1)))
	return w, nil
}

// Close 释放宿主（关连接）。
func (h *x11Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	h.cn.Close()
}

// ---------------------------------------------------------------- 悬浮窗

// x11Window X11 悬浮窗（override-redirect 32 位 ARGB）。
type x11Window struct {
	host    *x11Host
	id      uint32
	gc      uint32
	events  chan WindowEvent
	closed  chan struct{}
	evtOnce sync.Once
	mu      sync.Mutex
	bounds  Rect // DIP
	opacity float64
	visible bool
	lastBuf *Buffer
	scale   float64
}

func (w *x11Window) dpiScale() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scale <= 0 {
		return 1
	}
	return w.scale
}

// Bounds 当前几何（DIP）。
func (w *x11Window) Bounds() Rect {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bounds
}

// SetBounds 移动+缩放一次设齐（x/y/w/h 同包 ConfigureWindow）。
func (w *x11Window) SetBounds(r Rect) {
	w.mu.Lock()
	w.bounds = r
	scale := w.scale
	w.mu.Unlock()
	_, _ = w.host.cn.request(x11ReqConfigureWindow(w.id,
		int16(float64(r.X)*scale), int16(float64(r.Y)*scale),
		uint16(float64(r.W)*scale), uint16(float64(r.H)*scale)), false)
}

// SetOpacity 设置全局不透明度（烘进像素 alpha 重绘，与 Windows 同口径）。
func (w *x11Window) SetOpacity(a float64) {
	if a < 0.3 {
		a = 0.3
	}
	if a > 1 {
		a = 1
	}
	w.mu.Lock()
	w.opacity = a
	buf := w.lastBuf
	w.mu.Unlock()
	if buf != nil {
		_ = w.Paint(buf)
	}
}

// SetVisible 映射/取消映射（override-redirect 窗无焦点抢占问题）。
func (w *x11Window) SetVisible(v bool) {
	w.mu.Lock()
	w.visible = v
	w.mu.Unlock()
	if v {
		_, _ = w.host.cn.request(x11ReqMapWindow(w.id), false)
	} else {
		_, _ = w.host.cn.request(x11ReqUnmapWindow(w.id), false)
	}
}

// Paint 提交整窗内容（RGBA 预乘 → BGRA 预乘 + 不透明度，按行块 PutImage）。
func (w *x11Window) Paint(b *Buffer) error {
	if b == nil {
		return errors.New("空内容缓冲")
	}
	w.mu.Lock()
	w.lastBuf = b
	bb := w.bounds
	opacity := w.opacity
	scale := w.scale
	w.mu.Unlock()
	if b.W != bb.W || b.H != bb.H {
		return errors.New("内容缓冲尺寸与窗口不一致")
	}
	pxW := int(float64(bb.W) * scale)
	pxH := int(float64(bb.H) * scale)
	if pxW < 1 {
		pxW = 1
	}
	if pxH < 1 {
		pxH = 1
	}
	bgra := b.BGRA()
	if pxW != b.W || pxH != b.H {
		bgra = scaleFrameBGRA(bgra, b.W, b.H, pxW, pxH, 0).Pix
	}
	if opacity < 1 {
		f := uint8(opacity * 255)
		for i := 0; i < len(bgra); i += 4 {
			bgra[i] = uint8(uint32(bgra[i]) * uint32(f) / 255)
			bgra[i+1] = uint8(uint32(bgra[i+1]) * uint32(f) / 255)
			bgra[i+2] = uint8(uint32(bgra[i+2]) * uint32(f) / 255)
			bgra[i+3] = uint8(uint32(bgra[i+3]) * uint32(f) / 255)
		}
	}
	// 按行块分包（请求上限 maxReq 字节）
	maxPayload := w.host.cn.maxReq - 32
	if maxPayload < pxW*4 {
		maxPayload = pxW * 4
	}
	rowsPerReq := maxPayload / (pxW * 4)
	if rowsPerReq < 1 {
		rowsPerReq = 1
	}
	for y := 0; y < pxH; y += rowsPerReq {
		rows := minInt(rowsPerReq, pxH-y)
		data := bgra[y*pxW*4 : (y+rows)*pxW*4]
		req := x11ReqPutImage(w.id, w.gc, 0, int16(y), uint16(pxW), uint16(rows), data)
		if _, err := w.host.cn.request(req, false); err != nil {
			return err
		}
	}
	return nil
}

// Events 输入事件流。
func (w *x11Window) Events() <-chan WindowEvent { return w.events }

// Close 销毁窗口（幂等）。
func (w *x11Window) Close() {
	w.evtOnce.Do(func() {
		_, _ = w.host.cn.request(x11ReqDestroyWindow(w.id), false)
		w.host.cn.winMu.Lock()
		delete(w.host.cn.windows, w.id)
		w.host.cn.winMu.Unlock()
		close(w.events)
		close(w.closed)
	})
}

func (w *x11Window) markClosed() {
	w.evtOnce.Do(func() {
		w.host.cn.winMu.Lock()
		delete(w.host.cn.windows, w.id)
		w.host.cn.winMu.Unlock()
		close(w.events)
		close(w.closed)
	})
}
