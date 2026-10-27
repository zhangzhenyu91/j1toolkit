package integrations

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fastRetry 快速重试策略（不真睡）。
func fastRetry(maxAttempts int) RetryPolicy {
	return RetryPolicy{
		MaxAttempts: maxAttempts,
		BaseWait:    time.Millisecond,
		MaxWait:     time.Millisecond,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
}

// TestRetryBounded 有界重试：瞬时故障在预算内重试至成功。
func TestRetryBounded(t *testing.T) {
	calls := 0
	err := fastRetry(3).Do(context.Background(), transientError, func(context.Context, int) error {
		calls++
		if calls < 3 {
			return context.DeadlineExceeded // 瞬时超时：触发重试
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	if calls != 3 {
		t.Fatalf("调用次数 = %d，期望 3", calls)
	}
}

// TestRetryTransientOnly 仅瞬时故障重试：5xx 重试、4xx 单次。
func TestRetryTransientOnly(t *testing.T) {
	calls := 0
	err := fastRetry(3).Do(context.Background(), transientError, func(context.Context, int) error {
		calls++
		return &HTTPError{StatusCode: 500, Body: "x"}
	})
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 500 {
		t.Fatalf("Do = %v，期望 HTTPError 500", err)
	}
	if calls != 3 {
		t.Fatalf("5xx 调用次数 = %d，期望 3（重试至上限）", calls)
	}

	calls = 0
	err = fastRetry(3).Do(context.Background(), transientError, func(context.Context, int) error {
		calls++
		return &HTTPError{StatusCode: 400, Body: "x"}
	})
	if !errors.As(err, &he) || he.StatusCode != 400 {
		t.Fatalf("Do = %v，期望 HTTPError 400", err)
	}
	if calls != 1 {
		t.Fatalf("4xx 调用次数 = %d，期望 1（不重试）", calls)
	}
}

// TestRetryZeroValueNoRetry 零值策略 = 单次尝试（COS 缺省口径）。
func TestRetryZeroValueNoRetry(t *testing.T) {
	calls := 0
	var p RetryPolicy
	_ = p.Do(context.Background(), transientError, func(context.Context, int) error {
		calls++
		return context.DeadlineExceeded // 瞬时故障也只试一次
	})
	if calls != 1 {
		t.Fatalf("零值调用次数 = %d，期望 1", calls)
	}
}

// TestRetryContextCancel 父 ctx 取消不再重试。
func TestRetryContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := fastRetry(3).Do(ctx, transientError, func(context.Context, int) error {
		calls++
		cancel()
		return context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("Do 应返回错误")
	}
	if calls != 1 {
		t.Fatalf("调用次数 = %d，期望 1（取消后不重试）", calls)
	}
}

// TestRetryBackoffSchedule 退避按序递增且封顶。
func TestRetryBackoffSchedule(t *testing.T) {
	var waits []time.Duration
	p := RetryPolicy{
		MaxAttempts: 4,
		BaseWait:    10 * time.Millisecond,
		MaxWait:     25 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	}
	_ = p.Do(context.Background(), transientError, func(context.Context, int) error {
		return context.DeadlineExceeded
	})
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("等待次数 = %d，期望 %d", len(waits), len(want))
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("第 %d 次等待 = %v，期望 %v", i+1, waits[i], want[i])
		}
	}
}

// TestTransientError 瞬时判定：超时/EOF 可重试，业务错误与解析错误不可。
func TestTransientError(t *testing.T) {
	if transientError(nil) {
		t.Fatal("nil 不应重试")
	}
	if transientError(&WorkflowError{Status: "failed"}) {
		t.Fatal("工作流失败不应重试")
	}
	if transientError(ErrBadEnvelope) {
		t.Fatal("解析错误不应重试")
	}
	if transientError(context.Canceled) {
		t.Fatal("取消不应重试")
	}
	if !transientError(context.DeadlineExceeded) {
		t.Fatal("超时应重试")
	}
	if !transientError(&HTTPError{StatusCode: 429}) {
		t.Fatal("429 应重试")
	}
	if !transientError(&HTTPError{StatusCode: 503}) {
		t.Fatal("503 应重试")
	}
	if transientError(&HTTPError{StatusCode: 403}) {
		t.Fatal("403 不应重试")
	}
}
