package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunSupervisorBoundsRestartBurstThenRetriesAfterCooldown(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	var delays []time.Duration
	wait := func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}
	run := func(context.Context) error {
		attempts++
		if attempts <= 6 {
			return errors.New("synthetic process failure")
		}
		return nil
	}
	if err := runSupervisor(ctx, run, wait, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("runSupervisor: %v", err)
	}
	want := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 5 * time.Minute, 2 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays=%v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Errorf("delay[%d]=%s, want %s", i, delays[i], want[i])
		}
	}
}

func TestRunSupervisorStopsWhenManagerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts int
	wait := func(_ context.Context, _ time.Duration) bool {
		cancel()
		return false
	}
	err := runSupervisor(ctx, func(context.Context) error {
		attempts++
		return errors.New("synthetic start failure")
	}, wait, nil)
	if err != nil {
		t.Fatalf("runSupervisor returned error on manager stop: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1 after cancel", attempts)
	}
}
