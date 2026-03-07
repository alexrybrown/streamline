# Epic 4: Stream Manager + Live Source + API

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 3 complete. Encoder -> Packager pipeline works end-to-end. HLS plays in browser.

**Goal:** ConnectRPC API for stream lifecycle (start/stop), MongoDB persistence, Big Buck Bunny as RTMP live source, packager stream lifecycle management, read-only API service.

**Architecture:** Stream Manager is the control plane entry point. It creates stream records in MongoDB, launches encoder workers (as local subprocesses for now), and coordinates the source -> encoder -> packager pipeline. The packager gains stream lifecycle awareness: state reset on stop, segment cleanup, dedup map clearing. The API Service provides a read-only query layer for stream state.

**Epic Milestone:** `buf curl` StartStream call starts a stream, video flows end-to-end. StopStream cleans up all resources. API Service serves stream queries.

---

## Story Dependency Graph

```
4.1 Stream Manager Service ─┐
                             ├─> 4.4 Wire Pipeline ─> 4.5 API Service
4.2 Packager Lifecycle ──────┤
                             │
4.3 Live Source (RTMP) ──────┘
```

**Stories 4.1, 4.2, and 4.3 can be developed in parallel worktrees.** They touch different packages (`internal/manager`, `internal/packager`, `internal/source`) with no shared changes. Story 4.4 integrates them. Story 4.5 (API Service) depends on 4.1 for the MongoDB store.

---

### Story 4.1: Stream Manager ConnectRPC Service

**Branch:** `story/4.1-stream-manager-service`
**Parallel:** yes (independent of 4.2, 4.3)

**Files:**
- Create: `internal/manager/service.go`
- Create: `internal/manager/service_test.go`
- Create: `internal/manager/store.go` (MongoDB stream store)
- Create: `internal/manager/store_test.go`
- Create: `cmd/stream-manager/main.go`

**Step 1: Write tests for the stream store**

Test MongoDB operations: CreateStream inserts a document and returns it with an ID and PROVISIONING state. GetStream retrieves by ID. UpdateState transitions state. Test that creating a stream with a duplicate source_uri returns the existing stream (idempotency).

Use testcontainers for a real MongoDB instance.

**Step 2: Implement the stream store**

Narrow interface for MongoDB operations:
```go
type streamStore interface {
    Create(ctx context.Context, stream *streamlinev1.Stream) error
    Get(ctx context.Context, streamID string) (*streamlinev1.Stream, error)
    UpdateState(ctx context.Context, streamID string, state streamlinev1.StreamState) error
    FindBySourceURI(ctx context.Context, sourceURI string) (*streamlinev1.Stream, error)
}
```

Concrete implementation wraps `*mongo.Collection`. Constructor accepts config struct with `Log`, `Database *mongo.Database`.

**Step 3: Write tests for the Stream Manager service**

Test StartStream: creates stream record, returns PROVISIONING state. Test StopStream: transitions to STOPPED. Test idempotency: starting the same source URI twice returns the existing stream. Test GetStream. Use a hand-written fake store for unit tests.

**Step 4: Implement Stream Manager service**

Implements `StreamManagerServiceHandler` from generated ConnectRPC code. On StartStream:
1. Check if stream already exists for this source_uri (idempotent)
2. Create stream record in MongoDB (PROVISIONING)
3. Return stream to caller

Worker launching comes in Story 4.4 after the source component is ready.

**Step 5: Create `cmd/stream-manager/main.go`**

Standard ConnectRPC server with h2c, gRPC reflection, health check. Environment variables: `SERVICE_PORT` (default 8080), `MONGO_URI`, `MONGO_DB_NAME`, `KAFKA_BROKERS`.

**Step 6: Run tests, commit**

```bash
go test ./internal/manager/... -v -race -timeout 60s
```

**Verify (before merge):**
- [ ] `go test ./internal/manager/... -race` passes
- [ ] `go build ./cmd/stream-manager` succeeds
- [ ] `go test ./... -race` passes (no regressions)
- [ ] Coverage >= 90% on `internal/manager`
- [ ] `goleak.VerifyNone` on all async tests

---

### Story 4.2: Packager Stream Lifecycle Management

**Branch:** `story/4.2-packager-lifecycle`
**Parallel:** yes (independent of 4.1, 4.3)

**Files:**
- Modify: `internal/packager/receiver.go`
- Modify: `internal/packager/manifest.go`
- Modify: `internal/packager/service.go`
- Create or modify: test files

**Step 1: Write tests for stream reset**

Test that after calling `ResetStream(streamID)`:
- The dedup map entries for that stream are cleared
- The manifest generator's segment window for that stream is cleared
- New segments with sequence numbers starting from 0 are accepted (not deduped)

Test that resetting one stream does not affect another stream.

**Step 2: Add ResetStream to Receiver**

```go
func (receiver *Receiver) ResetStream(streamID string) {
    receiver.dedupMu.Lock()
    for key := range receiver.dedup {
        if strings.HasPrefix(key, streamID+"/") {
            delete(receiver.dedup, key)
        }
    }
    receiver.dedupMu.Unlock()
}
```

**Step 3: Add ResetStream to ManifestGenerator**

```go
func (generator *ManifestGenerator) ResetStream(streamID string) {
    generator.mu.Lock()
    delete(generator.segments, streamID)
    generator.mu.Unlock()
}
```

**Step 4: Wire through Service**

Add `ResetStream(streamID string)` to the Service that delegates to both receiver and manifest generator. This will be called by the Stream Manager via a new `NotifyStreamStopped` RPC or through Kafka event consumption (decide during implementation based on what's simpler).

**Step 5: Run tests, commit**

**Verify (before merge):**
- [ ] `go test ./internal/packager/... -race` passes
- [ ] ResetStream clears dedup + manifest for target stream only
- [ ] Existing packager tests still pass (no regressions)
- [ ] Coverage on `internal/packager` stays >= 90%

---

### Story 4.3: Live Source Component (RTMP)

**Branch:** `story/4.3-rtmp-source`
**Parallel:** yes (independent of 4.1, 4.2)

**Files:**
- Create: `internal/source/rtmp.go`
- Create: `internal/source/rtmp_test.go`
- Modify: `deployments/docker/docker-compose.yml`

**Step 1: Add RTMP relay to Docker Compose**

Add an nginx-rtmp container to `deployments/docker/docker-compose.yml`:
```yaml
rtmp:
  image: tiangolo/nginx-rtmp:latest
  ports:
    - "1935:1935"
```

nginx-rtmp is the simplest option: zero config needed for basic relay, industry standard, battle-tested. A Go RTMP library (joy4) would keep it single-language but adds complexity for no showcase value — the interesting part is the pipeline, not the ingest server.

**Step 2: Write tests for the source component**

Test that `Source.Start(ctx)` spawns an FFmpeg process with the expected arguments (looping BBB, pushing to RTMP). Test that `Source.Stop()` cancels the process. Use a process factory stub (same pattern as encoder worker).

**Step 3: Implement Source**

```go
type SourceConfig struct {
    InputPath  string // Path to BBB file
    RTMPURL    string // e.g., rtmp://localhost:1935/live/stream-1
    Log        *zap.Logger
}
```

Wraps an FFmpeg subprocess:
```bash
ffmpeg -re -stream_loop -1 -i /path/to/bbb.mp4 -c copy -f flv rtmp://host:1935/live/stream-1
```

`-c copy` avoids re-encoding (just remuxes to FLV for RTMP). `-stream_loop -1` loops forever. `-re` reads at native framerate (simulates live).

**Step 4: Run tests, commit**

**Verify (before merge):**
- [ ] `go test ./internal/source/... -race` passes
- [ ] `docker compose up rtmp` starts nginx-rtmp on port 1935
- [ ] Manual smoke test: `ffmpeg -re -stream_loop -1 -i assets/bbb.mp4 -c copy -f flv rtmp://localhost:1935/live/test` succeeds
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 4.4: Wire Stream Manager to Pipeline

**Branch:** `story/4.4-wire-pipeline`
**Parallel:** no (depends on 4.1 + 4.2 + 4.3 merged to main)

**Files:**
- Modify: `internal/manager/service.go`
- Create: `internal/manager/pipeline.go` (subprocess orchestration)
- Create: `internal/manager/pipeline_test.go`

**Step 1: Write tests for pipeline orchestration**

Test that `StartPipeline(streamID, config)`:
1. Starts the source (FFmpeg -> RTMP)
2. Starts the encoder worker (RTMP -> segments -> packager)
3. Tracks running processes per stream ID

Test that `StopPipeline(streamID)`:
1. Cancels encoder and source contexts
2. Waits for processes to exit
3. Calls packager ResetStream (via Kafka event or direct RPC)

**Step 2: Implement pipeline orchestrator**

The orchestrator manages subprocess lifecycles per stream. For local development, encoder workers and sources run as goroutines within the stream-manager process (each with their own context for independent cancellation). In production, these would be K8s pods.

**Step 3: Update Stream Manager StartStream/StopStream**

On StartStream:
1. Create stream record (PROVISIONING)
2. Start pipeline (source + encoder)
3. Publish `StreamStateChanged` event to Kafka
4. Return stream

On StopStream:
1. Stop pipeline (cancel source + encoder)
2. Publish `StreamStateChanged` event
3. Update state to STOPPED in MongoDB
4. Return stream

**Step 4: Update encoder worker to accept RTMP URI**

Verify that the encoder's `FFmpegConfig.InputArgs` works with RTMP input: `[]string{"-re", "-i", "rtmp://localhost:1935/live/stream-1"}`. The encoder already accepts generic InputArgs, so this should work without code changes — just verify with an integration test.

**Step 5: End-to-end integration test**

```bash
make docker-up
go run ./cmd/packager &
go run ./cmd/stream-manager &

buf curl --data '{"source_uri":"bbb","encoding_profile":{"codec":"h264","width":1920,"height":1080,"bitrate_kbps":4000,"framerate":30,"segment_duration_s":6}}' \
  http://localhost:8080/streamline.v1.StreamManagerService/StartStream
```

**Verify (before merge):**
- [ ] `go test ./internal/manager/... -race` passes
- [ ] `buf curl StartStream` creates stream in MongoDB, starts encoder + source
- [ ] `curl http://localhost:8082/hls/stream-1/stream.m3u8` returns valid M3U8
- [ ] `web/verify.html` plays video
- [ ] `buf curl StopStream` stops encoder + source, resets packager state
- [ ] `go test ./... -race` passes (full suite, no regressions)

---

### Story 4.5: API Service (Read-Only Query Layer)

**Branch:** `story/4.5-api-service`
**Parallel:** no (depends on 4.1 for the MongoDB store)

**Files:**
- Create: `internal/api/service.go`
- Create: `internal/api/service_test.go`
- Create: `cmd/api-server/main.go`

**Step 1: Write tests**

`GetStream(streamID)` returns stream from MongoDB. `ListStreams(pageSize, pageToken)` returns paginated list. Use a hand-written fake store for unit tests, testcontainers for integration.

**Step 2: Implement**

Read-only service. Mutations go through Stream Manager. Defines its own narrow interface per Option D convention.

**Step 3: Main entry point**

ConnectRPC + h2c + gRPC reflection + CORS.

**Verify (before merge) — this is the Epic 4 milestone:**
- [ ] `go test ./internal/api/... -race` passes
- [ ] `go build ./cmd/api-server` succeeds
- [ ] `buf curl GetStream` and `ListStreams` work against running service
- [ ] Coverage >= 90% on `internal/api`
- [ ] `go test ./... -race` passes (full suite, no regressions)
