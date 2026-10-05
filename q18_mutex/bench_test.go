package q18_mutex

import (
	"sync"
	"sync/atomic"
	"testing"
)

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// 单 goroutine + 100 把独立的锁：没有争用，测的是快速路径本身的开销。
// 运行：go test -bench=. -benchmem ./q18_mutex

func BenchmarkMutexSerial(b *testing.B) {
	const n = 100
	var mutexes [n]sync.Mutex
	var counters [n]int64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx := i % n
		mutexes[idx].Lock()
		counters[idx]++
		mutexes[idx].Unlock()
	}
}

func BenchmarkAtomicCAS(b *testing.B) {
	const n = 100
	var counters [n]int64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx := i % n
		atomic.AddInt64(&counters[idx], 1)
	}
}

func BenchmarkRWMutexReadOnly(b *testing.B) {
	const n = 100
	var rws [n]sync.RWMutex
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		idx := i % n
		rws[idx].RLock()
		rws[idx].RUnlock()
	}
}
