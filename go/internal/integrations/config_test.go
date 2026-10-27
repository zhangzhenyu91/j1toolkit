package integrations

import (
	"context"
	"errors"
	"testing"
)

// TestMapProvider 热读取：Set 即时生效、Delete 删除、缺失报 ErrNotFound。
func TestMapProvider(t *testing.T) {
	ctx := context.Background()
	p := NewMapProvider(map[string]string{"A": "1"})
	if v, err := p.Get(ctx, "A"); err != nil || v != "1" {
		t.Fatalf("Get(A) = %q, %v", v, err)
	}
	p.Set("A", "2")
	if v, _ := p.Get(ctx, "A"); v != "2" {
		t.Fatalf("热更新后 Get(A) = %q，期望 2", v)
	}
	if _, err := p.Get(ctx, "B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失键错误 = %v，期望 ErrNotFound", err)
	}
	p.Delete("A")
	if _, err := p.Get(ctx, "A"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后错误 = %v，期望 ErrNotFound", err)
	}
}

// TestEnvProvider env 取值：空值视为未配置。
func TestEnvProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("INTEG_TEST_KEY", "val")
	p := NewEnvProvider()
	if v, err := p.Get(ctx, "INTEG_TEST_KEY"); err != nil || v != "val" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	t.Setenv("INTEG_TEST_EMPTY", "")
	if _, err := p.Get(ctx, "INTEG_TEST_EMPTY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空值错误 = %v，期望 ErrNotFound", err)
	}
}

// TestChainProvider 链式回退：首个非空值生效、空值与出错跳过、全缺报 ErrNotFound。
func TestChainProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("INTEG_CHAIN_FROM_ENV", "env-value")
	first := NewMapProvider(map[string]string{"K": "first", "EMPTY": ""})
	second := NewMapProvider(map[string]string{"K": "second", "ONLY_SECOND": "second-only", "EMPTY": "fallback"})
	chain := NewChainProvider(first, second, NewEnvProvider())
	if v, _ := chain.Get(ctx, "K"); v != "first" {
		t.Fatalf("Get(K) = %q，期望 first", v)
	}
	if v, _ := chain.Get(ctx, "ONLY_SECOND"); v != "second-only" {
		t.Fatalf("Get(ONLY_SECOND) = %q", v)
	}
	// 首源空串回退次源
	if v, _ := chain.Get(ctx, "EMPTY"); v != "fallback" {
		t.Fatalf("Get(EMPTY) = %q，期望 fallback", v)
	}
	// env 兜底
	if v, _ := chain.Get(ctx, "INTEG_CHAIN_FROM_ENV"); v != "env-value" {
		t.Fatalf("env 兜底 = %q", v)
	}
	if _, err := chain.Get(ctx, "MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("全缺错误 = %v，期望 ErrNotFound", err)
	}
	// 首源出错跳过（FuncProvider 报错）
	errChain := NewChainProvider(FuncProvider(func(context.Context, string) (string, error) {
		return "", errors.New("boom")
	}), second)
	if v, _ := errChain.Get(ctx, "K"); v != "second" {
		t.Fatalf("出错回退 Get(K) = %q，期望 second", v)
	}
	// 热更新：改首源即时生效
	first.Set("K", "updated")
	if v, _ := chain.Get(ctx, "K"); v != "updated" {
		t.Fatalf("热更新后 Get(K) = %q，期望 updated", v)
	}
}

// TestLookup 便捷读取：出错/nil 一律空串。
func TestLookup(t *testing.T) {
	ctx := context.Background()
	if v := Lookup(ctx, nil, "ANY"); v != "" {
		t.Fatalf("nil Provider = %q", v)
	}
	if v := Lookup(ctx, NewMapProvider(nil), "MISSING"); v != "" {
		t.Fatalf("缺失 = %q", v)
	}
	if v := Lookup(ctx, NewMapProvider(map[string]string{"A": "1"}), "A"); v != "1" {
		t.Fatalf("Get = %q", v)
	}
}

// TestRuntimeProvider 库内优先、env 兜底的装配形态。
func TestRuntimeProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("INTEG_RT_ENV_ONLY", "from-env")
	store := NewMapProvider(map[string]string{"INTEG_RT_ENV_ONLY": "from-store", "INTEG_RT_STORE_ONLY": "s"})
	chain := NewRuntimeProvider(store)
	if v, _ := chain.Get(ctx, "INTEG_RT_ENV_ONLY"); v != "from-store" {
		t.Fatalf("库内优先 = %q", v)
	}
	if v, _ := chain.Get(ctx, "INTEG_RT_STORE_ONLY"); v != "s" {
		t.Fatalf("库内值 = %q", v)
	}
}
