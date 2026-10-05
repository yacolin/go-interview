package q16_reflect

import (
	"fmt"
	"reflect"
	"testing"
)

// ---------- 被测类型 ----------

type User struct {
	ID    int    `json:"id" db:"user_id" validate:"required,min=1"`
	Name  string `json:"name" db:"name" validate:"required"`
	Email string `json:"email,omitempty" db:"email"`
	age   int    // 未导出字段
}

func (u User) String() string { return fmt.Sprintf("User#%d(%s)", u.ID, u.Name) }

// ---------- 1. 第一定律：TypeOf / ValueOf 成对使用 ----------

func describe(v any) string {
	t := reflect.TypeOf(v)
	val := reflect.ValueOf(v)
	if t == nil {
		return "TypeOf(nil) == nil（这就是 nil 接口的特殊之处）"
	}
	return fmt.Sprintf("Type=%s Kind=%s Value=%v CanSet=%v",
		t, t.Kind(), val, val.CanSet())
}

// ---------- 2. 第二定律：Value 可以还原成接口 ----------

func interfaceRoundTrip(v any) (any, bool) {
	rv := reflect.ValueOf(v)
	// 反射对象 -> interface{}：用 Interface() 还原，再用类型断言拿回具体类型
	back := rv.Interface()
	n, ok := back.(int)
	return n, ok
}

// ---------- 3. 第三定律：要修改值必须传指针 ----------

// setByValue 传值：拿到的是副本，CanSet == false，SetInt 会 panic。
func setByValue(v any) (canSet bool) {
	rv := reflect.ValueOf(v)
	return rv.CanSet()
}

// setByPtr 传指针：Elem() 解引用后是可寻址的，可以改。
func setByPtr(v any, newVal int64) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	elem := rv.Elem()
	if elem.Kind() == reflect.Int64 && elem.CanSet() {
		elem.SetInt(newVal)
	}
}

// ---------- 4. 结构体遍历与 tag 解析 ----------

// Dump 把结构体字段和 tag 打出来，这是 ORM / 参数校验器 / JSON 序列化的工作方式。
func Dump(v any) []string {
	t := reflect.TypeOf(v)
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		out = append(out, fmt.Sprintf("%s(%s) json=%q db=%q validate=%q 导出=%v",
			f.Name, f.Type, f.Tag.Get("json"), f.Tag.Get("db"),
			f.Tag.Get("validate"), f.IsExported()))
	}
	return out
}

// ---------- 5. 反射构造与调用 ----------

// CallByName 按名字调用方法（插件/路由/依赖注入的底层机制）。
func CallByName(v any, method string, args ...any) ([]any, error) {
	mv := reflect.ValueOf(v).MethodByName(method)
	if !mv.IsValid() {
		return nil, fmt.Errorf("方法 %s 不存在", method)
	}
	in := make([]reflect.Value, 0, len(args))
	for _, a := range args {
		in = append(in, reflect.ValueOf(a))
	}
	// 注意：参数类型不匹配会直接 panic，所以生产代码要先做类型检查
	out := mv.Call(in)
	res := make([]any, 0, len(out))
	for _, o := range out {
		res = append(res, o.Interface())
	}
	return res, nil
}

// ---------- 6. 用反射实现通用转换（encoding 风格的入口） ----------

// SetFieldByTag 按 json tag 名设置字段，模拟 JSON 反序列化的核心逻辑。
func SetFieldByTag(dst any, tagName, fieldName string, value any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("dst 必须是非 nil 指针")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("dst 必须指向结构体")
	}
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get(tagName) != fieldName {
			continue
		}
		if !f.IsExported() {
			return fmt.Errorf("字段 %s 未导出，反射无法设置（unexported field）", f.Name)
		}
		fv := rv.Field(i)
		v := reflect.ValueOf(value)
		if !v.Type().AssignableTo(fv.Type()) {
			return fmt.Errorf("类型不匹配: 需要 %s, 收到 %s", fv.Type(), v.Type())
		}
		fv.Set(v)
		return nil
	}
	return fmt.Errorf("没有 tag %s=%q 的字段", tagName, fieldName)
}

// ---------- 7. 反射的性能代价 ----------

func readViaReflection(users []any) int {
	total := 0
	for _, u := range users {
		rv := reflect.ValueOf(u).FieldByName("ID")
		total += int(rv.Int())
	}
	return total
}

func readDirect(users []User) int {
	total := 0
	for i := range users {
		total += users[i].ID
	}
	return total
}

// ---------- 8. 一个真实的坑：Kind() vs Type() ----------

func kindVsType(v any) string {
	t := reflect.TypeOf(v)
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return fmt.Sprintf("%s 的 Kind 是 %s，元素类型是 %s", t, t.Kind(), t.Elem())
	case reflect.Map:
		return fmt.Sprintf("%s 的 Kind 是 %s，key=%s value=%s", t, t.Kind(), t.Key(), t.Elem())
	case reflect.Pointer:
		return fmt.Sprintf("%s 的 Kind 是 %s，指向 %s", t, t.Kind(), t.Elem())
	default:
		return fmt.Sprintf("%s 的 Kind 是 %s", t, t.Kind())
	}
}

// ---------- 基准测试：反射 vs 直接访问 ----------

func Run() {
	fmt.Println("=== Q16: 反射（reflect）—— 三定律、tag 与代价 ===")

	// 1. TypeOf / ValueOf
	fmt.Println("--- 1. 第一定律：反射对象来自接口值 ---")
	for _, v := range []any{42, "go", 3.14, []int{1, 2}, map[string]int{}, new(User), nil} {
		fmt.Printf("   %s\n", describe(v))
	}
	fmt.Println("   注意：TypeOf(nil) 返回 nil 而不是 panic —— 但直接调 t.Kind() 就会空指针恐慌")

	// 2. 还原接口
	fmt.Println("--- 2. 第二定律：Value.Interface() 还原成接口值 ---")
	n, ok := interfaceRoundTrip(7)
	fmt.Printf("   interfaceRoundTrip(7) = %v, ok=%v（反射对象又变回了普通值）\n", n, ok)

	// 3. 可设置性
	fmt.Println("--- 3. 第三定律：要改值必须传指针 ---")
	u := User{ID: 1}
	fmt.Printf("   传值  CanSet=%v（SetInt 会 panic）\n", setByValue(u.ID))

	var x int64 = 10
	fmt.Printf("   传值  CanSet=%v\n", setByValue(x))
	setByPtr(&x, 99)
	fmt.Printf("   传指针 setByPtr(&x, 99) -> x=%d\n", x)
	fmt.Println("   原因：reflect.ValueOf 拿到的是接口里的副本，副本不可寻址；")
	fmt.Println("        Elem() 解引用指针后，指向的才是原变量，才可寻址可设置")

	// 4. tag
	fmt.Println("--- 4. 结构体 tag：ORM/校验/序列化的工作机制 ---")
	for _, line := range Dump(User{}) {
		fmt.Printf("   %s\n", line)
	}

	// 5. 反射调用方法
	fmt.Println("--- 5. 反射调用方法（插件/路由/DI 的底层） ---")
	res, err := CallByName(User{ID: 5, Name: "Tom"}, "String")
	fmt.Printf("   CallByName(user, \"String\") = %v, err=%v\n", res, err)
	_, err = CallByName(u, "NotExist")
	fmt.Printf("   调用不存在的方法: err=%v\n", err)

	// 6. 按 tag 写字段
	fmt.Println("--- 6. 按 tag 赋值（模拟 JSON 反序列化） ---")
	var target User
	if err := SetFieldByTag(&target, "json", "name", "Alice"); err != nil {
		fmt.Printf("   设置 name 失败: %v\n", err)
	} else {
		fmt.Printf("   设置 json=name -> %q\n", target.Name)
	}
	fmt.Printf("   设置未导出字段: err=%v\n", SetFieldByTag(&target, "json", "age", 30))
	fmt.Printf("   类型不匹配    : err=%v\n", SetFieldByTag(&target, "json", "id", "字符串"))

	// 7. Kind vs Type
	fmt.Println("--- 7. Kind 与 Type 的区别（最容易混的一对） ---")
	for _, v := range []any{[]int{1}, map[string]int{}, new(User), User{}} {
		fmt.Printf("   %s\n", kindVsType(v))
	}
	fmt.Println("   Type 是\"具体类型\"，Kind 是\"底层分类\"（Slice/Map/Ptr/Struct/Int...）")
	fmt.Println("   switch 要按 Kind 分派，比较相等才用 Type；对命名类型必须用 Kind")

	// 8. 性能
	fmt.Println("--- 8. 反射的代价（1000 个元素的字段读取，跑 1000 次） ---")
	refl := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = readViaReflection(benchUsersAny)
		}
	})
	dir := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = readDirect(benchUsersDirect)
		}
	})
	fmt.Printf("   反射读取: %10d ns/op\n", refl.NsPerOp())
	fmt.Printf("   直接访问: %10d ns/op\n", dir.NsPerOp())
	if dir.NsPerOp() > 0 {
		fmt.Printf("   反射慢约 %.1f 倍\n", float64(refl.NsPerOp())/float64(dir.NsPerOp()))
	}
	fmt.Println("   慢的原因：FieldByName 要按名字线性查找 + 每次都要做类型检查/装箱")
	fmt.Println("   优化手段：缓存 reflect.Type/StructField（TypeOf 只算一次）、")
	fmt.Println("            用 FieldByName 换成预计算的 Field index、或代码生成（easyjson/msgp）")

	// 9. 何时该用反射
	fmt.Println("--- 9. 什么时候该用反射 ---")
	fmt.Println("   v 框架/库的通用层：JSON、ORM、校验器、DI、路由绑定、mock")
	fmt.Println("   v 结构体 tag 驱动的行为，代码生成替代不了或成本过高时")
	fmt.Println("   x 业务热路径：性能敏感的地方用手写代码或代码生成")
	fmt.Println("   x 能用接口/泛型解决的地方：优先它们，编译期就能发现类型错误")
	fmt.Println("   判断标准：反射把\"编译期类型检查\"换成了\"运行期 panic\"，")
	fmt.Println("            只在\"类型在编译期确实不可知\"时才值得付这个代价")

	// 10. unsafe 的边界
	fmt.Println("--- 10. 补充：反射解决不了的事就走 unsafe（更危险） ---")
	var buf []byte
	hdr := reflect.TypeOf(buf)
	fmt.Printf("   []byte 的类型信息: %s, size=%d\n", hdr, hdr.Size())
	fmt.Println("   unsafe.Pointer <-> uintptr 的转换有个致命陷阱：")
	fmt.Println("   uintptr 不是指针，GC 不认为它引用对象 —— 中间一旦发生 GC，")
	fmt.Println("   对象可能被回收或移动，转换回来就是野指针。")
	fmt.Println("   规则：一次表达式内完成转换，绝不把 uintptr 存进变量")
}

var benchUsersAny = func() []any {
	out := make([]any, 1000)
	for i := range out {
		out[i] = User{ID: i}
	}
	return out
}()

var benchUsersDirect = func() []User {
	out := make([]User, 1000)
	for i := range out {
		out[i] = User{ID: i}
	}
	return out
}()
