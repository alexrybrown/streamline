# Epic 4: Stream Manager + Live Source

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 3 complete. Encoder → Packager pipeline works end-to-end. HLS plays in browser.

**Goal:** ConnectRPC API for stream lifecycle, Big Buck Bunny as RTMP live source.

**Milestone:** ConnectRPC call starts a stream, video flows end-to-end.

---

### Story 4.1: Stream Manager ConnectRPC Service

**Files:**
- Create: `internal/manager/service.go`
- Create: `internal/manager/service_test.go`
- Create: `cmd/stream-manager/main.go`

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

This is the Epic 4 milestone: **ConnectRPC call starts a stream, video flows end-to-end**.

**Step 3: Commit**

```bash
git commit -m "feat: wire Stream Manager to encoder and source for full pipeline orchestration"
```
