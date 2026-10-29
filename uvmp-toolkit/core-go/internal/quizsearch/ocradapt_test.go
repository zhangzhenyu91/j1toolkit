package quizsearch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
)

// fakeDet 假检测会话：gate 非 nil 时阻塞至关闭（构造「引擎在途」确定性场景）。
type fakeDet struct {
	gate chan struct{}
}

func (d *fakeDet) RunDet(_ []float32, dw, dh int) ([]float32, error) {
	if d.gate != nil {
		<-d.gate
	}
	return make([]float32, dw*dh), nil
}

func (d *fakeDet) Close() error { return nil }

type fakeRec struct{}

func (r *fakeRec) RunRec(_ []float32, _, _ int) ([]float32, int, int, error) {
	return make([]float32, 2*10), 2, 10, nil
}

func (r *fakeRec) Close() error { return nil }

type fakeFactory struct {
	det *fakeDet
}

func (f *fakeFactory) OpenDet(string) (ocr.DetSession, error) { return f.det, nil }
func (f *fakeFactory) OpenRec(string) (ocr.RecSession, error) { return &fakeRec{}, nil }

// fakeModelsDir 临时模型目录（仅词表必需；det/rec 由 fake 会话提供）。
func fakeModelsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ppocr_keys.txt"), []byte("你\n好\n测\n试\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func waitResults(t *testing.T, ch <-chan OCRResult, n int) []OCRResult {
	t.Helper()
	out := []OCRResult{}
	deadline := time.After(3 * time.Second)
	for len(out) < n {
		select {
		case r, ok := <-ch:
			if !ok {
				t.Fatalf("结果流提前关闭，仅收到 %d/%d", len(out), n)
			}
			out = append(out, r)
		case <-deadline:
			t.Fatalf("等待 %d 条结果超时，仅收到 %d", n, len(out))
		}
	}
	return out
}

func TestOCREngineAdapterSubmitResultIDMapping(t *testing.T) {
	f := &fakeFactory{det: &fakeDet{}}
	eng := NewOCREngineFactory(f)()
	if err := eng.Init(fakeModelsDir(t)); err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	pix := make([]byte, 8*8*4)
	eng.Submit(EncodeTaskID(3, 0, BoxA), pix, 8, 8) // 偶数=A，epoch=3
	got := waitResults(t, eng.Results(), 1)[0]
	if !got.OK {
		t.Fatalf("want OK, got %+v", got)
	}
	epoch, box := DecodeTaskID(got.ID)
	if epoch != 3 || box != BoxA {
		t.Fatalf("任务 id 未还原：epoch=%d box=%s", epoch, box)
	}
}

func TestOCREngineAdapterRejectEmitsFailure(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeFactory{det: &fakeDet{gate: gate}}
	eng := NewOCREngineFactory(f)()
	if err := eng.Init(fakeModelsDir(t)); err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	pix := make([]byte, 8*8*4)
	first := EncodeTaskID(1, 0, BoxA)
	second := EncodeTaskID(1, 1, BoxA) // 同框在途限一：第二帧必被拒
	eng.Submit(first, pix, 8, 8)
	eng.Submit(second, pix, 8, 8)
	close(gate)

	byID := map[int64]OCRResult{}
	for _, r := range waitResults(t, eng.Results(), 2) {
		byID[r.ID] = r
	}
	if r, ok := byID[first]; !ok || !r.OK {
		t.Fatalf("首帧应成功，got %+v ok=%v", r, ok)
	}
	r, ok := byID[second]
	if !ok || r.OK || r.Error == "" {
		t.Fatalf("被拒帧应补发失败结果，got %+v ok=%v", r, ok)
	}
}

func TestOCREngineAdapterCloseWithoutInit(t *testing.T) {
	eng := NewOCREngineFactory(&fakeFactory{det: &fakeDet{}})()
	eng.Submit(2, make([]byte, 4), 1, 1) // 未就绪：补发失败，不阻塞
	eng.Close()
	select {
	case r, ok := <-eng.Results():
		if ok && r.OK {
			t.Fatalf("未就绪提交不应成功: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 后结果流未收口")
	}
	// 结果流应已关闭
	for range eng.Results() {
	}
	eng.Close() // 幂等
}

func TestOCREngineAdapterInitMissingVocab(t *testing.T) {
	eng := NewOCREngineFactory(&fakeFactory{det: &fakeDet{}})()
	defer eng.Close()
	if err := eng.Init(t.TempDir()); err == nil {
		t.Fatal("缺少 ppocr_keys.txt 应报错")
	}
}

func TestOCREngineAdapterSelftest(t *testing.T) {
	f := &fakeFactory{det: &fakeDet{}}
	eng := NewOCREngineFactory(f)()
	if err := eng.Init(fakeModelsDir(t)); err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ts, ok := eng.(EngineSelftester)
	if !ok {
		t.Fatal("适配器应实现 EngineSelftester")
	}
	out := ts.Selftest()
	if out["ok"] != true || out["ready"] != true {
		t.Fatalf("selftest = %+v", out)
	}
}
