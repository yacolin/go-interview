package q22_network

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- 1. TCP 粘包 / 半包 ----------

// encodeWithLength 用"长度前缀"解决粘包：先写 4 字节大端长度，再写负载。
func encodeWithLength(payloads []string) []byte {
	var buf []byte
	for _, p := range payloads {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(p)))
		buf = append(buf, hdr[:]...)
		buf = append(buf, p...)
	}
	return buf
}

// decodeWithLength 从字节流里按长度前缀切出完整消息。
// 关键点：即使拿到的是半个包，也能靠"读满 N 字节"wait 到完整数据。
func decodeWithLength(r io.Reader) ([]string, error) {
	var out []string
	br := bufio.NewReader(r)
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n > 1<<20 {
			return out, fmt.Errorf("消息长度异常: %d", n)
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			return out, err
		}
		out = append(out, string(body))
	}
}

// splitByNewline 是另一种常见分帧方式（HTTP/Redis 的 RESP 也用这思路）。
func splitByNewline(data string) []string {
	sc := bufio.NewScanner(strings.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 防止超长行报错
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// ---------- 2. 真实 TCP 服务端/客户端 ----------

// startTCPServer 起一个最小 TCP 服务：读一行、回声一行。
// 返回监听地址和关闭函数。
func startTCPServer(ctx context.Context) (addr string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // 端口 0 = 让内核分配
	if err != nil {
		return "", nil, err
	}

	var conns sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close() // 关监听 -> Accept 立刻返回错误，这是优雅退出的第一环
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听已关闭
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer conns.Done()
				defer c.Close()
				// 只给这条连接 2 秒，避免慢客户端一直占着
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					if _, err := fmt.Fprintf(c, "echo: %s\n", sc.Text()); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close(); conns.Wait() }, nil
}

// talkOnce 连一次服务，发一句、收一句。
func talkOnce(addr, msg string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := fmt.Fprintf(conn, "%s\n", msg); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// ---------- 3. 短连接 vs 长连接的开销 ----------

// dialN 建 n 次连接（每次用完即关），返回耗时。
func dialN(addr string, n int) time.Duration {
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := talkOnce(addr, "ping"); err != nil {
			break
		}
	}
	return time.Since(start)
}

// reuseConnN 复用同一条连接发 n 次，返回耗时。
func reuseConnN(addr string, n int) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	for i := 0; i < n; i++ {
		if _, err := fmt.Fprintf(conn, "ping\n"); err != nil {
			return time.Since(start), err
		}
		if _, err := br.ReadString('\n'); err != nil {
			return time.Since(start), err
		}
	}
	return time.Since(start), nil
}

// ---------- 4. 资源泄漏：忘记 Close ----------

// leakConns 故意不关连接，观察 goroutine / fd 的增长。
// net.Conn 不关 = fd 泄漏 + 读 goroutine 永久阻塞。
func leakConns(addr string, n int) (before, after int) {
	before = runtime.NumGoroutine()
	var keep []net.Conn
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			break
		}
		keep = append(keep, c) // ✗ 不收不关
	}
	time.Sleep(50 * time.Millisecond)
	after = runtime.NumGoroutine()
	// 收尾，避免真的泄漏到后续用例
	for _, c := range keep {
		_ = c.Close()
	}
	return
}

// ---------- 5. netpoll 与阻塞模型 ----------

// goroutinePerConn 演示"每连接一个 goroutine"的模型：
// 1 万个空闲连接会占用 1 万个 goroutine，但内存开销很小（初始栈 8KB 上下）。
func goroutinePerConn(addr string, n int) int {
	before := runtime.NumGoroutine()
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			break
		}
		conns = append(conns, c)
	}
	time.Sleep(100 * time.Millisecond)
	peak := runtime.NumGoroutine() - before
	for _, c := range conns {
		_ = c.Close()
	}
	return peak
}

// ---------- 6. socket 选项：TCP_NODELAY ----------

// nodelayState 读取当前连接是否开启 TCP_NODELAY。
// Go 的 net 包对 TCP 默认就设置了 TCP_NODELAY=true（禁用 Nagle 算法）。
func nodelayState(addr string) (enabled bool, err error) {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return false, errors.New("不是 TCP 连接")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return false, err
	}
	var v int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		v, serr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
	}); err != nil {
		return false, err
	}
	if serr != nil {
		return false, serr
	}
	return v != 0, nil
}

func Run() {
	fmt.Println("=== Q22: 网络编程 —— 粘包、长连接与 netpoll ===")

	// 1. 粘包与半包
	fmt.Println("--- 1. TCP 是字节流，没有\"消息边界\" ---")
	payloads := []string{"hello", "world", "go"}
	stream := encodeWithLength(payloads)
	fmt.Printf("   应用层消息: %v\n", payloads)
	fmt.Printf("   编码成字节流: % x ...\n", stream[:minInt(16, len(stream))])
	fmt.Println("   两种常见错误：")
	fmt.Println("     - 一次 Read 拿到多个消息（粘包）")
	fmt.Println("     - 一次 Read 只拿到半个消息（半包）")
	fmt.Println("   两者都因为 TCP 只保证\"字节流有序\"，不保证\"边界\"")
	fmt.Println("   解决方案（三选一）：")
	fmt.Println("     1) 长度前缀（最通用，protobuf/gRPC 都是这么干的）")
	fmt.Println("     2) 固定分隔符（Redis RESP、文本协议）")
	fmt.Println("     3) 固定长度（定长报文）")

	// 模拟半包：把完整流拆成任意片段，用 io.ReadFull 拼回来
	full := encodeWithLength([]string{"aaa", "bb", "cccc"})
	var sb strings.Builder
	for i := 0; i < len(full); i += 3 { // 每次只喂 3 字节
		end := minInt(i+3, len(full))
		sb.Write(full[i:end])
	}
	msgs, err := decodeWithLength(strings.NewReader(sb.String()))
	fmt.Printf("   按 3 字节切碎后重新解码: %v, err=%v\n", msgs, err)
	fmt.Println("   核心是 io.ReadFull：读不满就继续读，天然处理半包")

	// 2. 起真实服务
	fmt.Println("--- 2. 一个最小 TCP 服务（含优雅退出）---")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, stop, err := startTCPServer(ctx)
	if err != nil {
		fmt.Printf("   启动失败: %v\n", err)
		return
	}
	defer stop()
	fmt.Printf("   监听 %s\n", addr)

	got, err := talkOnce(addr, "ping")
	fmt.Printf("   发 ping 收 %q, err=%v\n", got, err)

	// 3. 短连接 vs 长连接
	fmt.Println("--- 3. 短连接 vs 长连接（各 50 次请求）---")
	const n = 50
	short := dialN(addr, n)
	long, lerr := reuseConnN(addr, n)
	fmt.Printf("   每次新建连接: %v（约 %v/次）\n", short.Round(time.Millisecond), (short / n).Round(time.Microsecond))
	if lerr == nil {
		fmt.Printf("   复用同一连接: %v（约 %v/次）\n", long.Round(time.Millisecond), (long / n).Round(time.Microsecond))
		fmt.Printf("   长连接快约 %.0f 倍 —— 省掉的是三次握手、慢启动和 TIME_WAIT\n",
			float64(short)/float64(long))
	}
	fmt.Println("   但长连接要处理：空闲超时、心跳保活、连接失效重连、并发写要加锁")

	// 4. TCP_NODELAY
	fmt.Println("--- 4. Nagle 算法与 TCP_NODELAY ---")
	nd, ndErr := nodelayState(addr)
	fmt.Printf("   Go 的 TCP 连接默认 TCP_NODELAY=%v（err=%v）\n", nd, ndErr)
	fmt.Println("   Nagle 算法会攒小包再发，省带宽但增加延迟（最多 40ms）")
	fmt.Println("   Go 默认关掉它，因为大多数服务更在意延迟；")
	fmt.Println("   如果你在写\"小包很多、带宽敏感\"的场景，可以用 SetNoDelay(false) 打开")

	// 5. 每连接一个 goroutine
	fmt.Println("--- 5. \"每连接一个 goroutine\"在 Go 里为什么可行 ---")
	peak := goroutinePerConn(addr, 200)
	fmt.Printf("   200 个空闲连接 -> 额外 %d 个 goroutine（约 1:1）\n", peak)
	fmt.Println("   为什么不怕：goroutine 初始栈约 2~8KB，阻塞在网络读时被 park，")
	fmt.Println("              不占 OS 线程（见 Q8）；真正受限的是 fd 数量和内存")
	fmt.Println("   对比：C/Java 的\"每连接一线程\"，1 万连接就是 1 万个线程，")
	fmt.Println("         光栈就 8GB，上下文切换也会崩")
	fmt.Println("   上限参考：fd 上限（ulimit -n）、每连接约 2 个 fd（连出去）/1 个（被连）")

	// 6. netpoll
	fmt.Println("--- 6. netpoll：Go 网络 IO 的调度器 ---")
	fmt.Println("   在 Linux 上，runtime 用 epoll 实现 netpoll；")
	fmt.Println("   在 macOS 上用的是 kqueue，Windows 上是 IOCP")
	fmt.Println("   工作流程：")
	fmt.Println("     1) 业务 goroutine 调 Read -> 内核返回 EAGAIN")
	fmt.Println("     2) runtime 把 fd 注册进 epoll，并 park 这个 goroutine")
	fmt.Println("     3) epoll 就绪 -> runtime 唤醒对应 goroutine 重新 Read")
	fmt.Println("   关键：整个过程 P 不会被占住（它去跑别的 G 了），")
	fmt.Println("        所以\"阻塞式\"的代码写起来简单，性能却是事件驱动的")
	fmt.Println("   这就是 Go 常说的\"用同步的方式写异步\"")
	fmt.Println("   sysmon 线程还会定期轮询 netpoll，保证没有 goroutine 被漏掉")

	// 7. 连接泄漏
	fmt.Println("--- 7. 连接/FD 泄漏（最常见的线上事故之一）---")
	before, after := leakConns(addr, 20)
	fmt.Printf("   连 20 条不关：goroutine %d -> %d（+%d）\n", before, after, after-before)
	fmt.Println("   每个泄漏的连接占用：1 个 fd + 可能的读/写 goroutine + 缓冲区")
	fmt.Println("   常见成因：")
	fmt.Println("     - defer resp.Body.Close() 忘了写（且 Body 必须读完或关掉才能复用连接）")
	fmt.Println("     - 错误分支提前 return 导致 Close 被跳过")
	fmt.Println("     - http.Client 没设 Timeout，请求卡住后连接一直占着")
	fmt.Println("     - 自己 net.Dial 后忘了 Close")
	fmt.Println("   排查：lsof -p <pid> | wc -l、/proc/<pid>/fd、pprof goroutine")

	fmt.Println("--- 8. 一次完整的\"健壮客户端\"要考虑什么 ---")
	fmt.Println("   超时：DialTimeout / SetDeadline / http.Client.Timeout（三个都要设）")
	fmt.Println("   重试：指数退避 + 抖动，且要判断错误是否可重试（幂等性！）")
	fmt.Println("   连接池：MaxIdleConns / MaxIdleConnsPerHost / IdleConnTimeout")
	fmt.Println("   熔断限流：保护下游，见 Q25")
	fmt.Println("   可观测：连接数、QPS、P99、错误率（net/http/pprof + metrics）")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
