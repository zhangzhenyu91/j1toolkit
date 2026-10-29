package worklog

// fetchObjectsOrdered 测试：并发回源乱序完成仍按输入序交付、进度严格递增到 total；
// fake 存储桩 + 通道门控完成时机，不访问外部服务。

import (
	"context"
	"sync"
	"testing"
)

// gatedStore 回源完成时机可控的对象存储桩：GetBuffer 前等 gate 放行，取回后回调 onDone。
type gatedStore struct {
	*memObjects
	mu     sync.Mutex
	gates  map[string]chan struct{}
	onDone func(key string)
}

func (g *gatedStore) GetBuffer(ctx context.Context, key string) ([]byte, error) {
	g.mu.Lock()
	ch := g.gates[key]
	g.mu.Unlock()
	if ch != nil {
		<-ch
	}
	buf, err := g.memObjects.GetBuffer(ctx, key)
	if g.onDone != nil {
		g.onDone(key)
	}
	return buf, err
}

func TestFetchObjectsOrdered(t *testing.T) {
	slowGate := make(chan struct{})
	var mu sync.Mutex
	finished := map[string]bool{}
	store := &gatedStore{
		memObjects: &memObjects{items: map[string][]byte{
			"slow": []byte("SLOW"), "b": []byte("B"), "c": []byte("C"),
		}},
		gates: map[string]chan struct{}{"slow": slowGate},
		onDone: func(key string) {
			mu.Lock()
			finished[key] = true
			doneBC := finished["b"] && finished["c"]
			mu.Unlock()
			if doneBC {
				select {
				case <-slowGate:
				default:
					close(slowGate)
				}
			}
		},
	}
	refs := []photoRef{{cosKey: "slow"}, {cosKey: "b"}, {cosKey: "c"}}

	var progress []int
	var order []string
	fetchObjectsOrdered(context.Background(), store, refs, 3,
		func(done, total int) {
			if total != 3 {
				t.Errorf("total 应 3，实际 %d", total)
			}
			progress = append(progress, done)
		},
		func(i int, buf []byte, err error) {
			if err != nil {
				t.Errorf("回源不应失败: %v", err)
			}
			order = append(order, refs[i].cosKey)
		})

	// b、c 先于 slow 完成，交付仍须按输入序
	if len(order) != 3 || order[0] != "slow" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("交付应按输入序 [slow b c]，实际 %v", order)
	}
	// 进度须严格递增到 3（并发完成下不得回退或跳号）
	if len(progress) != 3 || progress[0] != 1 || progress[1] != 2 || progress[2] != 3 {
		t.Fatalf("进度应 [1 2 3]，实际 %v", progress)
	}
}
