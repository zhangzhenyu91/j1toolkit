package platform

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// GOOS 返回当前操作系统名（测试可经 stubGOOS 覆盖）。
func GOOS() string { return runtime.GOOS }

// ErrAlreadyRunning 已有实例在运行（本次启动应直接退出）。
var ErrAlreadyRunning = errors.New("已有实例在运行")

// InstanceLock 单实例锁：
//   - 锁文件（O_CREATE|O_EXCL）防并发双开；
//   - 锁内记录本机回环监听地址，二次启动拨号发送 show 信号唤起主窗口；
//   - 地址不可达的锁视为陈旧锁，自动回收（升级/崩溃残留场景）。
type InstanceLock struct {
	path string
	ln   net.Listener

	mu     sync.Mutex
	closed bool
	show   chan struct{}
}

// Acquire 非阻塞获取单实例锁。
// 已被占用时向首实例发送「显示主窗口」信号并返回 ErrAlreadyRunning。
func Acquire(lockPath string) (*InstanceLock, error) {
	for attempt := 0; attempt < 3; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("单实例监听失败: %w", err)
		}
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(ln.Addr().String())
			_ = f.Close()
			if werr != nil {
				_ = ln.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("写锁文件失败: %w", werr)
			}
			l := &InstanceLock{path: lockPath, ln: ln, show: make(chan struct{}, 4)}
			go l.acceptLoop()
			return l, nil
		}
		_ = ln.Close()
		if !os.IsExist(err) {
			return nil, fmt.Errorf("创建锁文件失败: %w", err)
		}
		// 已存在：读地址并尝试唤起首实例
		stale, err := signalExisting(lockPath)
		if err != nil {
			return nil, err
		}
		if !stale {
			return nil, ErrAlreadyRunning
		}
		// 陈旧锁：回收后重试（竞争下由下一轮 O_EXCL 裁决）
		_ = os.Remove(lockPath)
		time.Sleep(20 * time.Millisecond)
	}
	return nil, ErrAlreadyRunning
}

// signalExisting 向锁文件记录的地址发送 show 信号。
// 返回 stale=true 表示锁已失效（空地址/不可达且确认陈旧），可回收。
func signalExisting(lockPath string) (stale bool, err error) {
	data, rerr := os.ReadFile(lockPath)
	if rerr != nil {
		// 文件刚被删除：处于重试竞态窗口，判陈旧让上层重试
		time.Sleep(20 * time.Millisecond)
		return true, nil
	}
	addr := strings.TrimSpace(string(data))
	if addr == "" {
		// 首实例尚未写入地址：短暂等待后复查，仍为空才判陈旧
		time.Sleep(50 * time.Millisecond)
		data, _ = os.ReadFile(lockPath)
		addr = strings.TrimSpace(string(data))
		if addr == "" {
			return true, nil
		}
	}
	conn, derr := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if derr != nil {
		return true, nil
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("show\n"))
	return false, nil
}

func (l *InstanceLock) acceptLoop() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return // 监听已关闭
		}
		buf := make([]byte, 16)
		_, _ = conn.Read(buf)
		_ = conn.Close()
		if strings.Contains(string(buf), "show") {
			select {
			case l.show <- struct{}{}:
			default:
			}
		}
	}
}

// ShowRequests 返回二次启动「显示主窗口」信号通道（缓冲，不阻塞 accept）。
func (l *InstanceLock) ShowRequests() <-chan struct{} { return l.show }

// Addr 锁监听地址（诊断用）。
func (l *InstanceLock) Addr() string { return l.ln.Addr().String() }

// Close 释放锁：关监听并删除锁文件（仅当锁文件仍指向本实例）；幂等。
func (l *InstanceLock) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	_ = l.ln.Close()
	if data, err := os.ReadFile(l.path); err == nil {
		if strings.TrimSpace(string(data)) == l.Addr() {
			_ = os.Remove(l.path)
		}
	}
	return nil
}
