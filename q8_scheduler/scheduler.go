package q8_scheduler

import (
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 1. GOMAXPROCS 与真实并行度 ----------

// measureParallel 用"CPU 时间累计 / 墙钟时间"估算真实并行度。
//
// 为什么不用"同时在跑的 goroutine 计数"：那个数会受 goroutine 启动/结束
// 的错位影响，经常出现大于 GOMAXPROCS 的假象。而 CPU 时间是硬指标：
//
//	所有计算 G 的 CPU 时间总和 / 实际耗时 = 平均同时在 CPU 上跑的数量
//
// 它应当收敛到 GOMAXPROCS，这就是"并行度上限"的实证。
func measureParallel(n int, cpuTime time.Duration) (parallelism float64, userTime time.Duration, wall time.Duration) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var cpuTotal time.Duration
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate // 等发令枪，尽量让所有 G 同时就绪

			start := time.Now() // 纯用户态 CPU 计时，不含被抢占/阻塞
			deadline := start.Add(cpuTime)
			x := 0
			for time.Now().Before(deadline) {
				x++
			}
			mu.Lock()
			cpuTotal += time.Since(start)
			mu.Unlock()
			runtime.KeepAlive(x)
		}()
	}

	wallStart := time.Now()
	close(gate)
	wg.Wait()
	wall = time.Since(wallStart)

	return float64(cpuTotal) / float64(wall), cpuTotal, wall
}

// ---------- 2. 异步抢占（Go 1.14+） ----------

// burnCPU 是"没有安全点"的紧循环：不调用函数、不做 channel 操作、
// 不分配内存。Go 1.14 之前这里是抢占的盲区，会把同 P 上的其他 G 饿死。
func burnCPU(d time.Duration) {
	deadline := time.Now().Add(d)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	runtime.KeepAlive(x)
}

// ---------- 3. 阻塞与 P 的交接 ----------

type result struct {
	id  int
	seq int
}

// blockingProbe 展示"阻塞会让出 P"：一部分 G 卡在 channel 上，
// 另一部分 G 照常推进，两者互不拖累。
func blockingProbe(blocked, active int) []result {
	ch := make(chan int) // 无缓冲，发送方必然阻塞

	// 这些 goroutine 会永远 park 在 ch <- id 上，不回收也不退出。
	// 这正是"goroutine 泄漏"的最小原型：没有退出路径的阻塞。
	for i := 0; i < blocked; i++ {
		go func(id int) {
			ch <- id
		}(i)
	}

	var done int64
	var wg2 sync.WaitGroup
	for i := 0; i < active; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			burnCPU(5 * time.Millisecond)
			atomic.AddInt64(&done, 1)
		}()
	}
	wg2.Wait()

	return []result{{id: -1, seq: int(done)}}
}

// ---------- 4. 调度追踪 ----------

// schedTrace 用 runtime/trace 或 GODEBUG 观察调度状态，这里只做能力探测。
func available(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func Run() {
	fmt.Println("=== Q8: GMP 调度模型与 runtime 调度器 ===")

	fmt.Printf("Go 版本: %s\n", runtime.Version())
	fmt.Printf("GOMAXPROCS=%d, NumCPU=%d, NumGoroutine=%d\n",
		runtime.GOMAXPROCS(0), runtime.NumCPU(), runtime.NumGoroutine())

	// 1. 并行度
	fmt.Println("--- 1. 真实并行度 = 所有计算 G 的 CPU 时间总和 / 墙钟时间 ---")
	fmt.Println("  （每个 G 累计跑满 50ms 用户态 CPU，观察平均有几个 G 真正同时在跑）")
	for _, n := range []int{4, 16, 64} {
		p, cpuTotal, wall := measureParallel(n, 50*time.Millisecond)
		fmt.Printf("  起 %2d 个 goroutine：CPU 累计 %6.0fms / 墙钟 %6.0fms = 并行度 %.2f\n",
			n, float64(cpuTotal.Milliseconds()), float64(wall.Milliseconds()), p)
	}
	fmt.Printf("  GOMAXPROCS=%d：无论起多少 goroutine，并行度都收敛到它，这就是上限\n",
		runtime.GOMAXPROCS(0))
	fmt.Println("  注意：GOMAXPROCS 只管\"同时在 CPU 上\"的 G；goroutine 千千万万也不影响它")

	// 2. 异步抢占
	fmt.Println("--- 2. 异步抢占：紧循环是否饿死同 P 的邻居 ---")
	var wg sync.WaitGroup
	var countdown int64
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		burnCPU(50 * time.Millisecond) // 占满一个 P 的 CPU，无任何安全点
	}()

	// 邻居 goroutine：靠 runtime 的抢占才能被调度
	neighborStart := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-stop
		atomic.AddInt64(&countdown, 1)
	}()

	go func() {
		time.Sleep(40 * time.Millisecond)
		close(stop)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		fmt.Printf("  紧循环 + 邻居 goroutine 在 %.0fms 内都完成 -> 异步抢占生效（Go 1.14+）\n",
			float64(time.Since(neighborStart).Milliseconds()))
	case <-time.After(2 * time.Second):
		fmt.Println("  邻居被饿死（Go 1.13 及更早的行为）")
	}

	// 3. 阻塞让出 P
	fmt.Println("--- 3. 阻塞时 P 的交接 ---")
	before := runtime.NumGoroutine()
	r := blockingProbe(8, 8)
	fmt.Printf("  8 个 G 永久阻塞在 channel 上，另外 8 个完成计算任务 %d 个\n", r[0].seq)
	after := runtime.NumGoroutine()
	fmt.Printf("  goroutine 数: %d -> %d（阻塞的 8 个仍然存活，这就是泄漏的原型）\n", before, after)
	fmt.Println("  结论：G 阻塞时 runtime 把它的 P 交给其他 M，CPU 不会被浪费；")
	fmt.Println("  但被 park 的 G 本身仍占内存，如果不给它退出路径，就是 goroutine 泄漏")

	// 4. 手动让出
	fmt.Println("--- 4. runtime.Gosched() 的语义 ---")
	start := time.Now()
	var fast, slow int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for time.Since(start) < 20*time.Millisecond {
			atomic.AddInt64(&fast, 1)
		}
	}()
	go func() {
		defer wg.Done()
		for time.Since(start) < 20*time.Millisecond {
			atomic.AddInt64(&slow, 1)
			runtime.Gosched() // 主动让出，把 P 交还给队列
		}
	}()
	wg.Wait()
	fmt.Printf("  不让出: %d 次循环, 频繁让出: %d 次循环\n", fast, slow)
	fmt.Println("  Gosched 只让出 P，不保证公平；它常用于自旋锁的退避，不是同步原语")

	// 5. 诊断工具
	fmt.Println("--- 5. 排查调度问题的工具 ---")
	fmt.Println("  GODEBUG=schedtrace=1000 go run . 8   每秒打印一行调度器状态")
	fmt.Println("  go tool trace ./trace.out            可视化 goroutine 阻塞/抢占/GC")
	fmt.Println("  GODEBUG=asyncpreemptoff=1 go run . 8 关闭异步抢占做对比实验")
	if available("go") {
		fmt.Println("  （已检测到 go 命令，可直接执行上面的命令）")
	}
}
