// 有界重试：全包唯一重试层（瞬时传输故障才重试）。调用方不得在业务层再叠加重试，
// 避免多层重试连乘成「四层重试」；WorkflowClient 缺省总尝试 3 次，COSStore 缺省不重试。
package integrations

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// RetryPolicy 有界重试策略。零值 = 不重试（单次尝试）。
type RetryPolicy struct {
	// MaxAttempts 总尝试次数（含首次）；≤1 表示不重试。
	MaxAttempts int
	// BaseWait 首次重试前等待（默认 200ms），按重试序号指数递增，单次不超过 MaxWait。
	BaseWait time.Duration
	// MaxWait 单次等待上限（默认 2s）。
	MaxWait time.Duration
	// Sleep 等待实现（测试注入；nil 用定时器，可被 ctx 取消）。
	Sleep func(ctx context.Context, d time.Duration) error
}

// normalized 补默认值。
func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.BaseWait <= 0 {
		p.BaseWait = 200 * time.Millisecond
	}
	if p.MaxWait <= 0 {
		p.MaxWait = 2 * time.Second
	}
	if p.Sleep == nil {
		p.Sleep = sleepCtx
	}
	return p
}

// sleepCtx 可取消等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do 执行 op 至多 MaxAttempts 次：仅当 retryable(err) 为真、仍有次数且父 ctx 未取消时才等待后重试。
// 返回最后一次错误（成功返回 nil）；等待被 ctx 取消时返回 ctx.Err()。
func (p RetryPolicy) Do(ctx context.Context, retryable func(error) bool, op func(ctx context.Context, attempt int) error) error {
	p = p.normalized()
	var last error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		if attempt > 1 {
			wait := p.BaseWait << (attempt - 2)
			if wait <= 0 || wait > p.MaxWait {
				wait = p.MaxWait
			}
			if err := p.Sleep(ctx, wait); err != nil {
				return err
			}
		}
		last = op(ctx, attempt)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return last
		}
		if retryable == nil || !retryable(last) {
			return last
		}
	}
	return last
}

// statusCoder 携带 HTTP 状态码的错误（瞬时判定用）。
type statusCoder interface {
	HTTPStatus() int
}

// transientError 瞬时故障判定：传输错误 / 超时 / HTTP 429 与 5xx 可重试；
// 父 ctx 取消、4xx、业务失败与解析错误不重试。
func transientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var sc statusCoder
	if errors.As(err, &sc) {
		code := sc.HTTPStatus()
		return code == http.StatusTooManyRequests || code >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
