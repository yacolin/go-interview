package q17_sync

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- 1. sync.Once ----------

type heavyResource struct {
	Name string
	once sync.Once
	init int64 // 记录实际初始化次数
}

// Get 用 Once 保证初始化只执行一次，且**并发安全 + 内存可见**。
func (r *heavyResource) Get() string {
	r.once.Do(func() {
		// 模拟昂贵的初始化：建连接池、读配置、加载模型
		atomic.AddInt64(&r.init, 1)
		time.Sleep(10 * time.Millisecond)
		r.Name = "initialized"
	})
	return r.Name
}

// naiveOnce 是错误示范：check-then-act 在并发下会执行多次。
func naiveOnce() int64 {
	var initialized bool
	var executeCount int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !initialized { // 竞态：多个 goroutine 可能同时看到 false
				initialized = true
				atomic.AddInt64(&executeCount, 1)
			}
		}()
	}
	wg.Wait()
	return executeCount
}

// ---------- 2. OnceFunc / OnceValue（Go 1.21+）----------

var onceValueInitCalls int64

func expensiveConfig() int {
	atomic.AddInt64(&onceValueInitCalls, 1)
	return 42
}

// ---------- 3. sync.WaitGroup ----------

func waitGroupCorrect(n int) int {
	var wg sync.WaitGroup
	var total int64
	for i := 0; i < n; i++ {
		wg.Add(1) // 必须在 go 之前 Add
		go func(v int) {
			defer wg.Done()
			atomic.AddInt64(&total, int64(v))
		}(i)
	}
	wg.Wait()
	return int(total)
}

// waitGroupEarlyReturn 复现"Add 写在 goroutine 里"的经典 bug：
// Wait 可能在任何一个 Add 之前就返回，导致结果不完整。
//
// 这里用 reflect 动态调用 Add，是因为 go vet 的 copylocks 检查会直接
// 拒绝编译这种写法（"WaitGroup.Add called from inside new goroutine"）——
// 这恰好说明静态检查能抓住它。真实代码里请永远把 Add 放在 go 之前。
func waitGroupEarlyReturn() (result int64, early bool) {
	var wg sync.WaitGroup
	var total int64
	const n = 100

	addMethod := reflect.ValueOf(&wg).MethodByName("Add")

	for i := 0; i < n; i++ {
		go func() {
			addMethod.Call([]reflect.Value{reflect.ValueOf(1)}) // ✗ 错误示范
			defer wg.Done()
			atomic.AddInt64(&total, 1)
		}()
	}

	wg.Wait() // 此时计数器很可能还是 0，直接返回
	// 给还没跑完的 goroutine 一点时间，用来证明"Wait 确实提前返回了"
	time.Sleep(50 * time.Millisecond)
	final := atomic.LoadInt64(&total)
	return final, final != n
}

// waitGroupReuse 演示 WaitGroup 的正确复用方式：等一批全部结束再开下一批。
func waitGroupReuse() []int {
	var wg sync.WaitGroup
	var snapshots []int
	var mu sync.Mutex

	for batch := 1; batch <= 3; batch++ {
		wg.Add(10)
		for i := 0; i < 10; i++ {
			go func() { defer wg.Done() }()
		}
		wg.Wait() // Wait 返回后可以安全地再次 Add
		mu.Lock()
		snapshots = append(snapshots, batch)
		mu.Unlock()
	}
	return snapshots
}

// ---------- 4. sync.Cond ----------

// condQueue 用 Cond 实现"有数据才唤醒消费者"，避免轮询。
type condQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []int
	done  bool
}

func newCondQueue() *condQueue {
	q := &condQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *condQueue) Push(v int) {
	q.mu.Lock()
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.cond.Signal() // 唤醒一个等待者
}

func (q *condQueue) Close() {
	q.mu.Lock()
	q.done = true
	q.mu.Unlock()
	q.cond.Broadcast() // 唤醒所有等待者
}

func (q *condQueue) Pop() (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// 必须用 for 而不是 if：存在虚假唤醒，且被唤醒后条件可能又被抢走
	for len(q.items) == 0 && !q.done {
		q.cond.Wait() // Wait 内部：释放锁 -> 阻塞 -> 被唤醒后重新加锁
	}
	if len(q.items) == 0 {
		return 0, false
	}
	v := q.items[0]
	q.items = q.items[1:]
	return v, true
}

// condLostWakeup 复现"lost wakeup"：Signal 在 Wait 之前发生，信号就丢了。
func condLostWakeup() bool {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)

	// Signal 必须在持锁状态下调用（否则本身就违反 Cond 的使用约定）
	mu.Lock()
	mu.Unlock()
	cond.Signal() // 此刻还没有任何等待者，信号直接丢失

	done := make(chan struct{})
	go func() {
		mu.Lock()
		defer mu.Unlock()
		// 用 if 而不是 for，且没检查真正的条件
		cond.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true // 意外地没丢
	case <-time.After(50 * time.Millisecond):
		return false // 丢了：goroutine 永远等下去
	}
}

// condBroadcastDemo：Broadcast 一次唤醒全部，Signal 只唤醒一个。
func condBroadcastDemo(kind string, waiters int) int {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	var woken int64
	started := make(chan struct{}, waiters)

	for i := 0; i < waiters; i++ {
		go func() {
			mu.Lock()
			started <- struct{}{}
			cond.Wait()
			mu.Unlock()
			atomic.AddInt64(&woken, 1)
		}()
	}
	for i := 0; i < waiters; i++ {
		<-started
	}
	time.Sleep(30 * time.Millisecond) // 确保都进入 Wait

	mu.Lock()
	if kind == "Broadcast" {
		cond.Broadcast()
	} else {
		cond.Signal()
	}
	mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	count := int(atomic.LoadInt64(&woken))
	// 收拾残局：把剩余的唤醒，避免 goroutine 泄漏
	mu.Lock()
	cond.Broadcast()
	mu.Unlock()
	return count
}

// ---------- 5. sync.Map ----------

// syncMapCounter 演示 Q4 里提过的坑：存 int64 无法原地自增，
// 必须存指针 + 原子操作，且**始终使用 LoadOrStore 的返回值**。
type syncMapCounter struct {
	m sync.Map
}

func (c *syncMapCounter) Inc(key string) {
	// 正确：无论本次是否真的存进去，返回的都是 map 里实际存在的那份数据
	v, _ := c.m.LoadOrStore(key, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (c *syncMapCounter) Get(key string) int64 {
	v, ok := c.m.Load(key)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

func (c *syncMapCounter) Len() int {
	n := 0
	c.m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// mutexMapCounter 用 Mutex + 普通 map 做同样的事，用于对比。
type mutexMapCounter struct {
	mu sync.Mutex
	m  map[string]int64
}

func newMutexMapCounter() *mutexMapCounter {
	return &mutexMapCounter{m: make(map[string]int64)}
}

func (c *mutexMapCounter) Inc(key string) {
	c.mu.Lock()
	c.m[key]++
	c.mu.Unlock()
}

func (c *mutexMapCounter) Get(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

// ---------- 基准测试：sync.Map vs Mutex+map ----------

// 两个场景刻意区分开：
//
//	read heavy  —— key 集合固定，所有人都读同一个 key（缓存命中路径）
//	write heavy —— 不断写入不同的 key，dirty map 反复提升
func Run() {
	fmt.Println("=== Q17: sync 全家桶 —— Once / WaitGroup / Cond / Map ===")

	// 1. sync.Once
	fmt.Println("--- 1. sync.Once：并发下只执行一次，且有内存屏障 ---")
	r := &heavyResource{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Get() }()
	}
	wg.Wait()
	fmt.Printf("   20 个 goroutine 并发调用 Get()，实际初始化 %d 次，结果 %q\n",
		atomic.LoadInt64(&r.init), r.Name)
	fmt.Printf("   朴素的 check-then-act（错误示范）实际执行 %d/50 次 -> 竞态\n", naiveOnce())
	fmt.Println("   Once 比\"布尔标志\"多了内存屏障：`if done { return }` 这种老写法即使")
	fmt.Println("   只执行一次，也不能保证其他 goroutine 看到初始化后的数据")

	// 2. OnceValue
	fmt.Println("--- 2. OnceFunc / OnceValue / OnceValues（Go 1.21+）---")
	config := sync.OnceValue(expensiveConfig)
	var wg2 sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg2.Add(1)
		go func() { defer wg2.Done(); _ = config() }()
	}
	wg2.Wait()
	fmt.Printf("   10 次并发 OnceValue，廉价函数实际执行 %d 次，值=%d\n",
		atomic.LoadInt64(&onceValueInitCalls), config())
	fmt.Println("   注意：OnceValue 会缓存结果（包括 panic 造成的失败），")
	fmt.Println("        初始化失败需要重试时不要用它，用 sync.Once + 错误标记")

	// 3. WaitGroup
	fmt.Println("--- 3. sync.WaitGroup：Add 的位置决定正确性 ---")
	fmt.Printf("   Add 在 go 之前（正确）: 求和 0..99 = %d\n", waitGroupCorrect(100))
	final, early := waitGroupEarlyReturn()
	fmt.Printf("   Add 在 goroutine 内（错误）: Wait 返回时只完成了 %d/100，提前返回=%v\n", final, early)
	fmt.Println("   规则：Add 必须在 Wait 之前、且在启动 goroutine 之前调用")
	fmt.Println("   另一个坑：计数器减到负数会 panic \"negative WaitGroup counter\"")
	fmt.Printf("   复用：等一批结束再开下一批 -> %v\n", waitGroupReuse())

	// 4. sync.Cond
	fmt.Println("--- 4. sync.Cond：等待条件成立，避免轮询 ---")
	q := newCondQueue()
	var condWG sync.WaitGroup
	var sum int64
	for i := 0; i < 3; i++ {
		condWG.Add(1)
		go func() {
			defer condWG.Done()
			for {
				v, ok := q.Pop()
				if !ok {
					return
				}
				atomic.AddInt64(&sum, int64(v))
			}
		}()
	}
	for i := 1; i <= 10; i++ {
		q.Push(i)
	}
	q.Close()
	condWG.Wait()
	fmt.Printf("   3 个消费者从 Cond 队列取完 1..10，和=%d\n", sum)
	fmt.Printf("   Signal 唤醒 1 个 / Broadcast 唤醒全部：Signal -> %d，Broadcast -> %d\n",
		condBroadcastDemo("Signal", 5), condBroadcastDemo("Broadcast", 5))
	fmt.Printf("   lost wakeup（Signal 先于 Wait）导致消费者永久卡住: %v\n", !condLostWakeup())
	fmt.Println("   使用要点：Wait 必须在持锁状态下调用（它内部会先释放锁），")
	fmt.Println("            且必须写在 for 循环里检查条件 —— 存在虚假唤醒，")

	// 5. sync.Map
	fmt.Println("--- 5. sync.Map：read/dirty 双 map 的读写路径 ---")
	sc := &syncMapCounter{}
	var wg3 sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg3.Add(1)
		go func() { defer wg3.Done(); sc.Inc("hits") }()
	}
	wg3.Wait()
	fmt.Printf("   1000 个 goroutine 并发自增: %d（key 数 %d）\n", sc.Get("hits"), sc.Len())
	fmt.Println("   内部机制：read（只读，原子读无需加锁） + dirty（可写，需要加锁）")
	fmt.Println("            miss 次数达到阈值时把 dirty 提升为 read；")
	fmt.Println("            连续 miss 会触发慢路径并给 read 打上 amended 标记")
	fmt.Println("   适用：key 集合基本稳定、读远多于写（缓存、注册表）")
	fmt.Println("   不适用：写多、需要 len()、需要遍历统计 —— 用 Mutex+map 更好")

	// 6. 性能对比
	fmt.Println("--- 6. sync.Map vs Mutex+map（并行基准） ---")
	for _, bc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"读多写少 sync.Map", func(b *testing.B) {
			preload()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = syncMapBench.Get("key-5000")
				}
			})
		}},
		{"读多写少 Mutex  ", func(b *testing.B) {
			preload()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = mutexMapBench.Get("key-5000")
				}
			})
		}},
		{"写多     sync.Map", func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					i++
					syncMapBench.Inc(fmt.Sprintf("w-%d", i%10000))
				}
			})
		}},
		{"写多     Mutex  ", func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					i++
					mutexMapBench.Inc(fmt.Sprintf("w-%d", i%10000))
				}
			})
		}},
	} {
		res := testing.Benchmark(bc.fn)
		fmt.Printf("   %s : %12d ns/op\n", bc.name, res.NsPerOp())
	}
	fmt.Println("   结论：读多写少时 sync.Map 明显占优；写多时它反而更慢（要维护双 map）")
	fmt.Println("   所以\"并发场景就该用 sync.Map\"是错的 —— 先问读写比例")
}

var (
	syncMapBench  = &syncMapCounter{}
	mutexMapBench = newMutexMapCounter()
)

func preload() {
	for i := 0; i < 10000; i++ {
		k := fmt.Sprintf("key-%d", i)
		syncMapBench.Inc(k)
		mutexMapBench.Inc(k)
	}
}
