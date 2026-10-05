package q12_panic

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
)

// ---------- 1. 正确姿势：defer 里直接调用 recover ----------

func safeRun(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// 记录调用栈，否则现场信息会丢
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			_ = n
			err = fmt.Errorf("捕获 panic: %v (%T)", r, r)
		}
	}()
	return f()
}

// ---------- 2. 错误姿势：recover 在嵌套函数里 ----------

func recoverWrong() any {
	return recover() // 调用栈里没有它自己的 defer，recover 返回 nil
}

func nestedRecover() (result string) {
	defer func() {
		r := recoverWrong() // 隔了一层，recover 失效
		result = fmt.Sprintf("嵌套 recover 拿到: %v", r)
	}()
	panic("这个 panic 不会被上面那层拦住")
}

// ---------- 3. defer + recover 修改命名返回值（返回错误而不是崩溃） ----------

func divide(a, b int) (result int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("divide(%d,%d) 失败: %v", a, b, r)
			result = 0
		}
	}()
	return a / b, nil // b == 0 时触发 runtime panic
}

// ---------- 4. 显式 panic 与自定义 panic 值 ----------

type FatalConfig struct{ Key string }

func (e *FatalConfig) Error() string { return "缺少必需配置: " + e.Key }

func loadConfig(key string, cfg map[string]string) string {
	if _, ok := cfg[key]; !ok {
		panic(&FatalConfig{Key: key}) // panic 的值可以是任意类型
	}
	return cfg[key]
}

// ---------- 5. defer 的执行顺序与 recover 的作用域 ----------

func deferOrder() []string {
	var trace []string
	func() {
		defer func() { trace = append(trace, "defer-1") }()
		defer func() { trace = append(trace, "defer-2") }()
		defer func() {
			if r := recover(); r != nil {
				trace = append(trace, fmt.Sprintf("recover:%v", r))
			}
		}()
		trace = append(trace, "body")
		panic("boom")
	}()
	trace = append(trace, "after")
	return trace
}

// ---------- 6. panic 在 goroutine 里 = 整个进程崩 ----------

func goroutinePanic(crash bool) string {
	var wg sync.WaitGroup
	wg.Add(1)

	recovered := make(chan bool, 1)
	go func() {
		defer wg.Done()
		defer func() {
			// 只有子 goroutine 自己的 defer 能救它
			recovered <- recover() != nil
		}()
		if crash {
			panic("子 goroutine panic")
		}
	}()

	wg.Wait()
	select {
	case ok := <-recovered:
		if ok {
			return "子 goroutine 自己 recover 了，主进程存活"
		}
		return "子 goroutine 正常结束"
	default:
		return "没有 recover，主进程会直接退出（exit status 2）"
	}
}

func Run() {
	fmt.Println("=== Q12: panic / recover 的控制流与边界 ===")

	// 1. 正确姿势
	fmt.Println("--- 1. 唯一正确的 recover 姿势：defer 中直接调用 ---")
	err := safeRun(func() error {
		panic("业务代码炸了")
	})
	fmt.Printf("   safeRun 返回 error 而非崩溃: %v\n", err)

	err = safeRun(func() error { return nil })
	fmt.Printf("   没有 panic 时正常返回: %v\n", err)

	// 2. 错误姿势
	fmt.Println("--- 2. 把 recover 包进函数 = 失效 ---")
	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		_ = nestedRecover()
		return
	}()
	fmt.Printf("   nestedRecover 里的 recover 没拦住，最终由外层兜住: %v\n", panicked)
	fmt.Println("   规则：recover 只在\"panic 正在展开、且当前 goroutine 的 defer 直接调用它\"时有效")

	// 3. 修改命名返回值
	fmt.Println("--- 3. defer + recover 改写命名返回值 ---")
	for _, c := range [][2]int{{10, 2}, {10, 0}} {
		r, e := divide(c[0], c[1])
		fmt.Printf("   divide(%d,%d) -> result=%d err=%v\n", c[0], c[1], r, e)
	}

	// 4. 显式 panic
	fmt.Println("--- 4. 带类型的 panic 值（可被 errors.As 还原）---")
	_, perr := func() (v string, err error) {
		defer func() {
			if r := recover(); r != nil {
				if e, ok := r.(error); ok {
					err = e // panic 的是 error，直接当 error 用
				} else {
					err = fmt.Errorf("%v", r)
				}
			}
		}()
		return loadConfig("DB_DSN", map[string]string{"PORT": "8080"}), nil
	}()
	var fc *FatalConfig
	fmt.Printf("   错误: %v\n", perr)
	fmt.Printf("   errors.As 还原类型: %v\n", errors.As(perr, &fc))

	// 5. defer 顺序
	fmt.Println("--- 5. defer 后进先出 ---")
	fmt.Printf("   trace: %v\n", deferOrder())

	// 6. goroutine panic
	fmt.Println("--- 6. 子 goroutine 里的 panic ---")
	fmt.Printf("   自行 recover: %s\n", goroutinePanic(true))
	fmt.Println("   如果子 goroutine 没有 defer recover，测试它只能靠测试进程隔离——")
	fmt.Println("   所以每个长期运行的 goroutine 入口都应该带上 recover 兜底")

	// 7. 工程约定
	fmt.Println("--- 7. 工程约定 ---")
	fmt.Println("   recover 必须配 runtime.Stack 记录栈，否则日志里只有一行原因")
	fmt.Println("   HTTP/GRPC/mq/定时任务 的顶层 handler 必须 recover，避免单个请求打挂进程")
	fmt.Println("   recover 之后不要让流程\"继续往下走\"，要么返回 error，要么退出该 goroutine")
}
