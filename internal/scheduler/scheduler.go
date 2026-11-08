package scheduler

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"leadwatch/internal/pipeline"
	"leadwatch/internal/source"
)

// Scheduler coordinates concurrent source pollers and downstream worker goroutines.
type Scheduler struct {
	pipe        *pipeline.Pipeline
	sources     []source.Source
	interval    time.Duration
	downstream  time.Duration
}

func New(pipe *pipeline.Pipeline, sources []source.Source, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	downstreamInterval := 10 * time.Second
	if interval < downstreamInterval {
		downstreamInterval = interval / 2
		if downstreamInterval < 10*time.Millisecond {
			downstreamInterval = 10 * time.Millisecond
		}
	}

	return &Scheduler{
		pipe:       pipe,
		sources:    sources,
		interval:   interval,
		downstream: downstreamInterval,
	}
}

// SetDownstreamInterval customizes the interval for the downstream pipeline worker.
func (s *Scheduler) SetDownstreamInterval(d time.Duration) {
	if d > 0 {
		s.downstream = d
	}
}

// computeJitteredInterval calculates interval +/- 20% jitter.
func computeJitteredInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	// +/- 20% random float range [-0.2, +0.2]
	deltaFactor := (rand.Float64() * 0.4) - 0.2
	jittered := float64(base) * (1.0 + deltaFactor)
	if jittered < float64(time.Millisecond) {
		jittered = float64(time.Millisecond)
	}
	return time.Duration(jittered)
}

// Start spawns one goroutine per source on its own jittered interval, plus a downstream
// stage processing worker. It shuts down cleanly with zero leaks on context cancellation.
func (s *Scheduler) Start(ctx context.Context) error {
	slog.Info("starting leadwatch scheduler",
		"sources", len(s.sources),
		"interval", s.interval.String(),
		"downstream_interval", s.downstream.String(),
	)

	var wg sync.WaitGroup

	// 1. Spawn a dedicated poller goroutine for each source
	for _, src := range s.sources {
		wg.Add(1)
		go func(sourceAdapter source.Source) {
			defer wg.Done()
			s.runSourceLoop(ctx, sourceAdapter)
		}(src)
	}

	// 2. Spawn downstream worker goroutine (prefilter -> classify -> score -> draft -> notify)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runDownstreamWorker(ctx)
	}()

	// Wait for context cancellation
	<-ctx.Done()
	slog.Info("scheduler received shutdown signal; waiting for workers to terminate")

	// Wait for all goroutines to exit gracefully
	wg.Wait()
	slog.Info("all scheduler goroutines exited successfully")
	return ctx.Err()
}

// runSourceLoop continuously polls a single source with +/- 20% jitter and exponential backoff on failure.
func (s *Scheduler) runSourceLoop(ctx context.Context, src source.Source) {
	name := src.Name()
	slog.Info("starting poller loop for source", "source", name)

	consecutiveErrors := 0

	for {
		// Run poll cycle
		_, err := s.pipe.PollSource(ctx, src)
		if err != nil {
			consecutiveErrors++
			slog.Error("source poller error, backing off",
				"source", name,
				"consecutive_errors", consecutiveErrors,
				"err", err,
			)
		} else {
			consecutiveErrors = 0
		}

		// Calculate wait duration:
		// Normal case: base interval +/- 20% jitter.
		// Error case: exponential backoff bounded up to max 15 minutes.
		var nextWait time.Duration
		if consecutiveErrors > 0 {
			backoffSec := 5 * (1 << (consecutiveErrors - 1))
			if backoffSec > 900 {
				backoffSec = 900
			}
			nextWait = time.Duration(backoffSec) * time.Second
		} else {
			nextWait = computeJitteredInterval(s.interval)
		}

		select {
		case <-ctx.Done():
			slog.Info("stopping poller loop for source", "source", name)
			return
		case <-time.After(nextWait):
		}
	}
}

// runDownstreamWorker runs periodically to advance posts through pipeline stages.
func (s *Scheduler) runDownstreamWorker(ctx context.Context) {
	slog.Info("starting downstream stage worker")
	ticker := time.NewTicker(s.downstream)
	defer ticker.Stop()

	// Initial immediate pass
	if err := s.pipe.ProcessDownstream(ctx); err != nil && ctx.Err() == nil {
		slog.Error("downstream worker initial pass error", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("stopping downstream stage worker")
			return
		case <-ticker.C:
			if err := s.pipe.ProcessDownstream(ctx); err != nil && ctx.Err() == nil {
				slog.Error("downstream worker stage error", "err", err)
			}
		}
	}
}
