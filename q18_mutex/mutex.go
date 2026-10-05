package q18_mutex

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// ---------- 1. Mutex 的 state 位域 ----------
//
// Go 把 4 个信息塞进一个 int32（源码 runtime/sync.go + sync/mutex.go）：
//
//	b31 ... b3 |   b2   |   b1   |   b0
//	waiter 数量 | starv  | woken  | locked
//
// 对应常量：
//
//	mutexLocked      = 1 << 0 = 1
//	mutexWoken       = 1 << 1 = 2
//	mutexStarving    = 1 << 2 = 4
//	mutexWaiterShift = 3
//
// 用位域而不是多个字段，是为了能用一次 CAS 原子地判断和修改多个状态。

const (
	mutexLocked      = 1
	mutexWoken       = 2
	mutexStarving    = 4
	mutexWaiterShift = 3
)

// stateOf 读取 sync.Mutex 内部的 state 字段。
// 仅用于教学观测：这是未导出实现细节，生产代码绝不能这么干。
func stateOf(m *sync.Mutex) (state int32, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false // 布局变了就优雅退化
		}
	}()
	if unsafe.Sizeof(*m) < 4 {
		return 0, false
	}
	return *(*int32)(unsafe.Pointer(m)), true
}

// decodeState 把 state 按位域翻译成人话。
func decodeState(state int32) string {
	locked := state&mutexLocked != 0
	woken := state&mutexWoken != 0
	starving := state&mutexStarving != 0
	waiters := state >> mutexWaiterShift
	return fmt.Sprintf("state=%d -> locked=%v woken=%v starving=%v waiters=%d",
		state, locked, woken, starving, waiters)
}

// ---------- 2. 正常模式 vs 饥饿模式 ----------

// lockWaitTimes 把一个锁交给 n 个 goroutine 依次通过，返回每人的排队时长。
//
// 这里的加解锁是平衡的：主 goroutine 只 Lock 一次、Unlock 一次，
// 每个 worker 自己 Lock 后立刻 Unlock。
//
// 之前写错过一版，在循环里反复 Unlock，直接触发
// "fatal error: sync: unlock of unlocked mutex" —— 这个报错正是
// runtime 对锁误用的硬检测，比 C/C++ 的未定义行为友好得多。
func lockWaitTimes(n int) []time.Duration {
	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]time.Duration, n)

	mu.Lock() // 先占住，制造排队

	for i := 0; i < n; i++ {
		wg.Add(1) // Add 必须在 go 之前
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			mu.Lock() // 全部堵在这里
			results[idx] = time.Since(start)
			mu.Unlock()
		}(i)
	}

	time.Sleep(20 * time.Millisecond) // 让它们都进入等待队列
	mu.Unlock()                       // 放行，锁在 n 个 worker 之间传递

	wg.Wait()
	return results
}

// ---------- 3. 不可重入：自己锁自己 = 死锁 ----------

// reentrantDeadlock 在独立 goroutine 里重复加锁，用超时证明它卡住了。
func reentrantDeadlock() bool {
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		mu.Lock()
		mu.Lock() // 同一个 goroutine 再次加锁 -> 永久阻塞
		done <- struct{}{}
	}()
	select {
	case <-done:
		return false // 没有死锁
	case <-time.After(100 * time.Millisecond):
		return true // 死锁了
	}
}

// ---------- 4. 复制已使用的锁 ----------

// copiedMutex 演示"复制一把已加锁的 Mutex"会发生什么。
//
// 这里用 unsafe 按字节复制，因为 `cp := orig` 会被 go vet 的 copylocks
// 检查直接拒绝编译（"assignment copies lock value"）。
// 也就是说：这个错误在真实项目里根本提交不上去 —— 正好说明
// "给锁加 noCopy 标记 + vet 检查"是一套有效的防线。
func copiedMutex() (copyLooksLocked bool, originalStillLocked bool) {
	var orig sync.Mutex
	orig.Lock()

	// 按字节复制，模拟"不小心把锁复制了一份"
	var cp sync.Mutex
	*(*[1]sync.Mutex)(unsafe.Pointer(&cp)) = *(*[1]sync.Mutex)(unsafe.Pointer(&orig))

	s, ok := stateOf(&cp)
	if !ok {
		return false, false
	}
	copyLooksLocked = s&mutexLocked != 0

	cp.Unlock() // 解锁的是副本
	s2, _ := stateOf(&orig)
	originalStillLocked = s2&mutexLocked != 0 // 原锁还是锁着的，谁也解不开
	return
}

// ---------- 5. RWMutex ----------

// rwReadParallel 证明多个读者可以真正并行（写者不行）。
func rwReadParallel(readers int) int {
	var mu sync.RWMutex
	mu.RLock() // 主 goroutine 先拿读锁

	var concurrent int64
	var peak int64
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.RLock() // 与主 goroutine 的读锁并存
			cur := atomic.AddInt64(&concurrent, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&concurrent, -1)
			mu.RUnlock()
		}()
	}
	wg.Wait()
	mu.RUnlock()
	return int(atomic.LoadInt64(&peak))
}

// rwWriterBlocksNewReaders 演示写者到达后，新读者会被挡住，
// 防止写者被源源不断的读者饿死。
func rwWriterBlocksNewReaders() (writerWaited bool, newReaderBlocked bool) {
	var mu sync.RWMutex

	// 1) 一个读者持有读锁
	readerDone := make(chan struct{})
	release := make(chan struct{})
	go func() {
		mu.RLock()
		<-release
		mu.RUnlock()
		close(readerDone)
	}()
	time.Sleep(10 * time.Millisecond)

	// 2) 写者请求写锁 -> 排队等待当前读者
	writerAcquired := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		mu.Lock()
		writerAcquired <- time.Since(start)
		mu.Unlock()
	}()
	time.Sleep(20 * time.Millisecond)

	// 3) 写者在等的时候，新读者也会被挡住（这就是防写饥饿的机制）
	newReaderDone := make(chan struct{})
	go func() {
		mu.RLock()
		mu.RUnlock()
		close(newReaderDone)
	}()

	select {
	case <-newReaderDone:
		newReaderBlocked = false
	case <-time.After(30 * time.Millisecond):
		newReaderBlocked = true
	}

	close(release) // 放行原读者

	select {
	case d := <-writerAcquired:
		writerWaited = d > 10*time.Millisecond
	case <-time.After(time.Second):
	}
	<-readerDone
	<-newReaderDone
	return
}

// ---------- 6. 性能对比 ----------

// 这组基准要说明的是"原语本身的开销"，所以刻意避免所有 goroutine
// 争抢同一个变量 —— 那样测出来的主要是缓存行争用（cache line bouncing），
// 会把 Mutex 和 atomic 的差距淹没掉。这里每个 goroutine 用自己的计数器。
func Run() {
	fmt.Println("=== Q18: Mutex 深挖 —— 位域、两种模式与公平性 ===")

	// 1. state 位域
	fmt.Println("--- 1. state 是一个 int32 位域（4 个信息塞进一个字段）---")
	fmt.Printf("   Mutex 大小 = %d 字节（state int32 + sema uint32）\n", unsafe.Sizeof(sync.Mutex{}))
	fmt.Println("   位布局: [b31..b3 waiter 数][b2 starving][b1 woken][b0 locked]")

	var mu sync.Mutex
	s, _ := stateOf(&mu)
	fmt.Printf("   未加锁: %s\n", decodeState(s))

	mu.Lock()
	s, _ = stateOf(&mu)
	fmt.Printf("   加锁后: %s  <- locked 位 = 1\n", decodeState(s))

	// 让若干 goroutine 排队，观察 waiter 计数
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); mu.Lock(); mu.Unlock() }()
	}
	time.Sleep(50 * time.Millisecond)
	s, _ = stateOf(&mu)
	fmt.Printf("   5 个等待者: %s  <- waiters = state >> 3\n", decodeState(s))
	mu.Unlock()
	wg.Wait()
	fmt.Println("   为什么要位域？因为 Lock 的快速路径要\"一次 CAS 同时判断并修改\"，")
	fmt.Println("   拆成 4 个字段就没法原子操作了")

	// 2. 两种模式
	fmt.Println("--- 2. 正常模式 vs 饥饿模式 ---")
	times := lockWaitTimes(20)
	var maxWait, sum time.Duration
	for _, d := range times {
		sum += d
		if d > maxWait {
			maxWait = d
		}
	}
	fmt.Printf("   20 个竞争者的等待时间: 平均 %v, 最大 %v\n", sum/time.Duration(len(times)), maxWait)
	fmt.Println("   正常模式：被唤醒的 G 要和新来的 G 一起抢锁，新来的因为正在 CPU 上，")
	fmt.Println("             往往更容易抢到 -> 存在不公平，极端下会饥饿")
	fmt.Println("   饥饿模式：等待超过 1ms 就切换，锁直接交给队首，新来的 G 不抢也不自旋，")
	fmt.Println("             直接排队尾 -> 保证 FIFO，代价是吞吐下降")
	fmt.Println("   切换回去的条件：队首等待时间 < 1ms，或队首已拿到锁")

	// 3. 不可重入
	fmt.Println("--- 3. Mutex 不可重入 ---")
	fmt.Printf("   同一 goroutine 连续两次 Lock 会死锁: %v\n", reentrantDeadlock())
	fmt.Println("   设计取舍：可重入锁要记录持有者 goroutine id 和重入次数，")
	fmt.Println("            这会让 Lock/Unlock 都变慢，Go 选择了性能优先")
	fmt.Println("   实践：把临界区拆小，不要在持锁时调用可能再次加锁的函数")

	// 4. 复制锁
	fmt.Println("--- 4. 复制已使用的锁是致命错误 ---")
	copyLocked, origLocked := copiedMutex()
	fmt.Printf("   复制后副本的 state 也显示 locked=%v；Unlock 副本后原锁仍 locked=%v\n",
		copyLocked, origLocked)
	fmt.Println("   所以 sync.Mutex 内含 noCopy 标记，go vet 会拦下锁值拷贝；")
	fmt.Println("   含锁的结构体不要按值传递、不要放进 map 值、不要 return 副本")

	// 5. RWMutex
	fmt.Println("--- 5. RWMutex：读并行、写独占、防写饥饿 ---")
	fmt.Printf("   20 个读者同时在读锁内: 峰值并行数 = %d（读锁之间不互斥）\n", rwReadParallel(20))
	waited, blocked := rwWriterBlocksNewReaders()
	fmt.Printf("   写者被现有读者挡住: %v；写者等待期间新读者被挡: %v\n", waited, blocked)
	fmt.Println("   RWMutex 内部：readerCount 为正表示读锁数量；")
	fmt.Println("                 写者把它减掉 rwmutexMaxReaders 变成负数，表示\"有写者在等/持锁\"")
	fmt.Println("   代价：RWMutex 比 Mutex 重得多（维护 sema 和计数），只在读远多于写时才划算")

	// 6. 性能
	fmt.Println("--- 6. 同步原语的开销对比（并行基准）---")
	for _, bc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"Mutex 串行自增", func(b *testing.B) {
			const n = 100
			var mutexes [n]sync.Mutex
			var counters [n]int64
			for i := 0; i < b.N; i++ {
				idx := i % n
				mutexes[idx].Lock()
				counters[idx]++
				mutexes[idx].Unlock()
			}
		}},
		{"atomic CAS    ", func(b *testing.B) {
			const n = 100
			var counters [n]int64
			for i := 0; i < b.N; i++ {
				idx := i % n
				atomic.AddInt64(&counters[idx], 1)
			}
		}},
		{"RWMutex 只读  ", func(b *testing.B) {
			const n = 100
			var rws [n]sync.RWMutex
			for i := 0; i < b.N; i++ {
				idx := i % n
				rws[idx].RLock()
				rws[idx].RUnlock()
			}
		}},
	} {
		res := testing.Benchmark(bc.fn)
		fmt.Printf("   %s : %12d ns/op\n", bc.name, res.NsPerOp())
	}
	fmt.Println("   说明：单 goroutine + 100 把独立的锁，没有争用，测的是「快速路径本身」的开销；")
	fmt.Println("        如果所有 goroutine 抢同一个变量，测出来的主要是缓存行争用，不是原语差异")
	fmt.Println("   结论：atomic 明显快于 Mutex（无锁 CAS），")
	fmt.Println("        但 atomic 只能保护单个字段，多字段一致性还是得用锁")

	// 7. 选型
	fmt.Println("--- 7. 选型清单 ---")
	fmt.Println("   单个计数器/标志位         -> atomic")
	fmt.Println("   保护一段临界区（多字段）  -> sync.Mutex")
	fmt.Println("   读远多于写的共享数据      -> sync.RWMutex")
	fmt.Println("   只执行一次的初始化        -> sync.Once（Q17）")
	fmt.Println("   读多写少且 key 固定的缓存 -> sync.Map（Q17）")
	fmt.Println("   高频小对象复用            -> sync.Pool（Q7）")
}
