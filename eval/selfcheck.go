package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Check verifies a task without a model: the hidden tests must fail against
// the shipped stub and pass against the reference solution.
type Check struct {
	TaskID    string `json:"task_id"`
	Tier      string `json:"tier"`
	StubFails bool   `json:"stub_fails"`
	Reference Grade  `json:"reference"`
	Stub      Grade  `json:"stub"`
	Error     string `json:"error,omitempty"`
}

func (c Check) OK() bool { return c.Error == "" && c.StubFails && c.Reference.Passed }

// SelfCheck runs Check for each task with the given parallelism, in ladder order.
func SelfCheck(ctx context.Context, tasks []Task, parallel int, scratch string) []Check {
	if parallel < 1 {
		parallel = 1
	}
	checks := make([]Check, len(tasks))
	queue := make(chan int)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				checks[i] = CheckTask(ctx, tasks[i], filepath.Join(scratch, tasks[i].ID))
			}
		}()
	}
	for i := range tasks {
		queue <- i
	}
	close(queue)
	wg.Wait()
	return checks
}

// CheckTask materializes the task twice under scratch and grades both copies.
func CheckTask(ctx context.Context, task Task, scratch string) Check {
	c := Check{TaskID: task.ID, Tier: task.Tier}
	fail := func(err error) Check { c.Error = err.Error(); return c }
	stubDir, refDir := filepath.Join(scratch, "stub"), filepath.Join(scratch, "reference")
	for _, dir := range []string{stubDir, refDir} {
		if err := os.RemoveAll(dir); err != nil {
			return fail(err)
		}
		if err := Materialize(task, dir); err != nil {
			return fail(err)
		}
		if err := ApplyHidden(task, dir); err != nil {
			return fail(err)
		}
	}
	var err error
	if c.Stub, err = RunHiddenTests(ctx, task, stubDir); err != nil {
		return fail(fmt.Errorf("stub: %w", err))
	}
	c.StubFails = !c.Stub.Passed
	if !c.Stub.Compiled {
		return fail(fmt.Errorf("stub with hidden tests does not compile:\n%s", c.Stub.Output))
	}
	if err := ApplyReference(task, refDir); err != nil {
		return fail(err)
	}
	if c.Reference, err = RunHiddenTests(ctx, task, refDir); err != nil {
		return fail(fmt.Errorf("reference: %w", err))
	}
	return c
}
