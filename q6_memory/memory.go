package q6_memory

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
)

// ---------- 栈 vs 堆：逃逸分析 ----------

// returnLocalAddr 返回局部变量的地址。
// 如果 x 留在栈上，函数返回后这块内存就失效了，
// 因此编译器必须把它分配到堆上（escape to heap）。
func returnLocalAddr() *int {
	x := 42
	return &x
}

// closureCounter 返回一个闭包。闭包捕获了 count，
// 而 count 的生命周期长于本函数，所以 count 逃逸到堆。
func closureCounter() func() int {
	count := 0
	return func() int {
		count++
		return count
	}
}

// localOnly 全程没有把 x 的地址带出本函数，
// 内联/逃逸分析后 x 留在栈上，函数返回即回收，零堆分配。
func localOnly() int {
	x := 7
	return x * 2
}

// ---------- 栈扩容：栈是复制式增长的 ----------

// deep 递归 depth 层，观察栈的增长。
// Go 的 goroutine 栈初始 2KB（Go 1.19 之前）/ 8KB（1.19+），
// 按需翻倍扩容，每次扩容都会**复制**整个栈，并修正所有指向栈的指针。
func deep(depth int, acc *int) {
	var big [128]byte // 让每层栈帧更大一点，扩容更容易被观测到
	big[0] = byte(depth)
	*acc += int(big[0])
	if depth > 0 {
		deep(depth-1, acc)
	}
	// 防止 big 被优化掉
	runtime.KeepAlive(big)
}

// ---------- 优化1：预分配容量 ----------

// buildNoPrealloc 不预分配。slice 会经历 0->1->2->4->8... 的翻倍扩容，
// 每次扩容都要分配新数组 + 拷贝旧数据，并且产生大量可回收垃圾。
func buildNoPrealloc(n int) []int {
	var out []int
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

// buildPrealloc 预分配。整个循环只有一次分配，扩容次数为 0。
func buildPrealloc(n int) []int {
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

func countAllocs(f func(), runs int) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}

// ---------- 优化2：切片复用而不是反复分配 ----------

// reuseBuffer 演示"用 len=0 但保留 cap 的切片复用底层数组"，
// 这是网络编程里最常用的零分配技巧。
func reuseBuffer(data []string) int {
	buf := make([]byte, 0, 64)
	total := 0
	for _, s := range data {
		buf = buf[:0] // 重置长度，但底层数组保留
		buf = append(buf, s...)
		total += len(buf)
	}
	return total
}

// ---------- 优化3：字符串拼接的三种姿势 ----------

// concatPlus 用 + 拼接。每次 + 都会产生一个全新的字符串，
// n 次拼接产生 O(n^2) 级别的拷贝（因为字符串不可变）。
func concatPlus(parts []string) string {
	s := ""
	for _, p := range parts {
		s += p
	}
	return s
}

// concatBuilder strings.Builder 内部是 []byte + 扩容策略，
// 且用 unsafe 把 []byte 零拷贝转成 string。
func concatBuilder(parts []string) string {
	var b strings.Builder
	b.Grow(64)
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}

// concatBuffer bytes.Buffer 与 Builder 类似，但 String() 会做一次拷贝。
func concatBuffer(parts []string) string {
	var b bytes.Buffer
	b.Grow(64)
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}

// ---------- Run ----------

func Run() {
	fmt.Println("=== Q6: 内存分配、逃逸分析与栈/堆 ===")

	// 1. 逃逸分析
	fmt.Println("--- 1. 逃逸分析（结果与编译器分析一致：地址被带出 => 堆）---")
	p1 := returnLocalAddr()
	fmt.Printf("  returnLocalAddr() -> ptr=%p value=%d（堆上，函数返回后依然有效）\n", p1, *p1)
	c := closureCounter()
	fmt.Printf("  closureCounter: %d %d %d（count 被闭包捕获 => 堆）\n", c(), c(), c())
	fmt.Printf("  localOnly() = %d（地址没带出 => 栈，函数返回即回收）\n", localOnly())

	// 2. 栈增长
	fmt.Println("--- 2. 栈扩容（复制式增长）---")
	for _, depth := range []int{10, 1000, 100000} {
		acc := 0
		deep(depth, &acc)
		fmt.Printf("  递归 %6d 层：栈增长并多次复制后正常返回\n", depth)
	}

	// 3. 预分配
	fmt.Println("--- 3. 预分配容量对分配次数的影响（n=100000，跑 10 轮）---")
	const n = 100000
	const runs = 10
	noPre := countAllocs(func() { _ = buildNoPrealloc(n) }, runs)
	pre := countAllocs(func() { _ = buildPrealloc(n) }, runs)
	perRunNoPre := float64(noPre) / runs
	perRunPre := float64(pre) / runs
	fmt.Printf("  不预分配：%8d 次堆分配（约 %.0f 次/轮，绝大部分是扩容+拷贝产生的垃圾）\n", noPre, perRunNoPre)
	fmt.Printf("  预  分配：%8d 次堆分配（约 %.0f 次/轮，基本只有底层数组本身）\n", pre, perRunPre)
	fmt.Printf("  结论：分配次数降到约 1/%.0f，GC 需要扫描和回收的对象数量同步大幅下降\n", perRunNoPre/perRunPre)

	// 4. 切片复用
	fmt.Println("--- 4. 切片复用（buf[:0] 保留 cap）---")
	words := []string{"go", "memory", "allocation"}
	fmt.Printf("  reuseBuffer(%v) = %d（全程只 1 次分配）\n", words, reuseBuffer(words))

	// 5. 字符串拼接
	fmt.Println("--- 5. 字符串拼接 1000 片，分配次数对比 ---")
	parts := make([]string, 1000)
	for i := range parts {
		parts[i] = "x"
	}
	a := countAllocs(func() { _ = concatPlus(parts) }, 100)
	b := countAllocs(func() { _ = concatBuilder(parts) }, 100)
	d := countAllocs(func() { _ = concatBuffer(parts) }, 100)
	fmt.Printf("  s += p        : %8d 次分配（每次都是新串，O(n^2) 拷贝）\n", a)
	fmt.Printf("  strings.Builder: %6d 次分配\n", b)
	fmt.Printf("  bytes.Buffer   : %6d 次分配\n", d)

	fmt.Println("--- 6. 自己动手看逃逸结论 ---")
	fmt.Println("  go build -gcflags='-m' ./q6_memory        # 打印逃逸决策")
	fmt.Println("  go build -gcflags='-m -m' ./q6_memory     # 更详细的原因")
}
