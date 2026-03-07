# Epic 9: Performance Analysis + Architecture Documentation

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 8 complete. Full pipeline running with CDN cache and low-latency delivery. ADRs 1-4 already written after Epics 5 and 7.

**Goal:** Load characterization and profiling, Prometheus metrics for video-domain observability, remaining ADRs, a scaling design document covering production architecture patterns, and a README that ties everything together.

**Architecture:** All Go services expose `/debug/pprof` endpoints and Prometheus metrics. A load test harness starts N concurrent streams to characterize system behavior under pressure. The scaling design document analyzes how each component would evolve at production scale, referencing industry patterns across multiple companies.

**Epic Milestone:** Complete project with code, performance analysis, and architecture documentation.

---

## Story Dependency Graph

```
9.1 pprof + Load Characterization ──┐
                                     ├──> 9.4 Scaling Design Doc ──> 9.5 README
9.2 Prometheus Metrics ──────────────┤
                                     │
9.3 Remaining ADRs (5 ADRs) ────────┘
```

**Stories 9.1, 9.2, and 9.3 can be developed in parallel.** They touch different concerns (profiling, metrics, documentation). Story 9.4 synthesizes findings from all three. Story 9.5 comes last.

---

### Story 9.1: pprof Endpoints + Load Characterization

**Branch:** `story/9.1-pprof-load-test`
**Parallel:** yes (independent of 9.2, 9.3)

**Files:**
- Modify: all service `main.go` files
- Create: `internal/encoder/benchmark_test.go`
- Create: `internal/packager/benchmark_test.go`
- Create: `test/loadtest/` (load test harness)

**Step 1: Add pprof endpoints**

Add `/debug/pprof/` on a separate port (6060) to all services. Standard `net/http/pprof` import.

**Step 2: Write benchmarks**

Benchmarks for the hot paths: segment watcher throughput, manifest generation, concurrent segment push, S3 PutObject round-trip.

**Step 3: Load characterization**

Write a test harness (or script) that starts N concurrent streams via the Stream Manager API and measures:
- CPU and memory usage per service at 1, 5, 10, 25 concurrent streams
- Segment delivery P99 latency at each concurrency level
- Where the system bottlenecks first (encoder CPU? packager memory? MinIO write throughput? nginx connections?)
- Document findings in `docs/analysis/load-characterization.md`

This is not about building a load testing framework. It is about demonstrating the ability to profile and analyze a system, identify bottlenecks, and reason about scaling implications.

**Verify (before merge):**
- [ ] `curl http://localhost:6060/debug/pprof/` returns pprof index
- [ ] `go test -bench=. ./internal/encoder/...` runs benchmarks
- [ ] `go test -bench=. ./internal/packager/...` runs benchmarks
- [ ] Load characterization document written with findings
- [ ] No regressions in `go test ./... -race`

---

### Story 9.2: Prometheus Metrics

**Branch:** `story/9.2-prometheus-metrics`
**Parallel:** yes (independent of 9.1, 9.3)

**Files:**
- Create: `internal/metrics/metrics.go`
- Modify: key internal packages (encoder, packager, controller)
- Modify: all service `main.go` files

**Step 1: Define video-domain metrics**

Focus on metrics that matter for a live streaming pipeline:
- `segment_encoding_duration_seconds` (histogram, per rendition)
- `segment_push_duration_seconds` (histogram)
- `manifest_update_duration_seconds` (histogram)
- `failover_recovery_duration_seconds` (histogram)
- `segments_produced_total` (counter, per stream/rendition)
- `active_streams_gauge` (gauge)
- `cdn_cache_hit_ratio` (gauge, from nginx access log or stub)

**Step 2: Instrument services**

Add metric recording at the relevant points in encoder, packager, and controller. Use `prometheus/client_golang`.

**Step 3: Wire Prometheus scrape**

Ensure existing Prometheus in Docker Compose scrapes the service `/metrics` endpoints.

**Verify (before merge):**
- [ ] `go test ./... -race` passes
- [ ] Prometheus metrics exposed at `/metrics` on each service
- [ ] Prometheus UI at `http://localhost:9090` shows metric data when pipeline is running
- [ ] Coverage not regressed

---

### Story 9.3: Remaining ADRs

**Branch:** `story/9.3-remaining-adrs`
**Parallel:** yes (independent of 9.1, 9.2)

**Files:**
- Create: `docs/adrs/005-connectrpc-over-rest-grpc.md`
- Create: `docs/adrs/006-protobuf-kafka-events.md`
- Create: `docs/adrs/007-static-vs-per-title-encoding.md`
- Create: `docs/adrs/008-ll-hls-over-alternatives.md`
- Create: `docs/adrs/009-go-for-streaming-infrastructure.md`

Each ADR follows: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References.

**ADR 5: ConnectRPC over REST/gRPC**
Why ConnectRPC for inter-service communication. HTTP/1.1 + HTTP/2 compatibility, browser-native clients, generated types, simpler than raw gRPC while maintaining protobuf contracts.

**ADR 6: Protobuf over JSON for Kafka events**
Type safety, backward-compatible schema evolution, consistency with ConnectRPC. Trade-off: loss of human-readable messages in Kafka tooling.

**ADR 7: Static rendition ladder vs per-title encoding**
What this project builds (static 4-tier ladder) vs what companies do at scale (per-title encoding with VMAF-driven optimization). Why a static ladder is appropriate for live streaming at this scope. How per-title encoding works, its compute cost, and when the trade-off makes sense.

**ADR 8: LL-HLS over WebRTC/HESP/MoQ**
Latency tier analysis: LL-HLS (2-5s), WebRTC (<1s), HESP (0.4-2s), MoQ (emerging). Why LL-HLS is the right choice for HTTP-based live delivery. Community LHLS deprecated. HESP proprietary/royalty-bearing. MoQ pre-production but worth watching. WHIP standardized for ingest (RFC 9725).

**ADR 9: Go for streaming infrastructure**
Why Go for this project. Who uses Go for streaming (Twitch at massive scale, Netflix for infra tooling, YouTube/Google for backend services). Trade-offs vs Java (Netflix's primary, mature ecosystem), C++ (YouTube video processing), Rust (Netflix CDN edge). Goroutine concurrency model for managing concurrent stream connections.

**Verify (before merge):**
- [ ] All 5 ADRs written with consistent format
- [ ] Each ADR cites industry practices across multiple companies
- [ ] No broken internal links

---

### Story 9.4: Scaling Design Document

**Branch:** `story/9.4-scaling-doc`
**Parallel:** no (depends on 9.1 for load characterization findings, references ADRs from 9.3)

**Files:**
- Create: `docs/design/scaling-to-production.md`

This is the centerpiece of the project's documentation. It demonstrates the ability to reason about production-scale systems based on concrete experience building the local pipeline.

**Outline:**

1. **What This Project Demonstrates**
   - Full ingest-to-playback pipeline with CMAF/fMP4, ABR, fault tolerance, CDN, LL-HLS
   - Where each component maps to production equivalents

2. **What Was Intentionally Simplified and Why**
   - Single-machine vs distributed deployment
   - In-process workers vs K8s pods
   - CPU libx264 vs GPU NVENC/QSV
   - Single nginx vs multi-CDN
   - Static rendition ladder vs per-title encoding

3. **Storage: From S3 to Custom Origin**
   - How the project uses MinIO (S3-compatible) for segment storage
   - Why S3 alone becomes insufficient for live at scale (latency budgets, read/write path isolation)
   - How companies evolved: custom origin servers, EVCache/Cassandra for hot segments, S3 for DVR/archive
   - Edge packaging / Just-in-Time Packaging as an alternative model

4. **Encoding: From CPU to GPU Fleets**
   - Current: single-process FFmpeg with libx264
   - Production: GPU encoding (NVENC, QSV), per-title encoding with VMAF, content-adaptive bitrate ladders
   - Codec trajectory: H.264 (universal baseline) -> AV1 (30% of major platform traffic, royalty-free) -> AV2 (announced, ~30% better than AV1)
   - Why CMAF/fMP4 container choice future-proofs for AV1 (MPEG-TS cannot carry AV1)

5. **Delivery: From Single Proxy to Multi-CDN**
   - Current: nginx caching proxy with content-aware cache rules
   - Production: multi-CDN (Akamai + CloudFront + Fastly), origin shielding, edge PoPs
   - Cache invalidation strategies for live content
   - Manifest freshness as the critical CDN problem for live streaming
   - stale-while-revalidate and its trade-offs

6. **Protocol Trajectory**
   - Ingest: RTMP (declining, ~65%) -> SRT (growing, ~25%) -> WHIP/WebRTC (RFC 9725, OBS 30 support)
   - Delivery: HLS/DASH -> LL-HLS (current standard) -> MoQ (pre-production, Cloudflare 330+ PoPs)
   - Why each generation exists and what problems it solves

7. **Kafka at Scale**
   - Current: single-broker, all events on a few topics
   - Production: topic-per-stream vs partitioned-by-stream-ID, consumer group coordination, exactly-once semantics
   - Partition strategy trade-offs for segment events vs heartbeats

8. **Fault Tolerance at Scale**
   - Current: layered failover (FFmpeg supervisor, heartbeat, replacement)
   - Production: multi-region redundancy, epoch locking, segment pipeline masking
   - How companies handle simultaneous failures across renditions
   - DVR window considerations during failover

9. **What I Would Build Next**
   - DRM integration (FairPlay, Widevine, PlayReady via CPIX)
   - Multi-region deployment with active-active encoding
   - Per-title encoding pipeline with VMAF-driven optimization
   - SRT ingest support alongside RTMP
   - Prometheus alerting rules for stream health SLOs

**Verify (before merge):**
- [ ] All 9 sections written
- [ ] Each section references industry practices from multiple companies
- [ ] Architecture diagrams (Mermaid) for local vs production topology
- [ ] Load characterization findings integrated into relevant sections
- [ ] No broken internal links

---

### Story 9.5: README

**Branch:** `story/9.5-readme`
**Parallel:** no (depends on 9.4; this is the final story)

**Files:**
- Create: `README.md`

**Contents:**
- Architecture diagram (Mermaid) showing the full pipeline
- Tech stack with brief rationale for each choice
- Quick start instructions (docker compose up, build, run)
- Demo walkthrough: start stream, verify playback, trigger failover, observe recovery
- Link to ADR index (`docs/adrs/`)
- Link to scaling design document (`docs/design/scaling-to-production.md`)
- Link to load characterization (`docs/analysis/load-characterization.md`)
- Project structure guide

**Verify (before merge) — this is the Epic 9 (and project) milestone:**
- [ ] README renders correctly on GitHub
- [ ] Quick start instructions work from scratch
- [ ] All links resolve
- [ ] `go test ./... -race` passes (final full suite)
