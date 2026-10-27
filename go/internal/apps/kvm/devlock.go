// KVM 设备维护锁（对齐 server/src/kvm/devlock.js）：锁定期间「远程连接计算机 / 文件传输」
// 对该设备暂不可用（每日派车单同步窗口用）；锁存 Redis（key: kvm:maint:{ddns}，value=原因文案），
// TTL 兜底自动解禁；Redis 异常时降级为日志、按未锁放行（fail-open）。
package kvm

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Locker 设备维护锁存储（Redis 实现见 RedisLocker；测试用 MemoryLocker）。
type Locker interface {
	// Lock 写锁（ttlSec 兜底自动解禁，缺省/过小按 60s 保护）。
	Lock(ctx context.Context, ddns, reason string, ttlSec int) error
	// Unlock 解锁。
	Unlock(ctx context.Context, ddns string) error
	// GetReason 命中返回原因文案（锁定中），未锁返回空串；存储异常按未锁放行（fail-open）。
	GetReason(ctx context.Context, ddns string) string
}

// 默认 TTL 兜底（秒）与最小保护 TTL。
const (
	defaultLockTTLSec = 7200
	minLockTTLSec     = 60
)

// 编译期确认两种实现均可装配为 Locker。
var (
	_ Locker = (*MemoryLocker)(nil)
	_ Locker = (*RedisLocker)(nil)
)

// MemoryLocker 进程内锁实现（测试与无 Redis 场景；语义同 RedisLocker）。
type MemoryLocker struct {
	mu   sync.Mutex
	data map[string]memLock
	now  func() time.Time
}

type memLock struct {
	reason string
	expire time.Time
}

// NewMemoryLocker 构造内存锁。
func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{data: map[string]memLock{}, now: time.Now}
}

// Lock 写锁。
func (l *MemoryLocker) Lock(_ context.Context, ddns, reason string, ttlSec int) error {
	if ttlSec < minLockTTLSec {
		ttlSec = minLockTTLSec
	}
	if reason == "" {
		reason = "设备维护中"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.data[ddns] = memLock{reason: reason, expire: l.now().Add(time.Duration(ttlSec) * time.Second)}
	return nil
}

// Unlock 解锁。
func (l *MemoryLocker) Unlock(_ context.Context, ddns string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.data, ddns)
	return nil
}

// GetReason 命中返回原因文案，未锁/过期返回空串。
func (l *MemoryLocker) GetReason(_ context.Context, ddns string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.data[ddns]
	if !ok {
		return ""
	}
	if !entry.expire.IsZero() && l.now().After(entry.expire) {
		delete(l.data, ddns)
		return ""
	}
	return entry.reason
}

// RedisLocker Redis 锁实现（key: kvm:maint:{ddns}，value=原因文案，TTL 兜底），
// 与 Node devlock.js 同键名；写/删/读失败降级放行（fail-open，与 redis.js 黑名单同口径）。
type RedisLocker struct {
	Client *redis.Client
}

func keyOf(ddns string) string { return "kvm:maint:" + ddns }

// Lock 写锁（异常吞掉，fail-open）。
func (l *RedisLocker) Lock(ctx context.Context, ddns, reason string, ttlSec int) error {
	if ttlSec < minLockTTLSec {
		ttlSec = minLockTTLSec
	}
	if reason == "" {
		reason = "设备维护中"
	}
	if err := l.Client.Set(ctx, keyOf(ddns), reason, time.Duration(ttlSec)*time.Second).Err(); err != nil {
		return nil // fail-open：写入失败不阻断业务
	}
	return nil
}

// Unlock 解锁（异常吞掉，fail-open）。
func (l *RedisLocker) Unlock(ctx context.Context, ddns string) error {
	_ = l.Client.Del(ctx, keyOf(ddns)).Err()
	return nil
}

// GetReason 命中返回原因文案，未锁/异常返回空串（fail-open）。
func (l *RedisLocker) GetReason(ctx context.Context, ddns string) string {
	value, err := l.Client.Get(ctx, keyOf(ddns)).Result()
	if err != nil {
		return "" // 未锁或存储异常一律按未锁放行
	}
	return value
}
