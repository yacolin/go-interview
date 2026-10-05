package q21_race

import (
	"os"
	"testing"
)

// TestRaceDemo 故意制造数据竞争，用来演示 race detector 的报告格式。
//
// 它默认跳过，因为**检测到竞争会让测试进程以失败退出**，
// 那样 `go test -race ./...` 就永远不可能全绿，不适合放进 CI。
//
// 想亲眼看报告就显式打开：
//
//	RACE_DEMO=1 go test -race -run TestRaceDemo -v ./q21_race
//
// 输出的 WARNING: DATA RACE 会带上冲突双方的完整调用栈。
func TestRaceDemo(t *testing.T) {
	if os.Getenv("RACE_SCENARIO") != "" {
		t.Skip("子进程入口，不在这里执行")
	}
	if os.Getenv("RACE_DEMO") == "" {
		t.Skip("需要 RACE_DEMO=1 才运行（它会制造真实竞争并让本进程失败）")
	}
	t.Log("下面两次调用在 -race 下会分别报出 counter 竞争和字段竞争")
	_ = counterRace(10)
	_, _ = partialLockRace(10)
}

// TestFatalScenario 是被子进程调用的入口：
// 并发写 map 会让进程 fatal 退出，只能在子进程里演示。
func TestFatalScenario(t *testing.T) {
	switch os.Getenv("RACE_SCENARIO") {
	case "":
		t.Skip("仅作为子进程入口使用")
	case "maprace":
		mapRace(100)
	case "counter":
		_ = counterRace(100)
	default:
		t.Fatalf("未知场景 %q", os.Getenv("RACE_SCENARIO"))
	}
}

// TestFixedVersionsAreRaceFree 在 -race 下验证三种修法都是干净的。
func TestFixedVersionsAreRaceFree(t *testing.T) {
	if got := fixAtomic(500); got != 500 {
		t.Errorf("atomic 版本: got %d, want 500", got)
	}
	if got := fixMutex(500); got != 500 {
		t.Errorf("Mutex 版本: got %d, want 500", got)
	}
	if got := fixChannel(500); got != 500 {
		t.Errorf("channel 版本: got %d, want 500", got)
	}
	if got := workerQueue(100); got != 5050 {
		t.Errorf("channel 交接: got %d, want 5050", got)
	}
}
