// X11 协议编解码测试：请求长度/字段偏移与握手/事件解析（纯字节，无真实 X 连接）。
package overlay

import (
	"encoding/binary"
	"testing"
)

func TestX11ReqSizes(t *testing.T) {
	// 请求长度字段 = 包长/4，且所有请求为 4 字节对齐（协议硬性要求）
	reqs := map[string][]byte{
		"InternAtom":     x11ReqInternAtom("WM_NAME"),
		"CreateWindow":   x11ReqCreateWindow(0x2000001, 0x123, 0x21, 0x22, 0x80c, 10, 20, 300, 100),
		"CreateColormap": x11ReqCreateColormap(0x30, 0x123, 0x21),
		"DestroyWindow":  x11ReqDestroyWindow(0x2000001),
		"MapWindow":      x11ReqMapWindow(0x2000001),
		"UnmapWindow":    x11ReqUnmapWindow(0x2000001),
		"Configure":      x11ReqConfigureWindow(0x2000001, 5, 6, 320, 240),
		"ChangeProperty": x11ReqChangeProperty(0x2000001, 0x40, 0x41, 32, make([]byte, 8)),
		"GetProperty":    x11ReqGetProperty(0x123, 0x40, 0),
		"QueryPointer":   x11ReqQueryPointer(0x123),
		"CreateGC":       x11ReqCreateGC(0x50, 0x2000001),
		"PutImage":       x11ReqPutImage(0x2000001, 0x50, 0, 0, 4, 2, make([]byte, 32)),
		"GetImage":       x11ReqGetImage(0x123, 100, 200, 640, 480),
		"QueryExtension": x11ReqQueryExtension("XINERAMA"),
	}
	for name, r := range reqs {
		if len(r)%4 != 0 {
			t.Errorf("%s 包长 %d 非 4 对齐", name, len(r))
		}
		if got := int(binary.LittleEndian.Uint16(r[2:4])) * 4; got != len(r) {
			t.Errorf("%s 长度字段=%d 字节，实际 %d", name, got, len(r))
		}
	}
	// CreateWindow：depth=32、value 掩码四值齐备
	cw := x11ReqCreateWindow(0x2000001, 0x123, 0x21, 0x22, 0x80c, 10, 20, 300, 100)
	if cw[1] != 32 {
		t.Errorf("CreateWindow depth = %d, want 32", cw[1])
	}
	if binary.LittleEndian.Uint32(cw[28:32]) != x11CWBorderPixel|x11CWOverrideRedirect|x11CWEventMask|x11CWColormap {
		t.Error("CreateWindow value-mask 错误")
	}
	if binary.LittleEndian.Uint32(cw[36:40]) != 1 {
		t.Error("override-redirect 应为 1")
	}
	if binary.LittleEndian.Uint16(cw[2:4]) != 12 {
		t.Errorf("request length = %d words, want 12", binary.LittleEndian.Uint16(cw[2:4]))
	}
	// ConfigureWindow：值为 32 位
	cf := x11ReqConfigureWindow(0x2000001, -5, 6, 320, 240)
	if int32(binary.LittleEndian.Uint32(cf[12:16])) != -5 {
		t.Error("ConfigureWindow x 应为 32 位有符号")
	}
	// GetProperty：long-length 位于偏移 20
	gp := x11ReqGetProperty(0x123, 0x40, 0)
	if binary.LittleEndian.Uint32(gp[20:24]) != 0x7fffffff {
		t.Error("GetProperty long-length 偏移错误")
	}
}

func TestX11ParseSetup(t *testing.T) {
	// 合成握手回复：1 root、1 depth(32)、1 TrueColor ARGB visual
	body := buildFakeSetup()
	info, err := x11ParseSetup(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Roots) != 1 || info.Roots[0].Width != 1920 || info.Roots[0].Height != 1080 {
		t.Fatalf("roots = %+v", info.Roots)
	}
	vis, depth, err := x11FindARGBVisual(info)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 32 || vis.ID != 0x21 || vis.RedMask != 0xff0000 {
		t.Fatalf("visual = %+v depth=%d", vis, depth)
	}
	if info.MaxRequestLen != 65535 {
		t.Errorf("max-request-len = %d", info.MaxRequestLen)
	}
}

func TestX11ParseSetupRefused(t *testing.T) {
	b := make([]byte, 16)
	b[0] = 0 // 失败
	b[1] = 4
	copy(b[8:], "nope")
	if _, err := x11ParseSetup(b); err == nil {
		t.Fatal("want 拒绝错误")
	}
}

func TestX11ParseEvents(t *testing.T) {
	b := make([]byte, 32)
	b[0] = x11EvtButtonPress
	b[1] = 1 // button 1
	binary.LittleEndian.PutUint32(b[4:8], 0x2000001)
	binary.LittleEndian.PutUint16(b[16:18], 42)
	binary.LittleEndian.PutUint16(b[18:20], 24)
	ev := x11ParseEvent(b)
	if ev.Kind != x11KindButtonPress || ev.Window != 0x2000001 || ev.X != 42 || ev.Y != 24 || ev.Button != 1 {
		t.Fatalf("button event = %+v", ev)
	}
	b[0] = x11EvtError
	b[1] = 3
	if ev := x11ParseEvent(b); ev.Kind != x11KindError || ev.ErrCode != 3 {
		t.Fatalf("error event = %+v", ev)
	}
	b[0] = x11EvtConfigureNotify
	binary.LittleEndian.PutUint32(b[8:12], 0x2000001)
	binary.LittleEndian.PutUint16(b[20:22], 720)
	binary.LittleEndian.PutUint16(b[22:24], 180)
	ev = x11ParseEvent(b)
	if ev.Kind != x11KindConfigure || ev.Width != 720 || ev.Height != 180 {
		t.Fatalf("configure event = %+v", ev)
	}
}

func TestX11ParseReplies(t *testing.T) {
	b := make([]byte, 32)
	b[0] = x11EvtReply
	binary.LittleEndian.PutUint32(b[8:12], 0x1234)
	if atom, err := x11ParseInternAtomReply(b); err != nil || atom != 0x1234 {
		t.Fatalf("atom = %x err=%v", atom, err)
	}
	negX := int16(-10)
	binary.LittleEndian.PutUint16(b[16:18], uint16(negX))
	binary.LittleEndian.PutUint16(b[18:20], 30)
	binary.LittleEndian.PutUint16(b[24:26], x11Button1Mask)
	st, err := x11ParseQueryPointerReply(b)
	if err != nil || st.RootX != -10 || st.RootY != 30 || st.Mask&x11Button1Mask == 0 {
		t.Fatalf("pointer = %+v err=%v", st, err)
	}
	// GetImage 回复：depth=24，像素从偏移 32 起
	img := make([]byte, 32+16)
	img[0] = x11EvtReply
	img[1] = 24
	binary.LittleEndian.PutUint32(img[4:8], 10) // 全长 8+10*4=48，体数据 16 字节
	depth, pix, err := x11ParseGetImageReply(img)
	if err != nil || depth != 24 || len(pix) != 16 {
		t.Fatalf("image depth=%d len=%d err=%v", depth, len(pix), err)
	}
}

// buildFakeSetup 合成最小握手回复字节流。
func buildFakeSetup() []byte {
	vendor := "fake"
	// 定长 40（8 头 + 32）+ vendor + pad + formats(0) + root(40) + depth(8) + visual(24)
	vendorPad := pad4(len(vendor))
	total := 8 + 32 + vendorPad + 40 + 8 + 24
	b := make([]byte, total+8)
	b[0] = 1                                                   // success
	binary.LittleEndian.PutUint16(b[2:4], 11)                  // protocol major
	binary.LittleEndian.PutUint16(b[4:6], 0)                   // protocol minor
	binary.LittleEndian.PutUint16(b[6:8], uint16((total-8)/4)) // 长度（4 字节单位）
	p := 8
	binary.LittleEndian.PutUint32(b[p:p+4], 1170000000) // release
	p += 4
	binary.LittleEndian.PutUint32(b[p:p+4], 0x2000000) // rid base
	p += 4
	binary.LittleEndian.PutUint32(b[p:p+4], 0x1fffff) // rid mask
	p += 4
	p += 4 // motion buffer
	binary.LittleEndian.PutUint16(b[p:p+2], uint16(len(vendor)))
	p += 2
	binary.LittleEndian.PutUint16(b[p:p+2], 65535) // max request len
	p += 2
	b[p] = 1 // roots
	p++
	b[p] = 0 // formats
	p++
	b[p] = 0 // LSBFirst
	p += 6
	p += 4 // pad
	copy(b[p:], vendor)
	p += vendorPad
	// root
	binary.LittleEndian.PutUint32(b[p:p+4], 0x123) // root
	p += 4
	p += 4 // colormap
	p += 12
	binary.LittleEndian.PutUint16(b[p:p+2], 1920)
	p += 2
	binary.LittleEndian.PutUint16(b[p:p+2], 1080)
	p += 2
	p += 8
	binary.LittleEndian.PutUint32(b[p:p+4], 0x20) // root visual
	p += 4
	p += 2     // backing-stores / save-unders
	b[p] = 32  // root depth
	b[p+1] = 1 // allowed depths
	p += 2
	// depth 32 + 1 visual
	b[p] = 32
	p += 2
	binary.LittleEndian.PutUint16(b[p:p+2], 1)
	p += 6
	binary.LittleEndian.PutUint32(b[p:p+4], 0x21) // visual id
	b[p+4] = 4                                    // TrueColor
	b[p+5] = 8
	binary.LittleEndian.PutUint32(b[p+8:p+12], 0xff0000)
	binary.LittleEndian.PutUint32(b[p+12:p+16], 0x00ff00)
	binary.LittleEndian.PutUint32(b[p+16:p+20], 0x0000ff)
	return b
}
