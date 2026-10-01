package q4_map

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// 方案1：Mutex + map
type SafeMap1 struct {
	mu sync.Mutex
	m  map[string]int
}

func NewSafeMap1() *SafeMap1 {
	return &SafeMap1{m: make(map[string]int)}
}

func (s *SafeMap1) Inc(key string) {
	s.mu.Lock()
	s.m[key]++
	s.mu.Unlock()
}

func (s *SafeMap1) Get(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}

// 方案2：RWMutex + map
type SafeMap2 struct {
	mu sync.RWMutex
	m  map[string]int
}

func NewSafeMap2() *SafeMap2 {
	return &SafeMap2{m: make(map[string]int)}
}

func (s *SafeMap2) Inc(key string) {
	s.mu.Lock()
	s.m[key]++
	s.mu.Unlock()
}

func (s *SafeMap2) Get(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[key]
}

// 方案3：sync.Map + 原子计数
type SafeMap3 struct {
	m sync.Map
}

func (s *SafeMap3) Inc(key string) {
	if v, ok := s.m.Load(key); ok {
		atomic.AddInt64(v.(*int64), 1)
		return
	}
	v, _ := s.m.LoadOrStore(key, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (s *SafeMap3) Get(key string) int64 {
	v, ok := s.m.Load(key)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

func Run() {
	fmt.Println("=== Q4: map 并发安全 ===")

	const N = 1000

	// 方案1
	m1 := NewSafeMap1()
	var wg1 sync.WaitGroup
	for i := 0; i < N; i++ {
		wg1.Add(1)
		go func() { defer wg1.Done(); m1.Inc("a") }()
	}
	wg1.Wait()
	fmt.Printf("SafeMap1 a = %d\n", m1.Get("a"))

	// 方案2
	m2 := NewSafeMap2()
	var wg2 sync.WaitGroup
	for i := 0; i < N; i++ {
		wg2.Add(1)
		go func() { defer wg2.Done(); m2.Inc("a") }()
	}
	wg2.Wait()
	fmt.Printf("SafeMap2 a = %d\n", m2.Get("a"))

	// 方案3
	m3 := &SafeMap3{}
	var wg3 sync.WaitGroup
	for i := 0; i < N; i++ {
		wg3.Add(1)
		go func() { defer wg3.Done(); m3.Inc("a") }()
	}
	wg3.Wait()
	fmt.Printf("SafeMap3 a = %d\n", m3.Get("a"))

	// 演示不安全的 map
	fmt.Println("--- 不安全的 map（注释掉，会 panic）---")
	// demoUnsafe()
}

// demoUnsafe 用于演示 concurrent map writes，默认不调用
func demoUnsafe() {
	m := make(map[string]int)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m["a"]++ }()
	}
	wg.Wait()
}
