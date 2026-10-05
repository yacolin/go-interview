package q7_gc

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

func memStats() runtime.MemStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

// ---------- 1. 观测 GC 周期与暂停 ----------

// allocGarbage 制造 size 字节约垃圾，返回最后一次分配的指针防止被优化掉。
func allocGarbage(rounds, size int) []byte {
	var keep []byte
	for i := 0; i < rounds; i++ {
		b := make([]byte, size)
		b[0] = byte(i)
		keep = b
	}
	return keep
}

// ---------- 2. GOGC 调优 ----------

func observeGC(allocMB, loop int) (cycles uint32, pauseTotalMs float64) {
	base := memStats()
	_ = allocGarbage(loop, allocMB*1024*1024)
	after := memStats()
	// PauseTotalNs 是累计 STW 暂停时间
	return after.NumGC - base.NumGC, float64(after.PauseTotalNs-base.PauseTotalNs) / 1e6
}

// ---------- 3. GOMEMLIMIT 软限制 ----------

// observeMemoryLimit 在限制生效期间反复分配 2MB 垃圾，并采样 HeapAlloc，
// 观察"堆是否被压在 limit 附近"。返回观测到的峰值堆大小与 GC 轮数。
//
// 注意：不能只统计"分配期间触发了多少轮 GC"——GC 是并发的，
// 数量的随机性很大，结论也不稳定。真正要验证的是"堆有没有失控"。
func observeMemoryLimit(loop int, limitBytes int64) (peakHeapAlloc uint64, cycles uint32, limit int64) {
	old := debug.SetMemoryLimit(limitBytes)
	defer debug.SetMemoryLimit(old)

	base := memStats()
	var peak uint64
	// 交错采样，避免采样本身过度干扰（ReadMemStats 会短暂 STW）
	for i := 0; i < loop; i++ {
		b := make([]byte, 2*1024*1024)
		b[0] = byte(i)
		if i%16 == 0 {
			if h := memStats().HeapAlloc; h > peak {
				peak = h
			}
		}
		runtime.KeepAlive(b)
	}
	after := memStats()
	cycles = after.NumGC - base.NumGC
	limit = debug.SetMemoryLimit(-1)
	return peak, cycles, limit
}

// ---------- 4. sync.Pool ----------

type payload struct {
	buf [1024]byte
}

var pool = sync.Pool{
	New: func() any { return new(payload) },
}

// poolWorkload 模拟"高并发下频繁创建/销毁大对象"的典型工作负载。
// usePool=false 时每次 make 新对象；true 时从 Pool 借还。
// 期间穿插 GC，模拟真实的 GC 压力。
// 返回这轮工作中真正发生的新分配次数。
func poolWorkload(usePool bool) uint64 {
	const (
		workers = 8
		rounds  = 20000
	)
	start := memStats() // 记录基线，返回的是本轮的增量
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				var p *payload
				if usePool {
					p = pool.Get().(*payload)
				} else {
					p = new(payload)
				}
				p.buf[0] = byte(i)
				runtime.KeepAlive(p)
				if usePool {
					pool.Put(p)
				}
			}
		}()
	}

	// 每 5ms 强制一次 GC，让 victim cache 的淘汰也参与进来
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				runtime.GC()
			}
		}
	}()

	wg.Wait()
	close(done)

	// 用 Mallocs 增量近似"新分配次数"（Pool 命中时不产生新的堆分配）
	return memStats().Mallocs - start.Mallocs
}

// ---------- Run ----------

func Run() {
	fmt.Println("=== Q7: 垃圾回收（三色标记 + 混合写屏障 + Pacer 调优）===")

	stats := memStats()
	fmt.Printf("Go 版本: %s, GOMAXPROCS=%d\n", runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Printf("启动时 NumGC=%d, GOGC=%d\n\n", stats.NumGC, debug.SetGCPercent(-1))
	debug.SetGCPercent(100) // 恢复默认值

	// 1. 观测 GC 周期
	fmt.Println("--- 1. GC 周期观测（固定分配总量，看 GC 触发次数）---")
	before := memStats()
	_ = allocGarbage(200, 1024*1024) // 200 次 1MB 分配 = 200MB 垃圾
	after := memStats()
	fmt.Printf("  分配 200MB 后新增 GC 周期: %d\n", after.NumGC-before.NumGC)
	fmt.Printf("  当前堆: HeapAlloc=%.1fMB HeapInuse=%.1fMB NextGC=%.1fMB\n",
		float64(after.HeapAlloc)/1024/1024,
		float64(after.HeapInuse)/1024/1024,
		float64(after.NextGC)/1024/1024)
	fmt.Printf("  累计 STW 暂停: %.3f ms, GC 累计 CPU 占用: %.3f ms\n",
		float64(after.PauseTotalNs)/1e6, float64(after.GCCPUFraction)*1000)
	fmt.Println("  默认 GOGC=100 表示：堆增长到上次存活量的 2 倍时触发下一轮 GC")

	// 2. GOGC 对比：低频 GC 换吞吐
	fmt.Println("--- 2. GOGC 调优对比（同样的分配量）---")
	oldGOGC := debug.SetGCPercent(100)
	c100, p100 := observeGC(4, 200)
	debug.SetGCPercent(800)
	c800, p800 := observeGC(4, 200)
	debug.SetGCPercent(oldGOGC)
	fmt.Printf("  GOGC=100 : %3d 轮 GC, STW 累计 %.3f ms（内存占用低，暂停多）\n", c100, p100)
	fmt.Printf("  GOGC=800 : %3d 轮 GC, STW 累计 %.3f ms（内存换吞吐，暂停少）\n", c800, p800)
	fmt.Println("  结论：GOGC 越大 GC 越懒，CPU 省了但堆峰值升高；批处理任务常用")
	fmt.Println("  GOGC=off + 手动 runtime.GC()，延迟敏感服务则调小 GOGC")

	// 3. GOMEMLIMIT
	fmt.Println("--- 3. GOMEMLIMIT 内存软限制（Go 1.19+）---")
	const limit = 32 << 20                              // 32MiB
	peak, cycles, cur := observeMemoryLimit(200, limit) // 200 * 2MB = 400MB 垃圾
	fmt.Printf("  设置 GOMEMLIMIT=%dMiB，累计制造 400MB 垃圾：\n", limit>>20)
	fmt.Printf("    观测到的 HeapAlloc 峰值 = %.1f MiB（被压在限制附近）\n", float64(peak)/1024/1024)
	fmt.Printf("    期间触发 GC %d 轮\n", cycles)
	fmt.Printf("    当前生效的内存限制: %d MiB\n", cur>>20)
	fmt.Println("  对比：不设限制时，堆可以长到 GOGC 允许的 2 倍存活集以上")
	fmt.Println("  结论：GOGC 管\"什么时候回收\"，GOMEMLIMIT 管\"最多能用多少\"；")
	fmt.Println("        Pacer 同时受两者约束，谁更紧就听谁的")
	fmt.Println("  容器里推荐组合：GOGC=100（或 off）+ GOMEMLIMIT=容器上限的 70~80%")

	// 4. sync.Pool 与 victim cache
	fmt.Println("--- 4. sync.Pool 与 victim cache ---")
	fmt.Println("  机制：每个 P 有 private 槽 + shared 链表；GC 时把\"当前池\"降级为 victim，")
	fmt.Println("        下一轮 GC 才把 victim 清掉（多活一轮是为了给\"每次都强制 GC\"的程序留活路）。")
	fmt.Println("  实测开销：并发热路径下复用对象能省掉多少分配")
	allocs := poolWorkload(false)
	allocsPooled := poolWorkload(true)
	fmt.Printf("    不使用 Pool：%d 次分配\n", allocs)
	fmt.Printf("    使用   Pool：%d 次分配\n", allocsPooled)
	fmt.Println("  设计含义：Pool 只是\"降低分配频率\"的概率优化，对象随时可能消失，")
	fmt.Println("  不能当缓存用（需要缓存就用带淘汰策略的 LRU 或 bigcache）")

	// 5. 四类内存指标
	fmt.Println("--- 5. 四个容易混的内存指标 ---")
	m := memStats()
	fmt.Printf("  HeapAlloc  = %.1fMB  // 当前存活对象（GC 的\"收益\"）\n", float64(m.HeapAlloc)/1024/1024)
	fmt.Printf("  HeapInuse  = %.1fMB  // 已分配 span 占用（含内部碎片）\n", float64(m.HeapInuse)/1024/1024)
	fmt.Printf("  HeapIdle   = %.1fMB  // 空闲 span，可归还 OS\n", float64(m.HeapIdle)/1024/1024)
	fmt.Printf("  TotalAlloc = %.1fMB  // 累计分配量（只增不减，看吞吐用）\n", float64(m.TotalAlloc)/1024/1024)
	fmt.Printf("  Sys        = %.1fMB  // 向 OS 申请的总内存\n", float64(m.Sys)/1024/1024)

	fmt.Println("--- 6. 观测命令 ---")
	fmt.Println("  GODEBUG=gctrace=1 go run . 7    # 每个 GC 周期一行日志")
	fmt.Println("  GOGC=50 go run . 7              # 更激进的 GC")

	// 让 GC 日志有稳定的观察窗口
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = allocGarbage(20, 1024*1024)
			time.Sleep(10 * time.Millisecond)
		}()
	}
	wg.Wait()
	fmt.Println("\n提示：GODEBUG=gctrace=1 时，上面每轮都会打印 gc N @X.XXXs P% CPU% 等信息")
}
