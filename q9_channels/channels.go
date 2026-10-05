package q9_channels

import (
	"fmt"
	"sync"
	"time"
)

// 说明：channel 的内部结构（runtime.hchan）没有导出，且字段偏移随版本变化，
// 用 unsafe/reflect 硬读既不可靠也不可移植。所以这一题改用"可观测行为"来验证
// hchan 的两个核心机制：环形缓冲区 与 两个等待队列。
//
// 想直接看内部结构的同学，用官方途径：
//   go tool compile -S  看 chan 相关的 runtime 调用（chansend/chanrecv/makechan）
//   或阅读 runtime/chan.go 的源码与注释。

// ---------- 1. 缓冲 vs 无缓冲：可观测的差异 ----------

// unbufferedDemo 证明无缓冲 channel 是"同步交接"：
// 发送方必须等到接收方就绪，数据不落在任何缓冲区里。
func unbufferedDemo() string {
	ch := make(chan string)
	// 没有接收者时，发送会阻塞：用 select 直接观测
	select {
	case ch <- "hello":
		return "无缓冲 channel 在没有接收者时居然没阻塞（不可能）"
	default:
	}

	recvReady := make(chan struct{})
	go func() {
		close(recvReady)
		<-ch
	}()
	<-recvReady

	// 接收者就绪后，发送立刻成功（由 runtime 直接把值拷给接收方）
	ch <- "hello"
	return "无接收者时发送会阻塞；接收者就绪后立即交接成功（同步，不经过缓冲区）"
}

// bufferedDemo 证明有缓冲 channel 是"异步暂存"：
// 容量未满时发送不阻塞，容量为空时接收不阻塞。
func bufferedDemo() string {
	ch := make(chan string, 3)

	// 容量没满 -> 不阻塞，直接入队
	for i := 0; i < 3; i++ {
		select {
		case ch <- fmt.Sprintf("item-%d", i):
		default:
			return "容量未满却阻塞了（不可能）"
		}
	}

	// 容量已满 -> 再发就阻塞
	full := "已满"
	select {
	case ch <- "item-3":
		full = "容量已满却还能写入（不可能）"
	default:
	}

	// 容量非空 -> 接收不阻塞
	got := <-ch
	return fmt.Sprintf("写入 3 个（容量 3）均不阻塞；第 4 个会阻塞(%s)；随后读出一个 %q", full, got)
}

// queueBlockingDemo 证明"等待队列"的存在：
// 阻塞的发送者会被 runtime park 住（对应 sendq），阻塞的接收者同理（对应 recvq），
// 一旦对端就绪，runtime 直接从队列里弹出配对的 goroutine 完成交接。
func queueBlockingDemo() string {
	ch := make(chan int)

	// 无接收者：用 select+default 观测"会阻塞"
	sendBlocked := false
	select {
	case ch <- 1:
	default:
		sendBlocked = true
	}

	// 无发送者：接收同样阻塞
	recvBlocked := false
	select {
	case <-ch:
	default:
		recvBlocked = true
	}

	// 派一个接收者 park 在 recvq 上，之后发送就不再阻塞
	go func() { <-ch }()
	time.Sleep(20 * time.Millisecond)
	handoff := "交接成功"
	select {
	case ch <- 2:
	case <-time.After(100 * time.Millisecond):
		handoff = "超时：没有等到接收者"
	}

	return fmt.Sprintf("无接收者时发送阻塞=%v，无发送者时接收阻塞=%v；有等待接收者后 %s",
		sendBlocked, recvBlocked, handoff)
}

// ---------- 2. 关闭与零值语义 ----------

func closedRecvDemo() (v string, ok bool) {
	ch := make(chan string, 2)
	ch <- "data"
	close(ch)
	first, ok1 := <-ch
	second, ok2 := <-ch
	third, ok3 := <-ch
	return fmt.Sprintf("1st=(%q,%v) 2nd=(%q,%v) 3rd=(%q,%v)", first, ok1, second, ok2, third, ok3), ok3
}

// ---------- 3. nil channel：永远阻塞 ----------

func nilChannelDemo() string {
	var nilCh chan int           // 零值 channel 就是 nil
	never := make(chan struct{}) // 永远不会被关闭
	select {
	case <-nilCh:
		return "从 nil channel 收到了数据（不可能）"
	case <-never:
		return "从 never 收到了数据（不可能）"
	case <-time.After(30 * time.Millisecond):
		return "30ms 内两个 case 都没就绪：nil channel 永远阻塞，可用于动态关闭某个分支"
	}
}

// ---------- 4. 关闭已关闭的 channel / 向已关闭的 channel 发送 ----------

func catchPanic(f func()) (panicked bool, msg string) {
	defer func() {
		if r := recover(); r != nil {
			panicked, msg = true, fmt.Sprint(r)
		}
	}()
	f()
	return
}

// ---------- 5. 广播：关闭 channel 代替逐个唤醒 ----------

func broadcastDemo(workers int) int {
	start := make(chan struct{})
	var wg sync.WaitGroup
	var started int
	var mu sync.Mutex

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 所有 worker park 在同一个 channel 的 recvq 上
			mu.Lock()
			started++
			mu.Unlock()
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(start) // 一次 close 唤醒 recvq 里的全部接收者
	wg.Wait()
	return started
}

// ---------- 6. 常见坑：无缓冲 channel + 无接收者 = 死锁 ----------

func selfDeadlock(d time.Duration) string {
	done := make(chan struct{})
	go func() {
		ch := make(chan int)
		go func() { ch <- 1 }() // 没有接收者，这个 G 永久 park
		select {
		case <-ch:
		case <-time.After(d):
		}
		close(done)
	}()

	select {
	case <-done:
		return "正常结束"
	case <-time.After(d * 2):
		return "超时：goroutine 泄漏在 ch <- 1 上"
	}
}

func Run() {
	fmt.Println("=== Q9: channel 内部结构与使用陷阱 ===")

	// 1. hchan 结构
	fmt.Println("--- 1. hchan 的两个核心：环形缓冲区 + 等待队列 ---")
	fmt.Printf("  1) 无缓冲（同步交接）: %s\n", unbufferedDemo())
	fmt.Printf("  2) 有缓冲（异步暂存）: %s\n", bufferedDemo())
	fmt.Printf("  3) 阻塞与配对: %s\n", queueBlockingDemo())
	fmt.Println("  说明：无缓冲 channel 没有 buf，值由 runtime 从发送者直接拷进接收者；")
	fmt.Println("       有缓冲 channel 有环形缓冲区 buf，队列只在缓冲满/空时才被用上")

	// 2. 关闭后接收
	fmt.Println("--- 2. 从已关闭的 channel 接收 ---")
	msg, _ := closedRecvDemo()
	fmt.Printf("   %s\n", msg)
	fmt.Println("   结论：关闭后接收永不阻塞，返回零值 + ok=false；因此\"零值\"不能当哨兵")

	// 3. nil channel
	fmt.Println("--- 3. nil channel ---")
	fmt.Printf("   %s\n", nilChannelDemo())

	// 4. 关闭的两种 panic
	fmt.Println("--- 4. 两种 panic ---")
	p1, m1 := catchPanic(func() {
		ch := make(chan int)
		close(ch)
		close(ch)
	})
	fmt.Printf("   重复 close   : panicked=%v msg=%q\n", p1, m1)
	p2, m2 := catchPanic(func() {
		ch := make(chan int)
		close(ch)
		ch <- 1
	})
	fmt.Printf("   关闭后发送   : panicked=%v msg=%q\n", p2, m2)
	fmt.Println("   注意：channel 的 panic 是普通 panic，可以 recover；")
	fmt.Println("        但 map 的 concurrent map writes 是 fatal error，recover 不了")

	// 5. 广播
	fmt.Println("--- 5. close 广播：一次唤醒所有等待者 ---")
	n := broadcastDemo(10)
	fmt.Printf("   close(start) 一次性唤醒了 %d/%d 个 worker\n", n, 10)
	fmt.Println("   这是\"优雅关闭\"的标准写法：不要逐个发信号，直接 close")

	// 6. 坑
	fmt.Println("--- 6. 泄漏原型 ---")
	fmt.Printf("   %s\n", selfDeadlock(50*time.Millisecond))

	// 7. 开销参考
	fmt.Println("--- 7. 性能与选型 ---")
	fmt.Println("   无缓冲 channel 一次收发约 1~2 次原子/锁操作，比 mutex 重但不夸张")
	fmt.Println("   高频计数场景：atomic > mutex > channel")
	fmt.Println("   数据流转/流水线/超时取消：channel 是首选，语义清晰")
	fmt.Println("   只做\"保护共享状态\"：mutex 更直接，别用 channel 硬套 CSP")
}
