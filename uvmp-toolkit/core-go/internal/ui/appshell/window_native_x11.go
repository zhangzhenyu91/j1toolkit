//go:build linux && x11

package appshell

/*
#cgo LDFLAGS: -lX11 -lXext -lm
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/extensions/shape.h>
#include <stdlib.h>
#include <math.h>

static int uvmp_send_client(Display *display, Window window, const char *message,
                            long first, long second, long third, long fourth) {
	XEvent event = {0};
	event.xclient.type = ClientMessage;
	event.xclient.display = display;
	event.xclient.window = window;
	event.xclient.message_type = XInternAtom(display, message, False);
	event.xclient.format = 32;
	event.xclient.data.l[0] = first;
	event.xclient.data.l[1] = second;
	event.xclient.data.l[2] = third;
	event.xclient.data.l[3] = fourth;
	event.xclient.data.l[4] = 1;
	return XSendEvent(display, DefaultRootWindow(display), False,
	                  SubstructureRedirectMask | SubstructureNotifyMask, &event);
}

static int uvmp_x11_minimize(unsigned long window) {
	Display *display = XOpenDisplay(NULL);
	if (!display) return 0;
	int result = XIconifyWindow(display, (Window)window, DefaultScreen(display));
	XFlush(display);
	XCloseDisplay(display);
	return result;
}

static int uvmp_x11_maximize(unsigned long window) {
	Display *display = XOpenDisplay(NULL);
	if (!display) return 0;
	Atom vertical = XInternAtom(display, "_NET_WM_STATE_MAXIMIZED_VERT", False);
	Atom horizontal = XInternAtom(display, "_NET_WM_STATE_MAXIMIZED_HORZ", False);
	int result = uvmp_send_client(display, (Window)window, "_NET_WM_STATE",
	                              2, (long)vertical, (long)horizontal, 0);
	XFlush(display);
	XCloseDisplay(display);
	return result;
}

static int uvmp_x11_maximized(unsigned long window) {
	Display *display = XOpenDisplay(NULL);
	if (!display) return 0;
	Atom state = XInternAtom(display, "_NET_WM_STATE", False);
	Atom vertical = XInternAtom(display, "_NET_WM_STATE_MAXIMIZED_VERT", False);
	Atom horizontal = XInternAtom(display, "_NET_WM_STATE_MAXIMIZED_HORZ", False);
	Atom actualType;
	int actualFormat;
	unsigned long count, remaining;
	unsigned char *raw = NULL;
	int foundVertical = 0, foundHorizontal = 0;
	if (XGetWindowProperty(display, (Window)window, state, 0, 64, False, XA_ATOM,
	                       &actualType, &actualFormat, &count, &remaining, &raw) == Success && raw) {
		Atom *atoms = (Atom *)raw;
		for (unsigned long i = 0; i < count; i++) {
			if (atoms[i] == vertical) foundVertical = 1;
			if (atoms[i] == horizontal) foundHorizontal = 1;
		}
	}
	if (raw) XFree(raw);
	XCloseDisplay(display);
	return foundVertical && foundHorizontal;
}

static int uvmp_x11_move_resize(unsigned long window, int direction) {
	Display *display = XOpenDisplay(NULL);
	if (!display) return 0;
	Window root, child;
	int rootX, rootY, childX, childY;
	unsigned int mask;
	if (!XQueryPointer(display, DefaultRootWindow(display), &root, &child,
	                   &rootX, &rootY, &childX, &childY, &mask)) {
		XCloseDisplay(display);
		return 0;
	}
	int result = uvmp_send_client(display, (Window)window, "_NET_WM_MOVERESIZE",
	                              rootX, rootY, direction, 1);
	XFlush(display);
	XCloseDisplay(display);
	return result;
}

static void uvmp_x11_round(unsigned long window, int radius) {
	Display *display = XOpenDisplay(NULL);
	if (!display) return;
	XWindowAttributes attributes;
	if (!XGetWindowAttributes(display, (Window)window, &attributes)) {
		XCloseDisplay(display);
		return;
	}
	if (radius < 1) radius = 1;
	if (radius * 2 > attributes.width) radius = attributes.width / 2;
	if (radius * 2 > attributes.height) radius = attributes.height / 2;
	const int steps = 6;
	const int count = 4 * (steps + 1);
	XPoint points[28];
	int index = 0;
	for (int corner = 0; corner < 4; corner++) {
		double cx = (corner == 0 || corner == 3) ? radius : attributes.width - radius;
		double cy = corner < 2 ? radius : attributes.height - radius;
		for (int i = 0; i <= steps; i++) {
			double angle = (180 + corner * 90 + i * 90.0 / steps) * 3.14159265358979323846 / 180.0;
			points[index].x = (short)(cx + radius * cos(angle));
			points[index].y = (short)(cy + radius * sin(angle));
			index++;
		}
	}
	Region region = XPolygonRegion(points, count, EvenOddRule);
	if (region) {
		XShapeCombineRegion(display, (Window)window, ShapeBounding, 0, 0, region, ShapeSet);
		XDestroyRegion(region);
	}
	XFlush(display);
	XCloseDisplay(display);
}
*/
import "C"

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// x11WindowOps 使用 ICCCM/EWMH，不调用 xdotool/wmctrl，麒麟与统信无需另装命令行工具。
type x11WindowOps struct{ win fyne.Window }

func newNativeWindowOps(win fyne.Window) nativeWindowOps {
	return &x11WindowOps{win: win}
}

func (w *x11WindowOps) withWindow(fn func(C.ulong)) {
	native, ok := w.win.(driver.NativeWindow)
	if !ok {
		return
	}
	native.RunNative(func(raw any) {
		ctx, ok := raw.(driver.X11WindowContext)
		if ok && ctx.WindowHandle != 0 {
			fn(C.ulong(ctx.WindowHandle))
		}
	})
}

func (w *x11WindowOps) Prepare() {}

func (w *x11WindowOps) Minimize() {
	w.withWindow(func(window C.ulong) { C.uvmp_x11_minimize(window) })
}

func (w *x11WindowOps) ToggleMaximize() {
	w.withWindow(func(window C.ulong) { C.uvmp_x11_maximize(window) })
}

func (w *x11WindowOps) BeginMove() {
	w.withWindow(func(window C.ulong) { C.uvmp_x11_move_resize(window, 8) })
}

func (w *x11WindowOps) BeginResize(direction int) {
	w.withWindow(func(window C.ulong) { C.uvmp_x11_move_resize(window, C.int(direction)) })
}

func (w *x11WindowOps) EndResize() {}

func (w *x11WindowOps) Maximized() bool {
	maximized := false
	w.withWindow(func(window C.ulong) { maximized = C.uvmp_x11_maximized(window) != 0 })
	return maximized
}

// Linux 无边框窗口由这些透明边缘热区把缩放交还窗口管理器，避免自算 DPI/多屏坐标。
func (w *x11WindowOps) SupportsEdgeResize() bool { return true }

// updateNativeFrame 通过 X Shape 扩展裁切圆角，麒麟/统信的 X11 会话无需额外命令。
func updateNativeFrame(win fyne.Window) {
	scale := float32(1)
	if canvas := win.Canvas(); canvas != nil && canvas.Scale() > 0 {
		scale = canvas.Scale()
	}
	ops := &x11WindowOps{win: win}
	ops.withWindow(func(window C.ulong) { C.uvmp_x11_round(window, C.int(12*scale)) })
}
