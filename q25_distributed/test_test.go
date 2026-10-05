package q25_distributed

// 测试与基准必须放在 *_test.go 里，go test 才会发现它们。
import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------- 测试 ----------

// TestIdempotentConsumption 锁定"重复投递只处理一次"这个不变量。
func TestIdempotentConsumption(t *testing.T) {
	q := newMsgQueue()
	q.enqueue(message{ID: "m1", Payload: 1})

	d := newDedupStore()
	processed := 0
	for i := 0; i < 5; i++ {
		m, ok := q.poll()
		if !ok {
			break
		}
		if !d.alreadyProcessed(m.ID) {
			processed++
		}
		if i < 2 {
			q.redeliver() // 前两轮模拟 ack 丢失
		} else {
			q.ack(m.ID)
		}
	}
	if processed != 1 {
		t.Errorf("重复投递下应当只处理 1 次，实际 %d", processed)
	}
}

// TestBackoffIsBounded 保证退避有上限、且抖动在合理范围内。
func TestBackoffIsBounded(t *testing.T) {
	const base, max = 100 * time.Millisecond, time.Second
	for attempt := 1; attempt <= 20; attempt++ {
		d := backoffDelay(attempt, base, max, 0, pseudoRand)
		if d > max {
			t.Fatalf("attempt=%d 退避 %v 超过上限 %v", attempt, d, max)
		}
		if d <= 0 {
			t.Fatalf("attempt=%d 退避时长非法: %v", attempt, d)
		}
	}
	// 抖动应当在 [0.8*base, 1.2*base] 内
	for i := 0; i < 50; i++ {
		d := backoffDelay(1, base, max, 0.2, pseudoRand)
		if d < time.Duration(0.8*float64(base)) || d > time.Duration(1.2*float64(base)) {
			t.Fatalf("抖动超出 ±20%% 范围: %v", d)
		}
	}
}

// TestTokenBucketLimits 验证桶容量决定瞬时突发上限。
func TestTokenBucketLimits(t *testing.T) {
	tb := newTokenBucket(100, 10)
	allowed := 0
	for i := 0; i < 100; i++ {
		if tb.Allow(1) {
			allowed++
		}
	}
	// 初始满桶 10 个令牌，且调用极快（补充量可忽略），应当只放行 ~10 个
	if allowed > 12 {
		t.Errorf("瞬时放行 %d 次，超过了 burst=10 的预期上限", allowed)
	}
	if allowed < 10 {
		t.Errorf("瞬时至少应放行 10 次（满桶），实际 %d", allowed)
	}
}

// TestCircuitBreakerStates 锁定三态流转。
func TestCircuitBreakerStates(t *testing.T) {
	cb := newCircuitBreaker(3, 50*time.Millisecond)

	if cb.State() != stateClosed {
		t.Fatal("初始应为 Closed")
	}
	for i := 0; i < 3; i++ {
		cb.Allow()
		cb.Report(false)
	}
	if cb.State() != stateOpen {
		t.Fatalf("连续 3 次失败后应为 Open，实际 %v", cb.State())
	}
	if cb.Allow() {
		t.Error("Open 状态应当拒绝放行")
	}

	time.Sleep(60 * time.Millisecond)
	if cb.State() != stateHalfOpen {
		t.Fatalf("冷却后应为 HalfOpen，实际 %v", cb.State())
	}
	if !cb.Allow() {
		t.Error("HalfOpen 应当允许一次探测")
	}
	cb.Report(true)
	if cb.State() != stateClosed {
		t.Fatalf("探测成功后应恢复 Closed，实际 %v", cb.State())
	}
}

// TestRetryStopsOnNonRetryable 不可重试的错误要立刻返回，不能浪费重试次数。
func TestRetryStopsOnNonRetryable(t *testing.T) {
	calls := 0
	attempts, err := retryWithBackoff(context.Background(), 5, func(int) error {
		calls++
		return errBadRequest
	})
	if !errors.Is(err, errBadRequest) {
		t.Fatalf("应当返回原错误，实际 %v", err)
	}
	if calls != 1 || attempts != 1 {
		t.Errorf("不可重试错误应当只调用 1 次，实际 calls=%d attempts=%d", calls, attempts)
	}
}

// TestRetryRespectsContext 上游取消后必须立刻停止重试。
func TestRetryRespectsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	calls := 0
	_, err := retryWithBackoff(ctx, 10, func(int) error {
		calls++
		return errTimeout
	})
	if err == nil {
		t.Fatal("应当因为 ctx 超时而返回错误")
	}
	if calls > 5 {
		t.Errorf("ctx 取消后不应继续重试，实际调用 %d 次", calls)
	}
}

// ---------- 基准测试 ----------

func BenchmarkTokenBucketAllow(b *testing.B) {
	tb := newTokenBucket(1e9, 1e9) // 足够大，专测加锁开销
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tb.Allow(1)
	}
}

func BenchmarkCircuitBreakerAllow(b *testing.B) {
	cb := newCircuitBreaker(1000, time.Second)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cb.Allow()
	}
}
