package ocr

import (
	"errors"
	"math"
)

// ---- 测试用 fake 会话/工厂（合成 det 概率图与 rec logits，口径同 jsGoldenFull
// 的打桩生成器 mkDetProb/mkRecOut，纯测试不依赖 ONNX 运行时）----

// fakeDet 合成 det 会话：按输入尺寸生成确定性概率图（两个归一化高分块 + 噪点）。
// gate 非 nil 时 RunDet 先等其关闭（模拟慢推理，供 epoch 作废时序测试用）。
type fakeDet struct {
	calls int
	err   error
	gate  chan struct{}
}

func (f *fakeDet) RunDet(nchw []float32, dw, dh int) ([]float32, error) {
	f.calls++
	if f.gate != nil {
		<-f.gate
	}
	if f.err != nil {
		return nil, f.err
	}
	prob := make([]float32, dw*dh)
	for i := range prob {
		prob[i] = float32(0.05 + float64((i*37)%100)/1000)
	}
	// 同 gen.mjs mkDetProb 的两个归一化矩形。
	rects := [][4]float64{{0.1, 0.15, 0.45, 0.30}, {0.55, 0.55, 0.95, 0.72}}
	for _, r := range rects {
		x0 := int(math.Floor(r[0] * float64(dw)))
		y0 := int(math.Floor(r[1] * float64(dh)))
		x1 := int(math.Ceil(r[2] * float64(dw)))
		y1 := int(math.Ceil(r[3] * float64(dh)))
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				prob[y*dw+x] = float32(0.9 + float64((x*13+y*7)%10)/100)
			}
		}
	}
	return prob, nil
}

func (f *fakeDet) Close() error { return nil }

// fakeRec 合成 rec 会话：固定 logits（文本恒 "AB"）。
type fakeRec struct {
	calls int
	err   error
}

func (f *fakeRec) RunRec(nchw []float32, w, h int) ([]float32, int, int, error) {
	f.calls++
	if f.err != nil {
		return nil, 0, 0, f.err
	}
	const t, c = 4, 6
	data := make([]float32, t*c)
	plan := [][2]float32{{1, 0.9}, {1, 0.8}, {0, 0.7}, {2, 0.85}}
	for i := range plan {
		for j := 0; j < c; j++ {
			data[i*c+j] = 0.01
		}
		data[i*c+int(plan[i][0])] = plan[i][1]
	}
	return data, t, c, nil
}

func (f *fakeRec) Close() error { return nil }

// fakeFactory fake 会话工厂（selftest/engine 注入用）。
type fakeFactory struct {
	det       *fakeDet
	rec       *fakeRec
	openErr   error
	detErr    error
	recErr    error
	version   string
	openedDet int
	openedRec int
}

func (f *fakeFactory) OpenDet(modelsDir string) (DetSession, error) {
	f.openedDet++
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.detErr != nil {
		return &fakeErrDet{err: f.detErr}, nil
	}
	if f.det == nil {
		f.det = &fakeDet{}
	}
	return f.det, nil
}

func (f *fakeFactory) OpenRec(modelsDir string) (RecSession, error) {
	f.openedRec++
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.rec == nil {
		f.rec = &fakeRec{}
	}
	return f.rec, nil
}

func (f *fakeFactory) RuntimeVersion() string { return f.version }

// fakeErrDet 恒失败的 det 会话（错误路径用）。
type fakeErrDet struct{ err error }

func (f *fakeErrDet) RunDet(nchw []float32, dw, dh int) ([]float32, error) {
	return nil, f.err
}
func (f *fakeErrDet) Close() error { return nil }

var errFakeSession = errors.New("fake 会话故障")
