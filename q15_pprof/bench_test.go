package q15_pprof

import (
	"fmt"
	"testing"
)

// 基准测试与单元测试必须放在 _test.go 里，
// 否则 go test 根本不会发现它们（普通 .go 文件里的 TestXxx 会被当成普通函数）。
//
// 常用命令：
//
//	go test -bench=. -benchmem ./q15_pprof
//	go test -bench=. -benchmem -cpuprofile=cpu.out -memprofile=mem.out ./q15_pprof
//	go tool pprof -http=:8080 cpu.out
//	go test -bench=Concat -benchtime=3s -count=5 ./q15_pprof
//	benchstat old.txt new.txt    # 多轮结果的统计显著性对比

func BenchmarkSliceNoPrealloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = buildNoPrealloc(1000)
	}
}

func BenchmarkSlicePrealloc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = buildPrealloc(1000)
	}
}

func BenchmarkConcatPlus(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = concatPlus()
	}
}

func BenchmarkConcatBuilder(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = concatBuilder()
	}
}

func BenchmarkConcatBuffer(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = concatBuffer()
	}
}

// TestAllocations 验证优化确实把分配次数降下来了（testing.AllocsPerRun 精确计数）。
func TestAllocations(t *testing.T) {
	noPre := testing.AllocsPerRun(100, func() { _ = buildNoPrealloc(1000) })
	pre := testing.AllocsPerRun(100, func() { _ = buildPrealloc(1000) })
	plus := testing.AllocsPerRun(50, func() { _ = concatPlus() })
	builder := testing.AllocsPerRun(50, func() { _ = concatBuilder() })

	fmt.Printf("  buildNoPrealloc: %.0f allocs/op\n", noPre)
	fmt.Printf("  buildPrealloc  : %.0f allocs/op\n", pre)
	fmt.Printf("  concatPlus     : %.0f allocs/op\n", plus)
	fmt.Printf("  concatBuilder  : %.0f allocs/op\n", builder)

	if pre >= noPre {
		t.Errorf("预分配应当减少分配次数: pre=%.0f noPre=%.0f", pre, noPre)
	}
	if builder >= plus {
		t.Errorf("Builder 应当减少分配次数: builder=%.0f plus=%.0f", builder, plus)
	}
}

// TestResultEquality 保证优化前后结果一致 —— 优化不能改变行为。
func TestResultEquality(t *testing.T) {
	a := buildNoPrealloc(1000)
	b := buildPrealloc(1000)
	if len(a) != len(b) {
		t.Fatalf("长度不一致: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("索引 %d 不一致: %d vs %d", i, a[i], b[i])
		}
	}
	if concatPlus() != concatBuilder() || concatBuilder() != concatBuffer() {
		t.Fatal("三种拼接方式结果应当相同")
	}
}
