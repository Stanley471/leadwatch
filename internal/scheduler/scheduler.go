package scheduler

import (
	"context"
	"log/slog"
	"time"

	"leadwatch/internal/pipeline"
)

type Scheduler struct {
	pipe     *pipeline.Pipeline
	interval time.Duration
}

func New(pipe *pipeline.Pipeline, interval time.Duration) *Scheduler {
	if interval < 1*time.Minute {
		interval = 15 * time.Minute
	}
	return &Scheduler{
		pipe:     pipe,
		interval: interval,
	}
}

// Start runs the leadwatch pipeline continuously until the context is canceled.
func (s *Scheduler) Start(ctx context.Context) error {
	slog.Info("Scheduler starting", "interval", s.interval.String())

	// Run initial cycle immediately
	if err := s.pipe.RunCycle(ctx); err != nil {
		slog.Error("Initial cycle failed", "err", err)
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Scheduler received stop signal")
			return ctx.Err()
		case <-ticker.C:
			if err := s.pipe.RunCycle(ctx); err != nil {
				slog.Error("Scheduled cycle failed", "err", err)
			}
		}
	}
}
