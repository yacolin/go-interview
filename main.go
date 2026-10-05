package main

import (
	"fmt"
	"os"
	"strconv"

	"go-interview/q10_iface"
	"go-interview/q11_errors"
	"go-interview/q12_panic"
	"go-interview/q13_context"
	"go-interview/q14_generics"
	"go-interview/q15_pprof"
	"go-interview/q1_slice"
	"go-interview/q2_defer"
	"go-interview/q3_concurrency"
	"go-interview/q4_map"
	"go-interview/q5_perf"
	"go-interview/q6_memory"
	"go-interview/q7_gc"
	"go-interview/q8_scheduler"
	"go-interview/q9_channels"
)

const usage = `Go 面试题演示程序

用法: go run . <题号> [题号...]

题号：
   1  slice 底层与陷阱                  9  channel 内部结构与陷阱
   2  defer 与返回值                   10  接口、类型系统与内存布局
   3  并发 worker pool + context       11  error 设计、错误链与最佳实践
   4  map 并发安全                     12  panic / recover 控制流
   5  range 变量与指针陷阱             13  context 传播与取消
   6  内存分配、逃逸分析与栈/堆        14  泛型、类型约束与选型
   7  垃圾回收与 GC 调优               15  性能分析 benchmark / pprof
   8  GMP 调度模型与 runtime 调度器

   0  按顺序跑完全部 15 题

示例: go run . 7
      GOGC=400 go run . 7
      GODEBUG=gctrace=1 go run . 7
      go test -bench=. -benchmem ./q15_pprof
`

// runners 按题号索引。
var runners = map[int]struct {
	name string
	run  func()
}{
	1:  {"slice", q1_slice.Run},
	2:  {"defer", q2_defer.Run},
	3:  {"concurrency", q3_concurrency.Run},
	4:  {"map", q4_map.Run},
	5:  {"perf", q5_perf.Run},
	6:  {"memory", q6_memory.Run},
	7:  {"gc", q7_gc.Run},
	8:  {"scheduler", q8_scheduler.Run},
	9:  {"channels", q9_channels.Run},
	10: {"iface", q10_iface.Run},
	11: {"errors", q11_errors.Run},
	12: {"panic", q12_panic.Run},
	13: {"context", q13_context.Run},
	14: {"generics", q14_generics.Run},
	15: {"pprof", q15_pprof.Run},
}

const maxQuestion = 15

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}

	for _, arg := range os.Args[1:] {
		n, err := strconv.Atoi(arg)
		if err != nil {
			fmt.Printf("无法解析题号 %q，可选 0~%d\n", arg, maxQuestion)
			os.Exit(2)
		}

		if n == 0 {
			for i := 1; i <= maxQuestion; i++ {
				runners[i].run()
				fmt.Println()
			}
			return
		}

		r, ok := runners[n]
		if !ok {
			fmt.Printf("未知题号 %d，可选 0~%d\n", n, maxQuestion)
			os.Exit(2)
		}
		r.run()
		fmt.Println()
	}
}
