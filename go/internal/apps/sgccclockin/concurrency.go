// 并发原语：引用计数键控互斥（同键串行 / 不同键并行）与有界并发闸门（上限按运行时配置热调整）。
// 两者都支持 ctx 退出等待——HTTP 请求等待名额时客户端可放弃，后台任务可随父 context 取消。
package sgccclockin

import (
	"context"
	"strconv"
	"sync"
)

// 并发缺省与收敛区间（运行时配置键见 runtimecfg.go）。
const (
	defaultSyncConcurrency  = 4
	maxSyncConcurrency      = 8
	defaultPhotoConcurrency = 2
	maxPhotoConcurrency     = 4
)

// memberDateKey 成员+日期键：同步单元与打卡/费用/照片关联的 read-modify-write 共用同一键空间，
// 同一成员的远端模板改写不会互相覆盖。
func memberDateKey(memberID int64, date string) string {
	return "m" + strconv.FormatInt(memberID, 10) + ":" + date
}

// keyedMutex 引用计数键控互斥：同键串行、不同键并行；最后一个使用者退出即回收条目，
// 键空间不随历史成员/日期增长。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

// keyedLock 单键锁：token 容量 1，占位即持有；refs 为持锁者+等待者计数。
type keyedLock struct {
	token chan struct{}
	refs  int
}

// Lock 取得键锁并返回解锁函数（幂等，必须调用）；ctx 取消时放弃等待并返回其错误。
func (k *keyedMutex) Lock(ctx context.Context, key string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{token: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	select {
	case l.token <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-l.token
				k.release(key, l)
			})
		}, nil
	case <-ctx.Done():
		k.release(key, l)
		return nil, ctx.Err()
	}
}

// release 退出一个使用者；归零即从表中移除（此后新来者重建条目，旧条目已无持有者）。
func (k *keyedMutex) release(key string, l *keyedLock) {
	k.mu.Lock()
	l.refs--
	if l.refs == 0 {
		delete(k.locks, key)
	}
	k.mu.Unlock()
}

// gate 有界并发闸门：上限由调用方按当前配置传入（变更后新进入者按新上限收敛，在途任务不打断），
// 超限排队；ctx 取消可退出等待。
type gate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	inFlight int
}

// acquire 取得一个并发名额（limit<=0 按 1 处理），返回幂等的释放函数。
func (g *gate) acquire(ctx context.Context, limit int) (func(), error) {
	if limit < 1 {
		limit = 1
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	if g.inFlight < limit {
		g.inFlight++
		g.mu.Unlock()
		var once sync.Once
		return func() { once.Do(g.release) }, nil
	}
	// cond.Wait 不可取消：ctx 结束时广播唤醒，等待循环里复查 ctx 与名额。
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		select {
		case <-ctx.Done():
			g.mu.Lock()
			g.cond.Broadcast()
			g.mu.Unlock()
		case <-stop:
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			stopOnce.Do(func() { close(stop) })
			g.mu.Unlock()
			return nil, err
		}
		if g.inFlight < limit {
			break
		}
		g.cond.Wait()
	}
	g.inFlight++
	stopOnce.Do(func() { close(stop) })
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(g.release) }, nil
}

// release 归还名额并唤醒一个等待者。
func (g *gate) release() {
	g.mu.Lock()
	g.inFlight--
	if g.cond != nil {
		g.cond.Signal()
	}
	g.mu.Unlock()
}

// snapshot 当前在途数（测试与诊断用）。
func (g *gate) snapshot() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight
}
