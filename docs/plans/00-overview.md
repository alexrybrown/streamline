# Streamline — Plan Overview

> **For Claude:** Read this file FIRST. Then read ONLY the epic file you need next. Do NOT load all epic files at once — they are chunked to preserve context window.
>
> **Coding conventions are in `CLAUDE.md` at repo root.** Follow them exactly.

**Goal:** Build a live streaming orchestration platform that ingests, transcodes, packages, and serves video — demonstrating deep domain expertise in live video infrastructure with Go, ConnectRPC, Kafka, and modern HLS delivery.

**Architecture:** Control plane (Stream Manager, Pipeline Controller, API Service) orchestrates a data plane (Source, Encoder Workers, Packager). Kafka carries pipeline telemetry. All inter-service communication uses ConnectRPC; encoder workers push segments to Packager via a `PushSegment` client-streaming RPC. Segments are stored in S3-compatible object storage (MinIO). An nginx caching proxy simulates CDN edge behavior in front of the origin. The system uses layered failover: local FFmpeg supervisor, heartbeat timeout, and K8s restart.

**Tech Stack:** Go, ConnectRPC + Protobuf (Buf CLI), Kafka (twmb/franz-go), MongoDB (official driver), FFmpeg + H.264, CMAF/fMP4, HLS (RFC 8216) + LL-HLS, MinIO (S3-compatible), nginx (CDN simulation), hls.js, Prometheus, Docker Compose

---

## Workflow: Story-by-Story with Human in the Loop

**Every story is developed in its own git worktree, merged independently.**

1. **Start a story:** Create a worktree branch (e.g., `story/4.1-stream-manager-service`)
2. **Develop:** TDD in the worktree — failing test, implement, pass, commit
3. **Verify:** Each story has explicit verification criteria. All must pass before merge.
4. **Review:** Human reviews the diff, runs verification, approves
5. **Merge:** Squash-merge (or fast-forward) to main, delete worktree
6. **Next story:** Repeat

**Parallelism:** Some stories within an epic can be developed in parallel worktrees (marked with `parallel: yes`). Most are sequential because they build on each other — the tradeoff is accepted for human-in-the-loop control.

**Epic milestones:** Each epic ends with an integration milestone that exercises everything built in that epic. This is the "demo checkpoint" where you verify the system works end-to-end at that stage.

---

## Progress Tracker

| Epic | File | Status | Milestone |
|------|------|--------|-----------|
| 1 | `01-foundation.md` | DONE | `buf generate` works, `docker compose up` starts infra |
| 2 | `02-encoding-pipeline.md` | DONE | Segments on disk, events in Kafka |
| 3 | `03-packaging-playback.md` | DONE | BBB plays in hls.js page |
| 4 | `04-stream-manager.md` | TODO | ConnectRPC call starts stream end-to-end |
| 5 | `05-cmaf-object-storage.md` | TODO | CMAF/fMP4 segments in MinIO + 2 ADRs |
| 6 | `06-abr-multi-rendition.md` | TODO | 4-tier ABR with master playlist, adaptive switching in hls.js |
| 7 | `07-pipeline-controller.md` | TODO | Kill encoder, controller recovers with segment continuity + 2 ADRs |
| 8 | `08-cdn-low-latency.md` | TODO | nginx CDN cache + LL-HLS partial segments + latency analysis |
| 9 | `09-perf-analysis-docs.md` | TODO | Load characterization, scaling design doc, 5 ADRs, README |

**Epics are sequential** — each builds on the previous.

```
Epic 1 (Foundation)  done
  Epic 2 (Encoding Pipeline)  done
    Epic 3 (Packaging + Playback)  done
      Epic 4 (Stream Manager + Live Source + API)  <-- NEXT
        Epic 5 (CMAF + Object Storage + 2 ADRs)
          Epic 6 (Multi-Rendition ABR)
            Epic 7 (Pipeline Controller + Failover + 2 ADRs)
              Epic 8 (CDN Simulation + Low-Latency)
                Epic 9 (Perf Analysis + Scaling Doc + 5 ADRs)
```

---

## Decisions & Conventions

### Coding Standards: CLAUDE.md
All Go coding conventions (config structs, logger scoping, code hygiene) are documented in `CLAUDE.md` at the repo root. That file is the single source of truth — do not duplicate here.

### Kafka: franz-go (not segmentio/kafka-go)
`twmb/franz-go` with `kfake` for in-memory test clusters. Constructors accept config structs (`ProducerConfig`, `ConsumerConfig`).

### Logging: Zap (not slog)
One root `*zap.Logger` in `main()`, injected via config structs. Constructors scope with `.Named("component").With(...)`. Methods add `zap.String("method", "name")` as a field. `zap.NewNop()` for tests. Nil logger defaults to `zap.NewNop()` in constructors. Already implemented in `internal/logging/logging.go`.

### Secrets: Proton Pass CLI
Use `pass-cli run --env-file .env.template -- <cmd>` when real secrets are needed. Go code reads `os.Getenv()` — zero code changes. Only needed when real credentials enter (e.g., AWS in production).

### Database Migrations: golang-migrate
`github.com/golang-migrate/migrate` with MongoDB driver. JSON migration files in `migrations/`. Pattern: `NNNNNN_description.up.json` / `NNNNNN_description.down.json`.

### Frontend: verify.html Only (No Dashboard)
The bare `web/verify.html` page from Epic 3 is the only frontend. It uses hls.js for HLS verification, quality level display, and ABR testing.

### Docker Compose Ports
MongoDB is mapped to **host port 27018** (not default 27017) to avoid conflicts with other local MongoDB instances. All services connecting to MongoDB on `localhost` must use port 27018. Kafka is on 9092, Prometheus on 9090, Grafana on 3001, MinIO API on 9000, MinIO Console on 9001, nginx on 8080.

### Inter-Service Communication: ConnectRPC Everywhere
All service-to-service communication uses ConnectRPC with protobuf, including data plane operations like segment transfer (encoder -> packager). Segment transfer uses a **client-streaming RPC** with 64KiB chunked messages (`oneof { SegmentMetadata metadata; bytes chunk; }`) so the server writes each chunk as it arrives — peak memory per concurrent upload is ~64KB.

### Segment Format: CMAF/fMP4 (not MPEG-TS)
Starting in Epic 5, segments use CMAF (Common Media Application Format) with fragmented MP4 containers instead of MPEG-TS. CMAF is required for LL-HLS (`EXT-X-PART`), enables unified HLS+DASH delivery, and is the current industry standard. FFmpeg flag: `-hls_segment_type fmp4`. Manifests include `#EXT-X-MAP` for the initialization segment.

### Object Storage: MinIO (S3-compatible)
Starting in Epic 5, packager writes segments and manifests to MinIO via the S3 API instead of local disk. This mirrors production architectures where segments go to object storage (S3/GCS) and are served via CDN. MinIO runs in Docker Compose for zero-cost local development.

### CDN Simulation: nginx Caching Proxy
Starting in Epic 8, an nginx reverse proxy with `proxy_cache` sits in front of the packager/MinIO origin. Demonstrates cache headers, stale-while-revalidate, origin shielding, and cache invalidation — the same concerns that apply to CloudFront/Fastly/Akamai in production.

### Kafka Event Serialization: Protobuf (not JSON)
Kafka events use proto-defined messages (`SegmentProduced`, `Heartbeat`, etc. from `events.proto`) marshalled with `proto.Marshal`. This ensures type safety, backward-compatible schema evolution, and consistency with ConnectRPC.

### GitHub Username
`alexrybrown` — already set in `go.mod` as `github.com/alexrybrown/streamline`.

### Interleaved ADRs
Architecture Decision Records are written immediately after the epic where the decision was implemented, while context is fresh. This ensures the portfolio always has both code and architectural thinking, regardless of how far implementation progresses. ADRs cite industry practices across multiple companies (not just one), making the project relevant for interviews at any streaming company.

ADR format: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References.

| # | ADR Topic | Written After |
|---|-----------|--------------|
| 1 | CMAF/fMP4 over MPEG-TS | Epic 5 |
| 2 | Object storage over local filesystem | Epic 5 |
| 3 | Layered failover design | Epic 7 |
| 4 | Segment continuity on encoder restart | Epic 7 |
| 5 | ConnectRPC over REST/gRPC | Epic 9 |
| 6 | Protobuf over JSON for Kafka events | Epic 9 |
| 7 | Static rendition ladder vs per-title encoding | Epic 9 |
| 8 | LL-HLS over WebRTC/HESP/MoQ | Epic 9 |
| 9 | Go for streaming infrastructure | Epic 9 |

### Scaling Design Document
Written in Epic 9 after the full pipeline is running. Covers what this project demonstrates, what changes at production scale, and specific trade-off analysis with industry references. See `09-perf-analysis-docs.md` for the full outline.

---

## Notes for Implementer

1. **FFmpeg must be installed** — `sudo apt install ffmpeg` or `brew install ffmpeg`
2. **Go 1.22+** recommended
3. **Docker must be running** for Docker Compose and testcontainers
4. **TDD everywhere** — write failing test -> implement -> verify pass -> commit
5. **Code in plan is a starting point, not gospel** — adapt per CLAUDE.md conventions
6. **Existing code is the source of truth** — prior epics may have adapted plan code during implementation. Do not assume plan code snippets match the actual codebase. Always read and understand the current code before integrating, and adapt new plan code to match existing patterns and APIs.
7. **Commit after every story** — intermediate commits within stories are fine too
8. **Packager stream lifecycle gap** — the packager has no stream lifecycle management: no state reset on stream stop, no segment cleanup, no handling of encoder reconnections with fresh sequence numbers. The in-memory segment dedup map and manifest generator accumulate state across encoder restarts, causing non-consecutive sequence numbers in HLS playlists. Must be addressed in Epic 4 (Stream Manager) when StopStream/StartStream are implemented.
