# Epic 9: ABR + Polish

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 8 complete. Low-latency delivery working smoothly.

**Goal:** Multi-rendition encoding, k6 load testing, CI, documentation.

**Milestone:** Complete, polished project.

---

### Story 9.1: Multi-Rendition Encoding

Update encoder worker to support multiple FFmpeg processes per stream (one per rendition). Define a rendition ladder:
- 1080p / 4 Mbps
- 720p / 2.5 Mbps
- 480p / 1 Mbps

Packager generates a master playlist referencing per-rendition media playlists. hls.js handles adaptive switching automatically on the client.

---

### Story 9.2: k6 Load Testing

Create k6 test scripts for:
- Stream Manager API (start/stop streams under load)
- Packager HLS serving (concurrent player requests)
- Kafka throughput (via xk6-kafka)

---

### Story 9.3: GitHub Actions CI

Workflow for: build, lint (buf lint + golangci-lint), unit tests, integration tests (with testcontainers via Docker-in-Docker or service containers).

---

### Story 9.4: ADR Documentation

Write the 9 ADRs as individual markdown files in `docs/adrs/`. Each follows the spec format: context, decision, why, tradeoff, alternatives considered.

---

### Story 9.5: README and Documentation

Project README with: architecture diagram, quickstart (make docker-up, make run), tech stack, ADR index, demo instructions.
