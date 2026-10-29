package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstanceLock_AcquireAndShow(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "uvmp-toolkit.lock")

	l1, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("首次获取锁失败: %v", err)
	}
	defer l1.Close()

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("锁文件应存在: %v", err)
	}

	// 二次启动：应拿到 ErrAlreadyRunning 并唤起首实例
	l2, err := Acquire(lockPath)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("二次获取应报 ErrAlreadyRunning，实际: %v", err)
	}
	if l2 != nil {
		t.Fatalf("二次获取不应返回锁: %+v", l2)
	}

	select {
	case <-l1.ShowRequests():
	case <-time.After(2 * time.Second):
		t.Fatal("首实例未收到显示主窗口信号")
	}
}

func TestInstanceLock_StaleReclaim(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "uvmp-toolkit.lock")
	// 陈旧锁：残留地址不可达（模拟崩溃残留）
	if err := os.WriteFile(lockPath, []byte("127.0.0.1:1"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("陈旧锁应可回收: %v", err)
	}
	defer l.Close()
}

func TestInstanceLock_CloseReleases(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "uvmp-toolkit.lock")
	l, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("获取锁失败: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("Close 后锁文件应删除: %v", err)
	}
	// 关闭后可再次获取
	l2, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("释放后再次获取失败: %v", err)
	}
	_ = l2.Close()
	// 幂等
	if err := l.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等: %v", err)
	}
}
