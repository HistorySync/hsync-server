package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestRunReturnsWithNoTasks verifies Run terminates immediately when there is
// nothing to schedule, without touching the (nil) pool.
func TestRunReturnsWithNoTasks(t *testing.T) {
	s := New(nil, zerolog.Nop())

	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return with no tasks")
	}
}

// TestRunSkipsDisabledTask verifies a task with a non-positive interval is never
// started, so the pool is not used and Run returns promptly.
func TestRunSkipsDisabledTask(t *testing.T) {
	ran := false
	s := New(nil, zerolog.Nop(), Task{
		Name:     "disabled",
		Interval: 0,
		Run:      func(context.Context) error { ran = true; return nil },
	})

	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return with only a disabled task")
	}
	if ran {
		t.Fatal("disabled task ran, want skipped")
	}
}

// TestRunStopsOnContextCancel verifies an enabled task's loop exits when the
// context is cancelled. A long interval guarantees the task body never fires, so
// the nil pool is never touched.
func TestRunStopsOnContextCancel(t *testing.T) {
	s := New(nil, zerolog.Nop(), Task{
		Name:     "long",
		LockKey:  1,
		Interval: time.Hour,
		Run:      func(context.Context) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

type fakeOpsRunner struct {
	dependencyRuns  int
	consistencyRuns int
	limit           int32
}

func (r *fakeOpsRunner) RunScheduledDependencyCheck(context.Context) {
	r.dependencyRuns++
}

func (r *fakeOpsRunner) RunScheduledConsistencyCheck(_ context.Context, limit int32) {
	r.consistencyRuns++
	r.limit = limit
}

func TestOpsTasksBuildsRecurringChecks(t *testing.T) {
	runner := &fakeOpsRunner{}
	tasks := OpsTasks(runner, OpsTaskConfig{
		DependencyInterval:  6 * time.Hour,
		ConsistencyInterval: 24 * time.Hour,
		ConsistencyLimit:    123,
	})
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	if tasks[0].Name != "ops-dependency-check" || tasks[0].LockKey != LockOpsDependencyCheck || tasks[0].Interval != 6*time.Hour {
		t.Fatalf("dependency task = %+v", tasks[0])
	}
	if tasks[1].Name != "ops-consistency-check" || tasks[1].LockKey != LockOpsConsistencyCheck || tasks[1].Interval != 24*time.Hour {
		t.Fatalf("consistency task = %+v", tasks[1])
	}
	if err := tasks[0].Run(context.Background()); err != nil {
		t.Fatalf("dependency Run: %v", err)
	}
	if err := tasks[1].Run(context.Background()); err != nil {
		t.Fatalf("consistency Run: %v", err)
	}
	if runner.dependencyRuns != 1 || runner.consistencyRuns != 1 || runner.limit != 123 {
		t.Fatalf("runner dependency=%d consistency=%d limit=%d", runner.dependencyRuns, runner.consistencyRuns, runner.limit)
	}
}

func TestOpsTasksNilRunner(t *testing.T) {
	if tasks := OpsTasks(nil, OpsTaskConfig{}); len(tasks) != 0 {
		t.Fatalf("tasks = %+v, want empty", tasks)
	}
}
