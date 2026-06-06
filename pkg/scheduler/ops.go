package scheduler

import (
	"context"
	"time"
)

type OpsRunner interface {
	RunScheduledDependencyCheck(ctx context.Context)
	RunScheduledConsistencyCheck(ctx context.Context, limit int32)
}

type OpsTaskConfig struct {
	DependencyInterval  time.Duration
	ConsistencyInterval time.Duration
	ConsistencyLimit    int32
}

func OpsTasks(runner OpsRunner, cfg OpsTaskConfig) []Task {
	if runner == nil {
		return nil
	}
	return []Task{
		{
			Name:     "ops-dependency-check",
			LockKey:  LockOpsDependencyCheck,
			Interval: cfg.DependencyInterval,
			Run: func(ctx context.Context) error {
				runner.RunScheduledDependencyCheck(ctx)
				return nil
			},
		},
		{
			Name:     "ops-consistency-check",
			LockKey:  LockOpsConsistencyCheck,
			Interval: cfg.ConsistencyInterval,
			Run: func(ctx context.Context) error {
				runner.RunScheduledConsistencyCheck(ctx, cfg.ConsistencyLimit)
				return nil
			},
		},
	}
}
