// 配置热读取：Provider 接口（Get(ctx, key)）与常用实现（内存 / env / 链式回退 / 函数适配）。
// 每次调用取当前生效值、不做缓存，运行期改配置即时生效；*config.Store（库内运行时配置）
// 等价满足本接口，可作为链式回退的首源。
package integrations

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
)

// ErrNotFound 配置键不存在（链式回退走到尽头时返回）。
var ErrNotFound = errors.New("integrations: 配置键不存在")

// Provider 运行时配置热读取接口：每次调用取当前生效值，不缓存。
// 与 safeday.ConfigReader / worklog.ConfigReader 同签名，可互相注入。
type Provider interface {
	Get(ctx context.Context, key string) (string, error)
}

// Lookup 读配置便捷函数：出错或缺失一律空串，调用方按未配置处理。
func Lookup(ctx context.Context, p Provider, key string) string {
	if p == nil {
		return ""
	}
	v, err := p.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

// MapProvider 内存配置源：测试桩与启动期覆盖值；Set 即时生效（热更新）。
type MapProvider struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewMapProvider 构造内存配置源（初始可选键值对）。
func NewMapProvider(pairs map[string]string) *MapProvider {
	m := make(map[string]string, len(pairs))
	for k, v := range pairs {
		m[k] = v
	}
	return &MapProvider{m: m}
}

// Get 读内存值；键不存在返回 ErrNotFound（空串原样返回）。
func (p *MapProvider) Get(_ context.Context, key string) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set 写入/更新键值（热更新，下次 Get 生效）。
func (p *MapProvider) Set(key, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]string{}
	}
	p.m[key] = value
}

// Delete 删除键。
func (p *MapProvider) Delete(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, key)
}

// EnvProvider 环境变量配置源（启动装配用；空值视为未配置返回 ErrNotFound）。
type EnvProvider struct {
	// Getenv 取环境变量（nil 时用 os.Getenv；测试注入桩）。
	Getenv func(string) string
}

// NewEnvProvider 构造 env 配置源。
func NewEnvProvider() *EnvProvider { return &EnvProvider{} }

// Get 读环境变量。
func (p *EnvProvider) Get(_ context.Context, key string) (string, error) {
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	v := getenv(key)
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

// FuncProvider 函数适配：把 func 包装为 Provider（对接 *config.Store 等任意来源）。
type FuncProvider func(ctx context.Context, key string) (string, error)

// Get 调用函数取值。
func (f FuncProvider) Get(ctx context.Context, key string) (string, error) {
	if f == nil {
		return "", ErrNotFound
	}
	return f(ctx, key)
}

// ChainProvider 链式回退配置源：按顺序取首个非空值（出错/空串跳过继续），改任一层即时生效。
type ChainProvider struct {
	Providers []Provider
}

// NewChainProvider 构造链式回退配置源（nil 元素跳过）。
func NewChainProvider(providers ...Provider) *ChainProvider {
	return &ChainProvider{Providers: providers}
}

// NewRuntimeProvider 组装「库内运行时配置优先、env 兜底」的热读取链（store 可为 nil）。
func NewRuntimeProvider(store Provider) *ChainProvider {
	chain := &ChainProvider{}
	if store != nil {
		chain.Providers = append(chain.Providers, store)
	}
	chain.Providers = append(chain.Providers, NewEnvProvider())
	return chain
}

// Get 按序回退取值。
func (c *ChainProvider) Get(ctx context.Context, key string) (string, error) {
	if c != nil {
		for _, p := range c.Providers {
			if p == nil {
				continue
			}
			v, err := p.Get(ctx, key)
			if err != nil || strings.TrimSpace(v) == "" {
				continue
			}
			return v, nil
		}
	}
	return "", ErrNotFound
}
