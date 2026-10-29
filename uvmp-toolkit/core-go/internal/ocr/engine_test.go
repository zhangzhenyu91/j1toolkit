package ocr

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// TestFrameIDGolden JS 金标对拍：帧 ID 编码/解码（index.cjs _queueOcr/_onWorkerMessage 公式）。
func TestFrameIDGolden(t *testing.T) {
	for _, c := range jsGoldenFrameID {
		box := BoxA
		if c.Box == "B" {
			box = BoxB
		}
		id := EncodeFrameID(int64(c.Epoch), c.Seq, box)
		if id != c.ID {
			t.Fatalf("encode(%d,%d,%s) = %d，want %d", c.Epoch, c.Seq, c.Box, id, c.ID)
		}
		epoch, gotBox := DecodeFrameID(id)
		if epoch != c.DecEpoch || gotBox.String() != c.DecBox {
			t.Fatalf("decode(%d) = (%d,%s)，want (%d,%s)", id, epoch, gotBox, c.DecEpoch, c.DecBox)
		}
	}
}

// writeTestModels 在临时目录写最小模型目录（仅字典，fake 会话不读模型）。
func writeTestModels(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/ppocr_keys.txt", []byte("A\nB\nC\nD\n"), 0o644); err != nil {
		t.Fatalf("写字典：%v", err)
	}
	return dir
}

// TestEngineSubmitAndResults 常驻 worker 流程：提交 → 结果（inFlight 限流同 JS）。
func TestEngineSubmitAndResults(t *testing.T) {
	dir := writeTestModels(t)
	e, err := NewEngine(EngineConfig{Factory: &fakeFactory{}, ModelsDir: dir})
	if err != nil {
		t.Fatalf("NewEngine：%v", err)
	}
	defer e.Close()
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start：%v", err)
	}
	if !e.Ready() {
		t.Fatal("Ready 应为 true")
	}
	bgra := syntheticBGRA()
	id, ok := e.Submit(BoxA, bgra, 400, 200)
	if !ok {
		t.Fatal("Submit 应受理")
	}
	if epoch, box := DecodeFrameID(id); epoch != 0 || box != BoxA {
		t.Fatalf("帧 ID 解码 = (%d,%s)，want (0,A)", epoch, box)
	}
	// 框 B 不同框，可受理（JS inFlight 按框限流）。
	if _, ok := e.Submit(BoxB, bgra, 400, 200); !ok {
		t.Fatal("框 B 应受理")
	}
	// 同框在途限流（JS inFlight）：A 再提交应被拒。
	if _, ok := e.Submit(BoxA, bgra, 400, 200); ok {
		t.Fatal("同框在途帧应被拒（inFlight 限流）")
	}
	deadline := time.After(5 * time.Second)
	seen := 0
	for seen < 2 {
		select {
		case res := <-e.Results():
			if !res.OK || res.Err != nil {
				t.Fatalf("结果异常：%v", res.Err)
			}
			if res.Result.Text != "AB\nAB" {
				t.Fatalf("识别文本 = %q", res.Result.Text)
			}
			seen++
		case <-deadline:
			t.Fatal("等待结果超时")
		}
	}
	// 结果返回后同框可再提交。
	if _, ok := e.Submit(BoxA, bgra, 400, 200); !ok {
		t.Fatal("结果返回后应可再提交")
	}
}

// TestEngineEpochStaleDrop NextEpoch 作废旧帧结果（同 JS ocrEpoch++）。
// fakeDet 加闸门保证两帧入队后才放行，作废时序确定。
func TestEngineEpochStaleDrop(t *testing.T) {
	dir := writeTestModels(t)
	gate := make(chan struct{})
	f := &fakeFactory{det: &fakeDet{gate: gate}}
	e, err := NewEngine(EngineConfig{Factory: f, ModelsDir: dir, QueueSize: 8})
	if err != nil {
		t.Fatalf("NewEngine：%v", err)
	}
	defer e.Close()
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start：%v", err)
	}
	bgra := syntheticBGRA()
	// 两帧入队（worker 阻塞在闸门上），推进 epoch 作废之。
	if _, ok := e.Submit(BoxA, bgra, 400, 200); !ok {
		t.Fatal("Submit#1 应受理")
	}
	if _, ok := e.Submit(BoxB, bgra, 400, 200); !ok {
		t.Fatal("Submit#2 应受理")
	}
	if got := e.NextEpoch(); got != 1 {
		t.Fatalf("NextEpoch = %d，want 1", got)
	}
	// 旧 epoch 结果不得出现；新帧结果应正常。
	id, ok := e.Submit(BoxA, bgra, 400, 200)
	if !ok {
		t.Fatal("NextEpoch 后 inFlight 应清空、可再提交")
	}
	close(gate) // 放行旧帧（应被丢弃）与新帧
	deadline := time.After(5 * time.Second)
	for {
		select {
		case res := <-e.Results():
			if res.ID == id {
				if res.Epoch != 1 {
					t.Fatalf("新帧 epoch = %d，want 1", res.Epoch)
				}
				return
			}
			t.Fatalf("旧 epoch 结果未被丢弃：%+v", res)
		case <-deadline:
			t.Fatal("等待新帧结果超时（旧帧可能未被正确作废）")
		}
	}
}

// TestEngineRecognizePNG 同步 golden PNG 识别（复用常驻会话、不占扫描队列语义）。
func TestEngineRecognizePNG(t *testing.T) {
	dir := writeTestModels(t)
	e, err := NewEngine(EngineConfig{Factory: &fakeFactory{}, ModelsDir: dir})
	if err != nil {
		t.Fatalf("NewEngine：%v", err)
	}
	defer e.Close()
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start：%v", err)
	}
	pngPath := dir + "/golden.png"
	writeSyntheticPNG(t, pngPath)
	res, err := e.RecognizePNG(context.Background(), pngPath)
	if err != nil {
		t.Fatalf("RecognizePNG：%v", err)
	}
	if res.Text != jsGoldenFull.Text {
		t.Fatalf("golden PNG text = %q，want %q", res.Text, jsGoldenFull.Text)
	}
	if _, err := e.RecognizePNG(context.Background(), dir+"/missing.png"); err == nil {
		t.Fatal("缺失 PNG 应报错")
	}
}

// TestEngineClose 关停语义：Close 幂等、之后 Submit/Recognize 拒绝、Results 关闭。
func TestEngineClose(t *testing.T) {
	dir := writeTestModels(t)
	e, err := NewEngine(EngineConfig{Factory: &fakeFactory{}, ModelsDir: dir})
	if err != nil {
		t.Fatalf("NewEngine：%v", err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start：%v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close：%v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close 幂等：%v", err)
	}
	if _, ok := e.Submit(BoxA, syntheticBGRA(), 400, 200); ok {
		t.Fatal("Close 后 Submit 应被拒")
	}
	if _, err := e.Recognize(context.Background(), syntheticBGRA(), 400, 200); err == nil {
		t.Fatal("Close 后 Recognize 应报错")
	}
	// Results 通道随 worker 退出关闭。
	for range e.Results() {
	}
}

// TestEngineStartErrors 模型加载失败路径（同 JS worker init → error）。
func TestEngineStartErrors(t *testing.T) {
	if _, err := NewEngine(EngineConfig{ModelsDir: "x"}); err == nil {
		t.Fatal("缺 Factory 应报错")
	}
	if _, err := NewEngine(EngineConfig{Factory: &fakeFactory{}}); err == nil {
		t.Fatal("缺 ModelsDir 应报错")
	}
	e, err := NewEngine(EngineConfig{Factory: &fakeFactory{openErr: errFakeSession}, ModelsDir: writeTestModels(t)})
	if err != nil {
		t.Fatalf("NewEngine：%v", err)
	}
	defer e.Close()
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("会话加载失败应报错")
	}
	// Start 幂等：同一错误重复返回。
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("Start 幂等应返回同一错误")
	}
}

// TestEngineSubmitCloseRace Submit/Recognize 与 Close 并发：不得 send on closed channel
// panic（Close 不关 jobs，靠 quit 收口 worker；回归防护）。
func TestEngineSubmitCloseRace(t *testing.T) {
	dir := writeTestModels(t)
	for i := 0; i < 30; i++ {
		e, err := NewEngine(EngineConfig{Factory: &fakeFactory{}, ModelsDir: dir})
		if err != nil {
			t.Fatalf("NewEngine：%v", err)
		}
		if err := e.Start(context.Background()); err != nil {
			t.Fatalf("Start：%v", err)
		}
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				e.Submit(BoxA, syntheticBGRA(), 400, 200)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				e.Submit(BoxB, syntheticBGRA(), 400, 200)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				e.Recognize(context.Background(), syntheticBGRA(), 400, 200)
			}
		}()
		time.Sleep(time.Millisecond) // 提交在途时并发关停
		if err := e.Close(); err != nil {
			t.Fatalf("Close：%v", err)
		}
		wg.Wait()
	}
}
