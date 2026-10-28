package exporter

import (
	"context"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
)

// TestRenderSlotRestartsOnGenerationChange token Generation 改变时重启 worker
// 并完整官方登录（factory 再次创建）。
func TestRenderSlotRestartsOnGenerationChange(t *testing.T) {
	creds := &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}
	f := &fakeFactory{}
	s := newRenderSlot(0, f.factory, creds, t.Logf)

	r1, err := s.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("首次应创建 1 个 worker，实际 %d", f.count())
	}
	// 同代复用
	if _, err := s.get(context.Background()); err != nil || f.count() != 1 {
		t.Fatalf("同代应复用 worker（工厂调用 %d）", f.count())
	}
	// 代数变化 → 重启
	creds.setGen(2)
	if _, err := s.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatalf("凭证代数变化应重启 worker（工厂调用 %d）", f.count())
	}
	if !r1.(*fakeRenderer).isClosed() {
		t.Fatal("旧 worker 应关闭")
	}
	s.close()
}

// TestRenderSlotReplaceAfterError 渲染错误后替换 worker。
func TestRenderSlotReplaceAfterError(t *testing.T) {
	creds := &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}
	f := &fakeFactory{}
	s := newRenderSlot(0, f.factory, creds, t.Logf)
	r1, err := s.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.replace()
	if !r1.(*fakeRenderer).isClosed() {
		t.Fatal("出错 worker 应关闭")
	}
	if _, err := s.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatalf("替换后应新建 worker（工厂调用 %d）", f.count())
	}
	s.close()
}

// TestRenderPoolCloseAll 取消/收尾关闭全部 worker。
func TestRenderPoolCloseAll(t *testing.T) {
	creds := &fakeCreds{c: render.Credentials{Token: "T", Generation: 1}}
	f := &fakeFactory{}
	p := newRenderPool(3, f.factory, creds, t.Logf)
	for _, s := range p.slots {
		if _, err := s.get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != 3 {
		t.Fatalf("应 3 个 worker，实际 %d", f.count())
	}
	p.close()
	for _, r := range f.all() {
		if !r.isClosed() {
			t.Fatal("close 应关闭全部 worker")
		}
	}
}
