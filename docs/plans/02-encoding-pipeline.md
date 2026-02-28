# Epic 2: Encoding Pipeline

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 1 complete. Docker Compose infra running. FFmpeg installed.

**Goal:** Go process wrapping FFmpeg that transcodes Big Buck Bunny to H.264 HLS segments and publishes events to Kafka.

**Milestone:** Segments on disk, segment events and heartbeats flowing in Kafka.

---

### Story 2.1: Kafka Producer/Consumer Wrappers

Create shared Kafka producer and consumer wrappers in `internal/kafka/`.

**Files:**
- Create: `internal/kafka/producer.go`
- Create: `internal/kafka/producer_test.go`
- Create: `internal/kafka/consumer.go`
- Create: `internal/kafka/consumer_test.go`
- Create: `internal/kafka/topics.go`

**Step 1: Define topic constants**

```go
// internal/kafka/topics.go
package kafka

const (
	TopicHeartbeats   = "streamline.heartbeats"
	TopicSegments     = "streamline.segments"
	TopicErrors       = "streamline.errors"
	TopicStateChanges = "streamline.state-changes"
)
```

**Step 2: Write producer integration test**

```go
// internal/kafka/producer_test.go
package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/kafka"
	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

func TestProducer_PublishAndConsume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	container, err := kafka.Run(ctx, "confluentinc/confluent-local:7.5.0")
	if err != nil {
		t.Fatal(err)
	}
	defer container.Terminate(ctx)

	brokers, err := container.Brokers(ctx)
	if err != nil {
		t.Fatal(err)
	}

	producer, err := streamkafka.NewProducer(brokers, streamkafka.TopicSegments)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	// Publish a message with a partition key
	err = producer.Publish(ctx, "stream-1", []byte(`{"test": true}`))
	if err != nil {
		t.Fatal(err)
	}

	// Consume and verify
	consumer, err := streamkafka.NewConsumer(brokers, streamkafka.TopicSegments, "test-group")
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	msg, err := consumer.ReadMessage(readCtx)
	if err != nil {
		t.Fatal(err)
	}

	if string(msg.Key) != "stream-1" {
		t.Errorf("expected key stream-1, got %s", string(msg.Key))
	}
}
```

**Step 3: Implement producer**

```go
// internal/kafka/producer.go
package kafka

import (
	"context"

	kafkago "github.com/segmentio/kafka-go"
)

// Producer wraps kafka-go Writer for publishing messages.
type Producer struct {
	writer *kafkago.Writer
}

// NewProducer creates a Kafka producer for the given topic.
// Messages are partitioned by key (stream ID).
func NewProducer(brokers []string, topic string) (*Producer, error) {
	w := &kafkago.Writer{
		Addr:     kafkago.TCP(brokers...),
		Topic:    topic,
		Balancer: &kafkago.Hash{},
	}
	return &Producer{writer: w}, nil
}

// Publish sends a message with the given partition key.
func (p *Producer) Publish(ctx context.Context, key string, value []byte) error {
	return p.writer.WriteMessages(ctx, kafkago.Message{
		Key:   []byte(key),
		Value: value,
	})
}

// Close flushes and closes the producer.
func (p *Producer) Close() error {
	return p.writer.Close()
}
```

**Step 4: Implement consumer**

```go
// internal/kafka/consumer.go
package kafka

import (
	"context"

	kafkago "github.com/segmentio/kafka-go"
)

// Message wraps a consumed Kafka message.
type Message struct {
	Key   []byte
	Value []byte
}

// Consumer wraps kafka-go Reader for consuming messages.
type Consumer struct {
	reader *kafkago.Reader
}

// NewConsumer creates a Kafka consumer for the given topic and group.
func NewConsumer(brokers []string, topic, groupID string) (*Consumer, error) {
	r := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: brokers,
		Topic:   topic,
		GroupID: groupID,
	})
	return &Consumer{reader: r}, nil
}

// ReadMessage blocks until a message is available or context is cancelled.
func (c *Consumer) ReadMessage(ctx context.Context) (Message, error) {
	m, err := c.reader.ReadMessage(ctx)
	if err != nil {
		return Message{}, err
	}
	return Message{Key: m.Key, Value: m.Value}, nil
}

// Close closes the consumer.
func (c *Consumer) Close() error {
	return c.reader.Close()
}
```

**Step 5: Run integration tests**

```bash
go test ./internal/kafka/... -v -race
```

**Step 6: Commit**

```bash
git add internal/kafka/
git commit -m "feat: add Kafka producer/consumer wrappers with integration tests"
```

---

### Story 2.2: FFmpeg Process Manager

The core of the encoder worker — a Go component that manages an FFmpeg subprocess, monitors its health, and detects output segments.

**Files:**
- Create: `internal/encoder/ffmpeg.go`
- Create: `internal/encoder/ffmpeg_test.go`
- Create: `internal/encoder/segment_watcher.go`
- Create: `internal/encoder/segment_watcher_test.go`

**Step 1: Write test for FFmpeg process manager**

```go
// internal/encoder/ffmpeg_test.go
package encoder_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrybrown/streamline/internal/encoder"
)

func TestFFmpegAvailable(t *testing.T) {
	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
}

func TestFFmpegProcess_ProducesSegments(t *testing.T) {
	_, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	outputDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use a generated test pattern instead of BBB for unit tests
	proc := encoder.NewFFmpegProcess(encoder.FFmpegConfig{
		InputURI:         "testsrc",  // FFmpeg test source
		OutputDir:        outputDir,
		Codec:            "libx264",
		Width:            320,
		Height:           240,
		BitrateKbps:      500,
		Framerate:        30,
		SegmentDurationS: 2,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- proc.Start(ctx) }()

	// Wait for at least one segment to appear
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for segments")
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				t.Fatalf("ffmpeg exited with error: %v", err)
			}
		default:
			matches, _ := filepath.Glob(filepath.Join(outputDir, "*.ts"))
			if len(matches) > 0 {
				cancel() // Got segments, done
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}
```

**Step 2: Implement FFmpeg process manager**

```go
// internal/encoder/ffmpeg.go
package encoder

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
)

// FFmpegConfig defines encoding parameters.
type FFmpegConfig struct {
	InputURI         string
	OutputDir        string
	Codec            string
	Width            int
	Height           int
	BitrateKbps      int
	Framerate        int
	SegmentDurationS int
}

// FFmpegProcess manages an FFmpeg subprocess.
type FFmpegProcess struct {
	cfg FFmpegConfig
	cmd *exec.Cmd
}

// NewFFmpegProcess creates a new FFmpeg process manager.
func NewFFmpegProcess(cfg FFmpegConfig) *FFmpegProcess {
	return &FFmpegProcess{cfg: cfg}
}

// Start launches FFmpeg and blocks until it exits or context is cancelled.
func (p *FFmpegProcess) Start(ctx context.Context) error {
	args := p.buildArgs()
	p.cmd = exec.CommandContext(ctx, "ffmpeg", args...)
	return p.cmd.Run()
}

func (p *FFmpegProcess) buildArgs() []string {
	outputPattern := filepath.Join(p.cfg.OutputDir, "segment_%05d.ts")
	playlistPath := filepath.Join(p.cfg.OutputDir, "stream.m3u8")

	args := []string{}

	// Input
	if p.cfg.InputURI == "testsrc" {
		// FFmpeg test source for testing
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("testsrc=size=%dx%d:rate=%d", p.cfg.Width, p.cfg.Height, p.cfg.Framerate))
	} else {
		args = append(args, "-re", "-i", p.cfg.InputURI)
	}

	// Encoding
	args = append(args,
		"-c:v", p.cfg.Codec,
		"-b:v", fmt.Sprintf("%dk", p.cfg.BitrateKbps),
		"-s", fmt.Sprintf("%dx%d", p.cfg.Width, p.cfg.Height),
		"-r", fmt.Sprintf("%d", p.cfg.Framerate),
		"-preset", "veryfast",
		"-g", fmt.Sprintf("%d", p.cfg.Framerate*p.cfg.SegmentDurationS), // keyframe interval
	)

	// HLS output
	args = append(args,
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", p.cfg.SegmentDurationS),
		"-hls_list_size", "10",
		"-hls_flags", "delete_segments+append_list",
		"-hls_segment_filename", outputPattern,
		playlistPath,
	)

	return args
}
```

**Step 3: Run tests**

```bash
go test ./internal/encoder/... -v -race
```

**Step 4: Write test for segment watcher**

The segment watcher monitors an output directory for new `.ts` files.

```go
// internal/encoder/segment_watcher_test.go
package encoder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrybrown/streamline/internal/encoder"
)

func TestSegmentWatcher_DetectsNewSegments(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watcher := encoder.NewSegmentWatcher(dir)
	segments := watcher.Watch(ctx)

	// Create a segment file
	segPath := filepath.Join(dir, "segment_00001.ts")
	if err := os.WriteFile(segPath, []byte("fake segment"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case seg := <-segments:
		if seg.Path != segPath {
			t.Errorf("expected %s, got %s", segPath, seg.Path)
		}
		if seg.SequenceNumber != 1 {
			t.Errorf("expected sequence 1, got %d", seg.SequenceNumber)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for segment notification")
	}
}
```

**Step 5: Implement segment watcher**

```go
// internal/encoder/segment_watcher.go
package encoder

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Segment represents a detected HLS segment file.
type Segment struct {
	Path           string
	SequenceNumber int64
	Size           int64
}

// SegmentWatcher monitors a directory for new .ts segment files.
type SegmentWatcher struct {
	dir  string
	seen map[string]bool
}

// NewSegmentWatcher creates a new watcher for the given directory.
func NewSegmentWatcher(dir string) *SegmentWatcher {
	return &SegmentWatcher{dir: dir, seen: make(map[string]bool)}
}

// Watch returns a channel that emits new segments as they appear.
func (w *SegmentWatcher) Watch(ctx context.Context) <-chan Segment {
	ch := make(chan Segment, 16)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.scan(ch)
			}
		}
	}()

	return ch
}

func (w *SegmentWatcher) scan(ch chan<- Segment) {
	matches, _ := filepath.Glob(filepath.Join(w.dir, "segment_*.ts"))
	sort.Strings(matches)

	for _, path := range matches {
		if w.seen[path] {
			continue
		}
		w.seen[path] = true

		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		seq := parseSequenceNumber(filepath.Base(path))
		ch <- Segment{
			Path:           path,
			SequenceNumber: seq,
			Size:           info.Size(),
		}
	}
}

func parseSequenceNumber(filename string) int64 {
	// segment_00001.ts → 1
	name := strings.TrimSuffix(filename, ".ts")
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	return n
}
```

**Step 6: Run tests**

```bash
go test ./internal/encoder/... -v -race
```

**Step 7: Commit**

```bash
git add internal/encoder/
git commit -m "feat: add FFmpeg process manager and segment watcher"
```

---

### Story 2.3: Encoder Worker Service

Wire the FFmpeg process manager, segment watcher, and Kafka producer into a runnable service.

**Files:**
- Create: `cmd/encoder-worker/main.go`
- Create: `internal/encoder/worker.go`
- Create: `internal/encoder/worker_test.go`

**Step 1: Write test for encoder worker**

```go
// internal/encoder/worker_test.go
package encoder_test

// This is an integration test — it requires FFmpeg and Kafka.
// Run with: go test -run TestWorker -v (not -short)

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/kafka"
	"github.com/alexrybrown/streamline/internal/encoder"
	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

func TestWorker_ProducesSegmentsAndPublishesEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start Kafka
	kafkaContainer, err := kafka.Run(ctx, "confluentinc/confluent-local:7.5.0")
	if err != nil {
		t.Fatal(err)
	}
	defer kafkaContainer.Terminate(ctx)

	brokers, err := kafkaContainer.Brokers(ctx)
	if err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()

	w, err := encoder.NewWorker(encoder.WorkerConfig{
		WorkerID:    "test-worker-1",
		StreamID:    "test-stream-1",
		KafkaBrokers: brokers,
		FFmpeg: encoder.FFmpegConfig{
			InputURI:         "testsrc",
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
	hbConsumer, err := streamkafka.NewConsumer(brokers, streamkafka.TopicHeartbeats, "test-hb")
	if err != nil {
		t.Fatal(err)
	}
	defer hbConsumer.Close()

	hbCtx, hbCancel := context.WithTimeout(ctx, 30*time.Second)
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

**Step 2: Implement the Worker struct**

The Worker coordinates the FFmpeg process, segment watcher, heartbeat emitter, and Kafka publisher. This is where the local supervisor (Layer 1 failover) lives — if FFmpeg crashes, the worker restarts it.

```go
// internal/encoder/worker.go
package encoder

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.uber.org/zap"

	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
	"github.com/alexrybrown/streamline/internal/logging"
)

// WorkerConfig holds configuration for an encoder worker.
type WorkerConfig struct {
	WorkerID          string
	StreamID          string
	KafkaBrokers      []string
	FFmpeg            FFmpegConfig
	HeartbeatInterval time.Duration
}

// Worker manages encoding for a single stream.
type Worker struct {
	cfg        WorkerConfig
	log        *zap.Logger
	hbProducer *streamkafka.Producer
	segProducer *streamkafka.Producer
}

// NewWorker creates a new encoder worker.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 5 * time.Second
	}

	hbProducer, err := streamkafka.NewProducer(cfg.KafkaBrokers, streamkafka.TopicHeartbeats)
	if err != nil {
		return nil, err
	}

	segProducer, err := streamkafka.NewProducer(cfg.KafkaBrokers, streamkafka.TopicSegments)
	if err != nil {
		return nil, err
	}

	return &Worker{
		cfg:         cfg,
		log:         logging.New("encoder-worker").With(zap.String("worker_id", cfg.WorkerID), zap.String("stream_id", cfg.StreamID)),
		hbProducer:  hbProducer,
		segProducer: segProducer,
	}, nil
}

// Run starts encoding, heartbeats, and segment publishing. Blocks until context is cancelled.
// Implements Layer 1 failover: restarts FFmpeg on crash.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	// Start heartbeat emitter
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.emitHeartbeats(ctx)
	}()

	// Start segment watcher
	watcher := NewSegmentWatcher(w.cfg.FFmpeg.OutputDir)
	segments := watcher.Watch(ctx)

	// Publish segment events
	wg.Add(1)
	go func() {
		defer wg.Done()
		for seg := range segments {
			w.publishSegment(ctx, seg)
		}
	}()

	// Run FFmpeg with local supervisor (Layer 1 failover)
	w.runWithSupervisor(ctx)

	wg.Wait()
	return nil
}

// runWithSupervisor restarts FFmpeg if it crashes, up to a max retry count.
func (w *Worker) runWithSupervisor(ctx context.Context) {
	maxRetries := 3
	retries := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		proc := NewFFmpegProcess(w.cfg.FFmpeg)
		w.log.Info("starting FFmpeg")

		err := proc.Start(ctx)
		if ctx.Err() != nil {
			return // Context cancelled, clean exit
		}

		retries++
		w.log.Error("FFmpeg exited unexpectedly", zap.Error(err), zap.Int("retry", retries))

		if retries >= maxRetries {
			w.log.Error("max FFmpeg retries exceeded, worker giving up")
			return
		}

		time.Sleep(time.Duration(retries) * time.Second) // backoff
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
				w.log.Warn("failed to publish heartbeat", zap.Error(err))
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
		w.log.Warn("failed to publish segment event", zap.Error(err))
	} else {
		w.log.Info("segment produced", zap.Int64("sequence", seg.SequenceNumber), zap.Int64("size", seg.Size))
	}
}
```

**Step 3: Create main entry point**

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
		FFmpeg: encoder.FFmpegConfig{
			InputURI:         inputURI,
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

	log.Info("starting encoder worker", zap.String("worker_id", workerID), zap.String("stream_id", streamID))
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

**Step 4: Build and verify**

```bash
go build ./cmd/encoder-worker
```

**Step 5: Run integration tests**

```bash
go test ./internal/encoder/... -v -race
```

**Step 6: Manual smoke test (with Docker Compose infrastructure running)**

```bash
make docker-up
make download-bbb
INPUT_URI="file://$PWD/assets/bbb.mp4" go run ./cmd/encoder-worker
# In another terminal: check Kafka for events
# Ctrl+C to stop
```

**Step 7: Commit**

```bash
git add cmd/encoder-worker/ internal/encoder/ internal/kafka/
git commit -m "feat: add encoder worker with FFmpeg supervisor, heartbeats, and segment events"
```
