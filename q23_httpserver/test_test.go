package q23_httpserver

// 测试必须放在 *_test.go 里，go test 才会发现它们。
import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------- 测试 ----------

// TestMiddlewareOrder 固定中间件顺序，防止以后重构改坏。
func TestMiddlewareOrder(t *testing.T) {
	got := middlewareOrderDemo()
	want := []string{"in:A", "in:B", "in:C", "handler", "out:C", "out:B", "out:A"}
	if len(got) != len(want) {
		t.Fatalf("顺序长度不符: got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("中间件顺序错误:\n got %v\nwant %v", got, want)
		}
	}
}

// TestHandlerStatusCodes 覆盖各接口的状态码与关键响应头。
func TestHandlerStatusCodes(t *testing.T) {
	h := handler()
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/ping", http.StatusOK},
		{"GET", "/slow", http.StatusGatewayTimeout}, // withTimeout=100ms < handler 200ms
		{"GET", "/panic", http.StatusInternalServerError},
		{"POST", "/drain", http.StatusOK},
		{"GET", "/not-exist", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: got %d, want %d", c.method, c.path, rec.Code, c.want)
		}
		if rec.Code == http.StatusOK && rec.Header().Get("X-Request-ID") == "" {
			t.Errorf("%s %s: 缺少 X-Request-ID 响应头", c.method, c.path)
		}
	}
}

// TestRecoverMiddlewareKeepsServing panic 之后服务必须还能继续处理请求。
func TestRecoverMiddlewareKeepsServing(t *testing.T) {
	h := handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应返回 500，实际 %d", rec.Code)
	}

	// 关键断言：进程还活着，且仍能正常服务
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/ping", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("panic 之后服务不可用，状态码 %d", rec2.Code)
	}
}

// TestGracefulShutdown 验证优雅关闭会等在途请求、并停止接受新连接。
func TestGracefulShutdown(t *testing.T) {
	inFlight, stopped, err := runGraceful(true)
	if err != nil {
		t.Fatal(err)
	}
	if inFlight != 0 {
		t.Errorf("关闭时仍有 %d 个在途请求", inFlight)
	}
	if !stopped {
		t.Error("Shutdown 之后不应再接受新连接")
	}
}

// ---------- 基准测试 ----------

func BenchmarkMiddlewareChain(b *testing.B) {
	h := handler()
	req := httptest.NewRequest("GET", "/ping", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkDirectHandler(b *testing.B) {
	h := buildMux()
	req := httptest.NewRequest("GET", "/ping", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
