package usbcmd

import (
	"context"
	"sync"
)

// HubRef 目标 Hub 的设备定位信息；由服务端班组绑定解析后放入 context，
// bridge.UsbLink 据此覆盖模块默认的 *_SYNC_DEVICE_* 配置。
type HubRef struct {
	ID   string
	Ddns string
	Mac  string
	User string
}

type hubContextKey struct{}

// WithHub 把目标 Hub 放入本轮同步 context。
func WithHub(ctx context.Context, hub HubRef) context.Context {
	return context.WithValue(ctx, hubContextKey{}, hub)
}

// HubFrom 读取本轮指定的 Hub；未指定时返回 false（走旧单设备配置）。
func HubFrom(ctx context.Context) (HubRef, bool) {
	if ctx == nil {
		return HubRef{}, false
	}
	hub, ok := ctx.Value(hubContextKey{}).(HubRef)
	return hub, ok
}

// SessionPool 按设备（Hub）串行化 U 盘会话：同一 Hub 的树障/交跨/派车互斥，
// 不同 Hub 可并行。key 为空表示旧单设备模式。
type SessionPool struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewSessionPool 构造会话池。
func NewSessionPool() *SessionPool {
	return &SessionPool{sessions: map[string]*Session{}}
}

// DefaultSessions 服务端进程级会话池（单进程部署；测试可各自 NewSessionPool）。
var DefaultSessions = NewSessionPool()

func (p *SessionPool) session(key string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions == nil {
		p.sessions = map[string]*Session{}
	}
	session := p.sessions[key]
	if session == nil {
		session = NewSession()
		p.sessions[key] = session
	}
	return session
}

// Acquire 阻塞等待目标设备会话锁。
func (p *SessionPool) Acquire(ctx context.Context, key string) error {
	return p.session(key).Acquire(ctx)
}

// TryAcquire 非阻塞抢目标设备会话锁。
func (p *SessionPool) TryAcquire(key string) bool {
	return p.session(key).TryAcquire()
}

// Release 释放目标设备会话锁（未持有时安全返回）。
func (p *SessionPool) Release(key string) {
	p.session(key).Release()
}
