# streamline — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a live streaming orchestration platform that ingests, transcodes, packages, and serves video — demonstrating domain expertise in live video infrastructure with Go, ConnectRPC, Kafka, and Kubernetes.

**Architecture:** Control plane (Stream Manager, Pipeline Controller, API Service) orchestrates a data plane (Source, Encoder Workers, Packager). Kafka carries pipeline telemetry. Encoder workers push segments to Packager via HTTP. The system uses layered failover: local FFmpeg supervisor, heartbeat timeout, and K8s restart.

**Tech Stack:** Go, ConnectRPC + Protobuf (Buf CLI), Kafka (segmentio/kafka-go), MongoDB (official driver), FFmpeg + H.264, HLS (RFC 8216), Vite + React + hls.js, OpenTelemetry, Prometheus, Grafana, Docker Compose, Helm, OpenTofu

---

## How This Plan Is Organized

- **Epics** = Build phases from the spec (8 total)
- **Stories** = Discrete pieces of work within each epic, each producing a testable + committable result
- **Steps** = Bite-sized TDD tasks within each story

Each story follows TDD where applicable: write test → verify fail → implement → verify pass → commit.

Infrastructure/config stories that don't have meaningful unit tests skip the TDD cycle and commit after verification.

---

## Epic 1: Foundation

**Goal:** Repository structure, protobuf schemas, shared packages, Docker Compose infrastructure.

**Milestone:** `buf generate` produces Go code, `docker compose up` starts Kafka + MongoDB + Prometheus + Grafana, project builds cleanly.

---

### Story 1.1: Repository and Go Workspace

Initialize the Git repo and Go module with the project structure.

**Files:**
- Create: `go.mod`
- Create: `go.work` (Go workspace for multi-module if needed — evaluate during implementation)
- Create: `.gitignore`
- Create: `README.md`
- Create: `Makefile`

**Steps:**

**Step 1: Initialize repo and Go module**

```bash
mkdir -p ~/Projects/streamline && cd ~/Projects/streamline
git init
go mod init github.com/yourusername/streamline
```

Replace `yourusername` with Alex's GitHub username.

**Step 2: Create .gitignore**

Standard Go gitignore plus project-specific entries:
```
# Binaries
/bin/
*.exe

# IDE
.idea/
.vscode/
*.swp

# Generated
/gen/

# Dependencies
/vendor/

# Docker volumes
/data/

# OS
.DS_Store
Thumbs.db

# Video assets (downloaded at build time)
/assets/*.mp4

# Environment
.env
.env.local
```

**Step 3: Create directory structure**

```bash
mkdir -p cmd/{stream-manager,encoder-worker,packager,pipeline-controller,api-server}
mkdir -p internal/{kafka,mongodb,health,config}
mkdir -p proto/streamline/v1
mkdir -p gen/go
mkdir -p deployments/{docker,helm,tofu}
mkdir -p assets
mkdir -p docs/adrs
```

Directory layout:
- `cmd/` — service entry points (one per service)
- `internal/` — shared internal packages
- `proto/` — Protobuf definitions
- `gen/` — generated code (gitignored)
- `deployments/` — Docker Compose, Helm, OpenTofu
- `assets/` — Big Buck Bunny video file (downloaded, gitignored)
- `docs/adrs/` — ADR markdown files

**Step 4: Create Makefile with common targets**

```makefile
.PHONY: generate build test lint clean docker-up docker-down

generate:
	buf generate

build: generate
	go build ./cmd/...

test:
	go test ./... -v -race

lint:
	buf lint
	golangci-lint run ./...

clean:
	rm -rf gen/ bin/

docker-up:
	docker compose -f deployments/docker/docker-compose.yml up -d

docker-down:
	docker compose -f deployments/docker/docker-compose.yml down

download-bbb:
	@mkdir -p assets
	@if [ ! -f assets/bbb.mp4 ]; then \
		echo "Downloading Big Buck Bunny (1080p)..."; \
		curl -L -o assets/bbb.mp4 "http://commondatastorage.googleapis.com/gtv-videos-bucket/sample/BigBuckBunny.mp4"; \
	fi
```

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: initialize repo structure, Go module, and Makefile"
```

---

### Story 1.2: Buf CLI and Protobuf Schemas

Set up Buf for proto management and define the initial service schemas.

**Files:**
- Create: `buf.yaml`
- Create: `buf.gen.yaml`
- Create: `proto/streamline/v1/stream.proto`
- Create: `proto/streamline/v1/events.proto`
- Create: `proto/streamline/v1/stream_manager.proto`
- Create: `proto/streamline/v1/api.proto`

**Steps:**

**Step 1: Create buf.yaml**

```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

**Step 2: Create buf.gen.yaml**

```yaml
version: v2
clean: true
plugins:
  - local: protoc-gen-go
    out: gen/go
    opt: paths=source_relative
  - local: protoc-gen-connect-go
    out: gen/go
    opt: paths=source_relative
inputs:
  - directory: proto
```

**Step 3: Install Buf CLI and protoc-gen plugins**

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
```

**Step 4: Define core types — stream.proto**

This file defines the shared types used across services.

```protobuf
syntax = "proto3";

package streamline.v1;

option go_package = "github.com/yourusername/streamline/gen/go/streamline/v1;streamlinev1";

import "google/protobuf/timestamp.proto";

// Stream represents a live streaming pipeline instance.
message Stream {
  string id = 1;
  string source_uri = 2;
  StreamState state = 3;
  EncodingProfile encoding_profile = 4;
  google.protobuf.Timestamp created_at = 5;
  google.protobuf.Timestamp updated_at = 6;
}

enum StreamState {
  STREAM_STATE_UNSPECIFIED = 0;
  STREAM_STATE_PROVISIONING = 1;
  STREAM_STATE_ACTIVE = 2;
  STREAM_STATE_DEGRADED = 3;
  STREAM_STATE_FAILED = 4;
  STREAM_STATE_STOPPING = 5;
  STREAM_STATE_STOPPED = 6;
}

message EncodingProfile {
  string codec = 1;           // e.g., "h264"
  int32 width = 2;            // e.g., 1920
  int32 height = 3;           // e.g., 1080
  int32 bitrate_kbps = 4;     // e.g., 4000
  int32 framerate = 5;        // e.g., 30
  int32 segment_duration_s = 6; // e.g., 6
}
```

**Step 5: Define pipeline events — events.proto**

```protobuf
syntax = "proto3";

package streamline.v1;

option go_package = "github.com/yourusername/streamline/gen/go/streamline/v1;streamlinev1";

import "google/protobuf/timestamp.proto";

// Heartbeat emitted by encoder workers at a fixed interval.
message Heartbeat {
  string worker_id = 1;
  string stream_id = 2;
  google.protobuf.Timestamp timestamp = 3;
  WorkerStatus status = 4;
}

enum WorkerStatus {
  WORKER_STATUS_UNSPECIFIED = 0;
  WORKER_STATUS_IDLE = 1;
  WORKER_STATUS_ENCODING = 2;
  WORKER_STATUS_ERROR = 3;
}

// Emitted by encoder worker after pushing a segment to the Packager.
message SegmentProduced {
  string stream_id = 1;
  string worker_id = 2;
  int64 sequence_number = 3;
  int64 duration_ms = 4;
  int64 size_bytes = 5;
  google.protobuf.Timestamp timestamp = 6;
}

// Emitted by Packager after accepting a segment and updating the manifest.
message SegmentAvailable {
  string stream_id = 1;
  int64 sequence_number = 2;
  string playlist_path = 3;
  google.protobuf.Timestamp timestamp = 4;
}

// Emitted by Packager when a gap in sequence numbers is detected.
message SegmentGap {
  string stream_id = 1;
  int64 expected_sequence = 2;
  int64 received_sequence = 3;
  google.protobuf.Timestamp timestamp = 4;
}

// Emitted when stream state changes.
message StreamStateChanged {
  string stream_id = 1;
  StreamState previous_state = 2;
  StreamState new_state = 3;
  string reason = 4;
  google.protobuf.Timestamp timestamp = 5;
}

// Note: StreamState is defined in stream.proto and imported.
import "streamline/v1/stream.proto";
```

Note: The `import "streamline/v1/stream.proto"` in events.proto needs to come before the usage — the implementer should move the import to the top of the file and remove the duplicate `StreamState` reference. Alternatively, keep `StreamState` only in `stream.proto` and import it in `events.proto`.

**Step 6: Define Stream Manager service — stream_manager.proto**

```protobuf
syntax = "proto3";

package streamline.v1;

option go_package = "github.com/yourusername/streamline/gen/go/streamline/v1;streamlinev1";

import "streamline/v1/stream.proto";

service StreamManagerService {
  // Start a new live stream pipeline.
  rpc StartStream(StartStreamRequest) returns (StartStreamResponse) {}

  // Stop a running stream.
  rpc StopStream(StopStreamRequest) returns (StopStreamResponse) {}

  // Restart a stream (stop + start).
  rpc RestartStream(RestartStreamRequest) returns (RestartStreamResponse) {}
}

message StartStreamRequest {
  string source_uri = 1;
  EncodingProfile encoding_profile = 2;
}

message StartStreamResponse {
  Stream stream = 1;
}

message StopStreamRequest {
  string stream_id = 1;
}

message StopStreamResponse {
  Stream stream = 1;
}

message RestartStreamRequest {
  string stream_id = 1;
}

message RestartStreamResponse {
  Stream stream = 1;
}
```

**Step 7: Define API service — api.proto**

```protobuf
syntax = "proto3";

package streamline.v1;

option go_package = "github.com/yourusername/streamline/gen/go/streamline/v1;streamlinev1";

import "streamline/v1/stream.proto";

service StreamlineAPIService {
  // Get a stream by ID.
  rpc GetStream(GetStreamRequest) returns (GetStreamResponse) {}

  // List all streams.
  rpc ListStreams(ListStreamsRequest) returns (ListStreamsResponse) {}
}

message GetStreamRequest {
  string stream_id = 1;
}

message GetStreamResponse {
  Stream stream = 1;
}

message ListStreamsRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message ListStreamsResponse {
  repeated Stream streams = 1;
  string next_page_token = 2;
}
```

**Step 8: Generate Go code and verify**

```bash
buf lint
buf generate
```

Verify generated files exist:
```bash
ls gen/go/streamline/v1/
# Should see: stream.pb.go, events.pb.go, stream_manager.pb.go, api.pb.go
# Plus connect files in streamlinev1connect/
```

**Step 9: Run `go mod tidy` and verify build**

```bash
go mod tidy
go build ./...
```

**Step 10: Commit**

```bash
git add -A
git commit -m "feat: add protobuf schemas and Buf CLI config for all services"
```

---

### Story 1.3: Shared Internal Packages

Create the shared config, health, and logging packages that all services will use.

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/health/health.go`
- Create: `internal/health/health_test.go`
- Create: `internal/logging/logging.go`

**Steps:**

**Step 1: Write test for config package**

The config package loads service configuration from environment variables with sensible defaults.

```go
// internal/config/config_test.go
package config_test

import (
	"testing"

	"github.com/yourusername/streamline/internal/config"
)

func TestDefaults(t *testing.T) {
	cfg := config.Load()
	if cfg.KafkaBrokers == "" {
		t.Error("expected default Kafka broker")
	}
	if cfg.MongoURI == "" {
		t.Error("expected default Mongo URI")
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:29092")
	t.Setenv("MONGO_URI", "mongodb://mongo:27017")

	cfg := config.Load()
	if cfg.KafkaBrokers != "kafka:29092" {
		t.Errorf("expected kafka:29092, got %s", cfg.KafkaBrokers)
	}
	if cfg.MongoURI != "mongodb://mongo:27017" {
		t.Errorf("expected mongodb://mongo:27017, got %s", cfg.MongoURI)
	}
}
```

**Step 2: Implement config package**

```go
// internal/config/config.go
package config

import "os"

// Config holds service configuration loaded from environment variables.
type Config struct {
	KafkaBrokers string
	MongoURI     string
	MongoDBName  string
	ServicePort  string
}

// Load reads configuration from environment variables with defaults.
func Load() Config {
	return Config{
		KafkaBrokers: getEnv("KAFKA_BROKERS", "localhost:9092"),
		MongoURI:     getEnv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDBName:  getEnv("MONGO_DB_NAME", "streamline"),
		ServicePort:  getEnv("SERVICE_PORT", "8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Step 3: Run tests**

```bash
go test ./internal/config/... -v -race
```

**Step 4: Write test for health package**

```go
// internal/health/health_test.go
package health_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yourusername/streamline/internal/health"
)

func TestHealthHandler_Healthy(t *testing.T) {
	h := health.NewChecker()
	handler := h.Handler()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHealthHandler_WithFailingCheck(t *testing.T) {
	h := health.NewChecker()
	h.AddCheck("kafka", func() error {
		return fmt.Errorf("connection refused")
	})
	handler := h.Handler()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}
```

Note: add `"fmt"` to imports in the test file.

**Step 5: Implement health package**

```go
// internal/health/health.go
package health

import (
	"encoding/json"
	"net/http"
	"sync"
)

// CheckFunc returns nil if healthy, error otherwise.
type CheckFunc func() error

// Checker manages health check functions.
type Checker struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewChecker creates a new health checker.
func NewChecker() *Checker {
	return &Checker{checks: make(map[string]CheckFunc)}
}

// AddCheck registers a named health check.
func (c *Checker) AddCheck(name string, check CheckFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = check
}

// Handler returns an HTTP handler for health checks.
func (c *Checker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		defer c.mu.RUnlock()

		status := http.StatusOK
		results := make(map[string]string)

		for name, check := range c.checks {
			if err := check(); err != nil {
				status = http.StatusServiceUnavailable
				results[name] = err.Error()
			} else {
				results[name] = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(results)
	})
}
```

**Step 6: Run tests**

```bash
go test ./internal/... -v -race
```

**Step 7: Create structured logging setup**

```go
// internal/logging/logging.go
package logging

import (
	"log/slog"
	"os"
)

// New creates a structured JSON logger with common fields.
func New(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With("service", service)
}
```

Uses Go 1.21+ `slog` — no external dependency.

**Step 8: Commit**

```bash
git add -A
git commit -m "feat: add shared config, health, and logging packages"
```

---

### Story 1.4: Docker Compose Infrastructure

Set up Docker Compose with Kafka, MongoDB, Prometheus, and Grafana.

**Files:**
- Create: `deployments/docker/docker-compose.yml`
- Create: `deployments/docker/prometheus.yml`

**Steps:**

**Step 1: Create Docker Compose file**

```yaml
# deployments/docker/docker-compose.yml
services:
  kafka:
    image: confluentinc/confluent-local:7.5.0
    ports:
      - "9092:9092"
      - "9101:9101"
    environment:
      KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://localhost:9092
      KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT
    healthcheck:
      test: ["CMD", "kafka-topics", "--bootstrap-server", "localhost:9092", "--list"]
      interval: 10s
      timeout: 5s
      retries: 5

  mongodb:
    image: mongo:7
    ports:
      - "27017:27017"
    volumes:
      - mongo-data:/data/db
    healthcheck:
      test: ["CMD", "mongosh", "--eval", "db.adminCommand('ping')"]
      interval: 10s
      timeout: 5s
      retries: 5

  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3001:3000"
    environment:
      GF_SECURITY_ADMIN_PASSWORD: admin
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: Admin
    depends_on:
      - prometheus

volumes:
  mongo-data:
```

**Step 2: Create Prometheus config**

```yaml
# deployments/docker/prometheus.yml
global:
  scrape_interval: 15s

scrape_configs:
  - job_name: "streamline"
    static_configs:
      - targets:
          - "host.docker.internal:8080"   # stream-manager
          - "host.docker.internal:8081"   # encoder-worker
          - "host.docker.internal:8082"   # packager
          - "host.docker.internal:8083"   # pipeline-controller
          - "host.docker.internal:8084"   # api-server
```

**Step 3: Verify infrastructure starts**

```bash
make docker-up
# Wait for health checks
docker compose -f deployments/docker/docker-compose.yml ps
# All services should be healthy
make docker-down
```

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add Docker Compose with Kafka, MongoDB, Prometheus, Grafana"
```

---

### Story 1.5: Download Big Buck Bunny Test Asset

**Steps:**

**Step 1: Download Big Buck Bunny**

```bash
make download-bbb
```

This downloads the 1080p version (~150MB). Verify:
```bash
ffprobe assets/bbb.mp4
# Should show: H.264, 1920x1080, 30fps, ~10 min duration
```

**Step 2: Verify .gitignore excludes it**

```bash
git status
# assets/bbb.mp4 should NOT appear as untracked
```

No commit needed — the Makefile target is already committed.

---

## Epic 2: Encoding Pipeline

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

**Steps:**

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
	streamkafka "github.com/yourusername/streamline/internal/kafka"
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
git add -A
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

**Steps:**

**Step 1: Write test for FFmpeg process manager**

```go
// internal/encoder/ffmpeg_test.go
package encoder_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yourusername/streamline/internal/encoder"
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

	"github.com/yourusername/streamline/internal/encoder"
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
	"fmt"
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
git add -A
git commit -m "feat: add FFmpeg process manager and segment watcher"
```

---

### Story 2.3: Encoder Worker Service

Wire the FFmpeg process manager, segment watcher, and Kafka producer into a runnable service.

**Files:**
- Create: `cmd/encoder-worker/main.go`
- Create: `internal/encoder/worker.go`
- Create: `internal/encoder/worker_test.go`

**Steps:**

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
	"github.com/yourusername/streamline/internal/encoder"
	streamkafka "github.com/yourusername/streamline/internal/kafka"
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
	"log/slog"
	"sync"
	"time"

	streamkafka "github.com/yourusername/streamline/internal/kafka"
	"github.com/yourusername/streamline/internal/logging"
)

// WorkerConfig holds configuration for an encoder worker.
type WorkerConfig struct {
	WorkerID       string
	StreamID       string
	KafkaBrokers   []string
	FFmpeg         FFmpegConfig
	HeartbeatInterval time.Duration
}

// Worker manages encoding for a single stream.
type Worker struct {
	cfg       WorkerConfig
	log       *slog.Logger
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
		log:         logging.New("encoder-worker").With("worker_id", cfg.WorkerID, "stream_id", cfg.StreamID),
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
		w.log.Error("FFmpeg exited unexpectedly", "error", err, "retry", retries)

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
				w.log.Warn("failed to publish heartbeat", "error", err)
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
		w.log.Warn("failed to publish segment event", "error", err)
	} else {
		w.log.Info("segment produced", "sequence", seg.SequenceNumber, "size", seg.Size)
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

	"github.com/yourusername/streamline/internal/config"
	"github.com/yourusername/streamline/internal/encoder"
	"github.com/yourusername/streamline/internal/logging"
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
		log.Error("failed to create worker", "error", err)
		os.Exit(1)
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

	log.Info("starting encoder worker", "worker_id", workerID, "stream_id", streamID)
	if err := w.Run(ctx); err != nil {
		log.Error("worker exited with error", "error", err)
		os.Exit(1)
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
git add -A
git commit -m "feat: add encoder worker with FFmpeg supervisor, heartbeats, and segment events"
```

---

## Epic 3: Packaging + Playback

**Goal:** Packager accepts segment pushes, generates HLS manifests, serves content. Video plays in a browser.

**Milestone:** BBB plays in a bare hls.js page.

---

### Story 3.1: Segment Receiver (HTTP)

The Packager's HTTP endpoint that accepts segment pushes from encoder workers.

**Files:**
- Create: `internal/packager/receiver.go`
- Create: `internal/packager/receiver_test.go`

**Steps:**

**Step 1: Write test for segment receiver**

Test duplicate handling (idempotent by stream ID + sequence number) and gap detection.

```go
// internal/packager/receiver_test.go
package packager_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourusername/streamline/internal/packager"
)

func TestReceiver_AcceptsSegment(t *testing.T) {
	dir := t.TempDir()
	r := packager.NewReceiver(dir)
	handler := r.Handler()

	body := bytes.NewReader([]byte("fake segment data"))
	req := httptest.NewRequest(http.MethodPut, "/segments/stream-1/3", body)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify file was written
	segPath := filepath.Join(dir, "stream-1", "segment_00003.ts")
	if _, err := os.Stat(segPath); err != nil {
		t.Errorf("segment file not found: %v", err)
	}
}

func TestReceiver_DeduplicatesSegment(t *testing.T) {
	dir := t.TempDir()
	r := packager.NewReceiver(dir)
	handler := r.Handler()

	// Push same segment twice
	for i := 0; i < 2; i++ {
		body := bytes.NewReader([]byte("fake segment data"))
		req := httptest.NewRequest(http.MethodPut, "/segments/stream-1/3", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	// Should still only have one file (idempotent)
	segPath := filepath.Join(dir, "stream-1", "segment_00003.ts")
	info, err := os.Stat(segPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("fake segment data")) {
		t.Errorf("unexpected file size: %d", info.Size())
	}
}
```

**Step 2: Implement receiver**

```go
// internal/packager/receiver.go
package packager

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Receiver handles incoming segment pushes via HTTP.
type Receiver struct {
	outputDir string
	mu        sync.Mutex
	received  map[string]bool // "streamID/seq" → true
}

// NewReceiver creates a segment receiver writing to the given directory.
func NewReceiver(outputDir string) *Receiver {
	return &Receiver{
		outputDir: outputDir,
		received:  make(map[string]bool),
	}
}

// Handler returns an HTTP handler for segment pushes.
// Route: PUT /segments/{streamID}/{sequenceNumber}
func (r *Receiver) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse path: /segments/{streamID}/{seq}
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "segments" {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		streamID := parts[1]
		seq, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.Error(w, "invalid sequence number", http.StatusBadRequest)
			return
		}

		// Dedup check
		key := fmt.Sprintf("%s/%d", streamID, seq)
		r.mu.Lock()
		if r.received[key] {
			r.mu.Unlock()
			w.WriteHeader(http.StatusOK) // idempotent — no-op
			return
		}
		r.received[key] = true
		r.mu.Unlock()

		// Write segment to disk
		streamDir := filepath.Join(r.outputDir, streamID)
		os.MkdirAll(streamDir, 0755)

		filename := fmt.Sprintf("segment_%05d.ts", seq)
		segPath := filepath.Join(streamDir, filename)

		f, err := os.Create(segPath)
		if err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		if _, err := io.Copy(f, req.Body); err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	})
}
```

**Step 3: Run tests**

```bash
go test ./internal/packager/... -v -race
```

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add segment receiver with deduplication"
```

---

### Story 3.2: HLS Manifest Generator

Generates and updates HLS playlists as segments arrive.

**Files:**
- Create: `internal/packager/manifest.go`
- Create: `internal/packager/manifest_test.go`

**Steps:**

**Step 1: Write test for manifest generation**

```go
// internal/packager/manifest_test.go
package packager_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourusername/streamline/internal/packager"
)

func TestManifest_GeneratesValidHLS(t *testing.T) {
	dir := t.TempDir()

	m := packager.NewManifestGenerator(dir, packager.ManifestConfig{
		TargetDuration: 6,
		WindowSize:     10,
	})

	m.AddSegment("stream-1", packager.SegmentInfo{
		Filename:       "segment_00001.ts",
		SequenceNumber: 1,
		DurationS:      6.0,
	})

	m.AddSegment("stream-1", packager.SegmentInfo{
		Filename:       "segment_00002.ts",
		SequenceNumber: 2,
		DurationS:      6.0,
	})

	playlistPath := filepath.Join(dir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "#EXTM3U") {
		t.Error("missing #EXTM3U header")
	}
	if !strings.Contains(content, "#EXT-X-TARGETDURATION:6") {
		t.Error("missing target duration")
	}
	if !strings.Contains(content, "segment_00001.ts") {
		t.Error("missing segment 1")
	}
	if !strings.Contains(content, "segment_00002.ts") {
		t.Error("missing segment 2")
	}
}

func TestManifest_SlidingWindow(t *testing.T) {
	dir := t.TempDir()

	m := packager.NewManifestGenerator(dir, packager.ManifestConfig{
		TargetDuration: 2,
		WindowSize:     3, // Only keep 3 segments in playlist
	})

	for i := int64(1); i <= 5; i++ {
		m.AddSegment("stream-1", packager.SegmentInfo{
			Filename:       fmt.Sprintf("segment_%05d.ts", i),
			SequenceNumber: i,
			DurationS:      2.0,
		})
	}

	playlistPath := filepath.Join(dir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Should NOT contain segments 1-2 (slid out of window)
	if strings.Contains(content, "segment_00001.ts") {
		t.Error("segment 1 should have been removed from window")
	}
	// Should contain segments 3-5
	if !strings.Contains(content, "segment_00003.ts") {
		t.Error("segment 3 should be in window")
	}
	if !strings.Contains(content, "segment_00005.ts") {
		t.Error("segment 5 should be in window")
	}
}
```

Note: add `"fmt"` to imports for the sliding window test.

**Step 2: Implement manifest generator**

Implement with atomic writes (write to temp file, rename) per the spec requirement.

```go
// internal/packager/manifest.go
package packager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ManifestConfig controls playlist generation.
type ManifestConfig struct {
	TargetDuration int // EXT-X-TARGETDURATION in seconds
	WindowSize     int // Max segments in the sliding window
}

// SegmentInfo describes a segment for the playlist.
type SegmentInfo struct {
	Filename       string
	SequenceNumber int64
	DurationS      float64
}

// ManifestGenerator creates and updates HLS playlists.
type ManifestGenerator struct {
	outputDir string
	cfg       ManifestConfig
	mu        sync.Mutex
	segments  map[string][]SegmentInfo // streamID → segments
}

// NewManifestGenerator creates a new manifest generator.
func NewManifestGenerator(outputDir string, cfg ManifestConfig) *ManifestGenerator {
	return &ManifestGenerator{
		outputDir: outputDir,
		cfg:       cfg,
		segments:  make(map[string][]SegmentInfo),
	}
}

// AddSegment adds a segment to the playlist and regenerates the manifest.
func (m *ManifestGenerator) AddSegment(streamID string, seg SegmentInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.segments[streamID] = append(m.segments[streamID], seg)

	// Sliding window
	if len(m.segments[streamID]) > m.cfg.WindowSize {
		m.segments[streamID] = m.segments[streamID][len(m.segments[streamID])-m.cfg.WindowSize:]
	}

	return m.writePlaylist(streamID)
}

func (m *ManifestGenerator) writePlaylist(streamID string) error {
	segs := m.segments[streamID]
	if len(segs) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString(fmt.Sprintf("#EXT-X-VERSION:3\n"))
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", m.cfg.TargetDuration))
	b.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", segs[0].SequenceNumber))
	b.WriteString("\n")

	for _, seg := range segs {
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.DurationS))
		b.WriteString(seg.Filename + "\n")
	}

	streamDir := filepath.Join(m.outputDir, streamID)
	os.MkdirAll(streamDir, 0755)

	// Atomic write: write to temp file, then rename
	playlistPath := filepath.Join(streamDir, "stream.m3u8")
	tmpPath := playlistPath + ".tmp"

	if err := os.WriteFile(tmpPath, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, playlistPath)
}
```

**Step 3: Run tests**

```bash
go test ./internal/packager/... -v -race
```

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add HLS manifest generator with sliding window and atomic writes"
```

---

### Story 3.3: Packager Service

Wire receiver, manifest generator, Kafka consumer/producer, and HTTP server into a runnable service.

**Files:**
- Create: `internal/packager/service.go`
- Create: `cmd/packager/main.go`

**Steps:**

**Step 1: Implement packager service**

The packager service:
1. Listens for segment pushes via HTTP
2. Writes segments to disk
3. Updates HLS manifests
4. Publishes segment-available events to Kafka
5. Serves HLS content (segments + manifests) via HTTP

```go
// internal/packager/service.go
package packager

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	streamkafka "github.com/yourusername/streamline/internal/kafka"
	"github.com/yourusername/streamline/internal/logging"
)

// ServiceConfig holds packager service configuration.
type ServiceConfig struct {
	Port           string
	OutputDir      string
	KafkaBrokers   []string
	TargetDuration int
	WindowSize     int
}

// Service is the packager HTTP server.
type Service struct {
	cfg       ServiceConfig
	log       *slog.Logger
	receiver  *Receiver
	manifest  *ManifestGenerator
	producer  *streamkafka.Producer
}

// NewService creates a new packager service.
func NewService(cfg ServiceConfig) (*Service, error) {
	producer, err := streamkafka.NewProducer(cfg.KafkaBrokers, streamkafka.TopicSegments)
	if err != nil {
		return nil, err
	}

	return &Service{
		cfg:      cfg,
		log:      logging.New("packager"),
		receiver: NewReceiver(cfg.OutputDir),
		manifest: NewManifestGenerator(cfg.OutputDir, ManifestConfig{
			TargetDuration: cfg.TargetDuration,
			WindowSize:     cfg.WindowSize,
		}),
		producer: producer,
	}, nil
}

// Run starts the packager HTTP server.
func (s *Service) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Segment push endpoint
	mux.Handle("/segments/", s.segmentHandler())

	// HLS content serving
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.FileServer(http.Dir(s.cfg.OutputDir))))

	srv := &http.Server{Addr: ":" + s.cfg.Port, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	s.log.Info("packager listening", "port", s.cfg.Port)
	return srv.ListenAndServe()
}

func (s *Service) segmentHandler() http.Handler {
	// Wrap the receiver to also update manifests and publish events
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delegate to receiver for disk write + dedup
		rec := s.receiver.Handler()
		rw := &responseCapture{ResponseWriter: w}
		rec.ServeHTTP(rw, r)

		if rw.statusCode != http.StatusOK {
			return
		}

		// Parse stream ID and sequence from path
		// Path: /segments/{streamID}/{seq}
		// The receiver already validated and wrote the file.
		// Extract info for manifest update and event publishing.
		// Implementation: parse the path, update manifest, publish event.
		// (Details to be refined during implementation based on actual receiver interface)
	})
}

type responseCapture struct {
	http.ResponseWriter
	statusCode int
}

func (r *responseCapture) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
```

Note: The segment handler integration (receiver → manifest → kafka publish) will need refinement during implementation. The receiver should return structured info about the accepted segment so the service layer can update the manifest and publish events. Consider adding a callback or returning segment info from the receiver.

**Step 2: Create main entry point**

```go
// cmd/packager/main.go
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yourusername/streamline/internal/config"
	"github.com/yourusername/streamline/internal/logging"
	"github.com/yourusername/streamline/internal/packager"
)

func main() {
	log := logging.New("packager")
	cfg := config.Load()

	svc, err := packager.NewService(packager.ServiceConfig{
		Port:           getEnv("PACKAGER_PORT", "8082"),
		OutputDir:      getEnv("OUTPUT_DIR", "/tmp/packager"),
		KafkaBrokers:   strings.Split(cfg.KafkaBrokers, ","),
		TargetDuration: 6,
		WindowSize:     10,
	})
	if err != nil {
		log.Error("failed to create packager", "error", err)
		os.Exit(1)
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

	log.Info("starting packager")
	if err := svc.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Error("packager exited with error", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

Add `"net/http"` to imports.

**Step 3: Build and verify**

```bash
go build ./cmd/packager
```

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: add packager service with segment receiver, manifest generation, and HLS serving"
```

---

### Story 3.4: Encoder → Packager Integration

Update the encoder worker to push segments to the Packager via HTTP instead of just writing to local disk.

**Files:**
- Modify: `internal/encoder/worker.go`
- Create: `internal/encoder/segment_pusher.go`
- Create: `internal/encoder/segment_pusher_test.go`

**Steps:**

**Step 1: Write test for segment pusher**

```go
// internal/encoder/segment_pusher_test.go
package encoder_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourusername/streamline/internal/encoder"
)

func TestSegmentPusher_PushesSegment(t *testing.T) {
	// Set up a mock packager
	var receivedPath string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create a fake segment file
	dir := t.TempDir()
	segPath := filepath.Join(dir, "segment_00001.ts")
	os.WriteFile(segPath, []byte("test segment data"), 0644)

	pusher := encoder.NewSegmentPusher(server.URL, "stream-1")
	err := pusher.Push(segPath, 1)
	if err != nil {
		t.Fatal(err)
	}

	if receivedPath != "/segments/stream-1/1" {
		t.Errorf("unexpected path: %s", receivedPath)
	}
	if string(receivedBody) != "test segment data" {
		t.Errorf("unexpected body: %s", string(receivedBody))
	}
}
```

Add `"io"` to imports.

**Step 2: Implement segment pusher**

```go
// internal/encoder/segment_pusher.go
package encoder

import (
	"fmt"
	"net/http"
	"os"
)

// SegmentPusher pushes encoded segments to the Packager via HTTP.
type SegmentPusher struct {
	packagerURL string
	streamID    string
	client      *http.Client
}

// NewSegmentPusher creates a pusher targeting the given packager URL.
func NewSegmentPusher(packagerURL, streamID string) *SegmentPusher {
	return &SegmentPusher{
		packagerURL: packagerURL,
		streamID:    streamID,
		client:      &http.Client{},
	}
}

// Push sends a segment file to the packager.
func (p *SegmentPusher) Push(filePath string, sequenceNumber int64) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()

	url := fmt.Sprintf("%s/segments/%s/%d", p.packagerURL, p.streamID, sequenceNumber)
	req, err := http.NewRequest(http.MethodPut, url, f)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("push segment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("packager returned %d", resp.StatusCode)
	}
	return nil
}
```

**Step 3: Update worker to use segment pusher**

Modify `internal/encoder/worker.go` to push segments to the Packager instead of just publishing Kafka events. The segment watcher detects new segments → pusher sends to Packager → then publish Kafka event on success.

Add `PackagerURL string` to `WorkerConfig`. Update the `publishSegment` method to push first, then publish the Kafka event.

**Step 4: Run tests**

```bash
go test ./internal/encoder/... -v -race
```

**Step 5: End-to-end smoke test**

```bash
make docker-up
# Terminal 1: run packager
go run ./cmd/packager
# Terminal 2: run encoder (pointing at packager)
PACKAGER_URL=http://localhost:8082 INPUT_URI="file://$PWD/assets/bbb.mp4" go run ./cmd/encoder-worker
# Terminal 3: check HLS output
curl http://localhost:8082/hls/stream-1/stream.m3u8
# Should see a valid M3U8 playlist
```

**Step 6: Commit**

```bash
git add -A
git commit -m "feat: add segment pusher and wire encoder → packager pipeline"
```

---

### Story 3.5: Bare hls.js Verification Page

Minimal HTML page to verify the HLS output is playable.

**Files:**
- Create: `web/verify.html`

**Steps:**

**Step 1: Create verification page**

```html
<!-- web/verify.html -->
<!DOCTYPE html>
<html>
<head>
  <title>streamline — HLS Verification</title>
  <script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
  <style>
    body { font-family: monospace; background: #1a1a1a; color: #eee; padding: 20px; }
    video { max-width: 800px; width: 100%; background: #000; }
    #status { margin-top: 10px; }
  </style>
</head>
<body>
  <h1>streamline — HLS Verification</h1>
  <video id="video" controls></video>
  <div id="status">Connecting...</div>

  <script>
    const video = document.getElementById('video');
    const status = document.getElementById('status');
    const src = 'http://localhost:8082/hls/stream-1/stream.m3u8';

    if (Hls.isSupported()) {
      const hls = new Hls();
      hls.loadSource(src);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        status.textContent = 'Playing';
        video.play();
      });
      hls.on(Hls.Events.ERROR, (event, data) => {
        status.textContent = 'Error: ' + data.type + ' - ' + data.details;
      });
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = src;
    }
  </script>
</body>
</html>
```

**Step 2: Verify end-to-end**

With Kafka, encoder, and packager running:
```bash
# Open web/verify.html in a browser
# Video should play
```

This is the Phase 3 milestone: **video plays in a browser**.

**Step 3: Commit**

```bash
git add -A
git commit -m "feat: add bare hls.js verification page for end-to-end playback test"
```

---

## Epic 4: Stream Manager + Live Source

**Goal:** ConnectRPC API for stream lifecycle, Big Buck Bunny as RTMP live source.

**Milestone:** ConnectRPC call starts a stream, video flows end-to-end.

---

### Story 4.1: Stream Manager ConnectRPC Service

**Files:**
- Create: `internal/manager/service.go`
- Create: `internal/manager/service_test.go`
- Create: `cmd/stream-manager/main.go`

**Steps:**

**Step 1: Write tests for Stream Manager**

Test that StartStream creates a stream, assigns an ID, returns it in PROVISIONING state. Test that StopStream transitions to STOPPED. Test idempotency — starting the same source URI twice returns the existing stream.

**Step 2: Implement Stream Manager service**

The service:
- Implements `StreamManagerService` from the generated ConnectRPC code
- Stores stream state in MongoDB
- On StartStream: validates config, creates stream record, launches encoder worker (initially as a goroutine for local dev, later as a K8s pod)
- On StopStream: signals encoder to stop, transitions state
- Publishes lifecycle events to Kafka

**Step 3: Create main entry point**

Standard ConnectRPC server setup with `http.NewServeMux()`.

**Step 4: Integration test with testcontainers**

Test StartStream → verify stream appears in MongoDB with correct state → StopStream → verify state transition.

**Step 5: Commit**

```bash
git commit -m "feat: add Stream Manager ConnectRPC service with start/stop lifecycle"
```

---

### Story 4.2: Live Source Component (RTMP)

**Files:**
- Create: `internal/source/rtmp.go`
- Create: `internal/source/rtmp_test.go`
- Create: `cmd/source/main.go`

**Steps:**

**Step 1: Research RTMP relay options**

Evaluate: nginx-rtmp-module in Docker vs lightweight Go RTMP server (e.g., `joy4` library) vs SRT. Pick the simplest option that works in Docker Compose.

If a Go library works well, prefer it (keeps the project in one language). Otherwise, add nginx-rtmp as a Docker Compose service.

**Step 2: Implement source component**

Uses FFmpeg to loop Big Buck Bunny and push to RTMP endpoint:
```bash
ffmpeg -re -stream_loop -1 -i /path/to/bbb.mp4 -c copy -f flv rtmp://localhost:1935/live/stream-1
```

The Go wrapper manages this FFmpeg process, supports starting multiple streams (one per RTMP stream key), and handles restart on crash.

**Step 3: Add RTMP relay to Docker Compose**

Add the chosen RTMP relay service to `deployments/docker/docker-compose.yml`.

**Step 4: Update encoder worker to accept RTMP URI**

The encoder already accepts a generic URI. Verify that pointing it at `rtmp://source:1935/live/stream-1` works.

**Step 5: End-to-end test**

Start source → encoder → packager → verify.html plays.

**Step 6: Commit**

```bash
git commit -m "feat: add live source component with RTMP relay and BBB loop"
```

---

### Story 4.3: Wire Stream Manager to Encoder and Source

**Steps:**

**Step 1: Update Stream Manager to coordinate startup**

On `StartStream`:
1. Create stream record (PROVISIONING)
2. Start source (point FFmpeg at RTMP for this stream)
3. Start encoder worker (give it the RTMP source URI + packager URL)
4. Wait for first heartbeat → transition to ACTIVE

On `StopStream`:
1. Signal encoder to stop
2. Signal source to stop
3. Transition to STOPPED

**Step 2: End-to-end integration test**

```bash
# Start infra
make docker-up
# Start services
go run ./cmd/packager &
go run ./cmd/stream-manager &
go run ./cmd/source &

# Start a stream via ConnectRPC
buf curl --data '{"source_uri":"bbb","encoding_profile":{"codec":"h264","width":1920,"height":1080,"bitrate_kbps":4000,"framerate":30,"segment_duration_s":6}}' \
  http://localhost:8080/streamline.v1.StreamManagerService/StartStream
```

This is the Phase 4 milestone: **ConnectRPC call starts a stream, video flows end-to-end**.

**Step 3: Commit**

```bash
git commit -m "feat: wire Stream Manager to encoder and source for full pipeline orchestration"
```

---

## Epic 5: Pipeline Controller

**Goal:** State machine, layered failover, health monitoring.

**Milestone:** Kill an encoder → controller detects and recovers.

---

### Story 5.1: Stream State Machine

**Files:**
- Create: `internal/controller/state.go`
- Create: `internal/controller/state_test.go`

**Steps:**

**Step 1: Write state machine tests**

Test valid transitions: provisioning → active, active → degraded, degraded → active (recovered), degraded → failed, active → stopping → stopped. Test invalid transitions are rejected.

**Step 2: Implement state machine**

Pure Go state machine with a transition table. No external dependencies. Returns error on invalid transitions. Emits state change events.

**Step 3: Run tests**

```bash
go test ./internal/controller/... -v -race
```

**Step 4: Commit**

```bash
git commit -m "feat: add stream state machine with valid transition enforcement"
```

---

### Story 5.2: Heartbeat Monitor

**Files:**
- Create: `internal/controller/heartbeat.go`
- Create: `internal/controller/heartbeat_test.go`

**Steps:**

**Step 1: Write heartbeat monitor tests**

Test that a worker is marked healthy when heartbeats arrive within the timeout. Test that a worker is marked dead after the timeout elapses. Test that recovery is detected when heartbeats resume.

**Step 2: Implement heartbeat monitor**

Tracks last heartbeat timestamp per worker. Background goroutine checks for timeouts. Calls a configurable callback when a worker is detected as dead.

**Step 3: Run tests, commit**

```bash
git commit -m "feat: add heartbeat monitor with configurable timeout and dead worker detection"
```

---

### Story 5.3: Pipeline Controller Service

**Files:**
- Create: `internal/controller/service.go`
- Create: `cmd/pipeline-controller/main.go`

**Steps:**

**Step 1: Implement controller service**

Consumes all Kafka topics (heartbeats, segments, state changes, errors). Routes events to the appropriate handler:
- Heartbeat → heartbeat monitor
- Segment events → update stream metadata in MongoDB
- State changes → state machine transitions

On dead worker detected:
1. Transition stream to DEGRADED
2. Call Stream Manager via ConnectRPC to provision replacement
3. On new worker heartbeat → transition back to ACTIVE

Persists stream state to MongoDB for crash recovery.

**Step 2: Main entry point**

**Step 3: Integration test — Layer 1 (FFmpeg crash)**

Start a stream. Kill the FFmpeg process (not the worker). Verify the worker's local supervisor restarts FFmpeg. Stream recovers without Pipeline Controller involvement.

**Step 4: Integration test — Layer 2 (Worker death)**

Start a stream. Kill the encoder worker process. Verify heartbeats stop. Verify Pipeline Controller detects timeout. Verify new worker is provisioned. Stream recovers.

This is the Phase 5 milestone.

**Step 5: Commit**

```bash
git commit -m "feat: add Pipeline Controller with layered failover and stream state management"
```

---

## Epic 6: API + Dashboard

**Goal:** Read API, unified UI with player and health views.

**Milestone:** Full end-to-end in the dashboard.

---

### Story 6.1: API Service

**Files:**
- Create: `internal/api/service.go`
- Create: `internal/api/service_test.go`
- Create: `cmd/api-server/main.go`

**Steps:**

Implement `StreamlineAPIService` from the generated ConnectRPC code. Queries MongoDB for stream state. Serves GetStream (by ID) and ListStreams (with pagination).

Test with unit tests (mock MongoDB) and integration tests (real MongoDB via testcontainers).

---

### Story 6.2: Dashboard — Vite + React Setup

**Files:**
- Create: `web/dashboard/` (Vite + React project)

**Steps:**

**Step 1: Scaffold Vite + React project**

```bash
cd web
npm create vite@latest dashboard -- --template react-ts
cd dashboard
npm install
npm install hls.js @connectrpc/connect @connectrpc/connect-web @bufbuild/protobuf
npm install -D tailwindcss @tailwindcss/vite
```

**Step 2: Configure TailwindCSS**

Follow Tailwind Vite plugin setup.

**Step 3: Generate TypeScript ConnectRPC client**

Add TypeScript generation to `buf.gen.yaml`:
```yaml
  - remote: buf.build/connectrpc/es
    out: web/dashboard/src/gen
    opt: target=ts
  - remote: buf.build/bufbuild/es
    out: web/dashboard/src/gen
    opt: target=ts
```

```bash
buf generate
```

**Step 4: Commit**

```bash
git commit -m "feat: scaffold Vite + React dashboard with ConnectRPC client generation"
```

---

### Story 6.3: Dashboard — Player View

**Files:**
- Create/modify: `web/dashboard/src/components/Player.tsx`
- Create/modify: `web/dashboard/src/components/StreamSelector.tsx`

**Steps:**

Implement hls.js player component. Stream selector dropdown populated from ListStreams API. Player connects to Packager's HLS endpoint for the selected stream.

---

### Story 6.4: Dashboard — Pipeline Health View

**Files:**
- Create/modify: `web/dashboard/src/components/HealthView.tsx`

**Steps:**

Display stream state, active workers, recent events. Poll API Service at interval for updates. Show state machine visualization (current state highlighted).

---

### Story 6.5: Dashboard Integration

Wire player and health views into a single-page layout. Add navigation or side-by-side view.

This is the Phase 6 milestone: **video playing alongside live health data in the dashboard**.

```bash
git commit -m "feat: add Dashboard with player view and pipeline health view"
```

---

## Epic 7: Observability & Infrastructure

**Goal:** Video-specific metrics, Grafana dashboards, distributed tracing, Helm chart, OpenTofu.

**Milestone:** Full observability across the pipeline, deployable to K8s.

---

### Story 7.1: OpenTelemetry Instrumentation

Add OTel tracing to all services. Propagate trace context through Kafka headers. Add video-specific Prometheus metrics (encoding latency, segment duration drift, bitrate variance, worker utilization, time-to-first-segment, failover recovery time).

### Story 7.2: Grafana Dashboards

Create pre-built dashboards:
- Pipeline overview (all streams, aggregate health)
- Per-stream detail (state, segment flow, encoder health)
- Kafka health (consumer lag, throughput)
- Encoder detail (FFmpeg resource usage, encoding speed)

Provision dashboards via Grafana provisioning in Docker Compose.

### Story 7.3: Helm Chart

Create Helm chart in `deployments/helm/streamline/` with templates for all services. Parameterize image tags, replica counts, resource limits.

### Story 7.4: OpenTofu Configuration

Create OpenTofu configs in `deployments/tofu/` for AWS:
- EKS cluster
- ECR repositories
- S3 for state
- Security groups and networking

---

## Epic 8: ABR + Polish

**Goal:** Multi-rendition encoding, k6 load testing, CI, documentation.

**Milestone:** Complete, polished project.

---

### Story 8.1: Multi-Rendition Encoding

Update encoder worker to support multiple FFmpeg processes per stream (one per rendition). Define a rendition ladder: 1080p/4Mbps, 720p/2.5Mbps, 480p/1Mbps. Packager generates a master playlist referencing per-rendition media playlists. hls.js handles adaptive switching automatically on the client.

### Story 8.2: k6 Load Testing

Create k6 test scripts for:
- Stream Manager API (start/stop streams under load)
- Packager HLS serving (concurrent player requests)
- Kafka throughput (via xk6-kafka)

### Story 8.3: GitHub Actions CI

Workflow for: build, lint (buf lint + golangci-lint), unit tests, integration tests (with testcontainers via Docker-in-Docker or service containers).

### Story 8.4: ADR Documentation

Write the 9 ADRs as individual markdown files in `docs/adrs/`. Each follows the spec format: context, decision, why, tradeoff, alternatives considered.

### Story 8.5: README and Documentation

Project README with: architecture diagram, quickstart (make docker-up, make run), tech stack, ADR index, demo instructions.

---

## Dependency Graph

```
Epic 1 (Foundation)
  └─▶ Epic 2 (Encoding Pipeline)
       └─▶ Epic 3 (Packaging + Playback)
            └─▶ Epic 4 (Stream Manager + Live Source)
                 └─▶ Epic 5 (Pipeline Controller)
                      └─▶ Epic 6 (API + Dashboard)
                           └─▶ Epic 7 (Observability & Infra)
                                └─▶ Epic 8 (ABR + Polish)
```

All epics are sequential — each builds on the previous.

---

## Notes for Implementer

1. **Replace `yourusername`** with Alex's actual GitHub username throughout.
2. **FFmpeg must be installed** on the dev machine. Install via `sudo apt install ffmpeg` or `brew install ffmpeg`.
3. **Go 1.22+** recommended for `slog` and latest module features.
4. **Docker Desktop or equivalent** must be running for Docker Compose and testcontainers.
5. **Commit often.** Each story should end with a commit. Each step within a story can have intermediate commits.
6. **Tests first.** Write the failing test before the implementation wherever possible. Skip TDD only for config files and infrastructure setup.
7. **The code in this plan is a starting point, not gospel.** The implementer should adapt based on what they discover during development. The structure and interfaces may evolve — that's expected.
