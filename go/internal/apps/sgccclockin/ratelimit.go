// 登录链路限流：图形码/短信/绑定 三类操作按 用户+手机号 滑窗计数（对齐 Node 的 overLoginRate）。
package sgccclockin

import (
	"sync"
	"time"
)

const (
	// LoginRateWindowMS 滑窗时长（毫秒）。
	LoginRateWindowMS = 60 * 1000
)

// LoginRateLimits 各类操作窗口内上限（对齐 Node LOGIN_RATE_LIMITS）。
var LoginRateLimits = map[string]int{
	"captcha": 10,
	"sms":     5,
	"bind":    10,
}

type rateRecord struct {
	count int
	start time.Time
}

// LoginRateLimiter 登录限流器（可注入时钟；窗口过期记录惰性清理）。
type LoginRateLimiter struct {
	mu      sync.Mutex
	records map[string]*rateRecord
	limit   map[string]int
	window  time.Duration
	now     func() time.Time
}

// NewLoginRateLimiter 构造限流器（默认口径）。
func NewLoginRateLimiter() *LoginRateLimiter {
	limits := make(map[string]int, len(LoginRateLimits))
	for k, v := range LoginRateLimits {
		limits[k] = v
	}
	return &LoginRateLimiter{
		records: map[string]*rateRecord{},
		limit:   limits,
		window:  time.Duration(LoginRateWindowMS) * time.Millisecond,
		now:     time.Now,
	}
}

// Over 判定是否超限：kind= captcha/sms/bind；key 按 用户+手机号 组合。
func (l *LoginRateLimiter) Over(kind string, userID int64, mobile string) bool {
	key := kind + ":" + itoa(userID) + ":" + mobile
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.records[key]
	if rec == nil || now.Sub(rec.start) >= l.window {
		l.records[key] = &rateRecord{count: 1, start: now}
		return false
	}
	rec.count++
	return rec.count > l.limit[kind]
}

// Cleanup 清理过期窗口记录（定时调用，避免 Map 无限增长）。
func (l *LoginRateLimiter) Cleanup() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, rec := range l.records {
		if now.Sub(rec.start) >= l.window {
			delete(l.records, key)
		}
	}
}
