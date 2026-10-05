package q19_slice

import (
	"fmt"
	"unsafe"
)

// ---------- 1. 复刻 append 的扩容决策（Go 1.18+ 的公式） ----------
//
// 源码 runtime/slice.go 的 nextslicecap：
//
//	newcap := oldCap
//	doublecap := newcap + newcap
//	if newLen > doublecap {        // 需求超过两倍，直接用需求值
//	    return newLen
//	}
//	const threshold = 256
//	if oldCap < threshold {
//	    return doublecap           // 小切片：直接翻倍
//	}
//	for {
//	    newcap += (newcap + 3*threshold) / 4    // 增长约 1.25 倍 + 192
//	    if newcap >= newLen {
//	        return newcap
//	    }
//	}
//
// Go 1.18 之前是硬阈值 1024 + 1.25 倍，之后改成 256 + (x+3*256)/4 的
// 平滑过渡公式，避免 1024 附近出现"从 2 倍突然掉到 1.25 倍"的突变。
func nextCap(oldCap, newLen int) int {
	if newLen < 0 {
		panic("len out of range")
	}
	newCap := oldCap
	doubleCap := newCap + newCap
	if newLen > doubleCap {
		return newLen
	}
	const threshold = 256
	if oldCap < threshold {
		return doubleCap
	}
	for {
		newCap += (newCap + 3*threshold) / 4
		if newCap >= newLen {
			return newCap
		}
	}
}

// ---------- 2. 观测真实扩容行为 ----------

type growStep struct {
	OldCap, NewCap int
	PtrChanged     bool
}

// observeGrow 逐个 append，记录每次容量变化和底层数组是否换过。
// 用 &s[0] 判断地址变化：地址变了说明发生了扩容（分配了新数组）。
func observeGrow(n int) []growStep {
	var steps []growStep
	s := make([]int, 0)
	prevCap := cap(s)
	var prevPtr *int

	for i := 0; i < n; i++ {
		s = append(s, i)
		if cap(s) != prevCap {
			var ptr *int
			if len(s) > 0 {
				ptr = &s[0]
			}
			steps = append(steps, growStep{
				OldCap:     prevCap,
				NewCap:     cap(s),
				PtrChanged: prevPtr != nil && ptr != prevPtr,
			})
			prevCap = cap(s)
			prevPtr = ptr
		}
	}
	return steps
}

// ---------- 3. 数组 vs 切片：值语义 vs 引用语义 ----------

type arr [3]int

// modifyArray 按值传数组：改的是副本。
func modifyArray(a arr) { a[0] = 999 }

// modifySlice 按值传切片：拷贝的是 slice header（3 个字段），底层数组共享。
func modifySlice(s []int) { s[0] = 999 }

// modifySliceHeader 在函数内 append 并返回，观察 header 的变化：
// 因为 header 是值传递，原切片的 len 不受影响。
func modifySliceHeader(s []int) []int {
	s = append(s, 100)
	return s
}

// sliceHeaderSize 用 unsafe 确认 slice header 是 3 个机器字。
func sliceHeaderSize() uintptr {
	type sliceHeader struct {
		Data uintptr
		Len  int
		Cap  int
	}
	return unsafe.Sizeof(sliceHeader{})
}

// ---------- 4. 共享底层数组导致的"幽灵写入" ----------

// leakThroughSubslice 演示子切片写入影响原切片。
func leakThroughSubslice() (original []int, leaked bool) {
	s := []int{1, 2, 3, 4, 5}
	sub := s[1:3]         // len=2 cap=4，共享底层数组
	sub = append(sub, 99) // cap 足够，直接写底层数组 index 3
	return s, s[3] == 99
}

// isolateWithThreeIndex 用三索引切片切断共享。
func isolateWithThreeIndex() (original []int, leaked bool) {
	s := []int{1, 2, 3, 4, 5}
	sub := s[1:3:3]       // 把 cap 也限制成 2
	sub = append(sub, 99) // cap 不足 -> 分配新数组
	return s, s[3] == 99
}

// ---------- 5. 删除元素的两种写法与内存泄漏 ----------

type big struct {
	payload [1024]byte
}

// deleteKeepOrder 保持顺序：copy 覆盖。
func deleteKeepOrder(s []int, i int) []int {
	return append(s[:i], s[i+1:]...)
}

// deleteFast 不保序：把最后一个元素搬到被删位置，O(1)。
func deleteFast(s []int, i int) []int {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

// deleteWithZeroing 删除指针元素时，先把尾部元素置零，避免底层数组
// 继续持有已删除对象的引用（这是最常见的切片内存泄漏）。
func deleteWithZeroing(s []*big, i int) []*big {
	copy(s[i:], s[i+1:])
	s[len(s)-1] = nil // 关键：切断引用，否则对象无法被 GC
	return s[:len(s)-1]
}

// deleteWithoutZeroing 是错误示范：底层数组尾部仍然引用着被删对象。
func deleteWithoutZeroing(s []*big, i int) []*big {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}

func Run() {
	fmt.Println("=== Q19: slice 扩容公式与数组/切片语义 ===")

	// 1. 扩容公式
	fmt.Println("--- 1. 扩容公式推导（Go 1.18+：256 阈值 + (x+3*256)/4）---")
	fmt.Println("   旧容量 -> append(旧容量+1) 后的新容量：")
	for _, old := range []int{0, 1, 2, 4, 8, 128, 256, 512, 1024, 2048, 4096} {
		fmt.Printf("      cap %5d -> %5d", old, nextCap(old, old+1))
		if old > 0 {
			fmt.Printf("   (增长 %.2f 倍)", float64(nextCap(old, old+1))/float64(old))
		}
		fmt.Println()
	}
	fmt.Println("   注意：old=0 时 doublecap=0，直接返回 newLen，所以第一个 append 给 cap=1")
	fmt.Println("   Go 1.18 之前是 1024 硬阈值：cap<1024 翻倍，否则 1.25 倍；")
	fmt.Println("   新旧公式在 256~1024 之间的行为差异是老版本面试题的常见考点")

	// 2. 真实扩容观测
	fmt.Println("--- 2. 真实扩容观测（前 20 次容量变化）---")
	steps := observeGrow(600)
	for i, st := range steps {
		if i >= 14 {
			fmt.Printf("      ... (共 %d 次扩容)\n", len(steps))
			break
		}
		fmt.Printf("      cap %4d -> %4d  底层数组换了=%v\n", st.OldCap, st.NewCap, st.PtrChanged)
	}
	fmt.Println("   结论：扩容 = 分配新数组 + 拷贝旧数据 + 更新 header，全部发生在 append 内部")
	fmt.Println("        所以\"预分配容量\"省掉的不只是内存，还有拷贝的 CPU 时间（见 Q6/Q15）")

	// 3. 数组 vs 切片
	fmt.Println("--- 3. 数组是值类型，切片是\"值类型的 header\" ---")
	a := arr{1, 2, 3}
	modifyArray(a)
	fmt.Printf("   传数组  modifyArray(a) 后 a=%v（未被改动，传的是整个数组的副本）\n", a)

	sl := []int{1, 2, 3}
	modifySlice(sl)
	fmt.Printf("   传切片  modifySlice(sl) 后 sl=%v（被改动了！底层数组共享）\n", sl)

	s2 := []int{1, 2, 3}
	got := modifySliceHeader(s2)
	fmt.Printf("   函数内 append 后：原切片 len=%d cap=%d，返回的切片 len=%d（header 是值传递）\n",
		len(s2), cap(s2), len(got))
	fmt.Printf("   slice header 大小 = %d 字节（数据指针 + len + cap）\n", sliceHeaderSize())
	fmt.Println("   关键：切片本身是值类型，拷贝的是 24 字节的 header；")
	fmt.Println("        但 header 指向同一个底层数组，所以元素改动会互相可见")

	// 4. 幽灵写入
	fmt.Println("--- 4. 子切片共享底层数组（第 1 题的进阶版）---")
	orig, leaked := leakThroughSubslice()
	fmt.Printf("   s[1:3] 后 append：原切片 = %v，被意外改写=%v\n", orig, leaked)
	orig2, leaked2 := isolateWithThreeIndex()
	fmt.Printf("   s[1:3:3] 后 append：原切片 = %v，被意外改写=%v\n", orig2, leaked2)
	fmt.Println("   记忆方式：切片的三要素是 ptr/len/cap，cap 决定\"还能不能原地写\"")

	// 5. 删除元素
	fmt.Println("--- 5. 删除元素的两种写法 ---")
	nums := []int{0, 1, 2, 3, 4, 5}
	fmt.Printf("   保序删除 index=2: %v（O(n)，copy 覆盖）\n", deleteKeepOrder(append([]int(nil), nums...), 2))
	fmt.Printf("   快速删除 index=2: %v（O(1)，但顺序被打乱）\n", deleteFast(append([]int(nil), nums...), 2))
	fmt.Println("   顺序重要时用前者，顺序无所谓（如集合、对象池）用后者")

	// 6. 切片内存泄漏
	fmt.Println("--- 6. 切片持有指针时的内存泄漏 ---")
	items := []*big{{}, {}, {}, {}}
	fmt.Printf("   池中对象数 = %d，底层数组 cap = %d\n", len(items), cap(items))

	trimmed := deleteWithoutZeroing(append([]*big(nil), items...), 1)
	fmt.Printf("   不置零：len=%d，但底层数组尾部仍指向被删对象 -> 该对象无法被 GC\n", len(trimmed))
	trimmed2 := deleteWithZeroing(append([]*big(nil), items...), 1)
	fmt.Printf("   置零后：len=%d，尾部引用已切断 -> 对象可被回收\n", len(trimmed2))
	fmt.Println("   同类坑：大切片截取一小段长期持有（s := big[:10]），")
	fmt.Println("          只要这一段活着，整个底层数组都不会被回收 —— 需要 copy 出来")

	fmt.Println("--- 7. 其他高频小坑 ---")
	fmt.Println("   nil 切片可以直接 append、len 为 0、可以 range（零值可用）")
	fmt.Println("   但 nil 切片和空切片的区别在 JSON 序列化上会暴露：null vs []")
	fmt.Println("   var s []int; json.Marshal(s) -> null；s := []int{} -> []")
	fmt.Println("   切片不能直接用 == 比较（只能和 nil 比），要比较用 slices.Equal")
}
