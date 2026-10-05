# Go 面试题解析（3~5 年 → 资深/架构）

本文档只做**文字解析**，配套代码见项目各子包。共 **15 题**：

- **第 1~5 题（3~5 年经验）**：slice 底层、defer 语义、并发模型、map 并发安全、内存与指针陷阱。
- **第 6~15 题（5 年以上 / 资深）**：内存与逃逸、GC 调优、GMP 调度、channel 内部结构、接口与类型系统、错误链设计、panic 恢复边界、context 传播、泛型选型、性能分析。

## 怎么跑

```bash
go run .            # 打印用法
go run . 7          # 只跑第 7 题
go run . 0          # 按顺序跑完全部 15 题
go run . 6 7 8      # 跑多题

# 第 7 题可以配合环境变量观察
GODEBUG=gctrace=1 go run . 7
GOGC=400 go run . 7

# 第 8 题可以配合调度器追踪
GODEBUG=schedtrace=1000 go run . 8

# 第 15 题是唯一的 benchmark/测试包
go test -bench=. -benchmem ./q15_pprof
go test -v -run Test ./q15_pprof
go build -gcflags='-m' ./q6_memory
```

---

## 第 1 题：slice 底层与陷阱

### 题目回顾

对一个切片做 `s[1:3]` 得到子切片后，再对它 `append` 一个元素，原切片会不会被改动？

### 核心考点

- slice 的三要素：指针、长度（len）、容量（cap）
- 子切片与原切片**共享底层数组**
- `append` 在容量足够时**不扩容**，直接写入底层数组
- 容量不够时才分配新数组并拷贝

### 解析

子切片 `s[1:3]` 的指针指向原切片索引 1 的位置，长度是 2，容量是 4（从索引 1 到原底层数组末尾）。

当对它 `append` 时，长度 2 小于容量 4，**不需要扩容**，于是直接在底层数组索引 3 的位置写入新值。因为原切片和子切片共享同一个底层数组，原切片对应位置的值也就被改写了。

所以原切片被"意外"修改，输出里索引 3 的位置变成了新追加的值。

### 如何隔离

有两种思路：

1. **限制容量**：用三索引切片 `s[1:3:3]`，把容量也限制为 2。这样 `append` 时容量不足，会触发扩容，分配新数组，与原切片彻底分开。

2. **显式拷贝**：用 `copy` 把需要的元素复制到一个新切片，再对新切片 `append`。新切片和原切片没有任何共享关系。

### 延伸

判断一个切片操作是否会影响原切片，关键看三点：是否共享底层数组、len 和 cap 分别是多少、后续操作会不会触发扩容。只看 len 是不够的，cap 往往才是决定性的。

---

## 第 2 题：defer 与返回值

### 题目回顾

两个函数，一个用命名返回值，一个用匿名返回值，都在 defer 里对变量加 1，返回值分别是什么？

### 核心考点

- `return` 不是原子操作，分三步：赋值返回值 → 执行 defer → 真正返回
- 命名返回值是函数签名的一部分，defer 可以直接修改它
- 匿名返回值在 `return` 时就已经把值拷贝出去了

### 解析

**命名返回值的函数**：

`return 1` 等价于先把命名返回值变量赋值为 1，然后执行 defer，defer 里对这个变量加 1 变成 2，最后返回的是 2。defer 改的就是返回值本身。

**匿名返回值的函数**：

`return result` 会把局部变量 `result` 的值（1）拷贝给一个匿名的返回值临时变量，然后执行 defer，defer 里加 1 改的是**局部变量**，不影响已经拷贝出去的返回值，最后返回 1。

### 结论

- 命名返回值：defer 能改到，返回被 defer 修改后的值
- 匿名返回值：defer 改的是局部变量，改不到返回值

### 延伸

这个特性常被用来做统一错误处理、panic 恢复时修改返回值。但也要小心：如果 defer 里对命名返回值做了非预期修改，会让调用方拿到意想不到的结果。团队里最好约定清楚 defer 的使用规范。

---

## 第 3 题：并发与 channel

### 题目回顾

实现一个 worker pool：N 个 goroutine 从同一个 channel 消费任务，处理完关闭结果 channel，支持 context 取消，避免 goroutine 泄漏。

### 核心考点

- `sync.WaitGroup` 追踪所有发送者
- channel 的关闭原则：只有发送者能 close，且必须等所有发送者结束
- `select` 同时监听业务 channel 和 `ctx.Done()`
- 结果 channel 的缓冲大小与死锁的关系
- goroutine 泄漏的常见成因

### 解析

**为什么不能由 worker 自己 close 结果 channel？**

多个 worker 都往结果 channel 写数据，任何单个 worker 都不知道其他 worker 是否还有数据要发。如果它贸然 close，其他 worker 再发送就会 panic。channel 的语义是"close 之后再发送会 panic"，所以只有确认所有发送者都结束后才能 close。

**正确的关闭方式：**

用一个 `WaitGroup` 追踪所有 worker，每个 worker 退出时 `Done`。再单独起一个 goroutine，等 `Wait` 返回后 close 结果 channel。这个 goroutine 的角色是"收尾者"，它不代表任何发送者，只负责在所有发送者结束后关闭 channel。

**为什么结果 channel 要带缓冲？**

如果不带缓冲，最后一个 worker 发送结果时如果没人接收，就会一直阻塞，导致它无法退出，`WaitGroup` 永远不返回，收尾 goroutine 也无法执行 close，整个流程死锁。带缓冲（容量不小于 worker 数）能保证所有 worker 先发完、退出，再关闭 channel。

**为什么要监听 ctx.Done()？**

两个地方都要监听：

1. 从任务 channel 接收时：如果外部取消，worker 不应继续等任务
2. 向结果 channel 发送时：如果调用方提前退出、不再读结果，worker 会卡在发送上，加上 ctx 分支就能及时退出

只在接收端监听是不够的，发送端同样可能阻塞。

**任务 channel 由谁关闭？**

由生产者关闭。生产者发完所有任务后 `close`，worker 通过接收的第二个返回值判断 channel 是否关闭，关闭就退出。

### 延伸

goroutine 泄漏的常见成因：发送/接收时没有退出路径、channel 无人关闭、context 没有正确传递。排查手段有 `runtime.NumGoroutine` 对比、goleak 库、pprof 的 goroutine profile。

---

## 第 4 题：map 并发安全

### 题目回顾

多个 goroutine 同时读写同一个内置 map，会有什么问题？如何修复？`sync.Mutex + map` 和 `sync.Map` 怎么选？

### 核心考点

- 内置 map 非并发安全
- 并发写会触发 runtime 的硬检测，直接 fatal，无法 recover
- `sync.RWMutex` 的读写分离
- `sync.Map` 的 read/dirty 双 map 与原子操作
- `LoadOrStore` 的返回值语义

### 解析

**问题本质：**

Go 的内置 map 为了性能，没有做并发保护。多个 goroutine 同时写，或读写混合，runtime 会检测到并直接 `fatal error: concurrent map writes`。这是运行时错误，**不能 recover**，程序直接崩溃。

**三种修复方案：**

1. **`sync.Mutex + map`**：最简单直接。加锁保护所有读写。适合读写均衡、key 数量可控的场景。

2. **`sync.RWMutex + map`**：读多写少时用。读用 `RLock`，允许多个读并发；写用 `Lock`，独占。比方案 1 在读多场景下性能更好。

3. **`sync.Map`**：内部用 read/dirty 两个 map 加原子操作优化读路径，适合"一次写入、多次读取"的场景。但它不支持 len，遍历要用 `Range`，API 更啰嗦，**不要无脑用**。

**选型对比：**

- 读写均衡、需要遍历、key 频繁增删 → `sync.Mutex + map`
- 读多写少、key 相对稳定（缓存类）→ `sync.Map`
- 写多 → `sync.Mutex + map`，`sync.Map` 在写多时反而更慢

**一个经典坑：**

在 `sync.Map` 上做计数时，容易写出"先 `Load`，没有就 `LoadOrStore` 一个新指针，然后对**新指针**做原子加"的代码。问题在于 `LoadOrStore` 已经把一个指针存进了 map，而代码又新建了另一个指针去累加，导致累加丢失、计数偏小。

正确做法是：**始终使用 `LoadOrStore` 的返回值**——它才是 map 里实际存在的那份数据。无论本次是否真的存进去，返回值都是"当前 map 里的值"。

### 延伸

`sync.Map` 存 `int64` 时要注意：`Load` 出来的是值拷贝，直接对它 `atomic.AddInt64` 改不到 map 里。所以要么存 `*int64`，要么用 CAS 循环。前者更高效。

另外要记住：`sync.Map` 和 `atomic` 保证的是"没有数据竞争"，但**不保证业务逻辑正确**。丢失更新是逻辑 bug，race detector 抓不到。

---

## 第 5 题：range 变量与指针陷阱

### 题目回顾

遍历一个结构体切片，把每个元素的地址收集到一个指针切片里，会有什么 bug？如何修复？切片很大时如何减少内存分配和 GC 压力？

### 核心考点

- `for range` 中循环变量的复用与 Go 1.22 的语义变化
- 取循环变量地址的陷阱
- 指针切片与值切片的 GC 差异
- 预分配、逃逸分析、`sync.Pool`

### 解析

**Bug 成因：**

**Go 1.22 之前**，`for range` 的循环变量 `u` 是整个循环复用的**同一个变量**，每轮只是重新赋值。对它取地址 `&u`，每轮得到的地址都一样。所以最终收集到的所有指针都指向同一个变量，而这个变量在循环结束时保存的是**最后一个元素的值**。结果就是：指针切片里所有指针指向同一个值，全是最后一个元素。

**Go 1.22 及以后**，每次迭代都会创建一个新的 `u`，`&u` 每轮地址不同，这个 bug 已被修复。但迁移旧代码时仍要注意，因为 `go.mod` 里的 `go` 版本会决定语义。

**修复方案（兼容所有版本）：**

1. **索引取址**：`for i := range users { &users[i] }`。直接取切片元素本身的地址，不经过循环变量，任何版本都正确。

2. **显式副本**：`for _, u := range users { u := u; &u }`。在循环体里用短变量声明创建一个副本，每轮地址不同。Go 1.22 之后这一行冗余，但保留也无害。

**减少内存分配和 GC 压力：**

1. **预分配容量**：`make([]*User, 0, len(users))`，避免多次扩容。

2. **优先值切片**：如果不需要修改原对象，用 `[]User` 代替 `[]*User`。值切片是连续内存，GC 只需扫描一次；指针切片每个指针都逃逸到堆，GC 要逐个扫描，压力更大。

3. **避免取 range 变量地址**：否则每个循环变量都逃逸到堆，产生大量小对象。

4. **分块处理**：数据量很大时，分批处理并及时释放，降低峰值内存。

5. **`sync.Pool` 复用**：对象频繁创建销毁时，用池复用，减少分配。

6. **逃逸分析**：用 `go build -gcflags="-m"` 查看哪些变量逃逸到堆，针对性优化。

### 延伸

"用指针切片还是值切片"没有绝对答案。如果 `User` 结构体很小，值拷贝成本低，值切片更优；如果结构体很大，值拷贝成本可能超过指针的 GC 成本，这时指针切片反而更合适。最终要靠 `pprof` 实测决定。

---

---

## 第 6 题：内存分配、逃逸分析与栈/堆

### 题目回顾

一个变量什么时候分配在栈上、什么时候逃逸到堆？goroutine 的栈是怎么增长的？`append` 扩容、字符串拼接各自产生多少堆分配，怎么优化？

### 核心考点

- 逃逸分析（escape analysis）的判定依据
- goroutine 栈的初始大小与**复制式**扩容
- `append` 的扩容策略与预分配
- `strings.Builder` / `bytes.Buffer` / `+=` 的分配差异
- `Makefile` 级别的验证手段：`-gcflags="-m"`

### 解析

**逃逸分析判定的不是"变量类型"，而是"生命周期"。**

编译器只问一个问题：这个变量的地址会不会"活过"当前函数帧？会，就必须放堆上；不会，就放栈上，函数返回时随栈一起回收。

三种典型情况：

1. **返回局部变量地址**：`return &x`。函数返回后调用方还要用这块内存，栈帧已经失效，所以 `x` 必须逃逸到堆。
2. **闭包捕获**：闭包捕获的变量生命周期跟着闭包走，而闭包可能比定义它的函数活得久，所以捕获的变量逃逸。
3. **纯局部变量**：地址从没离开本函数，留在栈上，零堆分配。

注意：**逃逸不等于性能问题**。堆分配本身不慢，慢的是分配 + GC 回收的频率。优化目标是"减少热路径上的分配次数"，不是"消灭所有堆分配"。

**goroutine 栈是复制式增长的。**

goroutine 的栈不是操作系统线程栈（固定 8MB），而是从很小的初始值（Go 1.19 之后约 8KB）开始，按需翻倍扩容。关键点：

- 每次扩容都要**分配新栈 + 整体拷贝 + 修正所有指向栈的指针**。编译器为此维护了栈上指针的精确信息。
- 所以递归深度不是问题，"同时在栈上保留多少数据"才是问题。
- 栈内存不用了会缩回去（Go 1.20+ 支持栈收缩），GC 会考虑栈的使用量。

**`append` 的扩容策略。**

切片容量不足时，`append` 会分配新数组并把旧数据拷过去。粗略策略是：容量小于 1024 时翻倍，超过后按 1.25 倍左右增长（具体实现随版本调整）。

对于 `n = 100000` 的填充（跑 10 轮统计平均）：

| 写法 | 每轮堆分配次数 | 说明 |
|------|--------------|------|
| 不预分配 | 约 27 次 | 每轮扩容都要分配 + 拷贝，产生大量垃圾 |
| `make([]int, 0, n)` | 约 1 次 | 一次到位，没有扩容 |

差距是**约 27 倍**。而且不只是"多分配了 26 次"——每次扩容产生的旧数组都会变成垃圾，GC 要扫描并回收它们。这才是预分配真正的价值。

**字符串拼接的三种姿势。**

字符串不可变，所以 `s += p` 每次都要分配一个新字符串并整体拷贝，n 次拼接是 O(n²) 的拷贝量。实测 200 个 8 字节片段：

| 写法 | ns/op | B/op | allocs/op |
|------|-------|------|-----------|
| `s += p` | 36228 | 169705 | 199 |
| `strings.Builder` | 1095 | 1792 | 1 |
| `bytes.Buffer` | 1501 | 3584 | 2 |

`strings.Builder` 比 `bytes.Buffer` 还快一点，因为它用 `unsafe` 把内部 `[]byte` **零拷贝**转成 `string`，而 `Buffer.String()` 要多一次拷贝。代价是 Builder 用完就不能再改（拷贝后复用会踩到别名问题）。

**切片复用：`buf[:0]`。**

```go
buf := make([]byte, 0, 64)
for _, s := range data {
    buf = buf[:0]      // 长度归零，底层数组保留
    buf = append(buf, s...)
}
```

这是网络编程里最常用的零分配技巧：长度重置了，容量和底层数组都还在，下一次 `append` 不需要重新分配。同理，`sync.Pool` 解决的是"对象在多个 goroutine 之间复用"的场景。

### 延伸

自己看逃逸结论：

```bash
go build -gcflags='-m' ./q6_memory     # 打印逃逸决策
go build -gcflags='-m -m' ./q6_memory  # 更详细的原因
```

输出里 `escapes to heap` 就是逃逸，`does not escape` 就是留栈。`moved to heap: x` 表示 `x` 本身从栈被挪到了堆。

面试时可以提一句：**"栈上分配比堆上分配快"这个说法本身不准确**——真正的差别在于堆分配要参与 GC 记账和回收。理解了这点，才知道优化的方向是"降低分配频率和存活对象数量"，而不是"想办法都塞到栈上"。

---

## 第 7 题：垃圾回收与 GC 调优

### 题目回顾

Go 的 GC 是怎么工作的？为什么要三色标记 + 写屏障？`GOGC` 和 `GOMEMLIMIT` 分别控制什么？容器里该怎么配？`sync.Pool` 为什么不能当缓存用？

### 核心考点

- 三色标记法与混合写屏障
- 并发标记 + 短 STW 的整体流程
- Pacer 如何决定"什么时候开始 GC"
- `GOGC` / `GOMEMLIMIT` / `debug.SetGCPercent` 的区别与配合
- `sync.Pool` 的 victim cache 机制

### 解析

**三色标记的核心是一句话：白 = 待回收，灰 = 待扫描，黑 = 已扫描且存活。**

流程是：

1. **标记准备（STW，极短）**：开启写屏障，扫描根对象（栈、全局变量、寄存器）把它们染灰。
2. **并发标记**：从灰集合里取对象，把它引用的对象染灰，自己染黑。期间用户代码继续跑。
3. **标记终止（STW，极短）**：关闭写屏障，处理剩余灰对象。
4. **并发清除**：把仍然是白色的对象回收掉。

关键难点是第 2 步：用户代码在标记的同时还在改指针。如果不管，会出现"黑对象引用了白对象，而白对象没被扫描到"——白对象被误回收，程序崩溃。

**写屏障**就是用来堵这个漏洞的：在指针赋值时插一段代码，把可能遗漏的对象重新染灰。Go 早期用过两种单向屏障，都有性能问题；Go 1.8 之后用的是**混合写屏障**（结合 Dijkstra 插入屏障和 Yuasa 删除屏障），好处是**标记阶段不需要重新扫描整个栈**，把 STW 压到了亚毫秒级。

**Pacer：什么时候开始下一轮 GC？**

Pacer 是一个控制论式的调节器，目标是"在堆达到目标大小之前完成本轮标记"。它需要同时满足两个约束：

- **`GOGC`（默认 100）**：下一轮 GC 的堆目标是"上一轮存活量的 (1 + GOGC/100) 倍"。GOGC=100 表示存活量翻倍就 GC；GOGC=off 表示除非内存限制到了，否则不主动 GC。
- **`GOMEMLIMIT`（Go 1.19+，默认 `math.MaxInt64`）**：Go 运行时的总内存软上限。它优先级更高——GC 也会因为接近这个上限而提前触发。

实测对比（同样分配 400MB 垃圾）：

| 配置 | GC 轮数 | 累计 STW |
|------|--------|---------|
| GOGC=100 | 约 100 轮 | 约 5.3 ms |
| GOGC=800 | 约 22 轮 | 约 1.5 ms |

GOGC 调大 = GC 变懒 = 省 CPU、暂停少，但**堆峰值变高**。这是一个明确的吞吐 vs 内存的权衡。

**容器里怎么配？**

这是现在最常被问的实操问题。历史上 Go 程序在容器里被杀，多半是因为它不知道 cgroup 限制，按宿主机的内存来算 GC 目标。

推荐组合：

```bash
GOGC=100                 # 保守目标；也可以 off，靠内存限制驱动
GOMEMLIMIT=8589934592    # 容器上限的 70%~80%，比如 8GiB
```

要点：

- **不要把 GOMEMLIMIT 设成等于容器上限**。运行时之外还有非堆内存（栈、runtime 元数据、cgo、arena），设太紧会导致 GC 疯狂触发（thrashing），甚至因为运行时保留内存而 OOM。
- 如果设了 GOMEMLIMIT 且设了 `GOGC=off`，就是"纯按内存限制驱动 GC"，适合内存是硬约束、CPU 相对宽裕的场景。
- GOMEMLIMIT 是**软限制**：它靠"更频繁地 GC"来逼近目标，而不是拒绝分配。如果存活集本身就超过限制，程序会 GC 到死而不是优雅失败。

**`sync.Pool` 为什么不能当缓存？**

Pool 每个 P 有一个 private 槽和一个 shared 链表。GC 时运行时会做一次"降级"：

```
当前池  ──GC──▶  victim cache  ──再GC──▶  清空
```

多留一轮（victim）的设计目的，是给"每轮循环都手动调 runtime.GC()"的程序留活路，否则 Pool 会完全失效。

但这意味着：**Pool 里的对象随时可能被静默丢弃**。它只是"降低分配频率的概率优化"，没有任何"我放进去就还在"的保证。需要缓存语义请用带淘汰策略的 LRU / bigcache / freecache。

实测收益（8 个 goroutine 各做 2 万次大对象借还，期间每 5ms 强制一次 GC）：

| 写法 | 堆分配次数 |
|------|-----------|
| 每次 `new(payload)` | 160083 |
| 从 Pool 借还 | 31 |

**四个容易混的内存指标。**

| 指标 | 含义 | 用途 |
|------|------|------|
| `HeapAlloc` | 当前存活对象占用 | GC 的"收益"，判断泄漏看这个 |
| `HeapInuse` | 已分配 span 占用（含碎片） | 看实际占地 |
| `HeapIdle` | 空闲 span，可归还 OS | 看内存是否"占着不放" |
| `TotalAlloc` | 进程累计分配量，只增不减 | 看吞吐和分配频率 |

**别用 `HeapSys` 判断泄漏**——它只增不减，GC 之后也不会掉下来。

### 延伸

观察真实 GC 行为：

```bash
GODEBUG=gctrace=1 go run . 7
```

每行形如 `gc 12 @0.523s 3%: 0.5+1.2+0.3 ms clock, ...`，其中：

- `gc 12` 是第 12 轮
- `@0.523s` 是启动后时间
- `3%` 是 GC 占用的 CPU 比例
- 后面三段是 **标记准备 STW + 并发标记 + 标记终止 STW** 的耗时

面试常见追问："**STW 时间长怎么办？**"——现代 Go 的 STW 已经在亚毫秒级，真正影响延迟的是"并发标记阶段抢 CPU"和"辅助标记（mutator assist）让业务 goroutine 帮忙标记"。所以延迟敏感的服务要么降低分配速率，要么接受 GC 的 CPU 开销。

---

## 第 8 题：GMP 调度模型与 runtime 调度器

### 题目回顾

G、M、P 分别是什么？为什么需要 P？goroutine 阻塞时 CPU 会被浪费吗？一个没有函数调用的死循环会不会饿死其他 goroutine？work stealing 是怎么回事？

### 核心考点

- G / M / P 三者的关系与数量上限
- 本地运行队列 + 全局队列 + work stealing
- 阻塞系统调用时的 P 交接（handoff）
- 基于信号的异步抢占（Go 1.14+）
- 排查工具：`schedtrace`、`go tool trace`

### 解析

**三者是什么。**

| 角色 | 含义 | 数量 |
|------|------|------|
| **G** | goroutine，包含栈、指令指针、状态 | 可以几十万上百万，每个初始栈约 2~8KB |
| **M** | machine，操作系统线程 | 按需创建，默认上限 10000（`debug.SetMaxThreads`） |
| **P** | processor，逻辑处理器，**执行 G 所需的资源凭据** | 固定 `GOMAXPROCS` 个（默认 = CPU 核数） |

**为什么需要 P？**这是 GMP 相比早期 GM 模型的关键改进。P 持有：

- 一个本地运行队列（`runq`，256 个 G 的环形数组）
- 一个 `mcache`（每 P 的内存分配缓存，避免每次分配都加全局锁）

有 P 才能运行 G：`M` 必须绑定一个 `P` 才能执行 `G`。这样运行队列的竞争从"全局一把锁"变成了"各自操作自己的队列"，绝大多数情况下无锁。

**调度循环**大致是：

```
schedule():
  1. 每 61 次调度从全局队列取一个 G（防止全局队列饿死）
  2. 取 P 的本地队列
  3. work stealing：随机挑一个其他 P，偷它一半的 G
  4. 从 netpoller 取就绪的 G
  5. 都没有 -> 解绑 P，M 休眠
```

**work stealing** 是负载均衡的关键：本地队列空了的 P 会去偷别人的活，避免"一个 P 忙死、其他 P 闲死"。偷的是一半，减少下次再偷的概率。

**阻塞时 CPU 会被浪费吗？不会——关键是"谁阻塞"。**

- **channel / mutex / 网络 IO 阻塞**：runtime 知道这个 G 在等什么，把它 `park` 起来（状态变 `waiting`），然后 **P 被交给另一个 M** 去跑别的 G。CPU 不浪费。
- **阻塞式系统调用（如文件 IO、cgo）**：M 会真的被 OS 挂起。runtime 检测到这种情况，会把 P 从该 M 上"抢"下来（`handoffp`）交给其他空闲 M。这是 `sysmon` 监控线程的职责之一。
- **但是**：被 park 的 G 本身仍然占内存（栈、`g` 结构体）。如果它没有被唤醒的路径，就是**goroutine 泄漏**——CPU 没浪费，内存一直在涨。

实测：让 8 个 goroutine 永久阻塞在无缓冲 channel 上，另外 8 个正常计算，后者照常完成，但 goroutine 总数从 1 涨到 9 且不会回落。

**异步抢占：Go 1.14 的分水岭。**

在 Go 1.14 之前，抢占只能发生在"安全点"（函数调用、循环回边等）。像下面这种没有函数调用、没有内存分配的紧循环，会把同一个 P 上其他 G 活活饿死：

```go
for {
    x++    // 没有安全点
}
```

Go 1.14 引入了**基于信号的异步抢占**：sysmon 发现某个 G 运行超过 10ms，就给它所在的 M 发 `SIGURG`，M 的信号处理器在安全的地方（借助编译器为每条指令生成的栈映射）保存现场并让出。上面这个循环现在也能被抢占了。

可以自己做对比实验：

```bash
GODEBUG=asyncpreemptoff=1 go run . 8    # 关闭异步抢占
```

**并行度的正确度量。**

很多人以为"起更多 goroutine 就能更并行"。实测方法是算 CPU 时间总和 / 墙钟时间：

| 起的 goroutine 数 | CPU 累计 | 墙钟 | 并行度 |
|------------------|---------|------|--------|
| 4 | 201ms | 50ms | 3.98 |
| 16 | 800ms | 100ms | 7.93 |
| 64 | 3216ms | 291ms | 11.02 |

无论起多少个 goroutine，并行度都收敛到 `GOMAXPROCS`。注意最后一个略超 10，是"goroutine 起停的错位 + 计时误差"导致的，属于正常抖动——这也是为什么用 CPU 时间比值比用"同时在跑的计数器"更可靠。

**`runtime.Gosched()` 的语义。**

它只做一件事：把当前 G 放回队列，让出 P，让别的 G 跑。它**不保证公平**，也不保证让出多久。实测：同样 20ms 窗口，不让出能跑约 36 万次循环，每轮都 `Gosched` 只能跑约 5.7 万次。

它适合用在自旋锁的退避（`for !CAS() { runtime.Gosched() }`），**不能当同步原语用**。

### 延伸

排查调度问题的工具：

```bash
GODEBUG=schedtrace=1000 go run . 8    # 每秒打印一行调度器状态
go tool trace trace.out               # 可视化每个 G 的运行/阻塞/抢占/GC
```

`schedtrace` 那行里的 `runqueue` 是全局队列长度，`[n m]` 是各 P 的本地队列长度，`gc` 是 GC 状态，`idle` 是空闲 P 数。

面试追问："**GOMAXPROCS 设多大合适？**"——CPU 密集型任务用默认值（核数）；IO 密集型任务其实不用调，因为阻塞时 P 会自动交接，瓶颈在 IO 不在 CPU。容器里要注意：Go 1.25 之前它读的是宿主机核数，需要用 `automaxprocs` 或手动设置，否则会起过多 P 导致上下文切换开销。

---

## 第 9 题：channel 内部结构与使用陷阱

### 题目回顾

channel 的内部结构是什么？缓冲和无缓冲的本质差异在哪？关闭 channel 有哪些 panic？nil channel 有什么用？为什么说 channel 不是"万能通信工具"？

### 核心考点

- `hchan` 的两个核心：环形缓冲区 + 两个等待队列
- 无缓冲 = 同步交接，有缓冲 = 异步暂存
- `close` 的广播语义与"关闭后接收返回零值"
- 三种 panic：重复 close、关闭后发送、以及可 recover 的性质
- channel 的选型边界

### 解析

**内部结构**（runtime/chan.go 的 `hchan`）：

```
hchan
 ├── qcount    uint            // 缓冲区里当前有多少个元素
 ├── dataqsiz  uint            // 缓冲区容量（0 = 无缓冲）
 ├── buf       unsafe.Pointer  // 环形缓冲区指针
 ├── elemsize  uint16          // 元素大小（决定拷贝多少字节）
 ├── elemtype  *_type          // 元素类型（GC 扫描用）
 ├── sendx/recvx uint          // 环形缓冲区的读写下标
 ├── recvq     waitq           // 等待接收的 goroutine 队列
 └── sendq     waitq           // 等待发送的 goroutine 队列
```

**两个机制**：`buf` 是有缓冲 channel 的环形数组，`recvq`/`sendq` 是**双向链表**（元素是 `sudog`，记录被 park 的 goroutine 和它的数据位置）。

> 补充一点工程经验：`hchan` 是未导出的内部结构，字段偏移会随版本变化。用 `unsafe`/`reflect` 硬读既不可靠也不可移植，**面试时可以讲结构，但不要在生产代码里这么做**。因此本题的代码用"可观测行为"来验证结构，而不是偷看内存。

**无缓冲 vs 有缓冲的本质差异。**

| | 无缓冲 | 有缓冲 |
|---|--------|--------|
| `buf` | 无 | 有环形数组 |
| 发送 | 必须等到有接收者，**同步交接** | 容量未满时立即返回 |
| 接收 | 必须等到有发送者 | 容量非空时立即返回 |
| 数据路径 | runtime 直接从发送者拷贝到接收者 | 先写进 buf，接收方再读 |

实测（容量 3 的 channel）：写 3 个都不阻塞，第 4 个阻塞；随后读出一个，队列立刻腾出位置。而容量 0 的 channel，无论发送还是接收，在没有对端时都会阻塞——这就是"无缓冲是同步的"这句老话的准确含义：**它对双方都要求"必须有人接/有人给"**。

**关闭语义。**

```go
ch := make(chan string, 2)
ch <- "data"
close(ch)
v1, ok1 := <-ch    // "data", true    ← 缓冲区里还有数据，先读完
v2, ok2 := <-ch    // "", false       ← 缓冲空了，返回零值
v3, ok3 := <-ch    // "", false       ← 一直不阻塞
```

关键结论：

- **关闭后接收永不阻塞**，返回零值 + `ok=false`。
- 因此**"零值"不能当哨兵**。如果 channel 元素类型是 `int`，你无法区分"对方发了 0"和"channel 已关闭"——必须用 `ok` 或 `range`。
- `for v := range ch` 会在 channel 关闭且缓冲读空后自动退出，这是最常用的写法。

**三种 panic。**

| 操作 | 结果 |
|------|------|
| 向已关闭的 channel 发送 | `panic: send on closed channel` |
| 重复 close | `panic: close of closed channel` |
| 从已关闭的 channel 接收 | 不 panic，返回零值 + `ok=false` |

这两个 panic 都是**普通 panic，可以 recover**。这一点要和 map 区分开：`concurrent map writes` 是 runtime 的 fatal error，`recover` 抓不住，程序直接退出。

**nil channel 的用途。**

nil channel 上的发送和接收**永远阻塞**（不是 panic）。这在 `select` 里反而很有用——把不需要的分支"永久关闭"：

```go
var in <-chan int   // 初始为 nil
for {
    select {
    case v := <-in:      // in 为 nil 时这个分支永不就绪
        handle(v)
    case <-done:
        return
    }
}
```

因为 nil 分支永远不就绪，`select` 就会忽略它。这就是"动态开关某个 case"的惯用法。

**close 的广播语义。**

`close(ch)` 会唤醒 `recvq` 里**所有**等待的接收者（每个都拿到零值），而不是只唤醒一个。这是"优雅关闭"的标准写法：不要去逐个发信号，直接 `close`。实测 10 个 worker 同时 park 在同一个 channel 上，一次 `close` 全部唤醒。

### 延伸

**为什么"关闭 channel 只能由发送者做"？**

因为 close 的语义是"不会再有数据了"。只有发送方知道这件事；接收方关掉 channel，其他发送方再发就 panic。多个发送者时，要额外用一个 `sync.WaitGroup` 等所有发送者结束后，由"收尾者"关闭（见第 3 题）。

**channel 的性能与选型。**

| 场景 | 首选 |
|------|------|
| 高频计数、标志位 | `atomic` |
| 保护共享状态（读写一个 map/struct） | `sync.Mutex` / `RWMutex` |
| 数据流转、流水线、超时取消、广播 | `channel` |

channel 一次收发大约是一次锁操作 + 可能的 goroutine park/unpark，比 mutex 重，但远没到"不能用"的程度。真正的选型依据是**语义清晰度**：用 channel 表达"所有权转移"和"信号"是最自然的；用它去保护一个计数器就是在硬套 CSP，只会让代码更绕。

拿不准的时候问自己一句：**"我在传数据，还是在保护状态？"**——传数据用 channel，保护状态用 mutex。

---

## 第 10 题：接口、类型系统与内存布局

### 题目回顾

`var s SomeInterface = (*T)(nil)`，为什么 `s == nil` 是 `false`？接口值在内存里长什么样？值接收者和指针接收者的方法集有什么区别？结构体字段顺序为什么会浪费内存？

### 核心考点

- 接口值的"两字宽"：类型指针 + 数据指针
- typed nil 陷阱（Go 最经典的坑之一）
- 值接收者 vs 指针接收者的方法集规则
- 类型断言、type switch 的匹配顺序
- 结构体字段对齐与填充
- 接口调用的动态分派开销

### 解析

**接口值在内存里是两个指针。**

```
接口值（16 字节）
 ├── tab / _type : 类型信息（含方法表 itab）
 └── data        : 数据指针
```

判空看的是**两个都为空**。所以：

```go
var s Stringer        // tab=nil, data=nil
fmt.Println(s == nil) // true

var nd *NilDog        // 指针是 nil
s = nd                // 装箱后：tab=*NilDog, data=nil
fmt.Println(s == nil) // false  ← 坑在这里
```

`s` 现在携带了"我是 `*NilDog` 类型"这个信息，只是数据指针是空的。这时调用 `s.Describe()` 会在解引用 `nil` 接收者时 panic。

**为什么会有人写出这种代码？**因为函数返回接口类型时，随手 return 了一个可能为 nil 的具体指针：

```go
func find(id int) (Stringer, error) {
    var d *NilDog
    if id <= 0 {
        return d, nil     // 错！返回的不是 nil 接口
    }
    ...
}
```

调用方 `if s == nil` 判断失败，然后在别处 panic。

**正确写法**：要么返回具体类型（让调用方拿到真正的 nil 指针），要么显式判空后再装箱：

```go
var s Stringer
if nd != nil {
    s = nd
}
```

**方法集的规则。**

| 类型 | 方法集 |
|------|--------|
| `T` | 所有**值接收者**方法 |
| `*T` | 值接收者方法 + **指针接收者**方法 |

所以 `*NilDog` 满足 `Stringer`（`Describe` 是指针接收者），但 `NilDog` **不满足**。同时也解释了为什么"值可以调用指针方法"：`cat.Purr()` 实际上是编译器帮你取了地址 `(&cat).Purr()`——前提是 `cat` 可寻址。

**这条规则的实际影响**：如果一个类型同时有值接收者和指针接收者方法，那么只有 `*T` 能满足所有接口。所以**同一个类型的方法接收者最好保持一致**——混用会让方法集变得难以推理。

**type switch 的匹配顺序是自上而下的。**

```go
switch x := v.(type) {
case fmt.Stringer:   // 先匹配这个
    ...
case error:          // 能实现 Stringer 的错误类型永远到不了这里
    ...
}
```

一个同时实现了 `Stringer` 和 `error` 的类型，会命中先写的那个 case。**接口 case 要按"具体性"从窄到宽排列**，否则后面的分支是死代码。

**结构体字段顺序影响内存占用。**

```go
type BadLayout struct {   // 24 字节
    A int32   // offset 0
    B int64   // offset 8（中间补 4 字节）
    C int32   // offset 16（末尾再补 4 字节对齐）
}

type GoodLayout struct {  // 16 字节
    B int64   // offset 0
    A int32   // offset 8
    C int32   // offset 12
}
```

同样的字段，只调换顺序就省了 33%。规则是**按字段大小从大到小排列**，填充最少。

高频创建的小对象值得排一排；只创建几个的结构体不值得为此牺牲可读性。可以用 `unsafe.Sizeof` 或 `fieldalignment` 工具检查：

```bash
go vet -vettool=$(which fieldalignment) ./...
```

**接口调用为什么慢一点。**

- 直接调用：编译期确定地址，可能被内联 → **静态分派**
- 接口调用：运行时从 `itab` 查函数指针 → **动态分派**，无法内联

优化方向是"热点路径用具体类型或泛型"，而不是"消灭所有接口"。接口带来的可测试性、可扩展性收益，通常远大于那点分派开销。

### 延伸

一个常被忽略的细节：**小整数装箱进接口不一定真的分配内存**。runtime 对 0~255 的小整数做了缓存（`runtime.staticuint64s`），装箱时直接指向静态数组，不产生堆分配。所以"接口装箱一定慢"也是不准确的——要看具体类型。

---

## 第 11 题：error 设计、错误链与最佳实践

### 题目回顾

`errors.Is` 和 `errors.As` 有什么区别？为什么不能用 `==` 比较错误？`%w` 和 `%v` 差在哪？怎么聚合多个错误？什么情况该 panic 而不是返回 error？

### 核心考点

- 哨兵错误（sentinel error） vs 自定义错误类型
- `errors.Is` / `errors.As` / `errors.Unwrap` 的语义
- `%w` 建立错误链，`%v` 断链
- `errors.Join`（Go 1.20+）聚合多错误
- 错误设计与 panic 的边界

### 解析

**三种错误表达方式，各有用途。**

| 方式 | 写法 | 适用场景 |
|------|------|---------|
| 哨兵错误 | `var ErrNotFound = errors.New("not found")` | 调用方只需知道"哪一类错误" |
| 自定义类型 | `type ValidationError struct{...}` | 调用方需要拿到**结构化上下文** |
| 直接 `errors.New` / `fmt.Errorf` | 就地构造 | 只需展示，不需要被程序判断 |

**`errors.Is` 穿透整条链，`==` 不行。**

```go
_, err := serviceLayer(0)
errors.Is(err, ErrNotFound) // true   ← 中间隔了一层包装也能命中
err == ErrNotFound          // false  ← 直接比较失效
```

因为 `serviceLayer` 用 `%w` 包装了 `findUser` 的错误，错误链是：

```
serviceLayer: findUser(id=0): not found
  └─ findUser(id=0): not found
       └─ not found   ← 哨兵在这里
```

`errors.Is` 会沿着 `Unwrap()` 一路往下找，`==` 只比最外层。**所以任何可能被包装的错误，都必须用 `errors.Is` 判断。**

**`errors.As` 用来取回结构化上下文。**

```go
var ve *ValidationError
if errors.As(err, &ve) {
    fmt.Println(ve.Field, ve.Value)   // 拿到字段名和值
}
```

注意第二个参数必须是**指向"实现了 error 的类型"的指针**（这里是 `**ValidationError`）。传错类型会 panic。

**`%w` vs `%v` 的区别。**

```go
base := errors.New("底层原因")
withW := fmt.Errorf("包装A: %w", base)   // Unwrap → 底层原因（链还在）
withV := fmt.Errorf("包装B: %v", base)   // Unwrap → nil（链断了）
```

`%w` 保留链（可被 `Is`/`As` 穿透），`%v` 只把字符串拼进去。Go 1.20 起一个 `fmt.Errorf` 里可以写**多个 `%w`**，形成一个多叉的错误树。

**用 `errors.Is` 配合标准库错误值。**

```go
err := fmt.Errorf("打开配置文件: %w", fs.ErrNotExist)
errors.Is(err, fs.ErrNotExist)    // true
errors.Is(err, fs.ErrPermission)  // false
```

这就是为什么**永远不要比较错误字符串**——`err.Error() == "file not found"` 在标准库改文案或换语言环境时就崩了。用 `fs.ErrNotExist`、`io.EOF`、`context.DeadlineExceeded` 这些导出的哨兵值。

**`errors.Join` 聚合多个错误。**

```go
var errs []error
for k, v := range fields {
    if s, ok := v.(string); ok && s == "" {
        errs = append(errs, &ValidationError{Field: k, Value: v})
    }
}
return errors.Join(errs...)   // 全为 nil 时返回 nil
```

`Join` 返回的错误实现了 `Unwrap() []error`，所以 `errors.Is` / `errors.As` 会**遍历所有子错误**——聚合后的错误依然可以被判断，这是它比"字符串拼接所有错误信息"强的地方。典型场景是一次性校验所有表单字段，把所有问题一起返回给用户。

**该 panic 还是返回 error？**

| 场景 | 选择 |
|------|------|
| 外部可预期的失败（IO、网络、参数非法、下游报错） | 返回 `error` |
| 程序员错误（越界、断言失败、违反不变量） | `panic` |
| `main` 里的初始化失败 | `panic`（反正也没法继续） |
| **库代码** | 尽量返回 `error`，不要 panic |

判断标准是**"调用方有没有可能优雅处理"**。能处理就返回 error，不能处理（说明是 bug）就 panic。

### 延伸

**错误信息的设计规范：**

- **小写开头，不带标点**——因为会被上层再包装，`"FindUser failed."` 嵌进 `"serviceLayer: FindUser failed."` 就很怪。
- **在边界处加唯一上下文**——你的函数知道的信息（参数、请求 id），上层不知道，所以要加；中间层不要重复包装同样的信息。
- **需要被判断的用哨兵/自定义类型，需要被展示的用字符串**——两者不要混为一谈。

**常见反模式清单：**

```go
// ✗ 比较错误字符串
if err.Error() == "not found" { }

// ✗ 循环里裸返回，丢失现场
for _, id := range ids {
    if err := f(id); err != nil {
        return err      // 不知道是哪个 id 失败的
    }
}

// ✓ 带上上下文
return fmt.Errorf("process id=%d: %w", id, err)
```

静态检查可以用 `errcheck`（检查未处理的 error）和 `errorlint`（检查该用 `Is`/`As` 的地方用了 `==`）。

---

## 第 12 题：panic / recover 的控制流与边界

### 题目回顾

`recover()` 为什么有时候"不起作用"？`recover` 能修改返回值吗？子 goroutine 里 panic 会怎样？`defer` 的执行顺序是什么？

### 核心考点

- `recover` 生效的**两个必要条件**
- `defer` 后进先出（LIFO）
- 命名返回值 + `recover` 改写错误
- panic 值可以是任意类型
- 子 goroutine panic = 整个进程崩溃

### 解析

**`recover` 生效的两个必要条件。**

```go
// ✓ 正确：在 defer 的函数里直接调用
defer func() {
    if r := recover(); r != nil { ... }
}()

// ✗ 错误：隔了一层函数调用
func tryRecover() any { return recover() }
defer func() {
    r := tryRecover()   // 永远是 nil
}()
```

必须同时满足：

1. 在 **defer 的函数体内**调用；
2. **直接调用**——不能包在另一层函数里。

原理：`recover` 是靠检查"当前 goroutine 的 `_panic` 链表中，最近的 panic 是否正在被当前 defer 处理"来工作的。包一层之后，调用栈上多了个普通函数帧，这个关联就断了。

**`defer` 是后进先出（LIFO）。**

实测输出：`[body recover:boom defer-2 defer-1 after]`

- `body` 先执行
- `panic` 触发，开始执行 defer
- 最后注册的 defer（recover 那个）先跑，拦住 panic
- 然后 `defer-2`、`defer-1` 依次跑
- 最后回到 panic 点的调用方，`after`

**这个顺序有实际意义**：恢复用的 defer 要**注册在最后**（最先执行），才能拦住 panic；清理资源的 defer 注册在前面，会在恢复之后执行。

**`defer` + `recover` 可以修改命名返回值。**

```go
func divide(a, b int) (result int, err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("divide(%d,%d) 失败: %v", a, b, r)
            result = 0
        }
    }()
    return a / b, nil   // b == 0 时触发 runtime panic
}
```

`divide(10, 0)` 不会崩，而是返回 `(0, error)`。这是"把内部 panic 转换成 error 返回"的标准写法。

**但注意**：这只能兜住**自己函数内**的 panic。如果 `a/b` 是在另一个 goroutine 里算的，兜不住。

**panic 的值可以是任意类型。**

```go
type FatalConfig struct{ Key string }
func (e *FatalConfig) Error() string { return "缺少必需配置: " + e.Key }

panic(&FatalConfig{Key: "DB_DSN"})
```

因为 `*FatalConfig` 实现了 `error`，捕获时可以直接当 error 用，甚至可以 `errors.As` 还原类型：

```go
if r := recover(); r != nil {
    if e, ok := r.(error); ok {
        err = e
    } else {
        err = fmt.Errorf("%v", r)
    }
}
```

**子 goroutine 里的 panic = 整个进程崩溃。**

这是最容易出事的地方。`recover` **不能跨 goroutine**——主 goroutine 的 defer 兜不住子 goroutine 的 panic：

```go
go func() {
    panic("boom")    // 主函数里的 recover 救不了它，进程直接 exit status 2
}()
```

所以**每个长期运行的 goroutine 入口都应该有自己的 recover 兜底**：

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            buf := make([]byte, 4096)
            n := runtime.Stack(buf, false)
            log.Printf("goroutine panic: %v\n%s", r, buf[:n])
        }
    }()
    worker()
}()
```

### 延伸

**工程约定清单：**

1. **recover 必须配 `runtime.Stack` 记录栈**——否则日志里只有一行原因，排查时完全不知道崩在哪。
2. **HTTP / gRPC / MQ / 定时任务的顶层 handler 必须 recover**——一个请求的 bug 不能打挂整个进程。注意：`net/http` 自己会 recover 每个请求，但 gRPC 的默认行为、自研的 worker 框架不一定。
3. **recover 之后不要让流程"继续往下走"**——要么返回 error，要么退出这个 goroutine。硬撑着继续跑，往往会在更远的地方以更难查的方式出问题。
4. **不要在库代码里 panic**——调用方无法优雅处理，只能自己也 recover，形成层层包裹。

**面试追问："recover 能捕获所有 panic 吗？"**——不能。以下情况 `recover` 无效：

- 其他 goroutine 的 panic
- runtime 的 fatal error（如 `concurrent map writes`、`out of memory`、栈溢出）
- `os.Exit` / `log.Fatal`（它们直接退出，不走 defer）

---

## 第 13 题：context 的传播、取消与工程实践

### 题目回顾

`context` 有哪几种派生方式？为什么 `WithValue` 的 key 不能用内建 `string`？取消是向上传播还是向下传播？忘记 `cancel` 会怎样？

### 核心考点

- `WithCancel` / `WithTimeout` / `WithDeadline` / `WithValue`
- **取消向下传播，不向上传播**
- 子 deadline 不能超过父 deadline
- 忘记 cancel 的两种代价
- `context.AfterFunc`（Go 1.21+）

### 解析

**四种派生方式。**

```go
ctx, cancel := context.WithCancel(parent)              // 手动取消
ctx, cancel := context.WithTimeout(parent, 3*time.Second)  // 相对超时
ctx, cancel := context.WithDeadline(parent, deadline)  // 绝对时间点
ctx := context.WithValue(parent, key, val)             // 携带请求域数据
```

前三个都返回 `cancel`，**必须调用**（通常是 `defer cancel()`）。

**取消只能向下传播。**

```go
parent, parentCancel := context.WithCancel(bg)
child, childCancel := context.WithCancel(parent)

childCancel()
<-parent.Done()   // 阻塞！父节点不受影响

parentCancel()
<-child.Done()    // 立即返回，子节点被级联取消
```

这是树形结构：取消父节点 = 取消整棵子树；取消子节点只影响自己这一支。这个方向性是 context 设计的核心——父任务不需要关心子任务具体何时结束，但父任务死了，子任务必须跟着死。

**子 deadline 不能超过父 deadline。**

```go
parent, _ := context.WithTimeout(bg, 200*time.Millisecond)
child, _ := context.WithTimeout(parent, time.Second)

// child 的 deadline 被自动收紧到 200ms（等于 parent 的）
```

这是自动的，不需要手动 min。**实践意义**：在 RPC 调用链上层层设置超时时，子调用不需要知道上游还剩多少时间，只要设自己的上限，runtime 会自动取更严的那个。这样"总超时"永远可控。

**忘记 `cancel` 的两种代价。**

```go
func bad() {
    ctx, _ := context.WithCancel(context.Background())
    go func() { <-ctx.Done() }()   // 永远收不到信号
}
```

1. **goroutine 永久 park**：实测 5 次调用泄漏 5 个 goroutine，且不会回落。它们占着栈和 `g` 结构体，直到进程退出。
2. **context 树无法回收**：子 context 会一直挂在父节点上。如果父节点是长生命周期的（比如一个后台服务的主 context），那么所有子节点都无法被 GC 回收——即使对应的 goroutine 早就退出了。

正确写法：

```go
ctx, cancel := context.WithCancel(parent)
defer cancel()     // 无论函数怎么返回，都释放
```

**统计静态检查手段：**

- `go vet` 会报 `lostcancel`：检测到 `cancel` 被丢弃
- `go.uber.org/goleak`：在测试里断言函数返回后没有 goroutine 泄漏
- `pprof` 的 goroutine profile：线上排查时看哪些 goroutine 堆在同一个地方

**`context.AfterFunc`（Go 1.21+）。**

```go
stop := context.AfterFunc(ctx, func() {
    cleanup()      // ctx 被取消时执行
})
// stop() 可以在取消前撤销这个回调
```

它把"取消时做清理"这个模式从"再起一个 goroutine 等 `<-ctx.Done()`"简化成了注册回调，而且 `stop()` 能撤销，不会泄漏。

**超时和取消要区分处理。**

```go
_, err := slowQuery(ctx)
switch {
case errors.Is(err, context.DeadlineExceeded):
    // 超时：转 504 / 超时错误码，应该告警
case errors.Is(err, context.Canceled):
    // 调用方主动取消（通常是客户端断连）：正常现象，不要告警
}
```

这个区分很重要：`context.Canceled` 大多是客户端断连，属于正常业务；`DeadlineExceeded` 才是真正的超时故障。把它们混在一起做成同一个告警，会导致告警疲劳。

### 延伸

**工程约定清单：**

1. **ctx 永远是第一个参数**，命名为 `ctx`（不要叫 `c`、`context`）。
2. **不要塞进 struct**——它跟一次调用绑定，不是对象状态。
3. **不要传 nil context**，不知道用什么就传 `context.Background()`。
4. **谁的 ctx 谁负责 cancel**，`defer cancel()` 是默认动作。
5. **下游每个阻塞点都要监听 `ctx.Done()`**——IO、DB、RPC、channel 收发、锁等待，一个都不能漏。漏一个，取消就不彻底。
6. **`WithValue` 只放请求域元数据**（trace id、用户身份）。不要用它传业务参数、可选参数、DB 连接——那会让函数签名失去意义。

**一个经典的面试追问："`context.WithValue` 的 key 为什么不能用内建 string？"**

因为多个包用同一个字符串字面量当 key 时会**互相覆盖**，而且编译器不会报错。正确做法是定义未导出的自定义类型：

```go
type ctxKey string
const keyUserID ctxKey = "user_id"
```

这样只有本包能构造出这个类型的值，其他包即使写了 `"user_id"` 也是不同的 key，不会冲突。

---

## 第 14 题：泛型、类型约束与选型

### 题目回顾

Go 泛型的约束怎么写？`~int` 和 `int` 有什么区别？`comparable` 约束是什么？Go 泛型和 C++ 模板、Java 泛型有什么不同？什么时候该用泛型，什么时候不该用？

### 核心考点

- 类型参数与约束（constraint）
- `~` 近似类型（underlying type）
- `comparable` 与 `any`
- 泛型类型（`Stack[T]`）
- Go 泛型的边界：没有泛型方法、没有特化
- 泛型的实现方式：GC shape + 字典传参

### 解析

**约束就是一个接口。**

```go
type Number interface {
    ~int | ~int64 | ~float64    // | 是"类型并集"
}

func Sum[T Number](nums []T) T {
    var total T
    for _, n := range nums {
        total += n
    }
    return total
}
```

**`~` 是关键。**`~int` 表示"底层类型是 `int` 的**所有**类型"，包括你自己定义的 `type MyInt int`。不加 `~` 的话，只有字面量 `int` 本身能传进去：

```go
Sum([]MyInt{1, 2, 3})   // ✓ 有 ~int 时可以
                        // ✗ 只写 int 时编译报错
```

**`comparable` 约束。**

```go
func Unique[T comparable](in []T) []T {
    seen := make(map[T]struct{})
    // ...
}
```

`comparable` 是预声明约束，表示"支持 `==` 和 `!=`"。用它才能做 map 的 key 或比较。

注意：**切片、map、函数不可比较**，所以 `[]int` 无法传给 `Unique`——这是编译期错误，比运行时的接口断言安全。

**泛型类型。**

```go
type Stack[T any] struct {
    items []T
}

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

func (s *Stack[T]) Pop() (T, bool) {
    var zero T                      // 泛型里用 var 声明零值
    if len(s.items) == 0 {
        return zero, false
    }
    // ...
}
```

注意 `var zero T` 这个写法——泛型代码里不能写 `nil`（`T` 可能是 `int`），需要用 `var` 声明零值。这是泛型代码里最常见的小坑。

**Go 泛型的边界（和 C++ / Java 都不同）。**

| 特性 | Go | C++ 模板 | Java 泛型 |
|------|-----|---------|-----------|
| 泛型方法 | ✗ 不支持 | ✓ | ✓ |
| 特化/偏特化 | ✗ | ✓ | ✗ |
| 运算符重载 | ✗（约束里列出的才行） | ✓ | ✗ |
| 运行时类型信息 | 有（具体类型在编译期确定） | 有 | **擦除** |
| 实现方式 | GC shape + 字典传参 | 完全单态化 | 类型擦除 |

几个具体影响：

- **没有泛型方法**：方法不能有自己独立的类型参数（只有接收者的类型参数）。这就是为什么标准库没有 `func (m Map[K,V]) Map[U](f func(V) U)` 这类方法，只能用顶层函数或接口折中。
- **运算符只能用约束里的**：所以求和得手写 `+`，没有统一的数学库。
- **不做完全单态化**：Go 编译器的策略是"**GC shape stenciling + 字典传参**"——内存布局相同的类型（比如所有指针类型）共享一份实例，具体类型的方法通过字典（dictionary）在运行时查表。所以泛型比 `interface{}` 快（不用装箱），但不等于"每个 T 一份手写代码"。

**什么时候该用泛型？**

| 场景 | 选择 |
|------|------|
| 容器、算法（逻辑相同、类型不同） | **泛型** |
| 需要运行时分派、插件式扩展 | 接口 |
| 只有一两个具体类型，不打算扩展 | 直接写两遍，更简单 |
| 需要极致性能或复杂特化 | 代码生成（`go:generate`） |

标准库已经给了最好的示范：`slices`、`maps`、`sync/atomic` 的泛型包装（`atomic.Pointer[T]`）。它们都是"逻辑完全不依赖具体类型"的典型。

**反例**：为了"看起来高级"把一个只有一种用法的函数改成泛型，结果约束写了一长串，调用方还要显式写类型实参——这是纯粹的负债。**Go 社区的共识是"泛型不是默认选择"**，这和 C++ 的模板文化很不一样。

### 延伸

泛型的另一个实用价值是**类型安全地替代 `interface{}` + 断言**。

```go
// 旧写法：调用方要断言，写错了运行时 panic
func Get(key string) interface{}

// 泛型写法：类型错误编译期就报
func Get[T any](key string) (T, error)
```

代价是 `Get[int]("k")` 这种显式实参略显啰嗦，而且 Go 的类型推断在某些场景（比如嵌套泛型调用）还不够强，需要手动指定。

---

## 第 15 题：性能分析（benchmark / pprof / 逃逸分析）

### 题目回顾

怎么写出可信的基准测试？`-benchmem` 的四列分别是什么意思？pprof 的四种内存口径怎么选？完整的性能优化工作流是什么？

### 核心考点

- `testing.B` 与基准测试的正确写法
- `-benchmem` 四列的含义
- `testing.AllocsPerRun` 精确计数分配
- pprof 的 CPU / 内存 / goroutine / block / mutex profile
- `benchstat` 判断优化是否统计显著
- 优化工作流与常见高收益手段

### 解析

**基准测试必须放在 `_test.go` 文件里。**

这一点很容易踩坑：**普通 `.go` 文件里的 `TestXxx` / `BenchmarkXxx` 不会被 `go test` 发现**，只有 `*_test.go` 才会。所以本项目的第 15 题拆成了两个文件：

- `impl.go`：被测实现 + `Run()`（供 `go run . 15` 使用）
- `bench_test.go`：基准测试和单元测试

**实测结果**（`go test -bench=. -benchmem ./q15_pprof`）：

```
BenchmarkSliceNoPrealloc-10    227241    4890 ns/op   25208 B/op   12 allocs/op
BenchmarkSlicePrealloc-10     1573838     768.7 ns/op      0 B/op    0 allocs/op
BenchmarkConcatPlus-10          32970   36228 ns/op  169705 B/op  199 allocs/op
BenchmarkConcatBuilder-10      987450    1095 ns/op    1792 B/op    1 allocs/op
BenchmarkConcatBuffer-10       810244    1501 ns/op    3584 B/op    2 allocs/op
```

预分配让耗时降到 **1/6**，分配次数从 12 降到 0；`strings.Builder` 比 `+=` 快 **33 倍**。这些不是"微优化"——在高 QPS 服务里，它们直接决定 GC 压力和 P99 延迟。

**读懂 `-benchmem` 的四列。**

| 列 | 含义 | 备注 |
|----|------|------|
| `ns/op` | 每次操作耗时 | 受 CPU 频率/负载影响，跨机器不要直接比 |
| `B/op` | 每次操作分配字节数 | 反映内存带宽压力 |
| `allocs/op` | 每次操作分配次数 | **最容易优化，也最能反映 GC 压力** |
| `MB/s` | 吞吐 | 只有调了 `b.SetBytes` 才有意义 |

**为什么优先看 `allocs/op`？**因为 GC 的成本主要跟"对象数量"和"分配速率"相关，而不是字节数。1000 次小分配比 1 次大分配对 GC 的伤害大得多。

**用 `testing.AllocsPerRun` 做精确断言。**

```go
func TestAllocations(t *testing.T) {
    pre := testing.AllocsPerRun(100, func() { _ = buildPrealloc(1000) })
    if pre != 0 {
        t.Errorf("预分配版本应当零堆分配，实际 %.0f", pre)
    }
}
```

这把"优化"变成了**可回归的测试**——以后谁改坏了，CI 会拦住。这比写注释"这里做过优化，别动"有效得多。

**pprof 的四种内存口径。**

```bash
go test -bench=. -benchmem -memprofile=mem.out ./q15_pprof
go tool pprof -http=:8080 mem.out
```

| 口径 | 含义 | 用来找什么 |
|------|------|-----------|
| `alloc_objects` | 累计分配对象数 | 谁分配得最频繁 |
| `alloc_space` | 累计分配字节数 | 谁分配得最多 |
| `inuse_objects` | 当前存活对象数 | 谁的对象最多 |
| `inuse_space` | 当前存活字节数 | 谁占着内存不放 |

**排查内存泄漏看 `inuse`，排查 GC 压力看 `alloc`**——这两个方向的优化手段完全不同。

其他 profile：

| 类型 | 采集方式 | 用途 |
|------|---------|------|
| CPU | `-cpuprofile` 或 `/debug/pprof/profile` | 找 CPU 热点 |
| heap | `-memprofile` 或 `/debug/pprof/heap` | 找内存热点/泄漏 |
| goroutine | `/debug/pprof/goroutine` | 找 goroutine 泄漏和堆积点 |
| block | `runtime.SetBlockProfileRate` | 找阻塞（channel/mutex） |
| mutex | `runtime.SetMutexProfileFraction` | 找锁竞争 |
| trace | `go tool trace` | 看调度延迟、GC 停顿、goroutine 时间线 |

**`benchstat`：判断优化是否统计显著。**

单次 `go test -bench` 的结果波动可能有 ±10%，很容易"优化"出一个噪声。正确做法是跑多轮：

```bash
go test -bench=Concat -benchtime=3s -count=5 ./q15_pprof | tee new.txt
go test -bench=Concat -benchtime=3s -count=5 ./q15_pprof | tee old.txt
benchstat old.txt new.txt
```

`benchstat` 会做统计检验，输出形如 `-97.0% (p=0.008 n=5+5)`——`p < 0.05` 才算显著变化。

**完整的优化工作流。**

1. **先有可复现的基准或压测**——别凭感觉优化。没有度量就没有优化。
2. **pprof 找到 Top 热点**——80% 的时间通常只在少数几个函数里。先优化最热的那个。
3. **只改一个变量**——同时改三处，你不知道是哪处起了作用。
4. **用 `benchstat -count=5` 以上确认变化不是噪声。**
5. **回归测试保证结果正确**——`TestResultEquality` 就是这个作用。优化不能改变行为。
6. **记录优化前后的数字**——否则下次没人知道为什么这段代码要这么写。

**常见高收益优化清单。**

| 手段 | 典型收益 |
|------|---------|
| 预分配 `slice` / `map` 容量 | 分配次数降一个数量级 |
| `strings.Builder` 替代 `+=` | 从 O(n²) 拷贝降到 O(n) |
| 复用 buffer（`buf[:0]`） | 热路径零分配 |
| `sync.Pool` 复用大对象 | 分配次数降几个数量级（实测 160083 → 31） |
| 避免热路径里的反射 / `fmt.Sprintf` | 省下大量装箱和临时对象 |
| 减少 `interface{}` 装箱 | 省分配，且调用变成静态分派 |
| 批量提交（DB / Redis / HTTP） | 减少 RTT 和系统调用 |
| 合理设置 `GOGC` / `GOMEMLIMIT` | 而非盲目调优 |

**最后一句提醒**：优化要有依据。`-gcflags='-m'` 看逃逸、pprof 看热点、benchstat 看差异——这三件事做完再动手，比"我觉得这样更快"靠谱得多。

---

## 总结

这 15 题串起来，覆盖了 Go 开发者从"会用"到"用对"再到"能定位和解决疑难问题"的三级台阶。

| 题号 | 主题 | 一句话要点 |
|------|------|-----------|
| 1 | slice | 共享底层数组是隐形耦合，cap 决定是否扩容 |
| 2 | defer | return 分三步，命名返回值会被 defer 改 |
| 3 | 并发 | close 只能由发送者做，且要等所有发送者结束 |
| 4 | map | 内置 map 非并发安全，sync.Map 有适用边界 |
| 5 | 内存 | range 变量会复用，值切片通常比指针切片对 GC 更友好 |
| 6 | 内存分配 | 逃逸分析看"生命周期"；预分配和 Builder 能省掉一个数量级的分配 |
| 7 | GC | 三色标记 + 混合写屏障；GOGC 管何时回收，GOMEMLIMIT 管能用多少 |
| 8 | 调度 | P 是执行凭据，阻塞时交接给其他 M；异步抢占消灭了饿死 |
| 9 | channel | 无缓冲是同步交接，有缓冲是环形队列；nil channel 永远阻塞 |
| 10 | 接口 | 两字宽，typed nil 是坑；字段顺序影响内存占用 |
| 11 | error | 用 Is/As 而非 ==，用 %w 保链；错误是值，不是字符串 |
| 12 | panic | recover 必须在 defer 里直接调用；子 goroutine 的 panic 兜不住 |
| 13 | context | 取消只向下传播；忘记 cancel 会泄漏 goroutine 和整棵 context 树 |
| 14 | 泛型 | 约束是接口，~ 表示底层类型；泛型不是默认选择 |
| 15 | 性能 | 先度量再优化；allocs/op 最能反映 GC 压力，benchstat 判断显著性 |

面试时，答对"是什么"只是及格；能说清"为什么"和"边界在哪"，才是资深工程师应有的深度。而第 6~15 题的核心差异在于：**它们问的都不是"语言怎么用"，而是"运行时怎么工作、工程上怎么取舍"。**
