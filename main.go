package main

import (
	"fmt"
	"os"

	"go-interview/q1_slice"
	"go-interview/q2_defer"
	"go-interview/q3_concurrency"
	"go-interview/q4_map"
	"go-interview/q5_perf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run . [1|2|3|4|5]")
		return
	}

	switch os.Args[1] {
	case "1":
		q1_slice.Run()
	case "2":
		q2_defer.Run()
	case "3":
		q3_concurrency.Run()
	case "4":
		q4_map.Run()
	case "5":
		q5_perf.Run()
	default:
		fmt.Println("未知题号，可选: 1 2 3 4 5")
	}
}
