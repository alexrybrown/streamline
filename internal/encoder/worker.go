package encoder

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

// ErrMaxRetriesExceeded is returned by Run when FFmpeg crashes more than
// maxFFmpegRetries times without a context cancellation.
var ErrMaxRetriesExceeded = errors.New("encoder: max FFmpeg retries exceeded")

const (
	// defaultHeartbeatInterval is how often the worker publishes heartbeats.
	defaultHeartbeatInterval = 5 * time.Second

	// maxFFmpegRetries is the maximum number of restart attempts for FFmpeg
	// before the worker gives up (Layer 1 failover).
	maxFFmpegRetries = 3

	// defaultBackoffBase is the base duration for linear backoff between FFmpeg
	// restart attempts. Retry N sleeps for N * backoffBase.
	defaultBackoffBase = 1 * time.Second

	// kafkaPublishTimeout caps how long a single Kafka Publish call can block.
	// 5 s is well above normal latency but short enough to avoid stalling the
	// heartbeat/segment pipelines if the broker is unreachable.
	kafkaPublishTimeout = 5 * time.Second

	// segmentPushTimeout caps how long the ConnectRPC client-streaming push
	// to the packager can take. Longer than kafkaPublishTimeout because segment
	// pushes stream potentially hundreds of KiB of file data over the network.
	segmentPushTimeout = 30 * time.Second
)

// WorkerConfig holds configuration for an encoder worker.
type WorkerConfig struct {
	WorkerID          string
	StreamID          string
	KafkaBrokers      []string
	FFmpeg            FFmpegConfig
	PackagerURL       string
	HeartbeatInterval time.Duration
	BackoffBase       time.Duration
	Log               *zap.Logger
	KafkaOpts         []kgo.Opt

	processFactory FFmpegFactory
}

// Worker manages encoding for a single stream. It coordinates the FFmpeg
// process, segment watcher, heartbeat emitter, Kafka publishers, and
// optional segment pushing to the packager.
type Worker struct {
	cfg                WorkerConfig
	log                *zap.Logger
	heartbeatPublisher publisher
	segmentPublisher   publisher
	segmentPusher      segmentPusher
	processFactory     FFmpegFactory
}

// NewWorker creates a new encoder worker. Validates required config fields.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.WorkerID == "" {
		return nil, errors.New("WorkerID is required")
	}
	if cfg.StreamID == "" {
		return nil, errors.New("StreamID is required")
	}
	if len(cfg.KafkaBrokers) == 0 {
		return nil, errors.New("KafkaBrokers is required")
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = defaultBackoffBase
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}

	log := cfg.Log.Named("encoder-worker").With(
		zap.String("worker_id", cfg.WorkerID),
		zap.String("stream_id", cfg.StreamID),
	)

	heartbeatPublisher, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cfg.KafkaBrokers,
		Topic:   streamkafka.TopicHeartbeats,
		Log:     log,
		Opts:    cfg.KafkaOpts,
	})
	if err != nil {
		return nil, err
	}

	segmentPublisher, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cfg.KafkaBrokers,
		Topic:   streamkafka.TopicSegments,
		Log:     log,
		Opts:    cfg.KafkaOpts,
	})
	if err != nil {
		heartbeatPublisher.Close()
		return nil, err
	}

	factory := cfg.processFactory
	if factory == nil {
		factory = defaultFFmpegFactory
	}

	var pusher segmentPusher
	if cfg.PackagerURL != "" {
		pusher, err = NewConnectSegmentPusher(SegmentPusherConfig{
			PackagerURL: cfg.PackagerURL,
			Log:         log,
		})
		if err != nil {
			heartbeatPublisher.Close()
			segmentPublisher.Close()
			return nil, err
		}
		log.Info("segment pushing enabled", zap.String("packager_url", cfg.PackagerURL))
	} else {
		log.Info("segment pushing disabled — no PackagerURL configured")
	}

	return &Worker{
		cfg:                cfg,
		log:                log,
		heartbeatPublisher: heartbeatPublisher,
		segmentPublisher:   segmentPublisher,
		segmentPusher:      pusher,
		processFactory:     factory,
	}, nil
}

// Close releases Kafka publisher resources.
func (w *Worker) Close() {
	w.heartbeatPublisher.Close()
	w.segmentPublisher.Close()
}

// Run starts encoding, heartbeats, and segment publishing. Blocks until
// context is cancelled. Implements Layer 1 failover: restarts FFmpeg on crash.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	// Start heartbeat emitter
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.emitHeartbeats(ctx)
	}()

	// Start segment watcher
	watcher := NewSegmentWatcher(SegmentWatcherConfig{
		Dir: w.cfg.FFmpeg.OutputDir,
		Log: w.log,
	})
	segments := watcher.Watch(ctx)

	// Publish segment events
	wg.Add(1)
	go func() {
		defer wg.Done()
		for segment := range segments {
			w.publishSegment(ctx, segment)
		}
	}()

	// Run FFmpeg with local supervisor (Layer 1 failover)
	err := w.runWithSupervisor(ctx)

	wg.Wait()
	return err
}

// runWithSupervisor restarts FFmpeg if it crashes, up to maxFFmpegRetries.
// Returns nil on context cancellation, ErrMaxRetriesExceeded when retries are
// exhausted, or an error if the factory fails.
func (w *Worker) runWithSupervisor(ctx context.Context) error {
	retries := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		process, err := w.processFactory(w.cfg.FFmpeg)
		if err != nil {
			w.log.Error("failed to create FFmpeg process",
				zap.String("method", "runWithSupervisor"),
				zap.Error(err),
			)
			return err
		}

		w.log.Info("starting FFmpeg", zap.String("method", "runWithSupervisor"))

		err = process.Start(ctx)
		if ctx.Err() != nil {
			return nil // Context cancelled, clean exit
		}

		retries++
		w.log.Error("FFmpeg exited unexpectedly",
			zap.String("method", "runWithSupervisor"),
			zap.Error(err),
			zap.Int("retry", retries),
		)

		if retries >= maxFFmpegRetries {
			w.log.Error("max FFmpeg retries exceeded, worker giving up",
				zap.String("method", "runWithSupervisor"),
			)
			return ErrMaxRetriesExceeded
		}

		// Context-aware backoff: exit immediately if context is cancelled
		// rather than blocking on time.Sleep.
		backoff := time.NewTimer(time.Duration(retries) * w.cfg.BackoffBase)
		select {
		case <-ctx.Done():
			backoff.Stop()
			return nil
		case <-backoff.C:
		}
	}
}

func (w *Worker) emitHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.publishHeartbeat(ctx)
		}
	}
}

func (w *Worker) publishHeartbeat(ctx context.Context) {
	heartbeat := &streamlinev1.Heartbeat{
		WorkerId:  w.cfg.WorkerID,
		StreamId:  w.cfg.StreamID,
		Timestamp: timestamppb.Now(),
		Status:    streamlinev1.WorkerStatus_WORKER_STATUS_ENCODING,
	}
	data, err := proto.Marshal(heartbeat)
	if err != nil {
		w.log.Error("failed to marshal heartbeat",
			zap.String("method", "publishHeartbeat"),
			zap.Error(err),
		)
		return
	}

	publishCtx, publishCancel := context.WithTimeout(ctx, kafkaPublishTimeout)
	defer publishCancel()

	if err := w.heartbeatPublisher.Publish(publishCtx, w.cfg.StreamID, data); err != nil {
		w.log.Warn("failed to publish heartbeat",
			zap.String("method", "publishHeartbeat"),
			zap.Error(err),
		)
	}
}

func (w *Worker) publishSegment(ctx context.Context, segment Segment) {
	log := w.log.With(
		zap.String("method", "publishSegment"),
		zap.Int64("sequence", segment.SequenceNumber),
		zap.Int64("size", segment.Size),
	)

	// Push segment data to the packager before publishing the Kafka event,
	// so the packager has the data available when consumers see the event.
	if w.segmentPusher != nil {
		pushCtx, pushCancel := context.WithTimeout(ctx, segmentPushTimeout)
		defer pushCancel()

		durationSeconds := float64(w.cfg.FFmpeg.SegmentDurationS)
		if err := w.segmentPusher.PushSegment(pushCtx, w.cfg.StreamID, segment, durationSeconds); err != nil {
			log.Error("failed to push segment to packager", zap.Error(err))
			return
		}
	}

	event := &streamlinev1.SegmentProduced{
		StreamId:       w.cfg.StreamID,
		WorkerId:       w.cfg.WorkerID,
		SequenceNumber: segment.SequenceNumber,
		SizeBytes:      segment.Size,
		Timestamp:      timestamppb.Now(),
	}
	data, err := proto.Marshal(event)
	if err != nil {
		log.Error("failed to marshal segment event", zap.Error(err))
		return
	}
	publishCtx, publishCancel := context.WithTimeout(ctx, kafkaPublishTimeout)
	defer publishCancel()
	if err := w.segmentPublisher.Publish(publishCtx, w.cfg.StreamID, data); err != nil {
		log.Warn("failed to publish segment event", zap.Error(err))
		return
	}
	log.Info("segment produced")
}
