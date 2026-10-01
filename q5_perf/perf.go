package q5_perf

import (
	"fmt"
)

type User struct {
	Name string
	Age  int
}

// Bug 版：Go 1.22 之前，所有指针指向同一个 u
func processBug(users []User) []*User {
	result := make([]*User, 0, len(users))
	for _, u := range users {
		result = append(result, &u) // Go 1.22 前：全是同一个地址
	}
	return result
}

// 修复版1：索引取地址（所有版本都正确）
func processFixed1(users []User) []*User {
	result := make([]*User, 0, len(users))
	for i := range users {
		result = append(result, &users[i])
	}
	return result
}

// 修复版2：显式创建副本（Go 1.22 前也正确）
func processFixed2(users []User) []*User {
	result := make([]*User, 0, len(users))
	for _, u := range users {
		u := u
		result = append(result, &u)
	}
	return result
}

// 优化版：返回值切片，减少指针逃逸和 GC 压力
func processValue(users []User) []User {
	result := make([]User, 0, len(users))
	for _, u := range users {
		result = append(result, u)
	}
	return result
}

func Run() {
	fmt.Println("=== Q5: range 变量与指针陷阱 ===")

	users := []User{
		{Name: "Alice", Age: 30},
		{Name: "Bob", Age: 25},
		{Name: "Carol", Age: 35},
	}

	// Bug 版
	bug := processBug(users)
	fmt.Println("--- processBug ---")
	for i, u := range bug {
		fmt.Printf("  [%d] %s %d\n", i, u.Name, u.Age)
	}
	fmt.Println("  提示: Go 1.22 之前这里会全部是 Carol 35；1.22 及以后正常")

	// 修复版1
	f1 := processFixed1(users)
	fmt.Println("--- processFixed1 (索引取址) ---")
	for i, u := range f1 {
		fmt.Printf("  [%d] %s %d\n", i, u.Name, u.Age)
	}

	// 修复版2
	f2 := processFixed2(users)
	fmt.Println("--- processFixed2 (显式副本) ---")
	for i, u := range f2 {
		fmt.Printf("  [%d] %s %d\n", i, u.Name, u.Age)
	}

	// 值切片
	v := processValue(users)
	fmt.Println("--- processValue (值切片) ---")
	for i, u := range v {
		fmt.Printf("  [%d] %s %d\n", i, u.Name, u.Age)
	}

	// 验证地址
	fmt.Println("--- 地址对比 ---")
	fmt.Printf("  bug[0] ptr=%p\n", bug[0])
	fmt.Printf("  bug[1] ptr=%p\n", bug[1])
	fmt.Printf("  bug[2] ptr=%p\n", bug[2])
	fmt.Println("  若三行地址相同 -> 命中 Go 1.22 之前的 bug")
}
