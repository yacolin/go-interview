package q15_pprof

import (
	"bytes"
	"fmt"
	"strings"
	"testing" // 仅用于 testing.AllocsPerRun 做精确分配计数
)

// ---------- 被测实现：预分配 vs 不预分配 ----------

func buildNoPrealloc(n int) []int {
	var out []int
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

func buildPrealloc(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

// ---------- 被测实现：字符串拼接 ----------

var parts = func() []string {
	p := make([]string, 200)
	for i := range p {
		p[i] = "abcdefgh"
	}
	return p
}()

func concatPlus() string {
	s := ""
	for _, p := range parts {
		s += p
	}
	return s
}

func concatBuilder() string {
	var b strings.Builder
	b.Grow(len(parts) * 8)
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}

func concatBuffer() string {
	var b bytes.Buffer
	b.Grow(len(parts) * 8)
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}

// Run 让 main 也能一键跑基准，不用记命令。
// 基准测试/单元测试在 bench_test.go 里（必须是 _test.go 才能被 go test 发现）。
func Run() {
	fmt.Println("=== Q15: 性能分析（benchmark / pprof / 逃逸）===")
	fmt.Println("--- 1. 内联跑一遍分配计数（等价于 go test -run TestAllocations -v）---")

	noPre := testing.AllocsPerRun(100, func() { _ = buildNoPrealloc(1000) })
	pre := testing.AllocsPerRun(100, func() { _ = buildPrealloc(1000) })
	plus := testing.AllocsPerRun(50, func() { _ = concatPlus() })
	builder := testing.AllocsPerRun(50, func() { _ = concatBuilder() })
	buffer := testing.AllocsPerRun(50, func() { _ = concatBuffer() })

	fmt.Printf("  buildNoPrealloc(1000): %6.0f allocs/op（翻倍扩容 + 拷贝）\n", noPre)
	fmt.Printf("  buildPrealloc(1000)  : %6.0f allocs/op（一次到位）\n", pre)
	fmt.Printf("  concatPlus           : %6.0f allocs/op\n", plus)
	fmt.Printf("  concatBuilder        : %6.0f allocs/op\n", builder)
	fmt.Printf("  concatBuffer         : %6.0f allocs/op\n", buffer)

	fmt.Println("--- 2. 完整基准测试命令 ---")
	fmt.Println("  go test -bench=. -benchmem ./q15_pprof")
	fmt.Println("  go test -bench=. -benchmem -cpuprofile=cpu.out -memprofile=mem.out ./q15_pprof")
	fmt.Println("  go tool pprof -http=:8080 cpu.out      # 火焰图/调用图/top")
	fmt.Println("  go tool pprof -http=:8080 mem.out      # 4 种采样口径")
	fmt.Println("  go test -bench=Concat -benchtime=3s -count=5 ./q15_pprof | tee new.txt")
	fmt.Println("  benchstat old.txt new.txt              # 判断优化是否统计显著")

	fmt.Println("--- 3. 读懂 -benchmem 的四列 ---")
	fmt.Println("  ns/op     每次操作耗时（越小越好，注意受 CPU 频率影响）")
	fmt.Println("  B/op      每次操作分配字节数")
	fmt.Println("  allocs/op 每次操作分配次数（这个最容易优化，也最能反映 GC 压力）")
	fmt.Println("  MB/s      吞吐（只有 b.SetBytes 时才有意义）")

	fmt.Println("--- 4. pprof 的四种内存口径 ---")
	fmt.Println("  alloc_objects / alloc_space：从进程启动至今的累计分配 —— 找\"谁分配得最多\"")
	fmt.Println("  inuse_objects / inuse_space：采样时刻仍然存活的 —— 找\"谁占着内存不放\"")
	fmt.Println("  排查内存泄漏看 inuse，排查 GC 压力看 alloc")

	fmt.Println("--- 5. 优化工作流 ---")
	fmt.Println("  1) 先有可复现的基准或压测，别凭感觉优化")
	fmt.Println("  2) pprof 找到 Top 热点，80% 的时间通常只在少数几个函数里")
	fmt.Println("  3) 只改一个变量，用 benchstat -count=5 以上确认变化不是噪声")
	fmt.Println("  4) 回归测试保证结果正确（TestResultEquality 就是这个作用）")
	fmt.Println("  5) 记录优化前后的数字，否则下次没人知道为什么这么写")

	fmt.Println("--- 6. 常见高收益优化清单 ---")
	fmt.Println("  预分配 slice/map 容量；用 strings.Builder 替代 +")
	fmt.Println("  复用 buffer（buf[:0] 或 sync.Pool）；避免在热路径里做反射/格式化")
	fmt.Println("  减少不必要的 interface{} 装箱；大对象用指针、小对象用值")
	fmt.Println("  批量提交代替逐条（DB/Redis/HTTP）；合理设置 GC 参数而非盲目调优")

	fmt.Println("--- 7. 别忘了逃逸分析 ---")
	fmt.Println("  go build -gcflags='-m' ./q15_pprof     # 哪些变量逃逸到了堆")
	fmt.Println("  逃逸到堆 = 每次调用都要分配 + 等 GC 回收；能留栈上就留栈上")
}
