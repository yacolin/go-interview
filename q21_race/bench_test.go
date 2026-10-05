package q21_race

import "testing"

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// 运行：go test -bench=. -benchmem ./q21_race

func BenchmarkFixAtomic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = fixAtomic(100)
	}
}

func BenchmarkFixMutex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = fixMutex(100)
	}
}

func BenchmarkFixChannel(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = fixChannel(100)
	}
}
