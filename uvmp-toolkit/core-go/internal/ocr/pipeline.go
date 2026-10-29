package ocr

import (
	"errors"
	"time"
)

// Pipeline PP-OCRv4 推理管线（det → 行裁剪 → rec），对一帧 BGRA 位图做 OCR。
// 对应 JS OcrPipeline：会话与词表常驻，Run 逐帧执行。
type Pipeline struct {
	det   DetSession
	rec   RecSession
	chars []string
}

// NewPipeline 组装管线（det/rec 会话非 nil、词表非空）。
func NewPipeline(det DetSession, rec RecSession, chars []string) (*Pipeline, error) {
	if det == nil || rec == nil {
		return nil, errors.New("ocr: det/rec 会话不可为 nil")
	}
	if len(chars) == 0 {
		return nil, errors.New("ocr: 词表不可为空")
	}
	return &Pipeline{det: det, rec: rec, chars: chars}, nil
}

// Chars 当前 CTC 词表。
func (p *Pipeline) Chars() []string { return p.chars }

// Run 对一帧密集 BGRA 位图做 OCR（det 前处理/推理/后处理 → 行裁剪 → rec → CTC）。
// 返回 text（各行 \n 连接）、lines（按 (y,x) 升序）与耗时；会话错误向上返回。
func (p *Pipeline) Run(bgra []byte, width, height int) (Result, error) {
	t0 := time.Now()
	var res Result
	if width <= 0 || height <= 0 || len(bgra) < width*height*4 {
		return res, errors.New("ocr: 位图尺寸非法或缓冲不足")
	}

	// ---- det 前处理：等比缩放（短边上采样、长边封顶、32 对齐）
	dw, dh, usedRatio := DetPreprocessSize(width, height)
	planes := BGRAToRGBPlanes(bgra, width, height)
	resized := ResizePlanes(planes, width, height, dw, dh)
	nchw := PlanesToNCHW(resized, dw, dh)

	// ---- det 推理
	prob, err := p.det.RunDet(nchw, dw, dh)
	if err != nil {
		return res, err
	}
	if len(prob) < dw*dh {
		return res, errors.New("ocr: det 输出尺寸不足")
	}

	// ---- det 后处理：阈值 → 膨胀 → 连通域 → unclip → 框得分 → 映射回原图
	boxes := DetPostprocess(prob, dw, dh, usedRatio, width, height)

	// ---- rec：逐行裁剪（原图）→ 48 高等比 → 推理 → CTC 解码
	var lines []Line
	for _, box := range boxes {
		line, ok := p.cropLine(planes, width, height, box)
		if !ok {
			continue
		}
		recRes, err := p.recognize(line.planes, line.w, line.h)
		if err != nil {
			return res, err
		}
		if recRes.text != "" && recRes.score >= TextScore {
			lines = append(lines, Line{Text: recRes.text, Score: recRes.score, Box: box})
		}
	}
	text := ""
	for i, l := range lines {
		if i > 0 {
			text += "\n"
		}
		text += l.Text
	}
	res = Result{Text: text, Lines: lines, ElapsedMs: time.Since(t0).Milliseconds()}
	return res, nil
}

// cropLine 从原图平面裁剪并按 rec 输入高 48 等比缩放（同 JS _cropLine；
// 裁剪小于 3px 返回 ok=false）。
func (p *Pipeline) cropLine(planes Planes, width, height int, box Box) (out struct {
	planes Planes
	w, h   int
}, ok bool,
) {
	w := maxInt(1, minInt(box.W, width-box.X))
	h := maxInt(1, minInt(box.H, height-box.Y))
	if w < MinCropSide || h < MinCropSide {
		return out, false
	}
	var cropped Planes
	for c := 0; c < 3; c++ {
		dst := make([]float32, w*h)
		src := planes[c]
		for y := 0; y < h; y++ {
			srcOff := (box.Y+y)*width + box.X
			copy(dst[y*w:(y+1)*w], src[srcOff:srcOff+w])
		}
		cropped[c] = dst
	}
	nw := maxInt(MinCropTargetW, int(jsRound(float64(w)*RecH/float64(h))))
	if nw > RecMaxW {
		nw = RecMaxW
	}
	out.planes = ResizePlanes(cropped, w, h, nw, RecH)
	out.w = nw
	out.h = RecH
	return out, true
}

// recognize 单行识别：NCHW → rec 推理 → CTC 解码（同 JS _recognize）。
func (p *Pipeline) recognize(planes Planes, w, h int) (struct {
	text  string
	score float64
}, error,
) {
	var out struct {
		text  string
		score float64
	}
	nchw := PlanesToNCHW(planes, w, h)
	logits, t, c, err := p.rec.RunRec(nchw, w, h)
	if err != nil {
		return out, err
	}
	if t <= 0 || c <= 0 || len(logits) < t*c {
		return out, errors.New("ocr: rec 输出尺寸不足")
	}
	out.text, out.score = DecodeCTC(logits, t, c, p.chars)
	return out, nil
}
