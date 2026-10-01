package q1_slice

import "fmt"

func Run() {
	fmt.Println("=== Q1: slice 底层与陷阱 ===")

	// 场景1：共享底层数组
	s := []int{1, 2, 3, 4, 5}
	s2 := s[1:3]
	s2 = append(s2, 100)
	fmt.Printf("s  = %v\n", s)  // [1 2 100 4 5]
	fmt.Printf("s2 = %v\n", s2) // [2 100]

	// 场景2：三索引切片，限制 cap
	s3 := []int{1, 2, 3, 4, 5}
	s4 := s3[1:3:3] // cap=2
	s4 = append(s4, 100)
	fmt.Printf("s3 = %v\n", s3) // [1 2 3 4 5] 不受影响
	fmt.Printf("s4 = %v\n", s4) // [2 3 100]

	// 场景3：显式拷贝
	s5 := []int{1, 2, 3, 4, 5}
	s6 := make([]int, 2)
	copy(s6, s5[1:3])
	s6 = append(s6, 100)
	fmt.Printf("s5 = %v\n", s5) // [1 2 3 4 5]
	fmt.Printf("s6 = %v\n", s6) // [2 3 100]

	// 场景4：查看 len 和 cap
	a := make([]int, 2, 5)
	b := a[0:2]
	fmt.Printf("a: len=%d cap=%d\n", len(a), cap(a)) // len=2 cap=5
	fmt.Printf("b: len=%d cap=%d\n", len(b), cap(b)) // len=2 cap=5
}
