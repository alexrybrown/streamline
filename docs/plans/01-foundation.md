# Epic 1: Foundation

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.

**Goal:** Repository structure, protobuf schemas, shared packages, Docker Compose infrastructure.

**Milestone:** `buf generate` produces Go code, `docker compose up` starts Kafka + MongoDB + Prometheus + Grafana, project builds cleanly.

---

## Completed Stories

### Story 1.1: Repository and Go Workspace — DONE
Commit: `f9d9727` — repo structure, Go module, Makefile, `.gitignore`

### Story 1.2: Buf CLI and Protobuf Schemas — DONE
Commit: `ffe9e70` — `buf.yaml`, `buf.gen.yaml`, all proto files, generated Go code

### Story 1.3: Shared Internal Packages — DONE
Commit: `6331f9e` — `internal/config`, `internal/health`, `internal/logging` with tests

### Story 1.4: Docker Compose Infrastructure — DONE
`deployments/docker/docker-compose.yml` and `deployments/docker/prometheus.yml`

**Deviations from original plan:**
- All images pinned to specific versions (confluent-local 8.1.1, mongo 8.2.3, prometheus v3.10.0, grafana 12.4.0)
- Kafka: dual-listener config (`PLAINTEXT` for inter-container on 29092, `PLAINTEXT_HOST` for host on 9092) — required for confluent-local 8.x
- MongoDB: host port remapped to **27018** to avoid conflict with existing local MongoDB
- Prometheus: added `extra_hosts: ["host.docker.internal:host-gateway"]` — required on Linux
- Prometheus: split into per-service `job_name` entries for better PromQL filtering
- Grafana: anonymous role set to `Viewer` instead of `Admin`

### Story 1.5: Download Big Buck Bunny Test Asset — DONE
`make download-bbb` downloads to `assets/bbb.mp4`. Verified: H.264, 1280x720, 24fps, 9:56 duration. `.gitignore` excludes it. No commit needed.
