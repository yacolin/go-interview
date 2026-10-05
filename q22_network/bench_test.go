package q22_network

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
)

// 基准测试必须放在 *_test.go 里，go test 才会发现它们。
// 这里临时起一个真实 TCP 服务，对比"每次新建连接"与"复用连接"。
// 运行：go test -bench=. -benchtime=200x ./q22_network

func benchServer(b *testing.B) (addr string, stop func()) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr, s, err := startTCPServer(ctx)
	if err != nil {
		b.Fatal(err)
	}
	return addr, func() { s(); cancel() }
}

func BenchmarkDialPerRequest(b *testing.B) {
	addr, stop := benchServer(b)
	defer stop()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := talkOnce(addr, "ping"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReuseConn(b *testing.B) {
	addr, stop := benchServer(b)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	br := bufio.NewReader(conn)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fmt.Fprintf(conn, "ping\n"); err != nil {
			b.Fatal(err)
		}
		if _, err := br.ReadString('\n'); err != nil {
			b.Fatal(err)
		}
	}
}
