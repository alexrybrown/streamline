# Epic 5: CMAF/fMP4 + Object Storage

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 4 complete. Stream Manager starts/stops streams. Pipeline runs end-to-end.

**Goal:** Migrate from MPEG-TS to CMAF/fMP4 segments (industry standard, required for LL-HLS). Introduce MinIO as S3-compatible object storage for segments and manifests, replacing local filesystem. Write ADRs for CMAF and object storage decisions.

**Architecture:** The encoder still writes segments to local disk via FFmpeg and pushes them to the packager via ConnectRPC. The packager now writes segments to MinIO (S3 API) instead of local disk. Manifests are also written to MinIO. The packager serves HLS by proxying reads from MinIO (or redirecting to presigned URLs). This mirrors production architectures where the packager is an "origin" that writes to object storage, and a CDN serves from it.

**Epic Milestone:** CMAF/fMP4 segments stored in MinIO, HLS plays from MinIO-backed origin, 2 ADRs documenting key decisions.

---

## Story Dependency Graph

```
5.1 MinIO Docker Compose ──> 5.2 S3 Storage Interface ──> 5.4 Packager Writes to MinIO ──> 5.5 E2E Verification
                                                              ^                                    |
5.3 FFmpeg CMAF Migration (encoder) ──────────────────────────┘                                    v
                                                                              5.6 ADR: CMAF ─┐
                                                                              5.7 ADR: Storage┘
```

**Stories 5.1+5.2 (infra/storage) and 5.3 (encoder CMAF) can be developed in parallel.** Story 5.4 integrates both. Story 5.5 is the e2e verification. Stories 5.6 and 5.7 (ADRs) can be written in parallel after 5.5.

---

### Story 5.1: MinIO in Docker Compose

**Branch:** `story/5.1-minio-docker`
**Parallel:** yes (independent of 5.3)

**Files:**
- Modify: `deployments/docker/docker-compose.yml`

**Step 1: Add MinIO service**

```yaml
minio:
  image: minio/minio:latest
  ports:
    - "9000:9000"   # S3 API
    - "9001:9001"   # Console
  environment:
    MINIO_ROOT_USER: minioadmin
    MINIO_ROOT_PASSWORD: minioadmin
  command: server /data --console-address ":9001"
  volumes:
    - minio-data:/data
  healthcheck:
    test: ["CMD", "mc", "ready", "local"]
    interval: 10s
    timeout: 5s
    retries: 5
```

Add `minio-data` to the volumes section.

**Step 2: Create default bucket on startup**

Add an `minio-init` service that depends on MinIO and runs `mc alias set local http://minio:9000 minioadmin minioadmin && mc mb --ignore-existing local/streamline-segments`.

**Verify (before merge):**
- [ ] `make docker-up` starts MinIO without errors
- [ ] MinIO console accessible at `http://localhost:9001` (minioadmin/minioadmin)
- [ ] `streamline-segments` bucket exists after init
- [ ] Existing services (Kafka, MongoDB, Prometheus, Grafana) unaffected

---

### Story 5.2: S3 Storage Interface

**Branch:** `story/5.2-s3-storage`
**Parallel:** yes (independent of 5.3; depends on 5.1 for integration test only)

**Files:**
- Create: `internal/storage/s3.go`
- Create: `internal/storage/s3_test.go`

**Step 1: Define the storage interface**

Define a narrow interface in the consuming package (per Option D convention):
```go
type segmentStore interface {
    PutObject(ctx context.Context, key string, data io.Reader, size int64) error
    GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}
```

**Step 2: Write tests for S3 storage**

Use MinIO's Go SDK (`github.com/minio/minio-go/v7`). Test against a real MinIO instance via testcontainers or the Docker Compose MinIO.

Table-driven tests:
- PutObject writes data, GetObject reads it back
- PutObject with same key overwrites (idempotent)
- GetObject for nonexistent key returns error

**Step 3: Implement S3 storage**

```go
type S3Config struct {
    Endpoint        string // "localhost:9000"
    AccessKeyID     string
    SecretAccessKey  string
    Bucket          string
    UseSSL          bool   // false for local MinIO
    Log             *zap.Logger
}
```

Wrap `minio.Client.PutObject` and `minio.Client.GetObject`. Key format: `{streamID}/{filename}`.

**Verify (before merge):**
- [ ] `go test ./internal/storage/... -race` passes
- [ ] PutObject + GetObject round-trip works against MinIO
- [ ] Coverage >= 90% on `internal/storage`
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 5.3: Migrate FFmpeg to CMAF/fMP4

**Branch:** `story/5.3-cmaf-encoder`
**Parallel:** yes (independent of 5.1, 5.2; touches only encoder package)

**Files:**
- Modify: `internal/encoder/ffmpeg.go`
- Modify: `internal/encoder/segment_watcher.go`
- Modify: `proto/streamline/v1/packager.proto` (add `is_init_segment` to SegmentMetadata)
- Modify: encoder test files

**Step 1: Update FFmpeg args for CMAF output**

Change `buildArgs()` in `ffmpeg.go`:
- Add `-hls_segment_type fmp4` — CMAF fragmented MP4 instead of MPEG-TS
- Add `-hls_fmp4_init_filename init.mp4` — initialization segment with codec config
- Change segment extension from `.ts` to `.m4s`

**Why CMAF matters:**
- CMAF uses ISO BMFF (MP4) containers, which are byte-range addressable — enabling partial segment delivery for LL-HLS
- Single container format works for both HLS and DASH (unified packaging)
- Better codec support: HEVC, AV1, and Dolby Vision are only supported in fMP4, not MPEG-TS
- Smaller overhead per segment compared to MPEG-TS (no 188-byte packet framing)

**Step 2: Update segment watcher to detect `.m4s` files**

Change the file pattern to `.m4s`. Also handle the `init.mp4` file — it must be pushed to the packager once (on first detection).

**Step 3: Update segment pusher for init segment**

Add `bool is_init_segment = 4;` to `SegmentMetadata` in `packager.proto`. Run `buf generate`. The packager can store init segments with a well-known key (`{streamID}/init.mp4`).

**Step 4: Update tests**

Update all encoder tests that reference `.ts` to use `.m4s`. Update segment watcher tests for the new file pattern.

**Verify (before merge):**
- [ ] `go test ./internal/encoder/... -race` passes
- [ ] FFmpeg produces `init.mp4` + `segment_*.m4s` files (manual smoke test with testsrc)
- [ ] Segment watcher detects `.m4s` files and `init.mp4`
- [ ] `buf lint` passes on updated proto
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 5.4: Packager Writes to MinIO

**Branch:** `story/5.4-packager-minio`
**Parallel:** no (depends on 5.2 + 5.3 merged to main)

**Files:**
- Modify: `internal/packager/receiver.go`
- Modify: `internal/packager/service.go`
- Modify: `internal/packager/manifest.go`
- Modify: `cmd/packager/main.go`
- Modify: packager test files

**Step 1: Write tests for MinIO-backed receiver**

Test that when PushSegment receives a segment:
1. The segment data is written to MinIO at key `{streamID}/segment_{seq}.m4s`
2. The init segment is written to `{streamID}/init.mp4`
3. Dedup still works (duplicate pushes don't re-upload)
4. The manifest is written to MinIO at `{streamID}/stream.m3u8`

Use a fake `segmentStore` (hand-written) for unit tests.

**Step 2: Update Receiver to write to object storage**

Replace `os.Create` / `os.Rename` with `store.PutObject`. S3 PUTs are atomic by nature, so the write becomes simpler (no temp file + rename needed).

The Receiver's constructor now takes a `segmentStore` dependency.

**Step 3: Update ManifestGenerator to write to MinIO**

Replace `os.WriteFile` + `os.Rename` with `store.PutObject`.

**Step 4: Update HLS serving**

Replace `http.FileServer` with a handler that proxies reads from MinIO:

```go
// GET /hls/{streamID}/stream.m3u8 -> store.GetObject("streamID/stream.m3u8")
// GET /hls/{streamID}/init.mp4    -> store.GetObject("streamID/init.mp4")
// GET /hls/{streamID}/segment_00003.m4s -> store.GetObject("streamID/segment_00003.m4s")
```

Set appropriate `Content-Type` and `Cache-Control` headers:
- Manifests: `no-cache, no-store` (must always be fresh for live)
- Init segments: `max-age=86400` (rarely change)
- Media segments: `max-age=3600` (immutable once written)

**Step 5: Update manifest for CMAF**

Add `#EXT-X-MAP:URI="init.mp4"` tag. Bump HLS version to 7 for `EXT-X-MAP` support.

**Verify (before merge):**
- [ ] `go test ./internal/packager/... -race` passes
- [ ] Segments written to MinIO (visible in MinIO console)
- [ ] HLS proxy handler serves content from MinIO with correct Content-Type
- [ ] Cache-Control headers set correctly per content type
- [ ] Coverage on `internal/packager` stays >= 90%

---

### Story 5.5: End-to-End CMAF + MinIO Verification

**Branch:** `story/5.5-cmaf-minio-e2e`
**Parallel:** no (depends on 5.4 merged to main)

**Files:**
- Modify: `web/verify.html` (if needed)

**Step 1: Start full pipeline**

```bash
make docker-up  # Kafka, MongoDB, MinIO, RTMP
go run ./cmd/stream-manager &
go run ./cmd/packager &

buf curl --data '{"source_uri":"bbb",...}' \
  http://localhost:8080/streamline.v1.StreamManagerService/StartStream
```

**Step 2: Verify segments in MinIO**

Navigate to MinIO console (`http://localhost:9001`), verify `streamline-segments` bucket has:
- `stream-1/init.mp4`
- `stream-1/segment_00001.m4s`, `segment_00002.m4s`, ...
- `stream-1/stream.m3u8`

**Step 3: Verify HLS playback**

Open `web/verify.html`. hls.js should fetch `stream.m3u8`, then `init.mp4`, then `.m4s` segments and play.

**Verify (before merge):**
- [ ] Segments visible in MinIO console
- [ ] `curl http://localhost:8082/hls/stream-1/stream.m3u8` returns valid CMAF M3U8 with `EXT-X-MAP`
- [ ] `web/verify.html` plays video end-to-end
- [ ] `go test ./... -race` passes (full suite)

---

### Story 5.6: ADR — CMAF/fMP4 over MPEG-TS

**Branch:** `story/5.6-adr-cmaf`
**Parallel:** yes (can parallel with 5.7)

**Files:**
- Create: `docs/adrs/001-cmaf-fmp4-over-mpeg-ts.md`

**Context:** The pipeline migrated from MPEG-TS to CMAF/fMP4 segments.

**Key points to cover:**
- CMAF (ISO/IEC 23000-19) enables unified HLS+DASH with a single container format
- Apple requires fMP4 for HEVC/4K content delivery (HLS Authoring Spec)
- LL-HLS partial segments (`EXT-X-PART`) require byte-range addressable containers (ISO BMFF), which MPEG-TS cannot provide
- CMAF future-proofs for AV1 — MPEG-TS cannot carry AV1, which already powers 30% of traffic at major platforms
- Smaller per-segment overhead compared to MPEG-TS (no 188-byte packet framing)
- Trade-off: MPEG-TS still dominates contribution/ingest networks and broadcast infrastructure
- Industry references: Apple HLS Authoring Spec, DASH-IF Live Media Ingest spec, codec adoption data across Netflix, YouTube, Meta

**Verify (before merge):**
- [ ] ADR follows format: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References
- [ ] References practices across multiple companies/standards bodies

---

### Story 5.7: ADR — Object Storage over Local Filesystem

**Branch:** `story/5.7-adr-object-storage`
**Parallel:** yes (can parallel with 5.6)

**Files:**
- Create: `docs/adrs/002-object-storage-over-local-filesystem.md`

**Context:** The packager writes segments to MinIO (S3-compatible) instead of local disk.

**Key points to cover:**
- Production architectures store segments in object storage (S3/GCS/Azure Blob) for durability and CDN integration
- S3 PUTs are atomic — eliminates the temp-file + rename pattern needed for filesystem writes
- Key format `{streamID}/{filename}` enables per-stream isolation and lifecycle management
- At scale, S3 alone becomes insufficient for live streaming's latency requirements — companies have evolved to custom origin servers with hot caches for recent segments and S3 for DVR/archive
- Path isolation between ingest writes and CDN reads prevents traffic surges from impacting publishing latency
- Edge packaging (Just-in-Time Packaging) as an alternative model where packaging happens at the CDN edge
- Trade-off: added network hop for every segment write/read vs direct filesystem I/O
- Industry references: Netflix Live Origin (Dec 2025), AWS Elemental MediaPackage, Unified Streaming, Disney+ BAMTech

**Verify (before merge) — this is the Epic 5 milestone:**
- [ ] ADR follows format: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References
- [ ] References practices across multiple companies
