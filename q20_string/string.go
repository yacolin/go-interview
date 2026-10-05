package q20_string

import (
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

// ---------- 1. string 的结构与不可变性 ----------

// stringHeader 是 string 的运行时表示：数据指针 + 长度（没有 cap）。
type stringHeader struct {
	Data unsafe.Pointer
	Len  int
}

func stringHeaderSize() uintptr { return unsafe.Sizeof(stringHeader{}) }

// ---------- 2. 子串共享底层数组导致的内存泄漏 ----------

var bigString = strings.Repeat("abcdefghij", 100000) // 1MB

// subKeepRef 直接切片：返回的子串仍指向那 1MB 的底层数组。
func subKeepRef() string { return bigString[:10] }

// subCopy 拷贝一份：只占 10 字节，与原来那 1MB 彻底脱钩。
func subCopy() string {
	b := make([]byte, 10)
	copy(b, bigString[:10])
	return string(b)
}

// ---------- 3. 零拷贝转换 ----------
//
// 注意：下面两个函数用 unsafe 把 string 和 []byte 的 header 互相转换，
// 完全不做内存拷贝。代价是破坏了 string 的不可变性 —— 拿到 []byte 后
// 一旦写入，就会改掉那个本该不可变的 string。
//
// 这里仅作原理演示。生产代码请用标准库：
//   strings.Builder（构造）
//   []byte(s) / string(b)（安全、会拷贝）
//   或 Go 1.20+ 的 unsafe.String / unsafe.SliceData

// stringToBytesZeroCopy 把 string 转成 []byte，不拷贝。
func stringToBytesZeroCopy(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	sh := (*stringHeader)(unsafe.Pointer(&s))
	return unsafe.Slice((*byte)(sh.Data), sh.Len)
}

// bytesToStringZeroCopy 把 []byte 转成 string，不拷贝。
func bytesToStringZeroCopy(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// ---------- 4. 字符串拼接性能 ----------

func apiDemo() string {
	var b strings.Builder
	// Grow 预分配，避免 Builder 内部扩容
	b.Grow(64)
	b.WriteString("go")
	b.WriteByte('-')
	b.WriteRune('语')
	b.WriteString("interview")

	var out []string
	out = append(out, "Builder: "+b.String())
	out = append(out, "Contains: "+fmt.Sprint(strings.Contains(b.String(), "interview")))
	out = append(out, "SplitN: "+fmt.Sprint(strings.SplitN("a,b,c", ",", 2)))
	out = append(out, "Cut: "+func() string {
		before, after, found := strings.Cut("key=value", "=")
		return fmt.Sprintf("%s|%s|%v", before, after, found)
	}())
	out = append(out, "ReplaceAll: "+strings.ReplaceAll("a-b-c", "-", "+"))
	out = append(out, "TrimSpace: "+fmt.Sprintf("%q", strings.TrimSpace("  x  ")))
	return strings.Join(out, "\n           ")
}

// ---------- 基准测试 ----------

func Run() {
	fmt.Println("=== Q20: string 与 []byte —— 不可变性、零拷贝与内存泄漏 ===")

	// 1. 结构
	fmt.Println("--- 1. string 的运行时表示 ---")
	fmt.Printf("   string header = %d 字节（数据指针 + 长度，注意没有 cap）\n", stringHeaderSize())
	fmt.Println("   所以 string 是不可变的：没有 cap 就意味着无法原地追加，")
	fmt.Println("   任何\"修改\"都是在分配新字符串")

	// 2. 子串泄漏
	fmt.Println("--- 2. 子串共享底层数组导致的内存泄漏 ---")
	kept := subKeepRef()
	copied := subCopy()
	fmt.Printf("   bigString 大小 = %d 字节\n", len(bigString))
	fmt.Printf("   切片取前 10 字节: %q，但它仍指向那 1MB 底层数组\n", kept)
	fmt.Printf("   copy 取前 10 字节: %q，只占 %d 字节，原数组可被回收\n", copied, len(copied))
	fmt.Println("   验证方法：pprof 里看到 service 函数持有巨大 inuse_space，")
	fmt.Println("            但代码里并没有大对象 —— 通常是子串/子切片没脱钩")

	// 3. 零拷贝
	fmt.Println("--- 3. string <-> []byte 的零拷贝转换（unsafe）---")
	literal := "hello"
	bs := stringToBytesZeroCopy(literal)
	fmt.Printf("   零拷贝读出 string: %q -> %v（共享同一块内存，无分配）\n", literal, bs)

	sum := 0
	for _, b := range bs {
		sum += int(b)
	}
	fmt.Printf("   只读遍历求和 = %d（安全，读不破坏任何不变量）\n", sum)

	// 写入呢？这里刻意不演示，因为它会直接杀掉进程。
	fmt.Println("   写入 bs[0] 会怎样？")
	fmt.Println("     字符串字面量位于二进制的只读数据段（.rodata），写入触发 SIGBUS ——")
	fmt.Println("     不是 panic，recover 抓不住，进程立刻死。")
	fmt.Println("     （这一条我在本地实测过，确认是 SIGBUS 而非 panic，所以演示里不敢跑）")
	fmt.Println("     如果 string 是堆上构造的（如 strings.Repeat），写入不会立刻崩，")
	fmt.Println("     但会静默改掉那个「不可变」的字符串 —— 更危险，因为它会污染其他持有者。")

	// 正确做法
	writable := []byte(literal) // 拷贝一份，拿到独立的可写内存
	writable[0] = 'H'
	fmt.Printf("   正确做法 []byte(s) 先拷贝: %q -> %q（改动不影响原 string）\n",
		literal, string(writable))
	fmt.Println("   结论：零拷贝只能用于「只读视图」；要写就必须拷贝")
	fmt.Println("   生产代码优先用标准库的 unsafe.String / unsafe.SliceData，")
	fmt.Println("   并保证：不写入、不被长期持有、不跨越 GC 边界")

	// 4. 拼接
	fmt.Println("--- 4. 拼接 100 个 10 字节片段 ---")
	for _, bc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"s += p        ", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = concatPlus()
			}
		}},
		{"strings.Builder", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = concatBuilder()
			}
		}},
		{"strings.Join  ", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = concatJoin()
			}
		}},
	} {
		res := testing.Benchmark(bc.fn)
		fmt.Printf("   %s : %10d ns/op  %8d B/op  %4d allocs/op\n",
			bc.name, res.NsPerOp(), res.AllocedBytesPerOp(), res.AllocsPerOp())
	}
	fmt.Println("   结论：+= 是 O(n²) 拷贝；Builder/Join 是 O(n)，且分配次数相差两个数量级")
	fmt.Println("   选型：已知片段集合 -> strings.Join；流式构造 -> strings.Builder")

	// 5. 转换开销
	fmt.Println("--- 5. string <-> []byte 转换开销（1024 字节）---")
	for _, bc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"string -> []byte 安全拷贝", func(b *testing.B) {
			s := strings.Repeat("x", 1024)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				byteSliceSink = []byte(s)
			}
		}},
		{"string -> []byte 零拷贝  ", func(b *testing.B) {
			s := strings.Repeat("x", 1024)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				byteSliceSink = stringToBytesZeroCopy(s)
			}
		}},
		{"[]byte -> string 安全拷贝", func(b *testing.B) {
			buf := []byte(strings.Repeat("x", 1024))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				stringSink = string(buf)
			}
		}},
		{"[]byte -> string 零拷贝  ", func(b *testing.B) {
			buf := []byte(strings.Repeat("x", 1024))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				stringSink = bytesToStringZeroCopy(buf)
			}
		}},
	} {
		res := testing.Benchmark(bc.fn)
		fmt.Printf("   %s : %10d ns/op  %8d B/op  %4d allocs/op\n",
			bc.name, res.NsPerOp(), res.AllocedBytesPerOp(), res.AllocsPerOp())
	}
	fmt.Println("   零拷贝快得多，但只有当\"转换本身是热点\"（如高频协议编解码）时才值得用")
	fmt.Println("   而且必须保证转换后的 []byte 不被写入、不被长期持有")

	// 6. 常用 API
	fmt.Println("--- 6. 高频字符串 API ---")
	fmt.Printf("           %s\n", apiDemo())

	// 7. 陷阱清单
	fmt.Println("--- 7. 陷阱清单 ---")
	fmt.Println("   x 循环里 s += 拼接（用 Builder）")
	fmt.Println("   x 用 + 拼 SQL/HTML（用参数化查询/模板，还涉及注入）")
	fmt.Println("   x 对 string 做 for i := 0; i < len(s); i++ 取\"字符\"")
	fmt.Println("     —— len 是字节数，中文/emoji 会取到半个字符，应该 range 或 []rune")
	fmt.Println("   x 大字符串截一小段长期持有（内存泄漏）")
	fmt.Println("   v strings.Builder 用完不要复制（内部含指针，复制会踩别名）")
	fmt.Println("   v 频繁拼接且长度可估时先 Grow")

	// 8. rune 与字节
	fmt.Println("--- 8. len 是字节数，不是字符数 ---")
	for _, x := range []string{"abc", "中文", "a中b", "🙂"} {
		fmt.Printf("   %-6q len=%d bytes  rune 数=%d  range 遍历=%d 次\n",
			x, len(x), len([]rune(x)), func() int {
				n := 0
				for range x {
					n++
				}
				return n
			}())
	}
	fmt.Println("   UTF-8 可变长：ASCII 1 字节，中文 3 字节，emoji 4 字节")
	fmt.Println("   []rune(s) 会分配新数组，只看长度用 utf8.RuneCountInString 更省")
}

var pieces = func() []string {
	p := make([]string, 100)
	for i := range p {
		p[i] = "0123456789"
	}
	return p
}()

func concatPlus() string {
	s := ""
	for _, p := range pieces {
		s += p
	}
	return s
}

func concatBuilder() string {
	var b strings.Builder
	b.Grow(len(pieces) * 10)
	for _, p := range pieces {
		b.WriteString(p)
	}
	return b.String()
}

func concatJoin() string { return strings.Join(pieces, "") }

// 两个基准都必须"消费"转换结果，否则编译器会把整个转换优化掉
// （内部的 escape analysis 会判定它不需要分配），测出来就是 0 ns/op。
// 注意：只把 len 存起来还不够 —— 编译器能证明 []byte(s) 的结果没逃逸，
// 于是直接优化掉整个分配。必须把切片本身存到全局变量，强制它逃逸。

var byteSliceSink []byte

var stringSink string
