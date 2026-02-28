package encoder

import (
	"go.uber.org/zap"
)

// WorkerConfigWithProcessFactory returns a copy of cfg with the given FFmpeg process factory.
func WorkerConfigWithProcessFactory(cfg WorkerConfig, factory FFmpegFactory) WorkerConfig {
	cfg.processFactory = factory
	return cfg
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
