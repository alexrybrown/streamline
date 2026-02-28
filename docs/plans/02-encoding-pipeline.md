# Epic 2: Encoding Pipeline

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions. Follow `CLAUDE.md` at repo root.
>
> **Prerequisites:** Epic 1 complete. Docker Compose infra running. FFmpeg installed.

**Goal:** Go process wrapping FFmpeg that transcodes Big Buck Bunny to H.264 HLS segments and publishes events to Kafka.

**Milestone:** Segments on disk, segment events and heartbeats flowing in Kafka.

---

### Story 2.1: Kafka Producer/Consumer Wrappers — DONE

Shared Kafka wrappers in `internal/kafka/` using **franz-go** (`twmb/franz-go`) with config structs, scoped loggers, and `kfake` for in-memory tests.

**Committed:** `dca2ab4`, refactored in `074cc30`

**API surface:**
- `NewProducer(ProducerConfig{Brokers, Topic, Log, Opts})` → `(*Producer, error)`
- `NewConsumer(ConsumerConfig{Brokers, Topic, GroupID, Log, Opts})` → `(*Consumer, error)`
- Topic constants: `TopicHeartbeats`, `TopicSegments`, `TopicErrors`, `TopicStateChanges`

---

### Story 2.2: FFmpeg Process Manager — DONE

FFmpeg subprocess manager and segment watcher with config validation, backpressure detection, and scoped logging.

**Committed:** `074cc30`

**API surface:**
- `NewFFmpegProcess(FFmpegConfig{InputArgs, OutputDir, Codec, Width, Height, BitrateKbps, Framerate, SegmentDurationS})` → `(*FFmpegProcess, error)`
- `NewSegmentWatcher(SegmentWatcherConfig{Dir, PollInterval, Log})` → `*SegmentWatcher`

**Key design decisions:**
- `InputArgs []string` instead of `InputURI string` — caller provides raw FFmpeg input args, no magic string branching
- Unbuffered channel with timed-send backpressure detection (logs warning after 200ms stall)
- Constructor validates `InputArgs`, `OutputDir`, `Framerate`, `SegmentDurationS`

---

### Story 2.3: Encoder Worker Service

Wire the FFmpeg process manager, segment watcher, and Kafka producer into a runnable service.

**Files:**
- Create: `internal/encoder/worker.go`
- Create: `internal/encoder/worker_test.go`
- Create: `cmd/encoder-worker/main.go`

**Step 1: Write test for encoder worker**

```go
// internal/encoder/worker_test.go
package encoder_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/encoder"
	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

func TestWorker_ProducesSegmentsAndPublishesEvents(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()
	outputDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	w, err := encoder.NewWorker(encoder.WorkerConfig{
		WorkerID:     "test-worker-1",
		StreamID:     "test-stream-1",
		KafkaBrokers: brokers,
		KafkaOpts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
		Log:          zap.NewNop(),
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
			OutputDir:        outputDir,
			Codec:            "libx264",
			Width:            320,
			Height:           240,
			BitrateKbps:      500,
			Framerate:        30,
			SegmentDurationS: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	go w.Run(ctx)

	// Consume heartbeat
	hbConsumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicHeartbeats,
		GroupID: "test-hb",
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hbConsumer.Close()

	hbCtx, hbCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hbCancel()

	msg, err := hbConsumer.ReadMessage(hbCtx)
	if err != nil {
		t.Fatalf("no heartbeat received: %v", err)
	}
	if string(msg.Key) != "test-stream-1" {
		t.Errorf("expected key test-stream-1, got %s", string(msg.Key))
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/encoder/... -run TestWorker -v
```

Expected: FAIL — `NewWorker` and `WorkerConfig` not defined.

**Step 3: Implement the Worker struct**

The Worker coordinates the FFmpeg process, segment watcher, heartbeat emitter, and Kafka publisher. Implements Layer 1 failover: restarts FFmpeg on crash.

```go
// internal/encoder/worker.go
package encoder

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

const (
	// defaultHeartbeatInterval is how often the worker emits a heartbeat.
	// 5 seconds balances liveness detection with Kafka throughput.
	defaultHeartbeatInterval = 5 * time.Second

	// maxFFmpegRetries is the number of times the supervisor restarts FFmpeg
	// before giving up. Prevents infinite restart loops on persistent failures.
	maxFFmpegRetries = 3
)

// WorkerConfig holds configuration for an encoder worker.
type WorkerConfig struct {
	WorkerID          string
	StreamID          string
	KafkaBrokers      []string
	KafkaOpts         []kgo.Opt
	Log               *zap.Logger
	FFmpeg            FFmpegConfig
	HeartbeatInterval time.Duration
}

// Worker manages encoding for a single stream.
type Worker struct {
	cfg         WorkerConfig
	log         *zap.Logger
	hbProducer  *streamkafka.Producer
	segProducer *streamkafka.Producer
}

// NewWorker creates a new encoder worker.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = defaultHeartbeatInterval
	}

	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	log = log.Named("encoder-worker").With(
		zap.String("worker_id", cfg.WorkerID),
		zap.String("stream_id", cfg.StreamID),
	)

	hbProducer, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cfg.KafkaBrokers,
		Topic:   streamkafka.TopicHeartbeats,
		Log:     log,
		Opts:    cfg.KafkaOpts,
	})
	if err != nil {
		return nil, err
	}

	segProducer, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cfg.KafkaBrokers,
		Topic:   streamkafka.TopicSegments,
		Log:     log,
		Opts:    cfg.KafkaOpts,
	})
	if err != nil {
		return nil, err
	}

	return &Worker{
		cfg:         cfg,
		log:         log,
		hbProducer:  hbProducer,
		segProducer: segProducer,
	}, nil
}

// Run starts encoding, heartbeats, and segment publishing. Blocks until context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.emitHeartbeats(ctx)
	}()

	watcher := NewSegmentWatcher(SegmentWatcherConfig{
		Dir: w.cfg.FFmpeg.OutputDir,
		Log: w.log,
	})
	segments := watcher.Watch(ctx)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for seg := range segments {
			w.publishSegment(ctx, seg)
		}
	}()

	w.runWithSupervisor(ctx)

	wg.Wait()
	return nil
}

func (w *Worker) runWithSupervisor(ctx context.Context) {
	retries := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		proc, err := NewFFmpegProcess(w.cfg.FFmpeg)
		if err != nil {
			w.log.Error("failed to create FFmpeg process", zap.Error(err))
			return
		}

		w.log.Info("starting FFmpeg", zap.String("method", "runWithSupervisor"))

		err = proc.Start(ctx)
		if ctx.Err() != nil {
			return
		}

		retries++
		w.log.Error("FFmpeg exited unexpectedly",
			zap.String("method", "runWithSupervisor"),
			zap.Error(err),
			zap.Int("retry", retries),
		)

		if retries >= maxFFmpegRetries {
			w.log.Error("max FFmpeg retries exceeded, worker giving up")
			return
		}

		time.Sleep(time.Duration(retries) * time.Second)
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
			hb := map[string]interface{}{
				"worker_id": w.cfg.WorkerID,
				"stream_id": w.cfg.StreamID,
				"timestamp": time.Now().UTC(),
				"status":    "encoding",
			}
			data, _ := json.Marshal(hb)
			if err := w.hbProducer.Publish(ctx, w.cfg.StreamID, data); err != nil {
				w.log.Warn("failed to publish heartbeat",
					zap.String("method", "emitHeartbeats"),
					zap.Error(err),
				)
			}
		}
	}
}

func (w *Worker) publishSegment(ctx context.Context, seg Segment) {
	event := map[string]interface{}{
		"stream_id":       w.cfg.StreamID,
		"worker_id":       w.cfg.WorkerID,
		"sequence_number": seg.SequenceNumber,
		"size_bytes":      seg.Size,
		"timestamp":       time.Now().UTC(),
	}
	data, _ := json.Marshal(event)
	if err := w.segProducer.Publish(ctx, w.cfg.StreamID, data); err != nil {
		w.log.Warn("failed to publish segment event",
			zap.String("method", "publishSegment"),
			zap.Error(err),
		)
	} else {
		w.log.Info("segment produced",
			zap.String("method", "publishSegment"),
			zap.Int64("sequence", seg.SequenceNumber),
			zap.Int64("size", seg.Size),
		)
	}
}
```

**Step 4: Run test to verify it passes**

```bash
go test ./internal/encoder/... -run TestWorker -v -timeout 60s
```

**Step 5: Create main entry point**

```go
// cmd/encoder-worker/main.go
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/config"
	"github.com/alexrybrown/streamline/internal/encoder"
	"github.com/alexrybrown/streamline/internal/logging"
)

func main() {
	log := logging.New("encoder-worker")
	cfg := config.Load()

	workerID := getEnv("WORKER_ID", "worker-1")
	streamID := getEnv("STREAM_ID", "stream-1")
	inputURI := getEnv("INPUT_URI", "file:///app/assets/bbb.mp4")

	w, err := encoder.NewWorker(encoder.WorkerConfig{
		WorkerID:     workerID,
		StreamID:     streamID,
		KafkaBrokers: strings.Split(cfg.KafkaBrokers, ","),
		Log:          log,
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-re", "-i", inputURI},
			OutputDir:        "/tmp/segments",
			Codec:            "libx264",
			Width:            1920,
			Height:           1080,
			BitrateKbps:      4000,
			Framerate:        30,
			SegmentDurationS: 6,
		},
	})
	if err != nil {
		log.Fatal("failed to create worker", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("shutting down")
		cancel()
	}()

	log.Info("starting encoder worker",
		zap.String("worker_id", workerID),
		zap.String("stream_id", streamID),
	)
	if err := w.Run(ctx); err != nil {
		log.Fatal("worker exited with error", zap.Error(err))
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Step 6: Build and verify**

```bash
go build ./cmd/encoder-worker
```

**Step 7: Run all tests**

```bash
go test ./internal/encoder/... -v -race -timeout 60s
```

**Step 8: Commit**

```bash
git add cmd/encoder-worker/ internal/encoder/worker.go internal/encoder/worker_test.go
git commit -m "feat: add encoder worker with FFmpeg supervisor, heartbeats, and segment events"
```
