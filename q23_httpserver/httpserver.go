package q23_httpserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 1. 超时级联：三层超时怎么配 ----------

// handlerWithTimeout 演示"服务端超时要比客户端超时短"的原则。
// 读超时 < 处理超时 < 写超时 是常见配法，但真正关键的是：
// 服务端各阶段超时必须小于调用方给它留的时间，否则调用方先超时、
// 服务端的活儿白干，还会造成"重复计算"。
type timeoutConfig struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	HandlerTimeout    time.Duration
}

// recommendedConfig 是面向"网关/API 服务"的一组保守配置。
func recommendedConfig() timeoutConfig {
	return timeoutConfig{
		ReadHeaderTimeout: 5 * time.Second,  // 防御 Slowloris 攻击
		ReadTimeout:       10 * time.Second, // 含 body 读取
		WriteTimeout:      15 * time.Second, // 含 handler 执行 + 写回
		IdleTimeout:       60 * time.Second, // keep-alive 空闲回收
		HandlerTimeout:    8 * time.Second,  // 业务自身超时，要小于 WriteTimeout
	}
}

// ---------- 2. 中间件：超时 + Request ID + 优雅错误 ----------

type ctxKey string

const keyRequestID ctxKey = "request_id"

var reqCounter int64

// withRequestID 注入请求 id，并把它透传到 context（见 Q13）。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = fmt.Sprintf("req-%d", atomic.AddInt64(&reqCounter, 1))
		}
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withTimeout 给每个请求套一个处理超时，避免慢请求拖住连接。
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRecover 保证单个请求的 panic 不会打挂整个服务（见 Q12）。
func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				buf := make([]byte, 2048)
				n := runtime.Stack(buf, false)
				fmt.Fprintf(os.Stderr, "[panic] %v req=%v\n%s\n", rec, r.URL.Path, buf[:n])
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------- 3. 用 httptest 做无网络依赖的测试 ----------

// buildMux 组装路由。用标准库 1.22+ 的方法+路径模式。
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(keyRequestID).(string)
		fmt.Fprintf(w, "pong id=%s", id)
	})

	// 慢接口：演示服务端超时
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			fmt.Fprint(w, "slow done")
		case <-r.Context().Done():
			// 必须处理这个分支，否则 goroutine 会继续跑完，白费 CPU
			http.Error(w, "timeout", http.StatusGatewayTimeout)
		}
	})

	// 会 panic 的接口：演示 recover 中间件
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]int
		m["boom"] = 1 // 向 nil map 写入 -> panic
	})

	// 未读 body 就返回：连接无法复用，是常见的性能陷阱
	mux.HandleFunc("POST /drain", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		fmt.Fprint(w, "drained")
	})

	return mux
}

func handler() http.Handler {
	return withRecover(withRequestID(withTimeout(100*time.Millisecond, buildMux())))
}

// ---------- 4. 优雅关闭 ----------

// runGraceful 演示完整的优雅关闭流程：
// 收到信号 -> 停止接受新连接 -> 等在途请求结束（带兜底超时）-> 退出。
func runGraceful(quiet bool) (inFlightHandled int32, stoppedAccepting bool, err error) {
	srv := &http.Server{Handler: handler()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, false, err
	}
	addr := ln.Addr().String()

	var inFlight int64
	var wg sync.WaitGroup

	// 一个"很慢"的接口，用来制造在途请求
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&inFlight, 1)
		defer atomic.AddInt64(&inFlight, -1)
		select {
		case <-time.After(150 * time.Millisecond):
			fmt.Fprint(w, "ok")
		case <-r.Context().Done():
		}
	})
	srv.Handler = mux

	go func() { _ = srv.Serve(ln) }()
	if !quiet {
		fmt.Printf("   服务启动于 %s\n", addr)
	}

	// 发起一个在途请求
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + addr + "/slow")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(30 * time.Millisecond) // 确保请求已经进入 handler

	// 触发优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = srv.Shutdown(ctx) // 等待在途请求完成
	}()

	wg.Wait() // 等在途请求结束
	time.Sleep(50 * time.Millisecond)

	// 关闭后新连接应当被拒绝
	_, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	stoppedAccepting = dialErr != nil

	return int32(atomic.LoadInt64(&inFlight)), stoppedAccepting, nil
}

// ---------- 5. 中间件顺序的重要性 ----------

func middlewareOrderDemo() []string {
	var order []string
	mk := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, "in:"+name)
				next.ServeHTTP(w, r)
				order = append(order, "out:"+name)
			})
		}
	}
	h := mk("A")(mk("B")(mk("C")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}))))
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	return order
}

func Run() {
	fmt.Println("=== Q23: HTTP 服务工程化 —— 超时、中间件与优雅关闭 ===")

	// 1. 超时配置
	fmt.Println("--- 1. Server 的四类超时 + 业务超时 ---")
	cfg := recommendedConfig()
	fmt.Printf("   ReadHeaderTimeout = %v  （防御 Slowloris，必须有）\n", cfg.ReadHeaderTimeout)
	fmt.Printf("   ReadTimeout       = %v  （含 body 读取）\n", cfg.ReadTimeout)
	fmt.Printf("   WriteTimeout      = %v  （含 handler 执行 + 写回）\n", cfg.WriteTimeout)
	fmt.Printf("   IdleTimeout       = %v  （keep-alive 空闲回收）\n", cfg.IdleTimeout)
	fmt.Printf("   HandlerTimeout    = %v  （业务自身，必须 < WriteTimeout）\n", cfg.HandlerTimeout)
	fmt.Println("   最常见的线上事故：一个超时都没设（零值 = 永不超时），")
	fmt.Println("   结果几个慢连接就把连接数和 goroutine 耗光")

	// 2. 中间件链
	fmt.Println("--- 2. 中间件链与执行顺序 ---")
	fmt.Printf("   %v\n", middlewareOrderDemo())
	fmt.Println("   洋葱模型：注册顺序 = 进入顺序 = 退出顺序的逆序")
	fmt.Println("   顺序很关键，推荐由外到内：")
	fmt.Println("     Recover -> RequestID -> 日志 -> 限流 -> 超时 -> 鉴权 -> 业务")
	fmt.Println("   recover 必须最外层，才能兜住内层全部 panic；")
	fmt.Println("   超时放在鉴权外面，避免鉴权本身把时间耗光")

	// 3. httptest：不起真实端口也能测
	fmt.Println("--- 3. 用 httptest 做无端口测试 ---")
	h := handler()
	for _, tc := range []struct {
		method, path string
		wantCode     int
	}{
		{"GET", "/ping", 200},
		{"GET", "/slow", 200},   // withTimeout=100ms，但 handler 要 200ms
		{"GET", "/panic", 500},  // 被 recover 兜住
		{"POST", "/drain", 200}, // 读完 body
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("body"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Code
		mark := "ok"
		if got != tc.wantCode {
			mark = fmt.Sprintf("(期望 %d)", tc.wantCode)
		}
		fmt.Printf("   %-4s %-8s -> %d %s\n", tc.method, tc.path, got, mark)
	}
	fmt.Println("   注意 /slow：中间件 100ms 超时先生效，返回 504，")
	fmt.Println("   而 handler 里如果没监听 ctx.Done()，goroutine 仍会跑到 200ms —— ")
	fmt.Println("   这就是\"超时了但 CPU 还在烧\"的原因，必须在每个阻塞点监听 ctx")

	// 4. 优雅关闭
	fmt.Println("--- 4. 优雅关闭（Shutdown 而不是 Close）---")
	inFlight, stopped, err := runGraceful(false)
	fmt.Printf("   关闭时在途请求数=%d，新连接被拒=%v, err=%v\n", inFlight, stopped, err)
	fmt.Println("   流程：收到 SIGTERM -> srv.Shutdown(ctx) ->")
	fmt.Println("         停止接受新连接 + 等待在途请求 -> 超时则强制退出")
	fmt.Println("   区别：srv.Close() 会立刻掐断所有连接（用户看到 502/连接重置）")
	fmt.Println("   配套要做的事：")
	fmt.Println("     - 从服务注册中心摘除自己（等一个心跳周期再 Shutdown）")
	fmt.Println("     - 关闭 DB/Redis/MQ 连接池、flush 埋点")
	fmt.Println("     - 给 Shutdown 设兜底超时（如 15s），别无限等")

	// 5. 连接复用与 body
	fmt.Println("--- 5. 容易忽略的客户端细节 ---")
	fmt.Println("   resp.Body 必须 Close，而且最好读完（或 io.Copy 到 Discard）")
	fmt.Println("   否则 keep-alive 连接无法复用，表现为\"连接数莫名增长\"")
	fmt.Println("   http.Client 一定要设 Timeout（默认无超时，会一直挂着）")
	fmt.Println("   连接池参数：MaxIdleConnsPerHost 默认只有 2，高并发下要调大")

	// 6. 工程清单
	fmt.Println("--- 6. 上线前检查清单 ---")
	fmt.Println("   [ ] 四类超时都设了，且 HandlerTimeout < WriteTimeout")
	fmt.Println("   [ ] 最外层有 recover 中间件，且记录 stack")
	fmt.Println("   [ ] 优雅关闭接上了 SIGTERM/SIGINT，并设了兜底超时")
	fmt.Println("   [ ] /healthz（存活）与 /readyz（就绪）分开，K8s 探针用对")
	fmt.Println("   [ ] 暴露 /metrics 与 /debug/pprof（且不对外网开放）")
	fmt.Println("   [ ] 请求日志带 request id / trace id / 耗时 / 状态码")
	fmt.Println("   [ ] body 大小限制（http.MaxBytesReader）防大包打爆内存")
	fmt.Println("   [ ] 慢接口单独限流，避免拖垮整个连接池")

	fmt.Println("--- 7. 关于 net/http 的一个高频追问 ---")
	fmt.Println("   \"每请求一个 goroutine 会不会太多？\"")
	fmt.Printf("   当前 goroutine 数 = %d\n", runtime.NumGoroutine())
	fmt.Println("   不会。goroutine 阻塞在网络读时会被 park，不占 OS 线程（见 Q8/Q22）；")
	fmt.Println("   受限于 fd 和内存，而不是线程数。真正要防的是\"无超时的慢请求堆积\"")
}
