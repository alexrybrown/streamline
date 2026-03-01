# Streamline — Plan Overview

> **For Claude:** Read this file FIRST. Then read ONLY the epic file you need next. Do NOT load all epic files at once — they are chunked to preserve context window.
>
> **Coding conventions are in `CLAUDE.md` at repo root.** Follow them exactly.

**Goal:** Build a live streaming orchestration platform that ingests, transcodes, packages, and serves video — demonstrating domain expertise in live video infrastructure with Go, ConnectRPC, Kafka, and Kubernetes.

**Architecture:** Control plane (Stream Manager, Pipeline Controller, API Service) orchestrates a data plane (Source, Encoder Workers, Packager). Kafka carries pipeline telemetry. All inter-service communication uses ConnectRPC; encoder workers push segments to Packager via a `PushSegment` RPC. The system uses layered failover: local FFmpeg supervisor, heartbeat timeout, and K8s restart.

**Tech Stack:** Go, ConnectRPC + Protobuf (Buf CLI), Kafka (twmb/franz-go), MongoDB (official driver), FFmpeg + H.264, HLS (RFC 8216), Vite + React + hls.js, OpenTelemetry, Prometheus, Grafana, Docker Compose, Helm, OpenTofu

---

## Progress Tracker

| Epic | File | Status | Milestone |
|------|------|--------|-----------|
| 1 | `01-foundation.md` | DONE | `buf generate` works, `docker compose up` starts infra |
| 2 | `02-encoding-pipeline.md` | DONE | Segments on disk, events in Kafka |
| 3 | `03-packaging-playback.md` | IN PROGRESS (3.1-3.3 done) | BBB plays in hls.js page |
| 4 | `04-stream-manager.md` | TODO | ConnectRPC call starts stream end-to-end |
| 5 | `05-pipeline-controller.md` | TODO | Kill encoder → controller recovers |
| 6 | `06-api-dashboard.md` | TODO | Full end-to-end in dashboard |
| 7 | `07-observability-infra.md` | TODO | Metrics, tracing, Helm, OpenTofu |
| 8 | `08-low-latency.md` | TODO | LL-HLS or shorter segments, smooth live playback |
| 9 | `09-abr-polish.md` | TODO | Multi-rendition, CI, docs |

**Epics are sequential** — each builds on the previous.

```
Epic 1 (Foundation)  ✓
  └─▶ Epic 2 (Encoding Pipeline)  ✓
       └─▶ Epic 3 (Packaging + Playback)  ← IN PROGRESS (Stories 3.1–3.3 done, 3.4–3.5 remain)
            └─▶ Epic 4 (Stream Manager + Live Source)
                 └─▶ Epic 5 (Pipeline Controller)
                      └─▶ Epic 6 (API + Dashboard)
                           └─▶ Epic 7 (Observability & Infra)
                                └─▶ Epic 8 (Low-Latency Delivery)
                                     └─▶ Epic 9 (ABR + Polish)
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
Use `pass-cli run --env-file .env.template -- <cmd>` when real secrets are needed. Go code reads `os.Getenv()` — zero code changes. Only needed when real credentials enter (e.g., AWS in Epic 7).

### Database Migrations: golang-migrate
`github.com/golang-migrate/migrate` with MongoDB driver. JSON migration files in `migrations/`. Pattern: `NNNNNN_description.up.json` / `NNNNNN_description.down.json`.

### Frontend Codegen: Connect-ES v2
Single `protoc-gen-es` plugin, `target=ts`. Plain TypeScript types (not classes). `create(Schema, {...})` pattern.

### Frontend Dev Server: Vite
The full dashboard (Epic 6) uses Vite + React + hls.js. The bare `web/verify.html` page from Epic 3 is a standalone file for quick HLS verification and does not use Vite.

### Docker Compose Ports
MongoDB is mapped to **host port 27018** (not default 27017) to avoid conflicts with other local MongoDB instances. All services connecting to MongoDB on `localhost` must use port 27018. Kafka is on 9092, Prometheus on 9090, Grafana on 3001.

### Inter-Service Communication: ConnectRPC Everywhere
All service-to-service communication uses ConnectRPC with protobuf, including data plane operations like segment transfer (encoder → packager). Segment transfer uses a **client-streaming RPC** with 64KiB chunked messages (`oneof { SegmentMetadata metadata; bytes chunk; }`) so the server writes each chunk to disk as it arrives — peak memory per concurrent upload is ~64KB, comparable to plain HTTP `io.Copy` streaming (~32KB). Alternatives considered: (1) plain HTTP PUT — simpler but inconsistent protocol, no codegen; (2) unary RPC with `bytes` field — buffers entire 2-6MB segment in memory, unacceptable at scale with concurrent streams and ABR renditions. The client-streaming approach preserves ConnectRPC's benefits (type-safe codegen, unified protocol, consistent error handling) with no meaningful memory or performance penalty.

### Kafka Event Serialization: Protobuf (not JSON)
Kafka events use proto-defined messages (`SegmentProduced`, `Heartbeat`, etc. from `events.proto`) marshalled with `proto.Marshal`. The encoder worker's original JSON serialization was replaced in Epic 3 to align with the protobuf-everywhere approach. This ensures type safety, backward-compatible schema evolution, and consistency with ConnectRPC.

### GitHub Username
`alexrybrown` — already set in `go.mod` as `github.com/alexrybrown/streamline`.

---

## Notes for Implementer

1. **FFmpeg must be installed** — `sudo apt install ffmpeg` or `brew install ffmpeg`
2. **Go 1.22+** recommended
3. **Docker must be running** for Docker Compose and testcontainers
4. **TDD everywhere** — write failing test → implement → verify pass → commit
5. **Code in plan is a starting point, not gospel** — adapt per CLAUDE.md conventions
6. **Existing code is the source of truth** — prior epics may have adapted plan code during implementation. Do not assume plan code snippets match the actual codebase. Always read and understand the current code before integrating, and adapt new plan code to match existing patterns and APIs.
7. **Commit after every story** — intermediate commits within stories are fine too
8. **Packager stream lifecycle gap** — the packager has no stream lifecycle management: no state reset on stream stop, no segment cleanup, no handling of encoder reconnections with fresh sequence numbers. The in-memory segment dedup map and manifest generator accumulate state across encoder restarts, causing non-consecutive sequence numbers in HLS playlists. Must be addressed in Epic 4 (Stream Manager) when StopStream/StartStream are implemented.
