package encoder

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"go.uber.org/zap"
)

// noopPublisher satisfies the publisher interface without doing anything.
// These tests exercise runWithSupervisor which never touches publishers.
type noopPublisher struct{}

func (n *noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (n *noopPublisher) Close()                                              {}

// crashingRunner is an FFmpegRunner whose Start always returns an error.
type crashingRunner struct{}

func (c *crashingRunner) Start(_ context.Context) error {
	return errors.New("crashed")
}

// blockingRunner blocks until the context is cancelled, simulating a long-running FFmpeg process.
type blockingRunner struct{}

func (b *blockingRunner) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRunWithSupervisor_ContextAlreadyCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	var factoryCalls atomic.Int32

	factory := func(_ FFmpegConfig) (FFmpegRunner, error) {
		factoryCalls.Add(1)
		return &crashingRunner{}, nil
	}

	worker := NewTestWorker(
		WorkerConfig{WorkerID: "w1", StreamID: "s1", Log: zap.NewNop()},
		&noopPublisher{}, &noopPublisher{}, factory,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before calling runWithSupervisor

	err := worker.runWithSupervisor(ctx)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls := factoryCalls.Load(); calls != 0 {
		t.Fatalf("expected factory to never be called, got %d calls", calls)
	}
}

func TestRunWithSupervisor_FactoryError(t *testing.T) {
	defer goleak.VerifyNone(t)

	var factoryCalls atomic.Int32

	factory := func(_ FFmpegConfig) (FFmpegRunner, error) {
		factoryCalls.Add(1)
		return nil, errors.New("factory broken")
	}

	worker := NewTestWorker(
		WorkerConfig{WorkerID: "w1", StreamID: "s1", Log: zap.NewNop()},
		&noopPublisher{}, &noopPublisher{}, factory,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := worker.runWithSupervisor(ctx)

	if err == nil || err.Error() != "factory broken" {
		t.Fatalf("expected factory error, got %v", err)
	}
	if calls := factoryCalls.Load(); calls != 1 {
		t.Fatalf("expected factory to be called exactly once, got %d calls", calls)
	}
}

func TestRunWithSupervisor_MaxRetriesExceeded(t *testing.T) {
	defer goleak.VerifyNone(t)

	var factoryCalls atomic.Int32

	factory := func(_ FFmpegConfig) (FFmpegRunner, error) {
		factoryCalls.Add(1)
		return &crashingRunner{}, nil
	}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID:    "w1",
			StreamID:    "s1",
			BackoffBase: 1 * time.Millisecond, // fast backoff for test
			Log:         zap.NewNop(),
		},
		&noopPublisher{}, &noopPublisher{}, factory,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := worker.runWithSupervisor(ctx)

	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Fatalf("expected ErrMaxRetriesExceeded, got %v", err)
	}
	if calls := factoryCalls.Load(); calls != int32(maxFFmpegRetries) {
		t.Fatalf("expected factory to be called %d times, got %d", maxFFmpegRetries, calls)
	}
}

func TestRunWithSupervisor_ContextCancelledDuringStart(t *testing.T) {
	defer goleak.VerifyNone(t)

	var factoryCalls atomic.Int32

	factory := func(_ FFmpegConfig) (FFmpegRunner, error) {
		factoryCalls.Add(1)
		return &blockingRunner{}, nil
	}

	worker := NewTestWorker(
		WorkerConfig{WorkerID: "w1", StreamID: "s1", Log: zap.NewNop()},
		&noopPublisher{}, &noopPublisher{}, factory,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- worker.runWithSupervisor(ctx)
	}()

	// Give runWithSupervisor time to enter Start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithSupervisor did not return after context cancellation")
	}

	if calls := factoryCalls.Load(); calls != 1 {
		t.Fatalf("expected factory to be called exactly once, got %d calls", calls)
	}
}

func TestRunWithSupervisor_ContextCancelledDuringBackoff(t *testing.T) {
	defer goleak.VerifyNone(t)

	var factoryCalls atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Channel signals that Start has returned (crashed), so we know
	// runWithSupervisor is about to enter the backoff timer.
	startReturned := make(chan struct{}, 1)

	factory := func(_ FFmpegConfig) (FFmpegRunner, error) {
		call := factoryCalls.Add(1)
		if call == 1 {
			// First call: return a runner whose Start fails immediately,
			// then signal so we can cancel during backoff.
			runner := &callbackRunner{
				startFunc: func(_ context.Context) error {
					defer func() { startReturned <- struct{}{} }()
					return errors.New("crashed")
				},
			}
			return runner, nil
		}
		// Should never reach a second call if we cancel during backoff.
		return &crashingRunner{}, nil
	}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID:    "w1",
			StreamID:    "s1",
			BackoffBase: 10 * time.Second, // long backoff so we cancel during it
			Log:         zap.NewNop(),
		},
		&noopPublisher{}, &noopPublisher{}, factory,
	)

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- worker.runWithSupervisor(ctx)
	}()

	// Wait for the first Start to return (crash), then cancel during backoff.
	select {
	case <-startReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Start to return")
	}

	cancel()

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithSupervisor did not return after context cancellation during backoff")
	}

	if calls := factoryCalls.Load(); calls != 1 {
		t.Fatalf("expected factory to be called exactly once, got %d calls", calls)
	}
}

// callbackRunner delegates Start to a caller-provided function.
type callbackRunner struct {
	startFunc func(ctx context.Context) error
}

func (c *callbackRunner) Start(ctx context.Context) error {
	return c.startFunc(ctx)
}
