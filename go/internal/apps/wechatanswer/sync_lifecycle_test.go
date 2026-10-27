package wechatanswer

import (
	"context"
	"sync"
	"testing"
	"time"
)

type lifecycleSync struct {
	once    sync.Once
	started chan struct{}
}

func (s *lifecycleSync) Run(ctx context.Context, _ func(map[string]any)) error {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil
}

func TestSyncStartsAfterLateTokenConfiguration(t *testing.T) {
	env := newTestEnv(t, treeRule())
	cfg := fakeCfg{values: map[string]string{}}
	env.m.Cfg = cfg
	syncSource := &lifecycleSync{started: make(chan struct{})}
	env.m.Sync = syncSource
	if err := env.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer env.m.Close()
	select {
	case <-syncSource.started:
		t.Fatal("未配置 Token 时不应建立连接")
	case <-time.After(30 * time.Millisecond):
	}
	cfg.values["WECHATPADPRO_TOKEN"] = "fixture-token"
	env.m.Reconnect(context.Background())
	select {
	case <-syncSource.started:
	case <-time.After(time.Second):
		t.Fatal("补配 Token 后未启动消息通道")
	}
}
