package q2_defer

import "fmt"

func f() (result int) {
	defer func() {
		result++
	}()
	return 1
}

func g() int {
	result := 1
	defer func() {
		result++
	}()
	return result
}

func Run() {
	fmt.Println("=== Q2: defer 与返回值 ===")
	fmt.Printf("f() = %d (期望 2)\n", f())
	fmt.Printf("g() = %d (期望 1)\n", g())

	// 额外演示：defer 修改命名返回值
	fmt.Println("--- 补充 ---")
	fmt.Printf("h() = %d\n", h())
	fmt.Printf("k() = %d\n", k())
}

// h: 命名返回值 + defer 修改
func h() (result int) {
	defer func() { result += 10 }()
	return 1 // result=1 -> defer -> 11
}

// k: 匿名返回值 + defer 修改局部变量
func k() int {
	result := 1
	defer func() { result += 10 }()
	return result // 拷贝 1 出去 -> defer 改局部 -> 返回 1
}
