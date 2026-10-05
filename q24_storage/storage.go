package q24_storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// 说明：这一题没有真实数据库/Redis 依赖，全部用内存实现"模拟"
// 对应的并发与一致性问题。目的是把工程场景里的**错误模式和修法**
// 用可运行的代码表达出来，而不是教你怎么写 SQL。

// ---------- 1. N+1 查询：最经典的性能问题 ----------

type user struct {
	ID   int
	Name string
}

type order struct {
	ID     int
	UserID int
	Amount int
}

// fakeDB 模拟数据库，并统计"查询次数"——这是 N+1 问题的度量方式。
type fakeDB struct {
	users  map[int]user
	orders map[int][]order

	mu            sync.Mutex
	queryCount    int64
	rowsScanned   int64
	queryDuration time.Duration
}

func newFakeDB() *fakeDB {
	db := &fakeDB{
		users:         map[int]user{},
		orders:        map[int][]order{},
		queryDuration: 50 * time.Microsecond, // 模拟一次查询的网络+解析开销
	}
	for i := 1; i <= 20; i++ {
		db.users[i] = user{ID: i, Name: fmt.Sprintf("user-%d", i)}
		for j := 0; j < 3; j++ {
			db.orders[i] = append(db.orders[i], order{
				ID: i*10 + j, UserID: i, Amount: 100 * (j + 1),
			})
		}
	}
	return db
}

func (db *fakeDB) count(fn func()) int64 {
	before := atomic.LoadInt64(&db.queryCount)
	fn()
	return atomic.LoadInt64(&db.queryCount) - before
}

// query 模拟一次"数据库往返"：累计计数 + 睡眠模拟延迟。
func (db *fakeDB) query(rows int) {
	atomic.AddInt64(&db.queryCount, 1)
	atomic.AddInt64(&db.rowsScanned, int64(rows))
	time.Sleep(db.queryDuration)
}

// listUsersNPlusOne 是 N+1 反模式：
// 1 次查用户列表 + N 次查每个用户的订单 = N+1 次查询。
func (db *fakeDB) listUsersNPlusOne(ids []int) (map[int][]order, error) {
	result := make(map[int][]order, len(ids))

	db.query(len(ids)) // 第 1 次：查用户列表
	for _, id := range ids {
		db.query(len(db.orders[id])) // 第 N 次：每个用户单独查订单
		result[id] = db.orders[id]
	}
	return result, nil
}

// listUsersBatch 是修法一：批量查询（IN 一次捞回来，再在内存里分组）。
func (db *fakeDB) listUsersBatch(ids []int) (map[int][]order, error) {
	result := make(map[int][]order, len(ids))

	db.query(len(ids)) // 第 1 次：查用户列表
	db.query(0)        // 第 2 次：WHERE user_id IN (...) 一次捞全部订单
	for _, id := range ids {
		result[id] = db.orders[id]
	}
	return result, nil
}

// listUsersJoin 是修法二：JOIN 一次查询拿全（要注意笛卡尔积放大行数）。
func (db *fakeDB) listUsersJoin(ids []int) (map[int][]order, error) {
	result := make(map[int][]order, len(ids))

	total := 0
	for _, id := range ids {
		total += len(db.orders[id])
	}
	db.query(total) // 1 次查询，但返回 total 行
	for _, id := range ids {
		result[id] = db.orders[id]
	}
	return result, nil
}

// ---------- 2. 连接池：参数配错的两种典型病 ----------

type poolStats struct {
	Open        int64
	InUse       int64
	WaitCount   int64
	WaitSeconds float64
}

// simulatePool 模拟连接池行为，演示"池太小"vs"池太大"。
//
//	maxOpen  —— 最大连接数（池太小 -> 请求排队；太大 -> 打爆数据库）
//	workers  —— 并发请求数
//	holdTime —— 每个请求占用连接的时间
type poolSimResult struct {
	TotalTime   time.Duration
	MaxWait     time.Duration
	WaitCount   int64
	PeakInUse   int64
	Throughput  float64
	QueueLength int
}

func simulatePool(maxOpen, workers int, holdTime time.Duration) poolSimResult {
	sem := make(chan struct{}, maxOpen)
	var wg sync.WaitGroup

	var inUse, peakInUse, waitCount int64
	var maxWait int64

	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			acquireStart := time.Now()
			sem <- struct{}{} // 池满则在这里排队
			waited := time.Since(acquireStart)

			atomic.AddInt64(&waitCount, 1)
			cur := atomic.AddInt64(&inUse, 1)
			for {
				old := atomic.LoadInt64(&peakInUse)
				if cur <= old || atomic.CompareAndSwapInt64(&peakInUse, old, cur) {
					break
				}
			}
			for {
				old := atomic.LoadInt64(&maxWait)
				if int64(waited) <= old || atomic.CompareAndSwapInt64(&maxWait, old, int64(waited)) {
					break
				}
			}

			time.Sleep(holdTime) // 模拟用连接干活

			atomic.AddInt64(&inUse, -1)
			<-sem
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	return poolSimResult{
		TotalTime:  elapsed,
		MaxWait:    time.Duration(atomic.LoadInt64(&maxWait)),
		WaitCount:  atomic.LoadInt64(&waitCount),
		PeakInUse:  atomic.LoadInt64(&peakInUse),
		Throughput: float64(workers) / elapsed.Seconds(),
	}
}

// ---------- 3. 缓存三大问题：穿透 / 击穿 / 雪崩 ----------

// fakeCache 模拟 Redis，带命中统计。
type fakeCache struct {
	mu     sync.Mutex
	data   map[string]string
	hits   int64
	misses int64
}

func newFakeCache() *fakeCache {
	return &fakeCache{data: map[string]string{}}
}

func (c *fakeCache) Get(_ context.Context, key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.data[key]; ok {
		atomic.AddInt64(&c.hits, 1)
		return v, true
	}
	atomic.AddInt64(&c.misses, 1)
	return "", false
}

func (c *fakeCache) Set(_ context.Context, key, val string, _ time.Duration) {
	c.mu.Lock()
	c.data[key] = val
	c.mu.Unlock()
}

func (c *fakeCache) Stats() (hits, misses int64) {
	return atomic.LoadInt64(&c.hits), atomic.LoadInt64(&c.misses)
}

// store 模拟回源（慢，且有 QPS 上限）。
type store struct {
	mu       sync.Mutex
	data     map[string]string
	queries  int64
	latency  time.Duration
	notFound map[string]bool
}

func newStore() *store {
	return &store{
		data:     map[string]string{},
		latency:  20 * time.Millisecond,
		notFound: map[string]bool{},
	}
}

func (s *store) get(_ context.Context, key string) (string, error) {
	atomic.AddInt64(&s.queries, 1)
	time.Sleep(s.latency)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notFound[key] {
		return "", sql.ErrNoRows
	}
	if v, ok := s.data[key]; ok {
		return v, nil
	}
	return "", sql.ErrNoRows
}

func (s *store) put(key, val string) {
	s.mu.Lock()
	s.data[key] = val
	s.mu.Unlock()
}

func (s *store) queriesCount() int64 { return atomic.LoadInt64(&s.queries) }

// getNoProtection 是"裸读缓存"，三种问题都会中招。
func getNoProtection(ctx context.Context, c *fakeCache, s *store, key string) (string, error) {
	if v, ok := c.Get(ctx, key); ok {
		return v, nil
	}
	v, err := s.get(ctx, key)
	if err != nil {
		return "", err // ✗ 不存在的 key 不写缓存 -> 每次都打 DB（穿透）
	}
	c.Set(ctx, key, v, time.Minute)
	return v, nil
}

// getWithSingleflight 用"单飞"合并并发回源（解决击穿）。
// 同一个 key 的并发请求只让一个去查 DB，其余等结果。
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	val  string
	err  error
	dups int
}

func newFlightGroup() *flightGroup {
	return &flightGroup{calls: map[string]*flightCall{}}
}

func (g *flightGroup) Do(key string, fn func() (string, error)) (string, error, bool) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		c.dups++
		g.mu.Unlock()
		<-c.done // 等第一个请求的结果
		return c.val, c.err, true
	}
	c := &flightCall{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	close(c.done)

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()

	return c.val, c.err, false
}

func getWithSingleflight(ctx context.Context, c *fakeCache, s *store, g *flightGroup, key string) (string, error) {
	if v, ok := c.Get(ctx, key); ok {
		return v, nil
	}

	v, err, shared := g.Do(key, func() (string, error) {
		// 双重检查：可能在等锁期间别人已经写进缓存了
		if v, ok := c.Get(ctx, key); ok {
			return v, nil
		}
		v, err := s.get(ctx, key)
		if err != nil {
			// ✓ 解决穿透：不存在也缓存（空值），但要设置较短的 TTL
			if errors.Is(err, sql.ErrNoRows) {
				c.Set(ctx, key, "", 10*time.Second)
			}
			return "", err
		}
		c.Set(ctx, key, v, time.Minute)
		return v, nil
	})
	_ = shared
	return v, err
}

// ---------- 4. 缓存与 DB 的一致性 ----------

// updateOrder 演示"先写 DB 再删缓存"（Cache-Aside 的推荐顺序）。
func updateOrder(ctx context.Context, c *fakeCache, s *store, key, newVal string) {
	s.put(key, newVal) // 1) 先落库
	c.mu.Lock()        // 2) 再删缓存（不是更新缓存）
	delete(c.data, key)
	c.mu.Unlock()
}

// ---------- 5. 分页与深分页 ----------

// deepPaging 对比 LIMIT OFFSET 与游标分页的扫描行数。
func deepPaging(totalRows, pageSize, targetPage int, cursorBased bool) (rowsScanned int) {
	if cursorBased {
		// 游标分页：WHERE id > last_id LIMIT n —— 只扫 pageSize 行
		return pageSize
	}
	// LIMIT offset, n —— 数据库要扫过 offset 行才能丢弃
	return targetPage*pageSize + pageSize
}

func Run() {
	fmt.Println("=== Q24: 数据库与缓存 —— 场景题 ===")

	// 1. N+1
	fmt.Println("--- 1. N+1 查询 ---")
	db := newFakeDB()
	ids := make([]int, 0, 20)
	for i := 1; i <= 20; i++ {
		ids = append(ids, i)
	}

	n1 := db.count(func() { _, _ = db.listUsersNPlusOne(ids) })
	start := time.Now()
	_, _ = db.listUsersNPlusOne(ids)
	n1Time := time.Since(start)

	batch := db.count(func() { _, _ = db.listUsersBatch(ids) })
	start = time.Now()
	_, _ = db.listUsersBatch(ids)
	batchTime := time.Since(start)

	join := db.count(func() { _, _ = db.listUsersJoin(ids) })
	start = time.Now()
	_, _ = db.listUsersJoin(ids)
	joinTime := time.Since(start)

	fmt.Printf("   20 个用户的订单：\n")
	fmt.Printf("     N+1 反模式 : %2d 次查询, 耗时 %v\n", n1, n1Time.Round(time.Microsecond))
	fmt.Printf("     批量 IN     : %2d 次查询, 耗时 %v\n", batch, batchTime.Round(time.Microsecond))
	fmt.Printf("     单次 JOIN   : %2d 次查询, 耗时 %v（返回 %d 行）\n",
		join, joinTime.Round(time.Microsecond), atomic.LoadInt64(&db.rowsScanned))
	fmt.Printf("   N+1 比批量慢约 %.1f 倍；用户数越多差距越大（线性放大）\n",
		float64(n1Time)/float64(batchTime))
	fmt.Println("   排查手段：GORM 的 Preload/Joins、慢查询日志、APM 里的\"同一 SQL 重复执行\"告警")
	fmt.Println("   注意 JOIN 的代价：一对多会产生笛卡尔积，行数膨胀，要权衡")

	// 2. 连接池
	fmt.Println("--- 2. 连接池参数（20 个并发请求，每个占用 50ms）---")
	fmt.Printf("   %-8s %-14s %-14s %-10s %s\n", "MaxOpen", "总耗时", "最大排队", "峰值占用", "吞吐(req/s)")
	for _, maxOpen := range []int{2, 5, 20, 100} {
		r := simulatePool(maxOpen, 20, 50*time.Millisecond)
		fmt.Printf("   %-8d %-14v %-14v %-10d %.1f\n",
			maxOpen, r.TotalTime.Round(time.Millisecond), r.MaxWait.Round(time.Millisecond),
			r.PeakInUse, r.Throughput)
	}
	fmt.Println("   池太小（2）：请求排队，P99 飙升，表现为\"服务变慢但 CPU/DB 都很闲\"")
	fmt.Println("   池太大（100）：DB 侧连接数爆掉，反而不如适度；")
	fmt.Println("   经验值：MaxOpen ≈ DB 能承受的并发 / 服务实例数，常见 10~50")
	fmt.Println("   另外两个参数：MaxIdle（不要小于 MaxOpen，否则连接反复建销）、")
	fmt.Println("                ConnMaxLifetime（必须小于 DB 的 wait_timeout，防\"连接已失效\"）")

	// 3. 缓存穿透
	fmt.Println("--- 3. 缓存穿透（查一个不存在的 key）---")
	ctx := context.Background()

	c1, s1 := newFakeCache(), newStore()
	const missing = "user:999999"
	for i := 0; i < 50; i++ {
		_, _ = getNoProtection(ctx, c1, s1, missing)
	}
	fmt.Printf("   无保护：50 次请求 -> DB 查询 %d 次（每次都穿透）\n", s1.queriesCount())

	c2, s2 := newFakeCache(), newStore()
	fg := newFlightGroup()
	for i := 0; i < 50; i++ {
		_, _ = getWithSingleflight(ctx, c2, s2, fg, missing)
	}
	fmt.Printf("   空值缓存 + 单飞：50 次请求 -> DB 查询 %d 次\n", s2.queriesCount())
	fmt.Println("   穿透的三种解法：")
	fmt.Println("     1) 缓存空值（TTL 短一点，如 10s）")
	fmt.Println("     2) 布隆过滤器（提前挡掉绝对不存在的 key）")
	fmt.Println("     3) 参数校验 + 接口限流（挡住恶意随机 key 扫描）")

	// 4. 缓存击穿
	fmt.Println("--- 4. 缓存击穿（热点 key 过期瞬间）---")
	c3, s3 := newFakeCache(), newStore()
	s3.put("hot:key", "value")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = getNoProtection(ctx, c3, s3, "hot:key") }()
	}
	wg.Wait()
	fmt.Printf("   无保护：100 个并发请求（缓存为空）-> DB 查询 %d 次\n", s3.queriesCount())

	c4, s4 := newFakeCache(), newStore()
	s4.put("hot:key", "value")
	fg2 := newFlightGroup()
	var wg2 sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg2.Add(1)
		go func() { defer wg2.Done(); _, _ = getWithSingleflight(ctx, c4, s4, fg2, "hot:key") }()
	}
	wg2.Wait()
	fmt.Printf("   单飞合并：100 个并发请求 -> DB 查询 %d 次\n", s4.queriesCount())
	fmt.Println("   击穿专指\"单个热点 key 失效瞬间被并发打爆\"，")
	fmt.Println("   解法：单飞（推荐）、逻辑过期（后台异步续期、永远不真过期）、互斥锁重建")

	// 5. 缓存雪崩
	fmt.Println("--- 5. 缓存雪崩（大量 key 同时失效）---")
	fmt.Println("   成因：批量预热时用了相同 TTL，或 Redis 整体宕机")
	fmt.Println("   解法：")
	fmt.Println("     1) TTL 加随机抖动：base + rand(0, base*10%)")
	fmt.Println("     2) 多级缓存：本地缓存（bigcache）+ Redis，本地顶一段时间")
	fmt.Println("     3) 限流降级：Redis 挂了直接走降级逻辑，别让请求全打到 DB")
	fmt.Println("     4) 集群高可用：哨兵/Cluster，避免单点")

	// 6. 一致性
	fmt.Println("--- 6. 缓存与 DB 的一致性 ---")
	c5, s5 := newFakeCache(), newStore()
	s5.put("k", "v1")
	c5.Set(ctx, "k", "v1", time.Minute)
	updateOrder(ctx, c5, s5, "k", "v2")
	got, hit := c5.Get(ctx, "k")
	fmt.Printf("   先写 DB 再删缓存：缓存命中=%v（已失效），重新回源会拿到最新值\n", hit)
	_ = got

	fmt.Println("   四种策略的取舍：")
	fmt.Println("     Cache-Aside（先更库再删缓存）：最常用，实现简单")
	fmt.Println("     Read/Write Through：由缓存层代理读写，一致性更好但复杂")
	fmt.Println("     Write Behind：只写缓存，异步刷库，性能最好但有丢数据风险")
	fmt.Println("   经典争议：\"先删缓存再更库\" vs \"先更库再删缓存\"")
	fmt.Println("     - 先删缓存：删除后、更新前有窗口，读请求会把旧值写回缓存（更危险）")
	fmt.Println("     - 先更库：仍是常见推荐；极端不一致窗口靠\"延迟双删\"或 CDC 兜底")
	fmt.Println("   结论：不要指望强一致，靠\"最终一致 + 短 TTL + 版本号\"来收敛")

	// 7. 深分页
	fmt.Println("--- 7. 深分页（LIMIT OFFSET 的性能陷阱）---")
	const total, pageSize = 1_000_000, 20
	for _, page := range []int{1, 100, 10000, 50000} {
		offsetScan := deepPaging(total, pageSize, page, false)
		cursorScan := deepPaging(total, pageSize, page, true)
		fmt.Printf("   第 %6d 页: LIMIT OFFSET 需扫描 %8d 行, 游标分页只扫 %d 行\n",
			page, offsetScan, cursorScan)
	}
	fmt.Println("   原因：OFFSET 是\"先扫过再丢弃\"，页越深越慢")
	fmt.Println("   解法：游标分页（WHERE id > last_id ORDER BY id LIMIT n）")
	fmt.Println("        或延迟关联（先走覆盖索引拿 id，再回表取数据）")

	fmt.Println("--- 8. 其他高频追问 ---")
	fmt.Println("   事务隔离级别：MySQL 默认 RR，靠 MVCC + 间隙锁；")
	fmt.Println("                 要注意\"当前读\"与\"快照读\"的差别")
	fmt.Println("   死锁：按固定顺序访问资源 + 缩短事务 + 设置 innodb_lock_wait_timeout")
	fmt.Println("   SQL 注入：一律用参数化查询（`?` 占位符），绝不字符串拼接")
	fmt.Println("   大事务：会长时间持有锁和 undo log，要拆小批量提交")
	fmt.Println("   索引失效：函数包裹列、隐式类型转换、前导模糊 LIKE、OR 混用")
}
