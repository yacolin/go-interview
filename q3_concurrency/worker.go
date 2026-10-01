package q3_concurrency

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Task struct{ ID int }
type Result struct {
	TaskID int
	Value  int
}

func process(ctx context.Context, t Task) Result {
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		return Result{TaskID: t.ID, Value: -1}
	}
	return Result{TaskID: t.ID, Value: t.ID * 2}
}

// WorkerPool 是核心实现
func WorkerPool(ctx context.Context, tasks <-chan Task, workers int) <-chan Result {
	results := make(chan Result, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-tasks:
					if !ok {
						return
					}
					r := process(ctx, task)
					select {
					case results <- r:
					case <-ctx.Done():
						return
					}
				}
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

// Run 是包入口
func Run() {
	fmt.Println("=== Q3: worker pool + context ===")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tasks := make(chan Task, 100)
	go func() {
		defer close(tasks)
		for i := 1; i <= 10; i++ {
			select {
			case tasks <- Task{ID: i}:
			case <-ctx.Done():
				return
			}
		}
	}()

	results := WorkerPool(ctx, tasks, 3)
	for r := range results {
		fmt.Printf("task %d -> value %d\n", r.TaskID, r.Value)
	}
	fmt.Println("all done")
}
