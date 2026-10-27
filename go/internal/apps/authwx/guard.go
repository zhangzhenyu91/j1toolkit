package authwx

import "time"

// 登录口令接口限流（同 Node routes/auth.js：纯内存防爆破）。
// 同一 IP 在窗口期内失败达上限即拒绝（42901），成功登录清零。
const (
	loginFailWindow = 10 * time.Minute // 限流统计窗口
	loginFailMax    = 20               // 窗口期内允许的最大失败次数
)

// loginAttempt 单 IP 的失败计数（窗口起点 + 失败次数）。
type loginAttempt struct {
	count int
	start time.Time
}

// limited 是否已触发限流（只查询，不计数）。
func (m *Module) limited(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.fails[ip]
	return ok && time.Since(rec.start) < loginFailWindow && rec.count >= loginFailMax
}

// recordFail 记录一次登录失败（窗口过期则重新起算）；顺带清理过期记录防 Map 无限增长。
func (m *Module) recordFail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	rec, ok := m.fails[ip]
	if !ok || now.Sub(rec.start) >= loginFailWindow {
		m.fails[ip] = loginAttempt{count: 1, start: now}
	} else {
		rec.count++
		m.fails[ip] = rec
	}
	for key, r := range m.fails {
		if now.Sub(r.start) >= loginFailWindow {
			delete(m.fails, key)
		}
	}
}

// clearFail 登录/校验成功：清零该 IP 的失败计数。
func (m *Module) clearFail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fails, ip)
}
