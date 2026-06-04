// Package scheduler runs periodic background tasks with single-instance
// (leader-elected) execution. Across a multi-instance deployment each task runs
// on exactly one node per tick, chosen via a Postgres advisory lock, so a task
// such as a maintenance reconcile is not performed concurrently by every node.
package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Advisory-lock keys for built-in periodic tasks. Distinct values let each task
// elect its leader independently. Keep these stable across releases so a rolling
// upgrade does not run two leaders for the same task.
const (
	LockQuotaReconcile int64 = 9_000_001
)

// Task is a periodic background job. Run is invoked at most once per Interval and
// only on the instance that holds the advisory lock for LockKey, so concurrent
// instances do not run it at the same time.
type Task struct {
	Name     string
	LockKey  int64
	Interval time.Duration
	Run      func(context.Context) error
}

// Scheduler runs a set of tasks, each on its own ticker, until its context is
// cancelled.
type Scheduler struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
	tasks  []Task
}

// New creates a scheduler bound to a connection pool used for leader election.
func New(pool *pgxpool.Pool, logger zerolog.Logger, tasks ...Task) *Scheduler {
	return &Scheduler{pool: pool, logger: logger, tasks: tasks}
}

// Run starts a ticker per task and blocks until ctx is cancelled, then waits for
// any in-flight task run to finish before returning. Tasks with a non-positive
// Interval are skipped, so a task can be disabled via configuration.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range s.tasks {
		if t.Interval <= 0 {
			s.logger.Info().Str("task", t.Name).Msg("scheduler task disabled (interval <= 0)")
			continue
		}
		wg.Add(1)
		go func(t Task) {
			defer wg.Done()
			s.runTaskLoop(ctx, t)
		}(t)
	}
	wg.Wait()
}

func (s *Scheduler) runTaskLoop(ctx context.Context, t Task) {
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()
	s.logger.Info().Str("task", t.Name).Dur("interval", t.Interval).Msg("scheduler task started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx, t)
		}
	}
}

// runOnce executes the task only if this instance wins its advisory lock.
func (s *Scheduler) runOnce(ctx context.Context, t Task) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Str("task", t.Name).Msg("scheduler could not acquire connection")
		return
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", t.LockKey).Scan(&locked); err != nil {
		s.logger.Warn().Err(err).Str("task", t.Name).Msg("scheduler advisory lock failed")
		return
	}
	if !locked {
		// Another instance holds the lock and is running this task; skip quietly.
		return
	}
	defer func() {
		// Unlock on a fresh context so cancellation during shutdown cannot strand
		// the lock. A session-level advisory lock is also released by Postgres
		// when the backing connection drops, so it cannot leak indefinitely.
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", t.LockKey); err != nil {
			s.logger.Warn().Err(err).Str("task", t.Name).Msg("scheduler advisory unlock failed")
		}
	}()

	start := time.Now()
	if err := t.Run(ctx); err != nil {
		s.logger.Error().Err(err).Str("task", t.Name).Msg("scheduler task failed")
		return
	}
	s.logger.Info().Str("task", t.Name).Dur("took", time.Since(start)).Msg("scheduler task completed")
}
