package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	supervisorRestartLimit = 5
	supervisorCooldown     = 5 * time.Minute
)

var supervisorBackoff = [...]time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}

// RunSupervisor retries a failed in-process agent with bounded backoff. After
// five consecutive failures it pauses for five minutes before opening another
// retry window. Service-manager signals cancel ctx and stop the current agent
// cleanly. Camera and SaaS outages do not reach this loop; their modules retry
// independently while the agent remains alive.
func RunSupervisor(ctx context.Context, run func(context.Context) error, log *slog.Logger) error {
	return runSupervisor(ctx, run, waitContext, log)
}

func runSupervisor(ctx context.Context, run func(context.Context) error, wait func(context.Context, time.Duration) bool, log *slog.Logger) error {
	if run == nil {
		return fmt.Errorf("service supervisor: run function is required")
	}
	if wait == nil {
		wait = waitContext
	}
	consecutive := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := run(ctx)
		if ctx.Err() != nil || err == nil {
			return nil
		}
		consecutive++
		delay := supervisorDelay(consecutive)
		if log != nil {
			log.Error("managed Edge attempt failed; retry is backoff-limited",
				slog.String("error_type", fmt.Sprintf("%T", err)),
				slog.Int("consecutive_failures", consecutive),
				slog.Duration("retry_in", delay),
			)
		}
		if !wait(ctx, delay) {
			return nil
		}
		if consecutive >= supervisorRestartLimit {
			consecutive = 0
		}
	}
}

func supervisorDelay(consecutive int) time.Duration {
	if consecutive >= supervisorRestartLimit {
		return supervisorCooldown
	}
	index := consecutive - 1
	if index < 0 {
		index = 0
	}
	if index >= len(supervisorBackoff) {
		index = len(supervisorBackoff) - 1
	}
	return supervisorBackoff[index]
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
