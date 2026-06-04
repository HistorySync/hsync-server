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
