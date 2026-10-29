package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// 首页任务图形：用 canvas 基础图元画「一眼能懂」的任务示意（派车单表格 + 轨迹路线、
// 题目卡 + 扫描框 + 放大镜、显示器 + 播放进度），不用图标充当插画，也不做渐变/阴影。

// artPart 图形部件（设计坐标，面板固定尺寸时按原样摆放）。
type artPart struct {
	obj     fyne.CanvasObject
	x, y, w float32
	h       float32
}

// artPanel 浅蓝实底圆角面板 + 图形部件。
// 线条（canvas.Line）直接以面板绝对坐标绘制，不参与 Move/Resize——
// Fyne 的 Line.Resize 会按包围盒重写端点，统一布局会把斜线拉成贯穿面板的大叉。
func artPanel(design fyne.Size, parts []artPart) fyne.CanvasObject {
	bg := canvas.NewRectangle(LightBlueBG)
	bg.CornerRadius = 12
	objs := make([]fyne.CanvasObject, len(parts))
	for i, p := range parts {
		objs[i] = p.obj
		if _, isLine := p.obj.(*canvas.Line); isLine {
			continue
		}
		p.obj.Move(fyne.NewPos(p.x, p.y))
		p.obj.Resize(fyne.NewSize(p.w, p.h))
	}
	stack := container.NewStack(bg, container.NewWithoutLayout(objs...))
	return container.NewGridWrap(design, stack)
}

// artRect 实心圆角矩形。
func artRect(fill color.Color, radius float32) *canvas.Rectangle {
	r := canvas.NewRectangle(fill)
	r.CornerRadius = radius
	return r
}

// artOutlinedRect 带描边的圆角矩形（显示器外框、卡片轮廓）。
func artOutlinedRect(fill, stroke color.Color, radius, strokeW float32) *canvas.Rectangle {
	r := canvas.NewRectangle(fill)
	r.CornerRadius = radius
	r.StrokeColor = stroke
	r.StrokeWidth = strokeW
	return r
}

// circlePart 圆点部件（语义圆点、放大镜、路线端点）。
func circlePart(cx, cy, r float32, fill, stroke color.Color, strokeW float32) artPart {
	c := canvas.NewCircle(fill)
	c.StrokeColor = stroke
	c.StrokeWidth = strokeW
	return artPart{c, cx - r, cy - r, r * 2, r * 2}
}

// artLine 折线/线段（坐标相对图形面板原点）。
func artLine(x1, y1, x2, y2, width float32, c color.Color) *canvas.Line {
	l := canvas.NewLine(c)
	l.StrokeWidth = width
	l.Position1 = fyne.NewPos(x1, y1)
	l.Position2 = fyne.NewPos(x2, y2)
	return l
}

// artText 小号图注文字。
func artText(text string, size float32, c color.Color, bold bool) *canvas.Text {
	t := canvas.NewText(text, c)
	t.TextSize = size
	if bold {
		t.TextStyle = fyne.TextStyle{Bold: true}
	}
	return t
}

// pcdArt 轨迹派车单导出：白色派车单纸张（表头 + 表格线 + PDF 角标）+ 蓝色行驶轨迹。
func pcdArt() fyne.CanvasObject {
	design := fyne.NewSize(232, 150)

	// 轨迹在纸张后面进出：起点 → 两段折线 → 终点。
	route := BrandBlue
	parts := []artPart{
		{artLine(14, 126, 64, 96, 2.2, route), 0, 0, 232, 150},
		{artLine(64, 96, 124, 120, 2.2, route), 0, 0, 232, 150},
		{artLine(124, 120, 206, 40, 2.2, route), 0, 0, 232, 150},
		circlePart(14, 126, 5, CardBG, route, 2), // 起点
		circlePart(206, 40, 5, CardBG, route, 2), // 终点
		{artRect(CardBG, 8), 56, 18, 136, 112},   // 纸张
		{artRect(BrandBlue, 6), 56, 18, 136, 24}, // 表头
		{artText("派车单", 10, CardBG, true), 68, 23, 56, 14},
		// 表格线
		{artLine(70, 56, 178, 56, 1, Border), 0, 0, 232, 150},
		{artLine(70, 72, 178, 72, 1, Border), 0, 0, 232, 150},
		{artLine(70, 88, 178, 88, 1, Border), 0, 0, 232, 150},
		{artLine(70, 104, 178, 104, 1, Border), 0, 0, 232, 150},
		{artLine(70, 118, 178, 118, 1, Border), 0, 0, 232, 150},
		{artLine(112, 52, 112, 116, 1, Border), 0, 0, 232, 150},
		{artRect(BlockBG, 2), 78, 63, 26, 5},
		{artRect(BlockBG, 2), 78, 79, 26, 5},
		{artRect(BlockBG, 2), 78, 95, 26, 5},
		{artRect(LightBlueBG, 2), 120, 63, 46, 5},
		{artRect(BlockBG, 2), 120, 79, 38, 5},
		{artRect(BlockBG, 2), 120, 95, 42, 5},
		// PDF 角标
		{artRect(BrandBlue, 6), 160, 100, 44, 22},
		{artText("PDF", 9, CardBG, true), 173, 106, 30, 12},
	}
	return artPanel(design, parts)
}

// quizArt 题库搜题：题目卡 + 选项条 + 扫描框四角 + 放大镜。
func quizArt() fyne.CanvasObject {
	design := fyne.NewSize(104, 78)
	parts := []artPart{
		{artRect(CardBG, 6), 13, 10, 55, 49},     // 题目卡
		{artRect(BlockBG, 2), 21, 19, 39, 6},     // 题干
		{artRect(LightBlueBG, 2), 21, 30, 39, 7}, // 选中项
		{artRect(BlockBG, 2), 21, 41, 31, 6},     // 其他项
		{artLine(21, 30, 60, 30, 1.2, BrandBlue), 0, 0, 104, 78},
		// 扫描框四角
		{artLine(7, 17, 7, 5, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(7, 5, 19, 5, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(71, 5, 83, 5, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(83, 5, 83, 17, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(7, 61, 7, 73, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(7, 73, 19, 73, 2, BrandBlue), 0, 0, 104, 78},
		circlePart(76, 54, 12, LightBlueBG, BrandBlue, 2.2), // 放大镜
		{artLine(84, 62, 94, 72, 2.6, BrandBlue), 0, 0, 104, 78},
	}
	return artPanel(design, parts)
}

// courseArt 学堂刷课：显示器 + 播放键 + 课程进度条。
func courseArt() fyne.CanvasObject {
	design := fyne.NewSize(104, 78)
	parts := []artPart{
		{artOutlinedRect(CardBG, TextTertiary, 6, 1.2), 13, 9, 68, 44}, // 显示器
		circlePart(46, 31, 11, LightBlueBG, color.Transparent, 0),      // 播放键底
		{artLine(43, 26, 43, 36, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(43, 26, 51, 31, 2, BrandBlue), 0, 0, 104, 78},
		{artLine(51, 31, 43, 36, 2, BrandBlue), 0, 0, 104, 78},
		{artRect(BlockBG, 2), 23, 43, 48, 4},   // 进度轨
		{artRect(BrandBlue, 2), 23, 43, 29, 4}, // 已学进度
		{artLine(47, 54, 47, 60, 1.6, TextTertiary), 0, 0, 104, 78},
		{artLine(37, 61, 57, 61, 1.6, TextTertiary), 0, 0, 104, 78},
	}
	return artPanel(design, parts)
}
