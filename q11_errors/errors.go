package q11_errors

import (
	"errors"
	"fmt"
	"io/fs"
)

// ---------- 1. 自定义错误类型 ----------

// ValidationError 携带结构化上下文，调用方可以用 errors.As 取回字段。
type ValidationError struct {
	Field string
	Value any
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("字段 %s 校验失败: %v", e.Field, e.Value)
}

// ---------- 2. 哨兵错误 ----------

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
)

// ---------- 3. 错误包装 ----------

func findUser(id int) (string, error) {
	if id <= 0 {
		// %w 建立错误链；%v 只是把字符串拼进去，链会断
		return "", fmt.Errorf("findUser(id=%d): %w", id, ErrNotFound)
	}
	if id > 100 {
		return "", fmt.Errorf("findUser(id=%d): %w", id, &ValidationError{Field: "id", Value: id})
	}
	return fmt.Sprintf("user-%d", id), nil
}

// ---------- 4. 多层包装后依然可以 Is/As ----------

func serviceLayer(id int) (string, error) {
	name, err := findUser(id)
	if err != nil {
		return "", fmt.Errorf("serviceLayer: %w", err)
	}
	return name, nil
}

// ---------- 5. 错误链的方向 ----------

func unwrapChain(err error) []string {
	var chain []string
	for err != nil {
		chain = append(chain, err.Error())
		err = errors.Unwrap(err)
	}
	return chain
}

// ---------- 6. 多错误聚合（Go 1.20+）----------

func validateAll(fields map[string]any) error {
	var errs []error
	for k, v := range fields {
		if s, ok := v.(string); ok && s == "" {
			errs = append(errs, &ValidationError{Field: k, Value: v})
		}
	}
	return errors.Join(errs...) // 全部为 nil 时返回 nil
}

func Run() {
	fmt.Println("=== Q11: error 设计、错误链与最佳实践 ===")

	// 1. 哨兵错误 + Is
	fmt.Println("--- 1. 哨兵错误：errors.Is 穿透整条链 ---")
	_, err := serviceLayer(0)
	fmt.Printf("   serviceLayer(0) 错误: %v\n", err)
	fmt.Printf("   errors.Is(err, ErrNotFound) = %v  <-- 中间隔了一层包装也能命中\n", errors.Is(err, ErrNotFound))
	fmt.Printf("   err == ErrNotFound          = %v  <-- 直接比较失效\n", err == ErrNotFound)
	fmt.Printf("   链: %v\n", unwrapChain(err))

	// 2. 自定义类型 + As
	fmt.Println("--- 2. 自定义错误类型：errors.As 取回上下文 ---")
	_, err2 := serviceLayer(999)
	fmt.Printf("   serviceLayer(999) 错误: %v\n", err2)
	var ve *ValidationError
	if errors.As(err2, &ve) {
		fmt.Printf("   errors.As 成功 -> Field=%q Value=%v\n", ve.Field, ve.Value)
	}
	fmt.Println("   注意：As 的第二个参数必须是指向\"实现了 error 的类型\"的指针")

	// 3. %w vs %v
	fmt.Println("--- 3. 包装动词 w 与 v 的区别 ---")
	base := fmt.Errorf("底层原因")
	withW := fmt.Errorf("包装A: %w", base)
	withV := fmt.Errorf("包装B: %v", base)
	fmt.Printf("   %%w 版本 Unwrap 出来: %v（链还在）\n", errors.Unwrap(withW))
	fmt.Printf("   %%v 版本 Unwrap 出来: %v（链断了）\n", errors.Unwrap(withV))
	fmt.Println("   一个 fmt.Errorf 里可以写多个 %w（Go 1.20+），会形成一个多叉链")

	// 4. 标准库错误值的复用
	fmt.Println("--- 4. 复用标准库错误值（不要比较错误字符串）---")
	ioErr := fmt.Errorf("打开配置文件: %w", fs.ErrNotExist)
	fmt.Printf("   %v\n", ioErr)
	fmt.Printf("   errors.Is(err, fs.ErrNotExist) = %v\n", errors.Is(ioErr, fs.ErrNotExist))
	fmt.Printf("   errors.Is(err, fs.ErrPermission) = %v\n", errors.Is(ioErr, fs.ErrPermission))
	fmt.Println("   这就是为什么应该用 fs.ErrNotExist 而不是 err.Error() == \"file not found\"")

	// 5. Join 聚合
	fmt.Println("--- 5. errors.Join 聚合多个错误 ---")
	errs := validateAll(map[string]any{"name": "", "email": "", "age": 18})
	fmt.Printf("   errors.Join 结果:\n%v\n", errs)
	fmt.Printf("   能否 Is 到 ValidationError: %v\n", func() bool {
		var v *ValidationError
		return errors.As(errs, &v)
	}())
	fmt.Printf("   全部合法时返回: %v\n", validateAll(map[string]any{"name": "go"}))

	// 6. 常见反模式
	fmt.Println("--- 6. 反模式清单 ---")
	fmt.Println("   x 比较错误字符串 err.Error() == \"not found\"（文案一改就崩）")
	fmt.Println("   x 在循环里 if err != nil { return err } 而不带上下文（丢失现场）")
	fmt.Println("   x 用 panic 表达业务错误（panic 是给\"不可恢复\"用的）")
	fmt.Println("   x 忽略 err（至少写 _ = 或用 errcheck 静态检查）")
	fmt.Println("   v 错误信息小写、不带标点，因为会被上层再包装")
	fmt.Println("   v 自己在边界处加唯一上下文，中间层尽量透传")
	fmt.Println("   v 需要被判断的用哨兵/自定义类型，需要被展示的用字符串")

	// 7. 该不该 panic
	fmt.Println("--- 7. panic 还是 return error ---")
	fmt.Println("   panic: 程序员错误（越界、断言失败）、main 初始化失败、内部不变量被打破")
	fmt.Println("   返回 error: 一切外部可预期的失败（IO、网络、参数非法、下游报错）")
	fmt.Println("   库代码尤其不要 panic：调用方没法优雅处理")
}
