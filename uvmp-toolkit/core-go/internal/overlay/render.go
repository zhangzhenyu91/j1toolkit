// overlay：扫描框/结果窗内容绘制与命中测试（视觉对齐 desktop/scanbox.html 与 result.html/resultpanel）。
package overlay

import (
	"image/color"
)

// 视觉 token（对齐 design 设计规范「政企蓝白」与 result.html/scanbox.html 取色）。
var (
	cBrand      = color.NRGBA{R: 0x0e, G: 0x3d, B: 0xa8, A: 255} // 品牌蓝 #0E3DA8
	cBrandFill  = color.NRGBA{R: 0x0e, G: 0x3d, B: 0xa8, A: 31}  // rgba(14,61,168,0.12)
	cWhite      = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	cTextMain   = color.NRGBA{R: 0x1d, G: 0x21, B: 0x29, A: 255} // 正文 #1D2129
	cTextSec    = color.NRGBA{R: 0x4e, G: 0x59, B: 0x69, A: 255} // 次要 #4E5969
	cTextAux    = color.NRGBA{R: 0x86, G: 0x90, B: 0x9c, A: 255} // 辅助 #86909C
	cBorder     = color.NRGBA{R: 0xe5, G: 0xe6, B: 0xeb, A: 255} // 发丝线 #E5E6EB
	cWeak       = color.NRGBA{R: 0xc9, G: 0xcd, B: 0xd4, A: 255} // 弱化 #C9CDD4
	cDanger     = color.NRGBA{R: 0xf5, G: 0x3f, B: 0x3f, A: 255} // 危险红 #F53F3F
	cLightBlue  = color.NRGBA{R: 0xe8, G: 0xf0, B: 0xff, A: 255} // 浅蓝底 #E8F0FF
	cShadowSoft = color.NRGBA{R: 0, G: 0, B: 0, A: 13}           // 0 2px 8px rgba(0,0,0,.05)
)

// HitZone 窗内命中区域。
type HitZone int

const (
	// ZoneNone 无操作区（结果窗正文）。
	ZoneNone HitZone = iota
	// ZoneMove 拖动移动区。
	ZoneMove
	// ZoneResize 拖动缩放区（右下角柄）。
	ZoneResize
	// ZoneClose 隐藏按钮区（扫描框右上角 ×）。
	ZoneClose
)

// 扫描框部件尺寸（对齐 scanbox.html）。
const (
	scanTagH     = 20 // 左上标签高
	scanClose    = 22 // 右上隐藏钮 22×22
	scanGrip     = 16 // 右下缩放柄 16×16
	scanRadius   = 4
	scanBorderPx = 2
)

// 结果窗部件尺寸（对齐 result.html）。
const (
	resultMargin  = 6  // body margin 6px
	resultRadius  = 12 // 卡片圆角
	resultPadX    = 14 // 卡片左右 padding
	resultHeadH   = 34 // 标题栏高（含分隔线）
	resultGrip    = 14 // 右下缩放柄 14×14（距边 8px）
	resultGripOff = 8
)

// HitTestScanBox 扫描框命中测试（右上 × 隐藏、右下柄缩放、其余拖动）。
func HitTestScanBox(x, y, w, h int) HitZone {
	if x >= w-scanClose && y <= scanClose {
		return ZoneClose
	}
	if x >= w-scanGrip && y >= h-scanGrip {
		return ZoneResize
	}
	return ZoneMove
}

// HitTestResult 结果窗命中测试（标题栏拖动、右下柄缩放、正文无操作）。
func HitTestResult(x, y, w, h int) HitZone {
	if x >= w-resultGripOff-resultGrip && y >= h-resultGripOff-resultGrip {
		return ZoneResize
	}
	if y <= resultHeadH {
		return ZoneMove
	}
	return ZoneNone
}

// DrawScanBox 绘制扫描框：品牌蓝描边 + 透明着色 + 左上框别标签 + 右上隐藏钮 + 右下缩放柄。
func DrawScanBox(b *Buffer, label string) {
	w, h := b.W, b.H
	b.FillRoundedRect(Rect{X: 0, Y: 0, W: w, H: h}, scanRadius, cBrandFill)
	b.StrokeRect(Rect{X: 1, Y: 1, W: w - 2, H: h - 2}, scanBorderPx, cBrand)
	// 左上标签：品牌蓝底 + 白字（radius 0 4 0 4 近似为直角小片）
	b.FillRect(Rect{X: 0, Y: 0, W: 30, H: scanTagH}, cBrand)
	DrawText(b, 8, 3, label, 12, cWhite)
	// 右上隐藏钮 ×
	b.FillRect(Rect{X: w - scanClose, Y: 0, W: scanClose, H: scanClose}, cBrand)
	DrawText(b, w-scanClose+7, 2, "×", 14, cWhite)
	// 右下缩放柄：135° 对角三角（透明 50% → 品牌蓝 50%）
	for y := h - scanGrip; y < h; y++ {
		for x := w - scanGrip; x < w; x++ {
			if float64(x-(w-scanGrip))/float64(scanGrip)+float64(y-(h-scanGrip))/float64(scanGrip) >= 1 {
				blendNRGBA(b.Image().Pix[b.Image().PixOffset(x, y):][:4], cBrand)
			}
		}
	}
}

// ResultOpt 结果窗选项行。
type ResultOpt struct {
	Letter string // "A"~"F"；判断题命中行为空
	Text   string
	Hit    bool
}

// ResultSection 结果窗分区（对齐 resultpanel/main.js renderSec 三种状态）。
type ResultSection struct {
	Tag        string // 框别 "A"/"B"
	ShowTag    bool   // 单框布局不显框标
	Waiting    bool   // 等待扫描…
	Matched    bool
	Answer     string // 大字答案（单选/多选为字母串）
	JudgeLine  string // 判断题命中行（正确/错误）；与 Options 互斥
	BankName   string
	ScorePct   int
	Stem       string
	Options    []ResultOpt
	OCRSnippet string // 未匹配时「识别到：…」
}

// ResultView 结果窗视图模型。
type ResultView struct {
	Title    string
	OcrMs    int64
	Sections []ResultSection
}

// DrawResultCard 绘制结果窗：白卡（圆角 12 + 轻阴影）+ 标题栏 + 框分区 + 右下缩放柄。
func DrawResultCard(b *Buffer, v ResultView) {
	w, h := b.W, b.H
	card := Rect{X: resultMargin, Y: resultMargin, W: w - 2*resultMargin, H: h - 2*resultMargin}
	// 阴影（0 2px 8px rgba(0,0,0,.1) 近似两层软边）
	b.FillRoundedRect(Rect{X: card.X, Y: card.Y + 2, W: card.W, H: card.H}, resultRadius, cShadowSoft)
	b.FillRoundedRect(Rect{X: card.X - 1, Y: card.Y + 1, W: card.W + 2, H: card.H}, resultRadius, cShadowSoft)
	b.FillRoundedRect(card, resultRadius, cWhite)

	// 标题栏（拖动把手）：标题左、OCR 耗时右、底部发丝线
	DrawText(b, card.X+resultPadX, card.Y+10, v.Title, 13, cTextMain)
	if v.OcrMs > 0 {
		ms := "OCR " + itoa(int(v.OcrMs)) + "ms"
		DrawText(b, card.X+card.W-resultPadX-TextWidth(ms, 11), card.Y+11, ms, 11, cTextAux)
	}
	sepY := card.Y + resultHeadH - 1
	b.FillRect(Rect{X: card.X + resultPadX, Y: sepY, W: card.W - 2*resultPadX, H: 1}, cBorder)

	y := sepY + 8
	for i, sec := range v.Sections {
		if i > 0 {
			drawDashedH(b, card.X+resultPadX, y, card.W-2*resultPadX)
			y += 10
		}
		y = drawSection(b, card, y, sec)
	}

	// 右下缩放柄：135° 对角三角（透明 50% → #C9CDD4 50%）
	gx, gy := w-resultGripOff-resultGrip, h-resultGripOff-resultGrip
	for yy := gy; yy < gy+resultGrip; yy++ {
		for xx := gx; xx < gx+resultGrip; xx++ {
			if float64(xx-gx)/float64(resultGrip)+float64(yy-gy)/float64(resultGrip) >= 1 {
				blendNRGBA(b.Image().Pix[b.Image().PixOffset(xx, yy):][:4], cWeak)
			}
		}
	}
}

// drawSection 绘制单个框分区，返回下一个分区的 y。
func drawSection(b *Buffer, card Rect, y int, sec ResultSection) int {
	x := card.X + resultPadX
	right := card.X + card.W - resultPadX
	// 框标（双框布局才显示）
	if sec.ShowTag && sec.Tag != "" {
		tw := TextWidth("框"+sec.Tag, 11) + 12
		b.FillRoundedRect(Rect{X: x, Y: y, W: tw, H: 18}, 4, cLightBlue)
		DrawText(b, x+6, y+2, "框"+sec.Tag, 11, cBrand)
		x += tw + 8
	}
	switch {
	case sec.Waiting:
		DrawText(b, x, y+2, "等待扫描…", 12, cTextAux)
		return y + 24
	case !sec.Matched:
		DrawText(b, x, y+2, "未匹配到题目", 12, cTextAux)
		y += 22
		if sec.OCRSnippet != "" {
			DrawText(b, x, y, Ellipsize("识别到："+sec.OCRSnippet, 11, right-x), 11, cWeak)
			y += 18
		}
		return y
	}
	// 命中：大字答案 + 右侧「库名 · 匹配度 N%」
	ans := sec.Answer
	if sec.Answer != "" {
		DrawText(b, x, y-4, ans, 22, cDanger)
		x += TextWidth(ans, 22) + 10
	}
	meta := ""
	if sec.BankName != "" {
		meta = sec.BankName + " · "
	}
	meta += "匹配度 " + itoa(sec.ScorePct) + "%"
	DrawText(b, right-TextWidth(meta, 11), y+3, meta, 11, cTextAux)
	y += 26
	// 题干（单行省略）
	if sec.Stem != "" {
		DrawText(b, card.X+resultPadX, y, Ellipsize(sec.Stem, 12, card.W-2*resultPadX), 12, cTextSec)
		y += 20
	}
	// 选项逐行（命中项标红）；判断题显示命中行 正确/错误
	if sec.JudgeLine != "" {
		DrawText(b, card.X+resultPadX, y, sec.JudgeLine, 12, cDanger)
		y += 19
	}
	for _, o := range sec.Options {
		line := o.Text
		if o.Letter != "" {
			line = o.Letter + ". " + o.Text
		}
		col := cTextSec
		if o.Hit {
			col = cDanger
		}
		DrawText(b, card.X+resultPadX, y, Ellipsize(line, 12, card.W-2*resultPadX), 12, col)
		y += 19
	}
	return y + 2
}

// drawDashedH 水平虚线（对齐 .sec+.sec 的 border-top dashed）。
func drawDashedH(b *Buffer, x, y, w int) {
	for i := 0; i < w; i += 4 {
		d := 2
		if i+d > w {
			d = w - i
		}
		b.FillRect(Rect{X: x + i, Y: y, W: d, H: 1}, cBorder)
	}
}

// itoa 整数转字符串（免 strconv 引入抖动）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
