package q20_string

import (
	"strings"
	"testing"
)

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// 运行：go test -bench=. -benchmem ./q20_string

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

func BenchmarkConcatJoin(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = concatJoin()
	}
}

// 注意：只把 len 存起来不够 —— 编译器能证明结果没逃逸就直接优化掉分配，
// 必须把切片本身存到全局变量强制逃逸。
func BenchmarkStringToBytesSafe(b *testing.B) {
	s := strings.Repeat("x", 1024)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		byteSliceSink = []byte(s)
	}
}

func BenchmarkStringToBytesZeroCopy(b *testing.B) {
	s := strings.Repeat("x", 1024)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		byteSliceSink = stringToBytesZeroCopy(s)
	}
}

func BenchmarkBytesToStringSafe(b *testing.B) {
	buf := []byte(strings.Repeat("x", 1024))
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stringSink = string(buf)
	}
}

func BenchmarkBytesToStringZeroCopy(b *testing.B) {
	buf := []byte(strings.Repeat("x", 1024))
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stringSink = bytesToStringZeroCopy(buf)
	}
}
