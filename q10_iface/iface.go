package q10_iface

import (
	"fmt"
	"unsafe"
)

// ---------- 1. 接口的两字宽（type + data）----------

type Stringer interface {
	Describe() string
}

type Dog struct{ Name string }

func (d Dog) Describe() string { return "Dog:" + d.Name }

type NilDog struct{ Name string }

func (d *NilDog) Describe() string { return "NilDog:" + d.Name } // 指针接收者

// ---------- 2. 空接口与类型断言 ----------

func assertChain(v any) string {
	// 1) 直接断言，失败会 panic
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%T 不是 string（comma-ok 避免 panic）", v)
	}
	return "string=" + s
}

// typeSwitch 展示类型分支；注意会被自动装箱的数值类型顺序。
func typeSwitch(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case int, int32, int64:
		return fmt.Sprintf("整数族 %v(%T)", x, x)
	case string:
		return fmt.Sprintf("字符串 %q", x)
	case fmt.Stringer:
		return "实现了 Stringer: " + x.String()
	case error:
		return "error: " + x.Error()
	default:
		return fmt.Sprintf("其他类型 %T", x)
	}
}

// ---------- 3. 接口值的内存布局 ----------

type iface struct {
	tab  unsafe.Pointer // 类型信息（itab / _type）
	data unsafe.Pointer // 数据指针
}

func layoutSize() int { return int(unsafe.Sizeof(iface{})) }

// ---------- 4. 结构体字段对齐与填充 ----------

// BadLayout int64 夹在 int32 中间，为了对齐会补 8 字节
type BadLayout struct {
	A int32
	B int64
	C int32
}

// GoodLayout 把大字段往前放，填充最少
type GoodLayout struct {
	B int64
	A int32
	C int32
}

// ---------- 5. 方法集与接口满足关系 ----------

type Speaker interface{ Speak() }

type Cat struct{ Name string }

func (c Cat) Speak()         { fmt.Println("meow", c.Name) } // 值接收者
func (c *Cat) Purr()         { fmt.Println("purr", c.Name) } // 指针接收者
func (c Cat) String() string { return "Cat(" + c.Name + ")" }

func Run() {
	fmt.Println("=== Q10: 接口、类型系统与内存布局 ===")

	// 1. nil 接口 vs 接口持有 nil 指针
	fmt.Println("--- 1. 最经典的接口陷阱：typed nil ---")
	var s Stringer // 接口零值：tab=nil, data=nil
	fmt.Printf("   var s Stringer; s == nil ? %v\n", s == nil)

	var nd *NilDog // 指针是 nil
	s = nd         // 装箱：tab=*NilDog, data=nil
	fmt.Printf("   s = (*NilDog)(nil); s == nil ? %v  <-- 这里是 false！\n", s == nil)
	panicked := func() (p bool) {
		defer func() { p = recover() != nil }()
		_ = s.Describe() // 调用时解引用 nil 接收者 -> panic
		return
	}()
	fmt.Printf("   调用 s.Describe() 会 panic: %v\n", panicked)
	fmt.Println("   结论：接口判空要看\"tab 和 data 是否都为空\"；")
	fmt.Println("        返回接口的函数里千万不要 return 一个 nil 具体指针")

	// 修复写法
	var s2 Stringer
	if nd != nil {
		s2 = nd
	}
	fmt.Printf("   修复后 s2 == nil ? %v\n", s2 == nil)

	// 2. 值接收者 vs 指针接收者
	fmt.Println("--- 2. 方法集决定谁能满足接口 ---")
	var sp Speaker = Cat{Name: "Tom"} // 值接收者方法：Cat 和 *Cat 都满足
	sp.Speak()
	var sp2 Speaker = &Cat{Name: "Jerry"}
	sp2.Speak()
	fmt.Println("   规则：T 的方法集 = 值接收者方法；*T 的方法集 = 值 + 指针接收者方法")
	fmt.Println("   所以 (*NilDog).Describe 只有 *NilDog 满足 Stringer，NilDog 不满足")

	// 3. 类型断言
	fmt.Println("--- 3. 类型断言与 type switch ---")
	fmt.Printf("   %s\n", assertChain("hello"))
	fmt.Printf("   %s\n", assertChain(42))
	for _, v := range []any{nil, 7, "go", 3.14, fmt.Errorf("boom"), Cat{Name: "Mimi"}} {
		fmt.Printf("   typeSwitch(%-8v) -> %s\n", fmt.Sprint(v), typeSwitch(v))
	}
	fmt.Println("   注意 case 顺序：fmt.Stringer 在 error 之前会先命中 Stringer（Cat 同时满足两者时以先匹配者为准）")

	// 4. 内存布局
	fmt.Println("--- 4. 接口值的大小与结构体填充 ---")
	fmt.Printf("   interface 值大小 = %d 字节（1 个类型指针 + 1 个数据指针）\n", layoutSize())
	fmt.Printf("   BadLayout  = %2d 字节 (int32,int64,int32)\n", unsafe.Sizeof(BadLayout{}))
	fmt.Printf("   GoodLayout = %2d 字节 (int64,int32,int32)\n", unsafe.Sizeof(GoodLayout{}))
	fmt.Println("   字段顺序不改语义，但能省内存：高频创建的小对象尤其值得排一排")
	fmt.Println("   小整数装箱进接口还可能被 runtime 缓存（0~255），不一定真的分配")

	// 5. 接口调用的开销
	fmt.Println("--- 5. 接口调用为什么慢一点 ---")
	fmt.Println("   直接调用：编译期确定地址，可能被内联 -> 可静态分派")
	fmt.Println("   接口调用：运行时从 itab 查函数指针，无法内联 -> 动态分派")
	fmt.Println("   优化手段：热点路径用具体类型或泛型；泛型在实例化时是静态分派")
	fmt.Println("   但别过早优化：接口带来的抽象收益通常远大于这点开销")
}
