package q17_sync

import (
	"fmt"
	"testing"
)

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// 运行：go test -bench=. -benchmem ./q17_sync

// 读多写少：key 集合固定，所有人都读同一个 key（缓存命中路径）
func BenchmarkSyncMapReadHeavy(b *testing.B) {
	preload()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = syncMapBench.Get("key-5000")
		}
	})
}

func BenchmarkMutexMapReadHeavy(b *testing.B) {
	preload()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = mutexMapBench.Get("key-5000")
		}
	})
}

// 写多：不断写入不同的 key，dirty map 反复提升
func BenchmarkSyncMapWriteHeavy(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			syncMapBench.Inc(fmt.Sprintf("w-%d", i%10000))
		}
	})
}

func BenchmarkMutexMapWriteHeavy(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			mutexMapBench.Inc(fmt.Sprintf("w-%d", i%10000))
		}
	})
}

// TestOnceSemantics 验证 Once 在并发下确实只执行一次。
func TestOnceSemantics(t *testing.T) {
	r := &heavyResource{}
	var wg [20]struct{}
	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		_ = wg
		go func() { _ = r.Get(); done <- struct{}{} }()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if got := r.init; got != 1 {
		t.Errorf("Once 应当只初始化 1 次，实际 %d", got)
	}
}

// TestWaitGroupAndCond 在 -race 下验证 WaitGroup / Cond 用法正确。
func TestWaitGroupAndCond(t *testing.T) {
	if got := waitGroupCorrect(100); got != 4950 {
		t.Errorf("waitGroupCorrect(100) = %d, want 4950", got)
	}

	q := newCondQueue()
	got := make(chan int, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			v, ok := q.Pop()
			if !ok {
				return
			}
			got <- v
		}
	}()
	for i := 1; i <= 10; i++ {
		q.Push(i)
	}
	q.Close()
	<-done
	close(got)

	sum := 0
	for v := range got {
		sum += v
	}
	if sum != 55 {
		t.Errorf("Cond 队列累加 = %d, want 55", sum)
	}
}
