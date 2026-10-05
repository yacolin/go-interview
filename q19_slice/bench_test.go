package q19_slice

import "testing"

// 运行：go test -bench=. -benchmem ./q19_slice

func BenchmarkNoPrealloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s := make([]int, 0)
		for j := 0; j < 1000; j++ {
			s = append(s, j)
		}
		_ = s
	}
}

func BenchmarkPrealloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		s := make([]int, 0, 1000)
		for j := 0; j < 1000; j++ {
			s = append(s, j)
		}
		_ = s
	}
}
