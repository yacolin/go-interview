package q14_generics

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------- 1. 类型参数与约束 ----------

// Number 用 | 列出允许的类型集合。
type Number interface {
	~int | ~int64 | ~float64 // ~ 表示"底层类型是 int 的所有命名类型"
}

// MyInt 底层类型是 int，因此 ~int 让它也能被 Sum 接受。
type MyInt int

// Sum 既支持内建类型也支持自定义类型。
func Sum[T Number](nums []T) T {
	var total T
	for _, n := range nums {
		total += n
	}
	return total
}

// ---------- 2. comparable 约束 ----------

// Unique 需要 == 比较，所以必须用 comparable 约束。
func Unique[T comparable](in []T) []T {
	seen := make(map[T]struct{}, len(in))
	out := make([]T, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// ---------- 3. any 约束 + 类型断言 ----------

func Keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------- 4. 泛型类型 ----------

// Stack 是泛型数据结构。
type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

// ---------- 5. 泛型方法接口：不支持在方法上再引入类型参数 ----------

// Mapper 是 Go 里做"泛型 map"的常见折中：约束放在接口上，方法本身不再泛型。
type Mapper[T, R any] interface {
	Map(T) R
}

type IntToString struct{}

func (IntToString) Map(v int) string { return strconv.Itoa(v) }

func MapAll[T, R any](in []T, m Mapper[T, R]) []R {
	out := make([]R, 0, len(in))
	for _, v := range in {
		out = append(out, m.Map(v))
	}
	return out
}

// ---------- 6. 泛型 vs 接口 vs codegen 的性能 ----------

// StringifyIface 走接口动态分派，T 会被装箱成 any。
func StringifyIface(in []any) string {
	var b strings.Builder
	for _, v := range in {
		b.WriteString(fmt.Sprintf("%v", v))
	}
	return b.String()
}

// StringifyGeneric 走泛型，实例化后是静态分派，不装箱。
func StringifyGeneric[T fmt.Stringer](in []T) string {
	var b strings.Builder
	for _, v := range in {
		b.WriteString(v.String())
	}
	return b.String()
}

type ID int

func (i ID) String() string { return "ID-" + strconv.Itoa(int(i)) }

// ---------- Run ----------

func Run() {
	fmt.Println("=== Q14: 泛型、类型约束与选型 ===")

	// 1. 约束与 ~
	fmt.Println("--- 1. 类型参数 + 约束（~ 的含义）---")
	fmt.Printf("   Sum([]int{1,2,3})          = %v\n", Sum([]int{1, 2, 3}))
	fmt.Printf("   Sum([]float64{1.5,2.5})    = %v\n", Sum([]float64{1.5, 2.5}))
	fmt.Printf("   Sum([]MyInt{1,2,3})        = %v  <-- ~int 让自定义类型也能用\n", Sum([]MyInt{1, 2, 3}))
	fmt.Println("   去掉 ~ 就只能传底层类型完全相同的类型，自定义类型会被拒绝")

	// 2. comparable
	fmt.Println("--- 2. comparable 约束 ---")
	fmt.Printf("   Unique([1 2 2 3 1])        = %v\n", Unique([]int{1, 2, 2, 3, 1}))
	fmt.Printf("   Unique([a b a])            = %v\n", Unique([]string{"a", "b", "a"}))
	fmt.Println("   注意：切片/map/函数不可比较，所以 []int 无法传给 Unique（编译期就报错）")

	// 3. 泛型类型
	fmt.Println("--- 3. 泛型类型 Stack[T] ---")
	st := &Stack[string]{}
	st.Push("a")
	st.Push("b")
	v1, _ := st.Pop()
	v2, _ := st.Pop()
	_, ok := st.Pop()
	fmt.Printf("   Pop -> %q, %q, 空栈时 ok=%v（返回零值而不是 panic）\n", v1, v2, ok)

	// 4. 泛型接口
	fmt.Println("--- 4. 泛型函数 + 接口协作 ---")
	nums := []int{1, 2, 3}
	fmt.Printf("   MapAll(%v, IntToString)    = %v\n", nums, MapAll(nums, IntToString{}))

	// 5. 边界
	fmt.Println("--- 5. Go 泛型的边界（和 C++/Java 不同）---")
	fmt.Println("   x 方法不能有自己独立的类型参数（没有泛型方法）")
	fmt.Println("   x 不支持特化/偏特化，不能针对某个 T 写另一套实现")
	fmt.Println("   x 运算符只能用约束里列出的（所以求和要手写 +，没有统一数学库）")
	fmt.Println("   x 和 C++ 模板不同：Go 不做完全单态化，而是用 GC shape + 字典传参")
	fmt.Println("     （相同内存布局的类型共享一份实例，不同类型走字典里的函数表）")
	fmt.Println("     所以接口装箱的收益有，但不等于「每个 T 一份手写代码」")
	fmt.Println("   v 类型实参在编译期就确定，无需 interface{} 断言，类型错误编译期暴露")
	fmt.Println("   v 类型安全 + 免去 interface{} 断言，是它最大的价值")

	// 6. 选型
	fmt.Println("--- 6. 该不该上泛型 ---")
	fmt.Printf("   泛型版 Stringify: %s\n", StringifyGeneric([]ID{1, 2}))
	fmt.Printf("   接口版 Stringify: %s\n", StringifyIface([]any{1, 2}))
	fmt.Println("   容器/算法这类\"逻辑相同、类型不同\"的代码：泛型最合适")
	fmt.Println("   典型用例：sync/atomic 的泛型包装、slices/maps 标准库、并发安全容器")
	fmt.Println("   只有一两个具体类型、且不打算扩展：直接写两遍更简单，别为了泛型而泛型")
	fmt.Println("   需要运行时分派/插件式扩展：接口更合适")
	fmt.Println("   需要极致性能或复杂特化：代码生成（go:generate）仍然有用武之地")
}
