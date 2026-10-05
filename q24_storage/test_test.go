package q24_storage

// 测试必须放在 *_test.go 里，go test 才会发现它们。
import (
	"context"
	"sync"
	"testing"
	"time"
)

// ---------- 测试 ----------

// TestNPlusOneQueryCount 用"查询次数"这个硬指标锁定 N+1 问题。
func TestNPlusOneQueryCount(t *testing.T) {
	db := newFakeDB()
	ids := make([]int, 20)
	for i := range ids {
		ids[i] = i + 1
	}

	if n := db.count(func() { _, _ = db.listUsersNPlusOne(ids) }); n != 21 {
		t.Errorf("N+1 应当产生 21 次查询，实际 %d", n)
	}
	if n := db.count(func() { _, _ = db.listUsersBatch(ids) }); n != 2 {
		t.Errorf("批量查询应当是 2 次，实际 %d", n)
	}
	if n := db.count(func() { _, _ = db.listUsersJoin(ids) }); n != 1 {
		t.Errorf("JOIN 应当是 1 次，实际 %d", n)
	}
}

// TestSingleflightCollapses 验证单飞把并发回源合并成 1 次。
func TestSingleflightCollapses(t *testing.T) {
	ctx := context.Background()
	c := newFakeCache()
	s := newStore()
	s.put("hot", "v")
	fg := newFlightGroup()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = getWithSingleflight(ctx, c, s, fg, "hot") }()
	}
	wg.Wait()

	if q := s.queriesCount(); q != 1 {
		t.Errorf("单飞应当只回源 1 次，实际 %d", q)
	}
}

// TestPoolBoundsConcurrency 验证连接池确实限制了并发上限。
func TestPoolBoundsConcurrency(t *testing.T) {
	r := simulatePool(3, 30, 10*time.Millisecond)
	if r.PeakInUse > 3 {
		t.Errorf("峰值占用 %d 超过了池上限 3", r.PeakInUse)
	}
	if r.WaitCount != 30 {
		t.Errorf("WaitCount = %d, want 30", r.WaitCount)
	}
}

// TestDeepPagingScanRows 固定深分页的扫描行数差异。
func TestDeepPagingScanRows(t *testing.T) {
	if got := deepPaging(1_000_000, 20, 10000, false); got != 200020 {
		t.Errorf("OFFSET 分页扫描行数 = %d, want 200020", got)
	}
	if got := deepPaging(1_000_000, 20, 10000, true); got != 20 {
		t.Errorf("游标分页扫描行数 = %d, want 20", got)
	}
}

// ---------- 基准测试 ----------

func BenchmarkNPlusOne(b *testing.B) {
	db := newFakeDB()
	ids := make([]int, 20)
	for i := range ids {
		ids[i] = i + 1
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = db.listUsersNPlusOne(ids)
	}
}

func BenchmarkBatchQuery(b *testing.B) {
	db := newFakeDB()
	ids := make([]int, 20)
	for i := range ids {
		ids[i] = i + 1
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = db.listUsersBatch(ids)
	}
}
