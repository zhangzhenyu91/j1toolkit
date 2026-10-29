// overlay：X11 核心协议编解码（纯函数、无 cgo、平台无关，可跨平台测试）。
// 协议口径：X Window System Protocol（核心协议 11.0，小端）；只实现悬浮窗/截屏所需最小子集。
package overlay

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// X11 请求操作码（核心协议）
const (
	x11OpCreateWindow    = 1
	x11OpDestroyWindow   = 4
	x11OpMapWindow       = 8
	x11OpUnmapWindow     = 10
	x11OpConfigureWindow = 12
	x11OpInternAtom      = 16
	x11OpChangeProperty  = 18
	x11OpGetProperty     = 20
	x11OpQueryPointer    = 38
	x11OpCreateGC        = 55
	x11OpPutImage        = 72
	x11OpGetImage        = 73
	x11OpCreateColormap  = 78
	x11OpQueryExtension  = 98
)

// CreateWindow 值掩码与事件掩码
const (
	x11CWBorderPixel        = 0x00000008
	x11CWOverrideRedirect   = 0x00000200
	x11CWEventMask          = 0x00000800
	x11CWColormap           = 0x00002000
	x11EventButtonPressMask = 0x00000004
	x11EventButtonRelMask   = 0x00000008
	x11EventPointerMotion   = 0x00000040
	x11EventExposure        = 0x00008000
	x11EventStructureNotify = 0x00020000
	x11InputOutputClass     = 1
	x11ZPixmap              = 2
	x11PropModeReplace      = 0
	x11AllocNone            = 0
	x11Button1Mask          = 0x0100
	x11CfgX                 = 0x01
	x11CfgY                 = 0x02
	x11CfgW                 = 0x04
	x11CfgH                 = 0x08
)

// 事件码
const (
	x11EvtError           = 0
	x11EvtReply           = 1
	x11EvtButtonPress     = 4
	x11EvtButtonRelease   = 5
	x11EvtMotionNotify    = 6
	x11EvtExpose          = 12
	x11EvtDestroyNotify   = 17
	x11EvtMapNotify       = 19
	x11EvtConfigureNotify = 22
)

// x11SetupInfo 连接握手解析结果（用到的子集）。
type x11SetupInfo struct {
	Release        uint32
	RIDBase        uint32
	RIDMask        uint32
	Vendor         string
	MaxRequestLen  uint32 // 4 字节单位
	ImageByteOrder uint8
	Roots          []x11RootInfo
}

// x11RootInfo 屏幕（root 窗口）信息。
type x11RootInfo struct {
	Root            uint32
	DefaultColormap uint32
	Width           uint16
	Height          uint16
	RootVisual      uint32
	RootDepth       uint8
	Depths          []x11DepthInfo
}

// x11DepthInfo 深度与可视区。
type x11DepthInfo struct {
	Depth   uint8
	Visuals []x11VisualInfo
}

// x11VisualInfo 可视区（颜色模型）。
type x11VisualInfo struct {
	ID        uint32
	Class     uint8 // 4=TrueColor 5=DirectColor
	RedMask   uint32
	GreenMask uint32
	BlueMask  uint32
}

// errX11Short 数据包过短。
var errX11Short = errors.New("X11 数据包过短")

// x11ParseSetup 解析握手回复（含 8 字节回复头 + 后续定长/变长段）。
func x11ParseSetup(b []byte) (*x11SetupInfo, error) {
	if len(b) < 8 {
		return nil, errX11Short
	}
	status := b[0]
	if status != 1 {
		if status == 0 {
			reasonLen := int(b[1])
			return nil, fmt.Errorf("X11 连接被拒绝：%s", string(b[8:8+minInt(reasonLen, maxInt(len(b)-8, 0))]))
		}
		return nil, errors.New("X11 要求额外鉴权（xauth），请检查 XAUTHORITY")
	}
	// 握手回复长度为偏移 6 的 CARD16（4 字节单位，8 字节头之后）——与普通回复的 CARD32 不同
	total := 8 + int(binary.LittleEndian.Uint16(b[6:8]))*4
	if len(b) < total {
		total = len(b)
	}
	info := &x11SetupInfo{}
	p := 8
	if total < 40 {
		return nil, errX11Short
	}
	info.Release = binary.LittleEndian.Uint32(b[p : p+4])
	p += 4
	info.RIDBase = binary.LittleEndian.Uint32(b[p : p+4])
	p += 4
	info.RIDMask = binary.LittleEndian.Uint32(b[p : p+4])
	p += 4
	p += 4 // motion-buffer-size
	vendorLen := int(binary.LittleEndian.Uint16(b[p : p+2]))
	p += 2
	info.MaxRequestLen = uint32(binary.LittleEndian.Uint16(b[p : p+2]))
	p += 2
	rootsCount := int(b[p])
	p++
	formatsCount := int(b[p])
	p++
	info.ImageByteOrder = b[p]
	p += 6 // image-byte-order / bitmap-bit-order / scanline-unit / scanline-pad / min,max-keycode
	p += 4 // pad
	info.Vendor = string(b[p : p+minInt(vendorLen, maxInt(total-p, 0))])
	p += vendorLen
	p = (p + 3) &^ 3
	p += formatsCount * 8
	for r := 0; r < rootsCount && p+40 <= total; r++ {
		var root x11RootInfo
		root.Root = binary.LittleEndian.Uint32(b[p : p+4])
		p += 4
		root.DefaultColormap = binary.LittleEndian.Uint32(b[p : p+4])
		p += 4
		p += 12 // white/black pixel + current input masks
		root.Width = binary.LittleEndian.Uint16(b[p : p+2])
		p += 2
		root.Height = binary.LittleEndian.Uint16(b[p : p+2])
		p += 2
		p += 8 // mm 尺寸 + min/max installed maps
		root.RootVisual = binary.LittleEndian.Uint32(b[p : p+4])
		p += 4
		p += 2 // backing-stores / save-unders
		root.RootDepth = b[p]
		depthsCount := int(b[p+1])
		p += 2 // root-depth / allowed-depths-count（root 段共 40 字节收尾）
		for d := 0; d < depthsCount && p+8 <= total; d++ {
			var dep x11DepthInfo
			dep.Depth = b[p]
			p += 2
			visualsCount := int(binary.LittleEndian.Uint16(b[p : p+2]))
			p += 6
			for v := 0; v < visualsCount && p+24 <= total; v++ {
				var vis x11VisualInfo
				vis.ID = binary.LittleEndian.Uint32(b[p : p+4])
				vis.Class = b[p+4]
				vis.RedMask = binary.LittleEndian.Uint32(b[p+8 : p+12])
				vis.GreenMask = binary.LittleEndian.Uint32(b[p+12 : p+16])
				vis.BlueMask = binary.LittleEndian.Uint32(b[p+16 : p+20])
				p += 24
				dep.Visuals = append(dep.Visuals, vis)
			}
			root.Depths = append(root.Depths, dep)
		}
		info.Roots = append(info.Roots, root)
	}
	return info, nil
}

// x11FindARGBVisual 在握手中找 32 位 TrueColor（0xff0000/0xff00/0xff）可视区与深度。
func x11FindARGBVisual(info *x11SetupInfo) (x11VisualInfo, uint8, error) {
	for _, root := range info.Roots {
		for _, dep := range root.Depths {
			for _, vis := range dep.Visuals {
				if vis.Class == 4 && vis.RedMask == 0xff0000 && vis.GreenMask == 0xff00 && vis.BlueMask == 0xff {
					return vis, dep.Depth, nil
				}
			}
		}
	}
	return x11VisualInfo{}, 0, errors.New("X11 无 32 位 ARGB 可视区，无法透明置顶")
}

// ---------------------------------------------------------------- 请求编码

// x11NewReq 分配请求包（size 为 4 的倍数，含 4 字节头）并写长度字段。
func x11NewReq(op byte, size int) []byte {
	b := make([]byte, size)
	b[0] = op
	binary.LittleEndian.PutUint16(b[2:4], uint16(size/4))
	return b
}

func pad4(n int) int { return (n + 3) &^ 3 }

// x11ReqSetupRequest 握手请求（格式独立于核心请求）。
func x11ReqSetupRequest(authName, authData []byte) []byte {
	n := len(authName)
	d := len(authData)
	total := 12 + pad4(n) + pad4(d)
	b := make([]byte, total)
	b[0] = 'l' // LSBFirst
	binary.LittleEndian.PutUint16(b[2:4], 11)
	binary.LittleEndian.PutUint16(b[4:6], 0)
	binary.LittleEndian.PutUint16(b[6:8], uint16(n))
	binary.LittleEndian.PutUint16(b[8:10], uint16(d))
	copy(b[12:], authName)
	copy(b[12+pad4(n):], authData)
	return b
}

// x11ReqInternAtom 查原子（不缓存失败语义：only-if-exists=false 会创建）。
func x11ReqInternAtom(name string) []byte {
	b := x11NewReq(x11OpInternAtom, 8+pad4(len(name)))
	binary.LittleEndian.PutUint16(b[4:6], uint16(len(name)))
	copy(b[8:], name)
	return b
}

// x11ReqCreateWindow 创建 32 位 ARGB 窗口（override-redirect、指定事件掩码）。
// value 掩码含 BorderPixel/OverrideRedirect/EventMask/Colormap，值按位升序各 4 字节。
func x11ReqCreateWindow(wid, parent, visual, colormap, eventMask uint32, x, y int16, w, h uint16) []byte {
	b := x11NewReq(x11OpCreateWindow, 48)
	b[1] = 32 // 32 位深（ARGB 可视区）
	binary.LittleEndian.PutUint32(b[4:8], wid)
	binary.LittleEndian.PutUint32(b[8:12], parent)
	binary.LittleEndian.PutUint16(b[12:14], uint16(x))
	binary.LittleEndian.PutUint16(b[14:16], uint16(y))
	binary.LittleEndian.PutUint16(b[16:18], w)
	binary.LittleEndian.PutUint16(b[18:20], h)
	binary.LittleEndian.PutUint16(b[20:22], 0) // border-width
	binary.LittleEndian.PutUint16(b[22:24], x11InputOutputClass)
	binary.LittleEndian.PutUint32(b[24:28], visual)
	mask := uint32(x11CWBorderPixel | x11CWOverrideRedirect | x11CWEventMask | x11CWColormap)
	binary.LittleEndian.PutUint32(b[28:32], mask)
	binary.LittleEndian.PutUint32(b[32:36], 0) // BorderPixel
	binary.LittleEndian.PutUint32(b[36:40], 1) // OverrideRedirect = true
	binary.LittleEndian.PutUint32(b[40:44], eventMask)
	binary.LittleEndian.PutUint32(b[44:48], colormap)
	return b
}

// x11ReqCreateColormap 分配与 ARGB 可视区配套的 colormap。
func x11ReqCreateColormap(mid, window, visual uint32) []byte {
	b := x11NewReq(x11OpCreateColormap, 16)
	b[1] = x11AllocNone
	binary.LittleEndian.PutUint32(b[4:8], mid)
	binary.LittleEndian.PutUint32(b[8:12], window)
	binary.LittleEndian.PutUint32(b[12:16], visual)
	return b
}

// x11ReqDestroyWindow 销毁窗口。
func x11ReqDestroyWindow(window uint32) []byte {
	b := x11NewReq(x11OpDestroyWindow, 8)
	binary.LittleEndian.PutUint32(b[4:8], window)
	return b
}

// x11ReqMapWindow 映射（显示）。
func x11ReqMapWindow(window uint32) []byte {
	b := x11NewReq(x11OpMapWindow, 8)
	binary.LittleEndian.PutUint32(b[4:8], window)
	return b
}

// x11ReqUnmapWindow 取消映射（隐藏）。
func x11ReqUnmapWindow(window uint32) []byte {
	b := x11NewReq(x11OpUnmapWindow, 8)
	binary.LittleEndian.PutUint32(b[4:8], window)
	return b
}

// x11ReqConfigureWindow 移动/缩放一次设齐（值为 32 位，属性 16 位语义；
// 透明窗分步缩放在部分实现会失灵——已踩坑）。
func x11ReqConfigureWindow(window uint32, x, y int16, w, h uint16) []byte {
	b := x11NewReq(x11OpConfigureWindow, 28)
	binary.LittleEndian.PutUint32(b[4:8], window)
	binary.LittleEndian.PutUint16(b[8:10], x11CfgX|x11CfgY|x11CfgW|x11CfgH)
	binary.LittleEndian.PutUint32(b[12:16], uint32(int32(x)))
	binary.LittleEndian.PutUint32(b[16:20], uint32(int32(y)))
	binary.LittleEndian.PutUint32(b[20:24], uint32(w))
	binary.LittleEndian.PutUint32(b[24:28], uint32(h))
	return b
}

// x11ReqChangeProperty 写窗口属性（data 为 32 位小端值序列）。
func x11ReqChangeProperty(window, property, typ uint32, format uint8, data []byte) []byte {
	b := x11NewReq(x11OpChangeProperty, 24+pad4(len(data)))
	b[1] = x11PropModeReplace
	binary.LittleEndian.PutUint32(b[4:8], window)
	binary.LittleEndian.PutUint32(b[8:12], property)
	binary.LittleEndian.PutUint32(b[12:16], typ)
	b[16] = format
	binary.LittleEndian.PutUint32(b[20:24], uint32(len(data)*8/int(format)))
	copy(b[24:], data)
	return b
}

// x11ReqGetProperty 读窗口属性。
func x11ReqGetProperty(window, property, typ uint32) []byte {
	b := x11NewReq(x11OpGetProperty, 24)
	binary.LittleEndian.PutUint32(b[4:8], window)
	binary.LittleEndian.PutUint32(b[8:12], property)
	binary.LittleEndian.PutUint32(b[12:16], typ)
	binary.LittleEndian.PutUint32(b[20:24], 0x7fffffff) // long-length
	return b
}

// x11ReqQueryPointer 查询光标（root 坐标 + 按键掩码，拖拽轮询用）。
func x11ReqQueryPointer(window uint32) []byte {
	b := x11NewReq(x11OpQueryPointer, 8)
	binary.LittleEndian.PutUint32(b[4:8], window)
	return b
}

// x11ReqCreateGC 创建绘图上下文（PutImage 用，无附加属性）。
func x11ReqCreateGC(cid, drawable uint32) []byte {
	b := x11NewReq(x11OpCreateGC, 16)
	binary.LittleEndian.PutUint32(b[4:8], cid)
	binary.LittleEndian.PutUint32(b[8:12], drawable)
	return b
}

// x11ReqPutImage 塞入位图（ZPixmap 32bpp；data 为小端 BGRA 流，长度自含）。
func x11ReqPutImage(drawable, gc uint32, x, y int16, w, h uint16, data []byte) []byte {
	b := x11NewReq(x11OpPutImage, 24+pad4(len(data)))
	b[1] = x11ZPixmap
	binary.LittleEndian.PutUint32(b[4:8], drawable)
	binary.LittleEndian.PutUint32(b[8:12], gc)
	binary.LittleEndian.PutUint16(b[12:14], w)
	binary.LittleEndian.PutUint16(b[14:16], h)
	binary.LittleEndian.PutUint16(b[16:18], uint16(x))
	binary.LittleEndian.PutUint16(b[18:20], uint16(y))
	b[20] = 0 // left-pad
	b[21] = 32
	copy(b[24:], data)
	return b
}

// x11ReqGetImage 截取窗口/根窗口区域（ZPixmap）。
func x11ReqGetImage(drawable uint32, x, y int16, w, h uint16) []byte {
	b := x11NewReq(x11OpGetImage, 20)
	b[1] = x11ZPixmap
	binary.LittleEndian.PutUint32(b[4:8], drawable)
	binary.LittleEndian.PutUint16(b[8:10], uint16(x))
	binary.LittleEndian.PutUint16(b[10:12], uint16(y))
	binary.LittleEndian.PutUint16(b[12:14], w)
	binary.LittleEndian.PutUint16(b[14:16], h)
	binary.LittleEndian.PutUint32(b[16:20], 0xffffffff) // plane-mask
	return b
}

// x11ReqQueryExtension 查扩展（XINERAMA 多屏用）。
func x11ReqQueryExtension(name string) []byte {
	b := x11NewReq(x11OpQueryExtension, 8+pad4(len(name)))
	binary.LittleEndian.PutUint16(b[4:6], uint16(len(name)))
	copy(b[8:], name)
	return b
}

// ---------------------------------------------------------------- 回复/事件解析

// x11ReplyBodyLen 回复包全长（8 字节头 + 声明体长）。
func x11ReplyBodyLen(b []byte) int {
	if len(b) < 8 {
		return 0
	}
	return 8 + int(binary.LittleEndian.Uint32(b[4:8]))*4
}

// x11ParseInternAtomReply 解析 InternAtom 回复。
func x11ParseInternAtomReply(b []byte) (uint32, error) {
	if len(b) < 32 || b[0] != x11EvtReply {
		return 0, errX11Short
	}
	return binary.LittleEndian.Uint32(b[8:12]), nil
}

// x11PointerState QueryPointer 回复解析结果。
type x11PointerState struct {
	RootX, RootY int16
	Mask         uint16
}

// x11ParseQueryPointerReply 解析 QueryPointer 回复。
func x11ParseQueryPointerReply(b []byte) (x11PointerState, error) {
	var st x11PointerState
	if len(b) < 32 || b[0] != x11EvtReply {
		return st, errX11Short
	}
	st.RootX = int16(binary.LittleEndian.Uint16(b[16:18]))
	st.RootY = int16(binary.LittleEndian.Uint16(b[18:20]))
	st.Mask = binary.LittleEndian.Uint16(b[24:26])
	return st, nil
}

// x11ParseGetImageReply 解析 GetImage 回复（返回像素流与深度）。
func x11ParseGetImageReply(b []byte) (depth uint8, pixels []byte, err error) {
	if len(b) < 32 || b[0] != x11EvtReply {
		return 0, nil, errX11Short
	}
	depth = b[1]
	total := x11ReplyBodyLen(b)
	if len(b) < total {
		total = len(b)
	}
	if total < 32 {
		return depth, nil, nil
	}
	return depth, b[32:total], nil
}

// x11ParseGetPropertyReply 解析 GetProperty 回复（type/format/value）。
func x11ParseGetPropertyReply(b []byte) (typ uint32, format uint8, value []byte, err error) {
	if len(b) < 32 || b[0] != x11EvtReply {
		return 0, 0, nil, errX11Short
	}
	typ = binary.LittleEndian.Uint32(b[8:12])
	format = b[16]
	units := binary.LittleEndian.Uint32(b[20:24])
	bytesLen := int(units) * int(format) / 8
	total := x11ReplyBodyLen(b)
	if len(b) < total {
		total = len(b)
	}
	if total < 32 {
		return typ, format, nil, nil
	}
	if 32+bytesLen > total {
		bytesLen = total - 32
	}
	return typ, format, b[32 : 32+bytesLen], nil
}

// x11ParseQueryExtensionReply 解析 QueryExtension 回复。
func x11ParseQueryExtensionReply(b []byte) (present bool, majorOpcode uint8, err error) {
	if len(b) < 32 || b[0] != x11EvtReply {
		return false, 0, errX11Short
	}
	return b[8] != 0, b[9], nil
}

// x11ParseXineramaReply 解析 XineramaQueryScreens 回复（x,y,w,h 序列）。
func x11ParseXineramaReply(b []byte) [][4]int16 {
	if len(b) < 32 || b[0] != x11EvtReply {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b[8:12]))
	out := make([][4]int16, 0, n)
	p := 32
	for i := 0; i < n && p+8 <= len(b); i++ {
		out = append(out, [4]int16{
			int16(binary.LittleEndian.Uint16(b[p : p+2])),
			int16(binary.LittleEndian.Uint16(b[p+2 : p+4])),
			int16(binary.LittleEndian.Uint16(b[p+4 : p+6])),
			int16(binary.LittleEndian.Uint16(b[p+6 : p+8])),
		})
		p += 8
	}
	return out
}

// x11EventKind 事件类别。
type x11EventKind int

const (
	x11KindOther x11EventKind = iota
	x11KindButtonPress
	x11KindButtonRelease
	x11KindMotion
	x11KindExpose
	x11KindDestroy
	x11KindMap
	x11KindConfigure
	x11KindError
)

// x11Event 事件（用到的字段子集）。
type x11Event struct {
	Kind    x11EventKind
	Code    uint8
	Seq     uint16
	Window  uint32
	X, Y    int16 // 窗口内坐标
	Button  uint8
	Width   uint16
	Height  uint16
	ErrCode uint8
}

// x11ParseEvent 解析 32 字节事件/错误（回复包由连接层分流）。
func x11ParseEvent(b []byte) x11Event {
	if len(b) < 32 {
		return x11Event{Kind: x11KindOther}
	}
	ev := x11Event{Code: b[0], Seq: binary.LittleEndian.Uint16(b[2:4])}
	switch b[0] {
	case x11EvtError:
		ev.Kind = x11KindError
		ev.ErrCode = b[1]
	case x11EvtButtonPress, x11EvtButtonRelease:
		ev.Window = binary.LittleEndian.Uint32(b[4:8])
		ev.Button = b[1]
		ev.X = int16(binary.LittleEndian.Uint16(b[16:18]))
		ev.Y = int16(binary.LittleEndian.Uint16(b[18:20]))
		if b[0] == x11EvtButtonPress {
			ev.Kind = x11KindButtonPress
		} else {
			ev.Kind = x11KindButtonRelease
		}
	case x11EvtMotionNotify:
		ev.Kind = x11KindMotion
		ev.Window = binary.LittleEndian.Uint32(b[4:8])
		ev.X = int16(binary.LittleEndian.Uint16(b[16:18]))
		ev.Y = int16(binary.LittleEndian.Uint16(b[18:20]))
	case x11EvtExpose:
		ev.Kind = x11KindExpose
		ev.Window = binary.LittleEndian.Uint32(b[4:8])
	case x11EvtDestroyNotify:
		ev.Kind = x11KindDestroy
		ev.Window = binary.LittleEndian.Uint32(b[8:12])
	case x11EvtMapNotify:
		ev.Kind = x11KindMap
		ev.Window = binary.LittleEndian.Uint32(b[8:12])
	case x11EvtConfigureNotify:
		ev.Kind = x11KindConfigure
		ev.Window = binary.LittleEndian.Uint32(b[8:12])
		ev.Width = binary.LittleEndian.Uint16(b[20:22])
		ev.Height = binary.LittleEndian.Uint16(b[22:24])
	default:
		ev.Kind = x11KindOther
	}
	return ev
}
