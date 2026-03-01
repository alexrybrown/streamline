package encoder

import (
	"context"

	"go.uber.org/zap"
)

// WorkerConfigWithProcessFactory returns a copy of cfg with the given FFmpeg process factory.
func WorkerConfigWithProcessFactory(cfg WorkerConfig, factory FFmpegFactory) WorkerConfig {
	cfg.processFactory = factory
	return cfg
}

// TestSegmentPusher is a hand-written test double for the segmentPusher interface.
// It records calls for assertion in tests.
type TestSegmentPusher struct {
	Calls     []TestPushCall
	ReturnErr error
}

// TestPushCall records the arguments of a single PushSegment call.
type TestPushCall struct {
	StreamID        string
	Segment         Segment
	DurationSeconds float64
}

// PushSegment records the call and returns the configured error.
func (pusher *TestSegmentPusher) PushSegment(_ context.Context, streamID string, segment Segment, durationSeconds float64) error {
	pusher.Calls = append(pusher.Calls, TestPushCall{
		StreamID:        streamID,
		Segment:         segment,
		DurationSeconds: durationSeconds,
	})
	return pusher.ReturnErr
}

// NewTestWorker creates a Worker with injected dependencies for unit testing.
// Bypasses NewWorker's Kafka publisher construction.
func NewTestWorker(cfg WorkerConfig, heartbeatPublisher, segmentPublisher publisher, factory FFmpegFactory) *Worker {
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	log := cfg.Log.Named("encoder-worker").With(
		zap.String("worker_id", cfg.WorkerID),
		zap.String("stream_id", cfg.StreamID),
	)
	return &Worker{
		cfg:                cfg,
		log:                log,
		heartbeatPublisher: heartbeatPublisher,
		segmentPublisher:   segmentPublisher,
		processFactory:     factory,
	}
}

// NewTestWorkerWithPusher creates a Worker with an injected segment pusher
// for unit testing.
func NewTestWorkerWithPusher(cfg WorkerConfig, heartbeatPublisher, segmentPublisher publisher, pusher segmentPusher, factory FFmpegFactory) *Worker {
	worker := NewTestWorker(cfg, heartbeatPublisher, segmentPublisher, factory)
	worker.segmentPusher = pusher
	return worker
}
