package scheduler_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"leadwatch/internal/config"
	"leadwatch/internal/pipeline"
	"leadwatch/internal/scheduler"
	"leadwatch/internal/score"
	"leadwatch/internal/source"
	"leadwatch/internal/store"
)

type testMockSource struct {
	name       string
	pollCount  int32
	failCount  int32
	shouldFail bool
	mu         sync.Mutex
	posts      []source.RawPost
}

func (m *testMockSource) Name() string {
	return m.name
}

func (m *testMockSource) Poll(ctx context.Context, since time.Time) ([]source.RawPost, error) {
	atomic.AddInt32(&m.pollCount, 1)
	if m.shouldFail {
		atomic.AddInt32(&m.failCount, 1)
		return nil, errors.New("simulated source network failure")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.posts, nil
}

func TestScheduler_GracefulShutdownAndNoGoroutineLeaks(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "sched_test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	src1 := &testMockSource{name: "src_healthy_1"}
	src2 := &testMockSource{name: "src_failing_2", shouldFail: true}
	src3 := &testMockSource{name: "src_healthy_3"}

	sources := []source.Source{src1, src2, src3}

	cfg := &config.Config{
		Prefilter: config.DefaultPrefilterConfig(),
	}
	pipe := pipeline.New(cfg, st, sources, score.DefaultBio(), nil)

	// Baseline goroutine count before running scheduler
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	sched := scheduler.New(pipe, sources, 50*time.Millisecond)
	sched.SetDownstreamInterval(25 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- sched.Start(ctx)
	}()

	// Let pollers and downstream worker run several ticks
	time.Sleep(150 * time.Millisecond)

	// Cancel context for graceful shutdown
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not exit within timeout")
	}

	// Verify failing source did not stop the healthy sources
	if atomic.LoadInt32(&src1.pollCount) < 2 {
		t.Errorf("expected healthy src1 to poll at least 2 times, got %d", atomic.LoadInt32(&src1.pollCount))
	}
	if atomic.LoadInt32(&src2.failCount) < 1 {
		t.Errorf("expected failing src2 to attempt polling, got %d", atomic.LoadInt32(&src2.failCount))
	}
	if atomic.LoadInt32(&src3.pollCount) < 2 {
		t.Errorf("expected healthy src3 to poll at least 2 times despite src2 failing, got %d", atomic.LoadInt32(&src3.pollCount))
	}

	// Verify no goroutine leaks after shutdown
	runtime.GC()
	// Allow slight grace time for runtime background threads to stabilize
	time.Sleep(50 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()

	// Leaked goroutines threshold check
	if finalGoroutines > initialGoroutines+2 {
		t.Errorf("possible goroutine leak: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}
}
