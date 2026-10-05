package q13_context

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type ctxKey string

const (
	keyUserID  ctxKey = "user_id" // 自定义 key 类型，避免与别人的 key 冲突
	keyTraceID ctxKey = "trace_id"
)

// ---------- 1. 三种派生方式 ----------

func withCancelDemo() string {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		<-ctx.Done() // 等取消信号
		done <- ctx.Err()
	}()

	cancel()
	return fmt.Sprintf("WithCancel -> ctx.Err()=%v", <-done)
}

func withTimeoutDemo(d time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel() // 必须 defer cancel，否则父 context 会一直持有子节点（内存泄漏）

	start := time.Now()
	select {
	case <-time.After(time.Second):
		return "不该走到这里"
	case <-ctx.Done():
		return fmt.Sprintf("WithTimeout(%v) -> 在 %v 后返回 err=%v",
			d, time.Since(start).Round(time.Millisecond), ctx.Err())
	}
}

// deadline 会沿调用链向下传递
func deadlineChild() string {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	child, childCancel := context.WithTimeout(ctx, time.Second)
	defer childCancel()

	dl, ok := child.Deadline()
	if !ok {
		return "子 context 没有 deadline"
	}
	parentDL, _ := ctx.Deadline()
	return fmt.Sprintf("子 deadline 不能超过父：父剩 %v，子 deadline 被收紧为父的 %v",
		time.Until(parentDL).Round(time.Millisecond), time.Until(dl).Round(time.Millisecond))
}

// ---------- 2. WithValue 的 key 设计 ----------

func withValueDemo() string {
	ctx := context.Background()
	ctx = context.WithValue(ctx, keyUserID, 42)
	ctx = context.WithValue(ctx, keyTraceID, "abc-123")

	uid, ok := ctx.Value(keyUserID).(int)
	if !ok {
		return "取值失败"
	}
	// 用内建 string 当 key 会造成冲突（官方不建议）
	ctx2 := context.WithValue(ctx, "user_id", "别人的值")
	return fmt.Sprintf("uid=%d trace=%v；用 string key 写入后 ctx2.Value(\"user_id\")=%v，原 key 不受影响但也无法互相发现",
		uid, ctx.Value(keyTraceID), ctx2.Value("user_id"))
}

// ---------- 3. 取消会向下传播，不会向上传播 ----------

func cascadeCancel() string {
	parent, parentCancel := context.WithCancel(context.Background())
	child, childCancel := context.WithCancel(parent)
	defer parentCancel()
	defer childCancel()

	childCancel() // 取消子节点
	select {
	case <-parent.Done():
		return "取消子节点把父节点也取消了（错误结论）"
	default:
	}

	parentCancel() // 取消父节点
	select {
	case <-child.Done():
		return "取消父 -> 子被级联取消；取消子 -> 父不受影响"
	case <-time.After(50 * time.Millisecond):
		return "级联取消失效"
	}
}

// ---------- 4. 超时后必须把错误返回给调用方，而不是降级掩盖 ----------

func slowQuery(ctx context.Context) (string, error) {
	select {
	case <-time.After(300 * time.Millisecond):
		return "query result", nil
	case <-ctx.Done():
		return "", fmt.Errorf("slowQuery: %w", ctx.Err())
	}
}

func callerWithTimeout() string {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := slowQuery(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("上游超时: %v（应转成 504/超时错误码，不要静默返回空值）", err)
	case errors.Is(err, context.Canceled):
		return fmt.Sprintf("调用方主动取消: %v（通常是客户端断连，无需告警）", err)
	case err != nil:
		return fmt.Sprintf("其他错误: %v", err)
	}
	return "成功"
}

// ---------- 5. AfterFunc：取消时执行清理（Go 1.21+）----------

func afterFuncDemo() string {
	ctx, cancel := context.WithCancel(context.Background())
	fired := make(chan string, 1)

	stop := context.AfterFunc(ctx, func() {
		fired <- "清理已执行"
	})
	_ = stop // stop() 可在取消前撤销回调

	cancel()
	select {
	case msg := <-fired:
		return "context.AfterFunc -> " + msg
	case <-time.After(time.Second):
		return "AfterFunc 未触发"
	}
}

// ---------- 6. 忘记 cancel 的两种代价 ----------

// cancelLeakDemo 演示"忘调 cancel"的经典泄漏：
// 父 context 是 Background（永不取消），子 ctx 又没人 cancel，
// 那么子 goroutine 永远等不到 Done，永久 park —— 既不退出也不释放栈。
func cancelLeakDemo() int {
	base := runtime.NumGoroutine()

	const n = 5
	for i := 0; i < n; i++ {
		// 故意丢弃 cancel —— 这就是要演示的错误用法。
		// go vet 会在这里告警（lostcancel），正好说明静态检查能抓住这个坑。
		ctx, cancel := context.WithCancel(context.Background())
		_ = cancel // 真实代码里这里应该是 defer cancel()

		go func() {
			<-ctx.Done() // 永远不会收到信号，永久阻塞
		}()
	}
	time.Sleep(20 * time.Millisecond)
	return runtime.NumGoroutine() - base
}

// cancelFixedDemo 是同一段代码的正确写法：defer cancel，goroutine 立即收到信号退出。
func cancelFixedDemo() int {
	base := runtime.NumGoroutine()

	const n = 5
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ctx.Done()
		}()
		cancel() // 立刻取消，goroutine 收到信号后退出
	}
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	return runtime.NumGoroutine() - base
}

func Run() {
	fmt.Println("=== Q13: context 的传播、取消与工程实践 ===")

	// 1. 三种派生
	fmt.Println("--- 1. 三种派生方式 ---")
	fmt.Printf("   %s\n", withCancelDemo())
	fmt.Printf("   %s\n", withTimeoutDemo(80*time.Millisecond))
	fmt.Printf("   %s\n", deadlineChild())

	// 2. WithValue
	fmt.Println("--- 2. WithValue：key 一定要自定义类型 ---")
	fmt.Printf("   %s\n", withValueDemo())
	fmt.Println("   只放\"请求域\"的元数据（trace id、用户身份）；不要塞业务参数、可选参数、DB 连接")

	// 3. 传播方向
	fmt.Println("--- 3. 取消的传播方向 ---")
	fmt.Printf("   %s\n", cascadeCancel())

	// 4. 超时处理
	fmt.Println("--- 4. 超时/取消的区分与处理 ---")
	fmt.Printf("   %s\n", callerWithTimeout())

	// 5. AfterFunc
	fmt.Println("--- 5. Go 1.21 的 context.AfterFunc ---")
	fmt.Printf("   %s\n", afterFuncDemo())

	// 6. 泄漏
	fmt.Println("--- 6. 忘记 cancel 的代价 ---")
	fmt.Printf("   忘记 cancel：相对基线多出 %d 个存活的 goroutine（永久 park，典型泄漏）\n", cancelLeakDemo())
	fmt.Printf("   defer cancel：多出 %d 个（全部正常退出）\n", cancelFixedDemo())
	fmt.Println("   另一种代价：子 context 会一直挂在父节点上，父节点不释放则整条链都无法回收")
	fmt.Println("   检测手段：go vet（未使用 cancel）、goleak（测试里断言无泄漏）、pprof goroutine")

	// 7. 约定
	fmt.Println("--- 7. 工程约定 ---")
	fmt.Println("   ctx 永远作为第一个参数，命名统一叫 ctx，不要塞进 struct")
	fmt.Println("   不要传 nil context，不知道用什么就传 context.Background()")
	fmt.Println("   ctx 是并发安全的，但不要用它传\"可选参数\"")
	fmt.Println("   谁的 ctx 谁负责 cancel；defer cancel() 是默认动作")
	fmt.Println("   下游每个阻塞点（IO/DB/RPC/channel/lock）都要监听 ctx.Done()")
}
