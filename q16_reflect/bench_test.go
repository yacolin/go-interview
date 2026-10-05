package q16_reflect

import "testing"

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// Run() 里用的是等价的内联闭包，两者的测量逻辑保持一致。
func BenchmarkReadReflection(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = readViaReflection(benchUsersAny)
	}
}

func BenchmarkReadDirect(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = readDirect(benchUsersDirect)
	}
}
