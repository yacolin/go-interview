package q25_distributed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 1. 消费幂等：至少一次投递的必然结果 ----------

// msgQueue 模拟"至少一次（at-least-once）"投递的队列：
// 处理成功但 ack 丢失时，同一条消息会被重复投递。
type msgQueue struct {
	mu       sync.Mutex
	inflight []message
	acked    map[string]bool
	redelive int
}

type message struct {
	ID      string
	Payload int
	Attempt int
}

func newMsgQueue() *msgQueue {
	return &msgQueue{acked: map[string]bool{}}
}

func (q *msgQueue) enqueue(msgs ...message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inflight = append(q.inflight, msgs...)
}

// poll 取一条消息；如果之前没 ack，会被重新投递（Attempt 递增）。
func (q *msgQueue) poll() (message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.inflight) == 0 {
		return message{}, false
	}
	m := q.inflight[0]
	if !q.acked[m.ID] && m.Attempt > 0 {
		q.redelive++
	}
	return m, true
}

func (q *msgQueue) ack(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acked[id] = true
	if len(q.inflight) > 0 && q.inflight[0].ID == id {
		q.inflight = q.inflight[1:]
	}
}

// redeliver 把队首重新塞回去，模拟"处理完但 ack 丢了"。
func (q *msgQueue) redeliver() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.inflight) == 0 {
		return
	}
	m := q.inflight[0]
	m.Attempt++
	q.inflight = append(q.inflight, m)
}

// ---------- 2. 幂等消费的三种实现 ----------

// idempotentByDedup 用"去重表"实现幂等（最通用）。
type dedupStore struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newDedupStore() *dedupStore { return &dedupStore{seen: map[string]bool{}} }

func (d *dedupStore) alreadyProcessed(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[id] {
		return true
	}
	d.seen[id] = true // 真实场景要写进 DB/Redis，且带 TTL
	return false
}

// idempotentByVersion 用"版本号/CAS"实现幂等（适合状态更新）。
type versionedState struct {
	mu      sync.Mutex
	version int64
	value   int
}

func (s *versionedState) applyVersion(v int64, val int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v <= s.version { // 旧版本直接丢弃
		return false
	}
	s.version = v
	s.value = val
	return true
}

// idempotentByUniqueKey 用"唯一索引"实现幂等（最可靠，靠 DB 兜底）。
type insertOnce struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (i *insertOnce) insert(key string) (inserted bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.keys[key] {
		return false // 唯一索引冲突，视为已处理
	}
	i.keys[key] = true
	return true
}

// ---------- 3. 重试：指数退避 + 抖动 ----------

// backoffDelay 计算第 attempt 次重试的等待时间。
//
//	base * 2^attempt，上限 max，再乘以 [1-jitter, 1+jitter] 的随机因子。
//
// 抖动非常关键：没有它，所有失败请求会在同一时刻一起重试（惊群）。
func backoffDelay(attempt int, base, max time.Duration, jitter float64, randFn func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := math.Pow(2, float64(attempt-1))
	d := time.Duration(float64(base) * shift)
	if d > max || d <= 0 { // 溢出或超上限
		d = max
	}
	if jitter > 0 {
		f := 1 + jitter*(2*randFn()-1) // randFn 返回 [0,1)
		d = time.Duration(float64(d) * f)
	}
	if d < 0 {
		d = 0
	}
	return d
}

// isRetryable 判断错误是否可重试。
//
// 关键：网络超时/连接失败可重试；参数错误/业务拒绝不可重试；
// 非幂等操作（如扣款）默认不可重试，除非有幂等键。
func isRetryable(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.Is(err, context.Canceled):
		return false // 上游已放弃，别再重试
	case errors.Is(err, errBadRequest), errors.Is(err, errRejected):
		return false
	default:
		return true
	}
}

var (
	errBadRequest = errors.New("bad request")
	errRejected   = errors.New("rejected by business rule")
	errTimeout    = errors.New("network timeout")
)

// retryWithBackoff 带退避的重试，并受 ctx 控制（见 Q13）。
func retryWithBackoff(ctx context.Context, maxAttempts int, fn func(attempt int) error) (int, error) {
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt - 1, err
		}
		last = fn(attempt)
		if last == nil {
			return attempt, nil
		}
		if !isRetryable(last) {
			return attempt, last // 不可重试，立刻返回
		}
		if attempt == maxAttempts {
			break
		}
		delay := backoffDelay(attempt, 10*time.Millisecond, time.Second, 0.2, pseudoRand)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return attempt, ctx.Err()
		}
	}
	return maxAttempts, last
}

// pseudoRand 是确定性伪随机（LCG），保证演示可复现，返回 [0,1)。
//
// 注意这里必须用 uint64 运算：int64 的乘法会溢出成负数，
// 而 "负数 % 1000" 在 Go 里仍然是负数，会让抖动因子落到 [0,1) 之外。
// 之前就是因为这个 bug，抖动算出了 -38%，把退避时间压得比预期还短。
var randState uint64 = 12345

func pseudoRand() float64 {
	x := atomic.AddUint64(&randState, 1)
	x = x*6364136223846793005 + 1442695040888963407
	return float64(x>>11) / float64(uint64(1)<<53)
}

// ---------- 4. 令牌桶限流 ----------

// tokenBucket 是经典的令牌桶实现。
//
//	rate      每秒放入的令牌数
//	burst     桶容量（允许的瞬时突发）
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	last     time.Time
	nowFn    func() time.Time // 便于测试注入时间
	rejected int64
	allowed  int64
}

func newTokenBucket(rate, burst float64) *tokenBucket {
	return &tokenBucket{
		rate:   rate,
		burst:  burst,
		tokens: burst,
		last:   time.Now(),
		nowFn:  time.Now,
	}
}

// Allow 判断是否放行；n 表示本次需要几个令牌。
func (b *tokenBucket) Allow(n float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.nowFn()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now

	// 按时间补充令牌，上限为 burst
	b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)

	if b.tokens >= n {
		b.tokens -= n
		atomic.AddInt64(&b.allowed, 1)
		return true
	}
	atomic.AddInt64(&b.rejected, 1)
	return false
}

func (b *tokenBucket) stats() (allowed, rejected int64) {
	return atomic.LoadInt64(&b.allowed), atomic.LoadInt64(&b.rejected)
}

// ---------- 5. 熔断器 ----------

type breakerState int

const (
	stateClosed   breakerState = iota // 正常放行
	stateOpen                         // 直接拒绝
	stateHalfOpen                     // 试探性放行
)

func (s breakerState) String() string {
	switch s {
	case stateClosed:
		return "Closed(正常)"
	case stateOpen:
		return "Open(熔断)"
	default:
		return "HalfOpen(半开试探)"
	}
}

// circuitBreaker 是经典的三态熔断器。
type circuitBreaker struct {
	mu sync.Mutex

	failThreshold int           // 连续失败多少次触发熔断
	openDuration  time.Duration // 熔断后多久进入半开
	halfOpenProbe int           // 半开状态允许几次试探

	state        breakerState
	failures     int
	openedAt     time.Time
	probesLeft   int
	totalTrip    int64
	rejectedCall int64
	nowFn        func() time.Time
}

func newCircuitBreaker(failThreshold int, openDuration time.Duration) *circuitBreaker {
	return &circuitBreaker{
		failThreshold: failThreshold,
		openDuration:  openDuration,
		halfOpenProbe: 1,
		state:         stateClosed,
		nowFn:         time.Now,
	}
}

// Allow 判断本次调用是否放行，并在必要时做状态迁移。
func (cb *circuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case stateOpen:
		if cb.nowFn().Sub(cb.openedAt) >= cb.openDuration {
			cb.state = stateHalfOpen // 冷却结束，进入半开
			cb.probesLeft = cb.halfOpenProbe
		} else {
			atomic.AddInt64(&cb.rejectedCall, 1)
			return false
		}
	}

	if cb.state == stateHalfOpen {
		if cb.probesLeft <= 0 {
			atomic.AddInt64(&cb.rejectedCall, 1)
			return false
		}
		cb.probesLeft--
	}
	return true
}

// Report 上报调用结果，驱动状态机。
func (cb *circuitBreaker) Report(success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if success {
		// 半开状态下探测成功 -> 恢复
		cb.failures = 0
		cb.state = stateClosed
		return
	}

	cb.failures++
	if cb.state == stateHalfOpen {
		// 半开探测失败 -> 立刻重新熔断
		cb.state = stateOpen
		cb.openedAt = cb.nowFn()
		atomic.AddInt64(&cb.totalTrip, 1)
		return
	}
	if cb.failures >= cb.failThreshold {
		cb.state = stateOpen
		cb.openedAt = cb.nowFn()
		atomic.AddInt64(&cb.totalTrip, 1)
	}
}

// State 返回当前状态。
//
// 注意这里也做了 Open -> HalfOpen 的时间迁移：如果只在 Allow() 里迁移，
// 那么"只观察不调用"的调用方（监控、测试、健康检查）会一直读到过期的 Open。
func (cb *circuitBreaker) State() breakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == stateOpen && cb.nowFn().Sub(cb.openedAt) >= cb.openDuration {
		cb.state = stateHalfOpen
		cb.probesLeft = cb.halfOpenProbe
	}
	return cb.state
}

// ---------- 6. 重试放大 ----------

// retryAmplification 计算多层调用链上的总请求放大倍数。
//
// 3 层、每层重试 3 次 -> 最坏情况 3^3 = 27 倍请求打到最底层。
func retryAmplification(layers, retriesPerLayer int) int {
	total := 1
	for i := 0; i < layers; i++ {
		total *= retriesPerLayer
	}
	return total
}

func Run() {
	fmt.Println("=== Q25: 消息队列、分布式与可观测性 —— 场景题 ===")

	// 1. 至少一次投递与幂等
	fmt.Println("--- 1. 为什么必须做幂等消费 ---")
	q := newMsgQueue()
	q.enqueue(message{ID: "m1", Payload: 100})

	dedup := newDedupStore()
	processed := 0
	for round := 0; round < 3; round++ {
		m, ok := q.poll()
		if !ok {
			break
		}
		if dedup.alreadyProcessed(m.ID) {
			fmt.Printf("   第 %d 次投递 %s：已处理过，跳过（幂等生效）\n", round+1, m.ID)
			q.ack(m.ID)
			continue
		}
		processed++
		fmt.Printf("   第 %d 次投递 %s：首次处理，payload=%d\n", round+1, m.ID, m.Payload)
		// 模拟"处理成功但 ack 丢失"
		if round == 0 {
			q.redeliver()
		} else {
			q.ack(m.ID)
		}
	}
	fmt.Printf("   结果：投递 3 次，实际只处理 %d 次\n", processed)
	fmt.Println("   结论：MQ 只保证至少一次（at-least-once），")
	fmt.Println("        \"恰好一次\"是业务侧用幂等换来的，不是 MQ 给的")

	// 2. 三种幂等实现
	fmt.Println("--- 2. 幂等消费的三种实现 ---")
	d := newDedupStore()
	fmt.Printf("   去重表：  第一次=%v，第二次=%v\n",
		!d.alreadyProcessed("k1"), !d.alreadyProcessed("k1"))

	vs := &versionedState{}
	fmt.Printf("   版本号CAS：v2=%v, v1(旧)=%v, v3=%v\n",
		vs.applyVersion(2, 200), vs.applyVersion(1, 100), vs.applyVersion(3, 300))

	uq := &insertOnce{keys: map[string]bool{}}
	fmt.Printf("   唯一索引：insert=%v, 重复 insert=%v\n", uq.insert("order-1"), uq.insert("order-1"))
	fmt.Println("   选型：状态更新用版本号；创建类操作用唯一索引（最可靠）;")
	fmt.Println("        通用兜底用去重表，但要设 TTL 防无限增长")

	// 3. 重试退避
	fmt.Println("--- 3. 重试：指数退避 + 抖动 ---")
	fmt.Printf("   %-8s %-14s %s\n", "attempt", "无抖动", "有抖动(实际值)")
	for attempt := 1; attempt <= 5; attempt++ {
		noJitter := backoffDelay(attempt, 100*time.Millisecond, 5*time.Second, 0, pseudoRand)
		withJitter := backoffDelay(attempt, 100*time.Millisecond, 5*time.Second, 0.2, pseudoRand)
		fmt.Printf("   %-8d %-14v %v\n", attempt, noJitter, withJitter.Round(time.Millisecond))
	}
	fmt.Println("   为什么必须有抖动：没有它，所有失败实例会在同一毫秒一起重试，")
	fmt.Println("   把刚恢复的下游再次打挂（惊群 / thundering herd）")

	fmt.Println("   哪些错误该重试：")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"网络超时", errTimeout},
		{"上游取消", context.Canceled},
		{"超时", context.DeadlineExceeded},
		{"参数错误", errBadRequest},
		{"业务拒绝", errRejected},
	} {
		fmt.Printf("     %-10s 可重试=%v\n", tc.name, isRetryable(tc.err))
	}

	attempts, err := retryWithBackoff(context.Background(), 4, func(a int) error {
		if a < 3 {
			return errTimeout
		}
		return nil
	})
	fmt.Printf("   模拟前 2 次超时：第 %d 次成功, err=%v\n", attempts, err)

	// 4. 重试放大
	fmt.Println("--- 4. 重试放大（Retry Storm）---")
	for _, layers := range []int{1, 2, 3, 4} {
		fmt.Printf("   %d 层调用，每层重试 3 次 -> 最坏放大 %d 倍\n",
			layers, retryAmplification(layers, 3))
	}
	fmt.Println("   这就是\"一次故障引发雪崩\"的机制：底层抖动 -> 上层重试 -> 放大 27 倍")
	fmt.Println("   防护：")
	fmt.Println("     - 只在一层重试（通常在网关或最外层）")
	fmt.Println("     - 重试预算（retry budget）：重试量不超过总请求的 10%")
	fmt.Println("     - 熔断器：下游连续失败就快速失败，不要继续重试")
	fmt.Println("     - 重试前判断幂等性；非幂等操作带幂等键")

	// 5. 限流
	fmt.Println("--- 5. 令牌桶限流（rate=100/s, burst=10，模拟 0.5 秒内 200 次请求）---")
	tb := newTokenBucket(100, 10)
	for i := 0; i < 200; i++ {
		tb.Allow(1)
	}
	allowed, rejected := tb.stats()
	fmt.Printf("   瞬时 200 次请求：放行 %d，拒绝 %d\n", allowed, rejected)
	fmt.Println("   桶容量 burst 决定能扛多猛的瞬时突发；rate 决定长期平均速率")
	fmt.Println("   其他算法：漏桶（严格匀速）、滑动窗口（更平滑）、")
	fmt.Println("            固定窗口（简单但有临界突刺问题）")
	fmt.Println("   分布式限流：Redis + Lua 保证原子性，或令牌按实例数分摊")

	// 6. 熔断
	fmt.Println("--- 6. 熔断器三态流转 ---")
	cb := newCircuitBreaker(3, 100*time.Millisecond)
	fmt.Printf("   初始状态: %s\n", cb.State())
	for i := 1; i <= 3; i++ {
		cb.Allow()
		cb.Report(false)
		fmt.Printf("   第 %d 次失败后: %s（连续失败计数=%d）\n", i, cb.State(), i)
	}
	fmt.Printf("   熔断期间放行? %v（快速失败，保护下游）\n", cb.Allow())
	time.Sleep(120 * time.Millisecond) // 等冷却结束
	fmt.Printf("   冷却结束: %s\n", cb.State())
	fmt.Printf("   半开探测放行? %v\n", cb.Allow())
	cb.Report(true)
	fmt.Printf("   探测成功后: %s（恢复正常）\n", cb.State())
	fmt.Println("   三态意义：Closed 正常 -> Open 快速失败 -> HalfOpen 试探恢复")
	fmt.Println("   与重试的关系：熔断是\"别重试了\"，重试是\"再试一次\"，两者必须配合")

	// 7. 可观测性
	fmt.Println("--- 7. 可观测性三支柱 ---")
	fmt.Println("   Metrics（指标）：聚合数值，适合告警与趋势")
	fmt.Println("     - 黄金四信号：延迟、流量、错误、饱和度（Google SRE）")
	fmt.Println("     - Go 里用 Prometheus client_golang，注意不要用高基数 label")
	fmt.Println("   Logging（日志）：离散事件，适合定位具体请求")
	fmt.Println("     - 结构化日志（slog / zap），必须带 trace_id 才能串联")
	fmt.Println("     - 分级：ERROR 要能直接对应到告警，别把 ERROR 当日志用")
	fmt.Println("   Tracing（链路）：跨服务调用链，适合定位延迟瓶颈")
	fmt.Println("     - OpenTelemetry，靠 context 传递 trace id（见 Q13）")
	fmt.Println("   一个典型排查路径：")
	fmt.Println("     告警（Metrics 发现 P99 飙升）")
	fmt.Println("       -> 定位服务（Tracing 看哪个 span 慢）")
	fmt.Println("       -> 定位日志（用 trace_id 捞出那次请求的细节）")

	// 8. 分布式常见问题清单
	fmt.Println("--- 8. 分布式高频追问速查 ---")
	fmt.Println("   分布式锁：")
	fmt.Println("     - Redis SET NX PX + Lua 释放（要校验持有者，防误删别人的锁）")
	fmt.Println("     - 争议点：Redlock 在时钟漂移/GC 停顿下并不绝对安全")
	fmt.Println("     - 强一致场景用 etcd/ZooKeeper（基于 Raft/Paxos）")
	fmt.Println("   一致性哈希：解决节点增减导致的大规模缓存失效；")
	fmt.Println("             配合虚拟节点解决数据倾斜")
	fmt.Println("   CAP 与 BASE：分布式下 P（分区容忍）必须保留，")
	fmt.Println("             实际是在 C 和 A 之间选；BASE 是最终一致的工程妥协")
	fmt.Println("   分布式事务：2PC（阻塞、协调者单点）、TCC（侵入性强）、")
	fmt.Println("             本地消息表 / 事务消息（最终一致，最常用）")
	fmt.Println("   服务注册发现：etcd/Consul/Nacos；")
	fmt.Println("             注意\"摘除自己\"要早于优雅关闭（见 Q23）")
	fmt.Println("   超时传递：整条链路的超时必须递减，否则上游超时了下游还在算")
	fmt.Println("   幂等：本章核心，一切重试/补偿的前提")

	fmt.Println("--- 9. 一句话总结 ---")
	fmt.Println("   分布式系统的所有复杂度，本质上都来自两个事实：")
	fmt.Println("     1) 网络不可靠 —— 所以需要重试，而重试需要幂等")
	fmt.Println("     2) 节点会失败 —— 所以需要冗余，而冗余需要一致性协议")
	fmt.Println("   工程上的答案不是\"消灭问题\"，而是\"让失败可控\"：")
	fmt.Println("   超时 + 重试 + 幂等 + 熔断 + 限流 + 可观测，六件套缺一不可")
}
