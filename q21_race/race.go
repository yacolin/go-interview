package q21_race

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- 1. 数据竞争：三种真实形态 ----------

// counterRace 是最直观的竞争：读-改-写 不是原子操作。
func counterRace(n int) int64 {
	var counter int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter++ // 编译成 LOAD / ADD / STORE 三条指令，中间可被打断
		}()
	}
	wg.Wait()
	return counter
}

// mapRace 演示并发读写 map：会被 runtime 硬检测并 fatal。
//
// 要稳定复现需要一点技巧：让所有 goroutine 先卡在同一个起点，
// 再各自持续写/读同一个 map，制造出足够密的交叠窗口。
// 冲突不保证 100% 命中（runtime 的检测是采样式的），但概率很高。
func mapRace(n int) {
	m := make(map[int]int)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				m[j%32] = v // 写
			}
		}(i)

		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			<-start
			for j := 0; j < 200; j++ {
				_ = m[j%32] // 读
			}
		}(i)
	}

	close(start)
	wg.Wait()
}

// partialLockRace 演示"部分字段加了锁"的隐蔽竞争：
// 同一个结构体的不同字段被不同 goroutine 写，仍然算竞争。
type stats struct {
	mu   sync.Mutex
	hits int64 // 受 mu 保护
	miss int64 // 漏了保护！
}

func (s *stats) hit() {
	s.mu.Lock()
	s.hits++
	s.mu.Unlock()
}

func (s *stats) missOnce() {
	s.miss++ // ✗ 没有加锁
}

func partialLockRace(n int) (hits, miss int64) {
	s := &stats{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.hit() }()
		wg.Add(1)
		go func() { defer wg.Done(); s.missOnce() }()
	}
	wg.Wait()
	return s.hits, s.miss
}

// ---------- 2. 正确修法 ----------

// fixAtomic 用 atomic 修（适合单字段计数）。
func fixAtomic(n int) int64 {
	var counter int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt64(&counter, 1)
		}()
	}
	wg.Wait()
	return atomic.LoadInt64(&counter)
}

// fixMutex 用 Mutex 修（适合多字段需要保持一致）。
func fixMutex(n int) int64 {
	var mu sync.Mutex
	var counter int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			counter++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return counter
}

// fixChannel 用 channel 修（把共享状态收敛到一个 goroutine，即 CSP 思路）。
func fixChannel(n int) int64 {
	inc := make(chan struct{})
	done := make(chan int64)

	go func() {
		var counter int64
		for range inc {
			counter++
		}
		done <- counter
	}()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); inc <- struct{}{} }()
	}
	wg.Wait()
	close(inc)
	return <-done
}

// ---------- 3. 内存模型：happens-before ----------

// publishWithChannel：channel 的收发建立 happens-before，
// 发送操作 happens before 对应的接收操作完成。
func publishWithChannel() string {
	var data string
	done := make(chan struct{})

	go func() {
		data = "payload" // 写在发送之前，对接收方可见
		close(done)      // close 也是一种"发送"（广播）
	}()

	<-done // 接收 happens after 发送
	return data
}

// publishWithOnce：sync.Once.Do 返回后，一定能看到 f 内的写入。
var (
	onceInit sync.Once
	cfgValue string
)

func publishWithOnce() string {
	onceInit.Do(func() { cfgValue = "from-once" })
	return cfgValue
}

// workerQueue 展示"用 channel 传递所有权"如何天然避免竞争：
// 同一时刻只有一个 goroutine 持有数据，交接点就是同步点。
func workerQueue(jobs int) int64 {
	ch := make(chan int64)
	var total int64

	go func() {
		for i := int64(1); i <= int64(jobs); i++ {
			ch <- i
		}
		close(ch)
	}()

	for v := range ch { // 单消费者独占累加，无需加锁
		total += v
	}
	return total
}

// ---------- 4. check-then-act ----------

type lazy struct {
	once sync.Once
	v    *int
	n    int64
}

// getRace 是错误实现：两个 goroutine 可能同时通过 if 判断。
func (l *lazy) getRace() *int {
	if l.v == nil { // 检查
		time.Sleep(2 * time.Millisecond)
		x := 1
		l.v = &x // 行动（可能被多个 goroutine 同时执行）
		atomic.AddInt64(&l.n, 1)
	}
	return l.v
}

// getOnce 是正确实现。
func (l *lazy) getOnce() *int {
	l.once.Do(func() {
		x := 1
		l.v = &x
		atomic.AddInt64(&l.n, 1)
	})
	return l.v
}

// ---------- 5. 子进程里触发 fatal 场景 ----------

// ---------- 5. 子进程里触发 fatal 场景 ----------

// runSelfWithRetry 会重试几次：runtime 对并发 map 的检测带有采样性质，
// 极少数情况下子进程可能"侥幸"跑完而不触发 fatal，这时换个进程再来一次。
func runSelfWithRetry(scenario string, tries int) (string, int) {
	var out string
	var code int
	for i := 0; i < tries; i++ {
		out, code = runSelf(scenario)
		if strings.Contains(out, "fatal error:") {
			return out, code
		}
	}
	return out, code
}

// runSelf 在子进程里执行"会让整个进程退出"的场景。
//
// 为什么必须开子进程：并发写 map 触发的是 fatal error，
// 整个进程会直接退出，同进程里根本没有机会打印任何结果。
//
// 两种运行方式要分别处理：
//
//	go test  —— os.Executable() 就是测试二进制，直接带 -test.run 复用
//	go run   —— os.Executable() 是临时构建的 main 二进制，
//	            再传 -test.run 只会被 main 当成未知题号，所以改用 go run 跑测试文件
func runSelf(scenario string) (string, int) {
	if flag.Lookup("test.run") != nil {
		exe, err := os.Executable()
		if err != nil {
			return "", -1
		}
		return execScenario(scenario, exe, "-test.run=TestFatalScenario")
	}

	if _, err := findRepoRoot(); err != nil {
		return "无法定位仓库根目录（缺 go.mod）", -1
	}
	// go run 不能执行 *_test.go，所以这里走 go test。
	// Go 1.24+ 允许没有 _test.go 的包执行 go test，但真跑测试仍然需要它。
	return execScenario(scenario, "go", "test", "-run", "TestFatalScenario", "./q21_race")
}

// execScenario 用 RACE_SCENARIO 传递场景名（而不是命令行参数，
// 因为多余的参数会被 go test 当成包名，导致命令直接失败）。
func execScenario(scenario, name string, args ...string) (string, int) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "RACE_SCENARIO="+scenario)
	if name == "go" {
		cmd.Dir = mustRepoRoot()
	}
	out, err := cmd.CombinedOutput()

	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	return string(out), code
}

func mustRepoRoot() string {
	root, err := findRepoRoot()
	if err != nil {
		return "."
	}
	return root
}

// findRepoRoot 从当前工作目录向上找含 go.mod 的目录。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

// extractFatal 从子进程输出里挑出关键行（fatal error / panic / go run 的退出提示）。
func extractFatal(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
			return line
		}
	}
	// go test 的输出会带 "exit status 2"，这类噪音要跳过
	_ = strings.TrimSpace
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "(无输出)"
}

func Run() {
	fmt.Println("=== Q21: 数据竞争、-race 与 Go 内存模型 ===")

	fmt.Printf("GOMAXPROCS=%d, NumGoroutine=%d\n", runtime.GOMAXPROCS(0), runtime.NumGoroutine())
	fmt.Println("用 go test -race ./q21_race 打开 race detector（本包有 TestRaceDemo）")

	// 1. 竞争的三种形态
	fmt.Println("--- 1. 数据竞争长什么样 ---")
	const n = 1000
	got := counterRace(n)
	fmt.Printf("   counter++ 并发 %d 次，期望 %d，实际 %d", n, n, got)
	if got < n {
		fmt.Printf("  <- 丢失了 %d 次更新\n", n-got)
	} else {
		fmt.Printf("  （这次侥幸没丢 —— 竞争是概率性的，别指望每次都能复现）\n")
	}
	fmt.Println("   原因：counter++ = LOAD + ADD + STORE 三条指令，两个 goroutine 可能读到同一个旧值")

	out, code := runSelfWithRetry("maprace", 3)
	fmt.Printf("   并发读写 map：子进程退出码=%d\n", code)
	fmt.Printf("      报错: %s\n", extractFatal(out))
	fmt.Println("      这是 runtime 的硬检测（fatal error，recover 抓不住），")
	fmt.Println("      属于\"最好的情况\"——至少不会静默损坏数据")

	hits, miss := partialLockRace(500)
	fmt.Printf("   更隐蔽的一种（结构体部分字段忘了加锁）: hits=%d, miss=%d（期望 500）\n", hits, miss)
	if miss < 500 {
		fmt.Printf("      无锁字段丢了 %d 次更新，而有锁字段一次不少\n", 500-miss)
	}

	// 2. 正确修法
	fmt.Println("--- 2. 三种修法（各并发 1000 次）---")
	fmt.Printf("   atomic  : %d\n", fixAtomic(n))
	fmt.Printf("   Mutex   : %d\n", fixMutex(n))
	fmt.Printf("   channel : %d\n", fixChannel(n))
	fmt.Println("   选型：单字段计数用 atomic；多字段要保持一致用 Mutex；")
	fmt.Println("        把状态收敛到一个 goroutine（CSP）用 channel")

	// 3. 内存模型
	fmt.Println("--- 3. 内存模型：可见性的依据是 happens-before ---")
	fmt.Printf("   channel 建立 hb   : %q\n", publishWithChannel())
	fmt.Printf("   sync.Once 建立 hb : %q\n", publishWithOnce())
	fmt.Println("   Go 只承诺\"存在 happens-before 关系时\"的写入可见；")
	fmt.Println("   没有同步原语，读到旧值、指令被重排都是合法的（数据竞争属于未定义行为）")
	fmt.Println("   建立 hb 的手段：channel 收发、Mutex 的 Unlock->Lock、")
	fmt.Println("                   atomic 操作、Once/WaitGroup、goroutine 的启动与 Wait")

	// 4. 所有权交接
	fmt.Println("--- 4. 用 channel 交接所有权（最不容易写错的模型）---")
	fmt.Printf("   生产者 -> channel -> 单消费者累加 1..100 = %d\n", workerQueue(100))
	fmt.Println("   共享内存的难点是\"谁在什么时候可以写\"；")
	fmt.Println("   channel 把它变成\"谁持有数据\"，交接点即同步点")

	// 5. check-then-act
	fmt.Println("--- 5. check-then-act（单例/懒加载/缓存穿透的根源）---")
	const objs = 50

	arr := make([]*lazy, objs)
	for i := range arr {
		arr[i] = &lazy{}
	}
	var wg sync.WaitGroup
	for _, l := range arr {
		wg.Add(1)
		go func(x *lazy) { defer wg.Done(); _ = x.getRace() }(l)
	}
	wg.Wait()
	var raceInit int64
	for _, l := range arr {
		raceInit += atomic.LoadInt64(&l.n)
	}

	arr2 := make([]*lazy, objs)
	for i := range arr2 {
		arr2[i] = &lazy{}
	}
	var wg2 sync.WaitGroup
	for _, l := range arr2 {
		wg2.Add(1)
		go func(x *lazy) { defer wg2.Done(); _ = x.getOnce() }(l)
	}
	wg2.Wait()
	var onceCount int64
	for _, l := range arr2 {
		onceCount += atomic.LoadInt64(&l.n)
	}
	fmt.Printf("   %d 个对象各自并发初始化：朴素 if 判断共初始化 %d 次，Once 共 %d 次\n",
		objs, raceInit, onceCount)
	fmt.Println("   期望都是 50；朴素写法超过 50 就是重复初始化了（单例场景下可能是灾难）")

	// 6. 易错点
	fmt.Println("--- 6. 几个容易记错的点 ---")
	fmt.Println("   Go 的内存模型是 C++11 风格的 happens-before，不是顺序一致性")
	fmt.Println("   data race 是未定义行为，不是\"偶尔读到旧值\"这么轻描淡写")
	fmt.Println("   但 Go 比 C++ 友好：map 并发写会被检测并 fatal，而不是静默损坏")
	fmt.Println("   -race 有 5~20 倍性能开销，且只报告\"实际发生\"的竞争（不能证明没有竞争）")

	// 7. 实践清单
	fmt.Println("--- 7. 实践清单 ---")
	fmt.Println("   1) 开发和 CI 都跑 -race（发现过竞争的人都知道它值这个开销）")
	fmt.Println("   2) 共享可变状态越少越好：优先局部变量、传值、channel 交接")
	fmt.Println("   3) 结构体里的锁要保护\"所有\"可变字段，不能只保护一部分")
	fmt.Println("   4) 不要 close 一个仍可能被发送的 channel（见 Q3/Q9）")
	fmt.Println("   5) 线上配合 pprof 的 goroutine/block/mutex profile 反查争用")

	// 8. 不同同步方案的吞吐
	fmt.Println("--- 8. 三种修法的吞吐（100 个 goroutine 自增）---")
	for _, bc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"atomic ", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = fixAtomic(100)
			}
		}},
		{"Mutex  ", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = fixMutex(100)
			}
		}},
		{"channel", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = fixChannel(100)
			}
		}},
	} {
		res := testing.Benchmark(bc.fn)
		fmt.Printf("   %s : %10d ns/op\n", bc.name, res.NsPerOp())
	}
	fmt.Println("   结论：纯计数场景 atomic 最快，channel 最慢（最重但语义最清晰）")
	fmt.Println("   不要为了性能牺牲正确性，也不要在能写清楚的地方硬套 channel")
}

// ---------- Run ----------
