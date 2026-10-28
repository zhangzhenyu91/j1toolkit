package render

import (
	"strings"
	"sync"
	"time"
)

// netTracker 在途/已完成网络请求登记（对应 official_track.py CdpBrowser 的
// _net_requests / _net_done），供 wait_network_idle 的「地图加载完成」判定使用。
type netTracker struct {
	mu       sync.Mutex
	inflight map[string]string // requestId -> url
	done     []string          // 已完成请求 URL 环形缓冲（最多 netDoneKeep 条）
}

const (
	netInflightCap = 2000 // 在途表上限，超出整表清空（与 Python 版一致）
	netDoneKeep    = 600  // 已完成 URL 环形缓冲长度
)

func newNetTracker() *netTracker {
	return &netTracker{inflight: map[string]string{}}
}

// request 登记在途请求。
func (t *netTracker) request(id, url string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.inflight) > netInflightCap {
		t.inflight = map[string]string{}
	}
	t.inflight[id] = url
}

// finish 请求结束（完成或失败），移出在途表并计入已完成缓冲；返回其 URL。
func (t *netTracker) finish(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	url := t.inflight[id]
	delete(t.inflight, id)
	if url != "" {
		t.done = append(t.done, url)
		if len(t.done) > netDoneKeep {
			t.done = t.done[len(t.done)-netDoneKeep:]
		}
	}
	return url
}

// counts 返回 substr 匹配的已完成请求数与在途 URL 列表（substr 空则全量）。
func (t *netTracker) counts(substr string) (doneN int, busy []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, u := range t.done {
		if substr == "" || strings.Contains(u, substr) {
			doneN++
		}
	}
	for _, u := range t.inflight {
		if substr == "" || strings.Contains(u, substr) {
			busy = append(busy, u)
		}
	}
	return doneN, busy
}

// idleTracker 网络静默判定状态机（对应 wait_network_idle 内层逻辑）：
// 在途归零、累计见到的请求数 ≥ minSeen，且持续 quiet 时长才判「静默完成」。
type idleTracker struct {
	quiet     time.Duration
	minSeen   int
	baseDone  int
	idleSince time.Time
	hasIdle   bool
}

func newIdleTracker(quiet time.Duration, minSeen, baseDone int) *idleTracker {
	return &idleTracker{quiet: quiet, minSeen: minSeen, baseDone: baseDone}
}

// update 喂入一次观测（当前已完成数与在途数），返回是否已连续静默达到 quiet。
func (t *idleTracker) update(now time.Time, doneN, busyCount int) bool {
	seen := doneN - t.baseDone
	if seen < 0 {
		seen = 0
	}
	seen += busyCount
	if busyCount > 0 || seen < t.minSeen {
		t.hasIdle = false
		return false
	}
	if !t.hasIdle {
		t.hasIdle = true
		t.idleSince = now
		return false
	}
	return now.Sub(t.idleSince) >= t.quiet
}
