// 日志导出一次性短时票据：浏览器原生下载无法带 Authorization 头，签发随机票据
// 供 <a download>/location 使用。票据只保存导出筛选条件、60 秒有效、兑换一次即失效，
// 不是 JWT，也不复用登录令牌；查询串中的 ticket 由 logsafe 统一脱敏。
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	logExportTicketTTL    = 60 * time.Second
	logExportTicketMaxTry = 5
)

// issuedLogTicket 签发给前端的票据信息。
type issuedLogTicket struct {
	Token     string
	URL       string
	Filename  string
	ExpiresIn int // 秒
}

// logExportTicket 服务端保存的票据（仅用于导出用途）。
type logExportTicket struct {
	Filter    LogFilter
	Filename  string
	ExpiresAt time.Time
}

// logTicketStore 内存票据表；进程重启/多副本不共享，短时下载可接受。
type logTicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	tickets map[string]logExportTicket
}

func newLogTicketStore(ttl time.Duration, now func() time.Time) *logTicketStore {
	if ttl <= 0 {
		ttl = logExportTicketTTL
	}
	if now == nil {
		now = time.Now
	}
	return &logTicketStore{ttl: ttl, now: now, tickets: make(map[string]logExportTicket)}
}

// issue 生成 32 字节随机票据；同一时刻的多次签发互不覆盖。
func (s *logTicketStore) issue(f LogFilter) (issuedLogTicket, error) {
	if s == nil {
		return issuedLogTicket{}, errors.New("日志导出票据存储未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupLocked(now)
	for i := 0; i < logExportTicketMaxTry; i++ {
		var buf [32]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return issuedLogTicket{}, err
		}
		token := hex.EncodeToString(buf[:])
		if _, exists := s.tickets[token]; exists {
			continue
		}
		filename := "app-logs-" + now.Format("20060102-150405") + ".ndjson"
		s.tickets[token] = logExportTicket{Filter: f, Filename: filename, ExpiresAt: now.Add(s.ttl)}
		return issuedLogTicket{
			Token:     token,
			URL:       "/api/v1/app-logs/export/download?ticket=" + token,
			Filename:  filename,
			ExpiresIn: int(s.ttl / time.Second),
		}, nil
	}
	return issuedLogTicket{}, errors.New("生成日志导出票据失败")
}

// redeem 一次性兑换：无论是否过期都立即删除，避免重复使用；过期返回 false。
func (s *logTicketStore) redeem(token string) (LogFilter, string, bool) {
	if s == nil || token == "" {
		return LogFilter{}, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[token]
	if !ok {
		return LogFilter{}, "", false
	}
	delete(s.tickets, token)
	if !s.now().Before(t.ExpiresAt) {
		return LogFilter{}, "", false
	}
	return t.Filter, t.Filename, true
}

// cleanupLocked 清理过期票据（调用方持锁）。
func (s *logTicketStore) cleanupLocked(now time.Time) {
	for token, t := range s.tickets {
		if !now.Before(t.ExpiresAt) {
			delete(s.tickets, token)
		}
	}
}
