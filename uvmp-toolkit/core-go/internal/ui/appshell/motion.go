package appshell

import (
	"image/color"
	"math"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

const (
	pageMotionDuration    = 200 * time.Millisecond
	controlMotionDuration = 120 * time.Millisecond
)

// newMotionButton 保持 *widget.Button 类型与全部既有用法，仅补充短促的按压反馈。
func newMotionButton(label string, tapped func()) *widget.Button {
	return withMotionTap(widget.NewButton(label, nil), tapped)
}

func newMotionButtonWithIcon(label string, icon fyne.Resource, tapped func()) *widget.Button {
	return withMotionTap(widget.NewButtonWithIcon(label, icon, nil), tapped)
}

func withMotionTap(button *widget.Button, tapped func()) *widget.Button {
	button.OnTapped = func() {
		animateControlTap(button)
		if tapped != nil {
			tapped()
		}
	}
	return button
}

type controlTapMotion struct {
	origin fyne.Position
	anim   *fyne.Animation
}

var controlTapMotions = struct {
	sync.Mutex
	items map[fyne.CanvasObject]*controlTapMotion
}{items: map[fyne.CanvasObject]*controlTapMotion{}}

// animateControlTap 只移动 1px，不改尺寸、不触发布局重算，也不延迟真实点击回调。
func animateControlTap(obj fyne.CanvasObject) {
	if obj == nil || fyne.CurrentApp() == nil || !fyne.CurrentApp().Settings().ShowAnimations() {
		return
	}
	runUI(func() {
		controlTapMotions.Lock()
		if previous := controlTapMotions.items[obj]; previous != nil {
			previous.anim.Stop()
			obj.Move(previous.origin)
		}
		origin := obj.Position()
		obj.Move(origin.AddXY(0, 1))
		anim := fyne.NewAnimation(controlMotionDuration, func(progress float32) {
			obj.Move(origin.AddXY(0, 1-progress))
		})
		anim.Curve = enterpriseMotionCurve
		controlTapMotions.items[obj] = &controlTapMotion{origin: origin, anim: anim}
		controlTapMotions.Unlock()
		anim.Start()
		time.AfterFunc(controlMotionDuration+30*time.Millisecond, func() {
			runUI(func() {
				controlTapMotions.Lock()
				if current := controlTapMotions.items[obj]; current != nil && current.anim == anim {
					obj.Move(origin)
					delete(controlTapMotions.items, obj)
				}
				controlTapMotions.Unlock()
			})
		})
	})
}

// enterpriseMotionCurve 与定稿 cubic-bezier(.22,1,.36,1) 一致。
// 用牛顿迭代求给定时间的 x，再计算 y；每帧只做少量浮点运算。
func enterpriseMotionCurve(progress float32) float32 {
	if progress <= 0 {
		return 0
	}
	if progress >= 1 {
		return 1
	}
	t := float64(progress)
	x := t
	for range 5 {
		u := 1 - x
		estimate := 3*u*u*x*.22 + 3*u*x*x*.36 + x*x*x
		delta := estimate - t
		if math.Abs(delta) < .0001 {
			break
		}
		derivative := 3*u*u*.22 + 6*u*x*.36 + 3*x*x
		if derivative < .000001 {
			break
		}
		x -= delta / derivative
		x = math.Max(0, math.Min(1, x))
	}
	return float32(1 - math.Pow(1-x, 3))
}

// pageStage 页面切换只改变内容位置和前景透明度；逐帧不调用 Refresh/Layout。
type pageStage struct {
	widget.BaseWidget
	content   fyne.CanvasObject
	veil      *canvas.Rectangle
	offset    float32
	animation *fyne.Animation
}

func newPageStage(content fyne.CanvasObject) *pageStage {
	p := &pageStage{content: content, veil: canvas.NewRectangle(PageBG)}
	p.ExtendBaseWidget(p)
	p.veil.Hide()
	return p
}

func (p *pageStage) Play() {
	runUI(func() {
		if p.animation != nil {
			p.animation.Stop()
			p.animation = nil
		}
		if !fyne.CurrentApp().Settings().ShowAnimations() {
			p.offset = 0
			p.veil.Hide()
			p.content.Move(fyne.NewPos(0, 0))
			return
		}
		p.offset = 14
		p.veil.FillColor = PageBG
		p.veil.Show()
		p.content.Move(fyne.NewPos(0, p.offset))
		p.animation = fyne.NewAnimation(pageMotionDuration, func(progress float32) {
			p.offset = 14 * (1 - progress)
			p.content.Move(fyne.NewPos(0, p.offset))
			base := color.NRGBAModel.Convert(PageBG).(color.NRGBA)
			base.A = uint8(float32(base.A) * (1 - progress))
			p.veil.FillColor = base
			p.veil.Refresh()
		})
		p.animation.Curve = enterpriseMotionCurve
		played := p.animation
		played.Start()
		time.AfterFunc(pageMotionDuration+30*time.Millisecond, func() {
			runUI(func() {
				if p.animation != played {
					return // 快速连续切页时，只收尾最后一次动画
				}
				p.offset = 0
				p.content.Move(fyne.NewPos(0, 0))
				p.veil.Hide()
				p.animation = nil
			})
		})
	})
}

func (p *pageStage) CreateRenderer() fyne.WidgetRenderer {
	return &pageStageRenderer{stage: p}
}

type pageStageRenderer struct{ stage *pageStage }

func (r *pageStageRenderer) Layout(size fyne.Size) {
	r.stage.content.Move(fyne.NewPos(0, r.stage.offset))
	r.stage.content.Resize(size)
	r.stage.veil.Resize(size)
}
func (r *pageStageRenderer) MinSize() fyne.Size { return r.stage.content.MinSize() }
func (r *pageStageRenderer) Refresh() {
	r.stage.content.Refresh()
	r.stage.veil.Refresh()
}
func (r *pageStageRenderer) Destroy() {}
func (r *pageStageRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.stage.content, r.stage.veil}
}
