package render

import (
	"testing"
	"time"
)

func TestNetTrackerCounts(t *testing.T) {
	n := newNetTracker()
	n.request("r1", "http://map.sgcc.com.cn/tile/1.png")
	n.request("r2", "http://uvmp.sgcc.com.cn/api/x")
	doneN, busy := n.counts("map.sgcc.com.cn")
	if doneN != 0 || len(busy) != 1 {
		t.Fatalf("在途统计不对: done=%d busy=%v", doneN, busy)
	}
	n.finish("r1")
	n.finish("r2")
	doneN, busy = n.counts("map.sgcc.com.cn")
	if doneN != 1 || len(busy) != 0 {
		t.Fatalf("完成后统计不对: done=%d busy=%v", doneN, busy)
	}
	// 空 substr 计全量
	doneN, _ = n.counts("")
	if doneN != 2 {
		t.Fatalf("全量统计不对: %d", doneN)
	}
}

func TestNetTrackerRingCap(t *testing.T) {
	n := newNetTracker()
	for i := 0; i < netDoneKeep+50; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		n.request(id, "http://map.sgcc.com.cn/tile")
		n.finish(id)
	}
	n.counts("map.sgcc.com.cn")
	n.mu.Lock()
	got := len(n.done)
	n.mu.Unlock()
	if got != netDoneKeep {
		t.Fatalf("已完成缓冲应截到 %d，得到 %d", netDoneKeep, got)
	}
}

func TestIdleTrackerQuietWindow(t *testing.T) {
	tr := newIdleTracker(5*time.Second, 2, 0)
	base := time.Unix(1000, 0)

	// 未见到 2 个请求前不开始计静默
	if tr.update(base, 1, 0) {
		t.Fatal("minSeen 未满足不应判静默")
	}
	// 见到后开始计时，quiet 未满不判
	if tr.update(base.Add(time.Second), 2, 0) {
		t.Fatal("静默刚开始不应立即完成")
	}
	if tr.update(base.Add(3*time.Second), 2, 0) {
		t.Fatal("quiet 未满不应判完成")
	}
	if !tr.update(base.Add(6*time.Second), 2, 0) {
		t.Fatal("静默持续超过 quiet 应判完成")
	}
}

func TestIdleTrackerBusyResets(t *testing.T) {
	tr := newIdleTracker(5*time.Second, 0, 0)
	base := time.Unix(2000, 0)
	tr.update(base, 0, 0)
	if tr.update(base.Add(4*time.Second), 0, 1) {
		t.Fatal("有在途请求不应判静默")
	}
	// 在途归零后重新计时
	if tr.update(base.Add(5*time.Second), 0, 0) {
		t.Fatal("刚归零不应立即完成")
	}
	if !tr.update(base.Add(11*time.Second), 0, 0) {
		t.Fatal("重新静默满 quiet 应判完成")
	}
}

func TestIdleTrackerBaseDoneBaseline(t *testing.T) {
	// 等待开始时已有 1 个完成请求作为基线，新见 1 个（共 2-1=1 + 在途 0）< minSeen=2
	tr := newIdleTracker(time.Second, 2, 1)
	base := time.Unix(3000, 0)
	if tr.update(base, 2, 0) {
		t.Fatal("基线之后仅新增 1 个请求，不应满足 minSeen=2")
	}
	if tr.update(base.Add(time.Second), 3, 0) {
		t.Fatal("刚开始计静默不应立即完成")
	}
	if !tr.update(base.Add(3*time.Second), 3, 0) {
		t.Fatal("新增 2 个请求后静默应判完成")
	}
}
