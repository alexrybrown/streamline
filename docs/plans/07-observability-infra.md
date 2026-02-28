# Epic 7: Observability & Infrastructure

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 6 complete. Full application running with dashboard.

**Goal:** Video-specific metrics, Grafana dashboards, distributed tracing, Helm chart, OpenTofu.

**Milestone:** Full observability across the pipeline, deployable to K8s.

---

### Story 7.1: OpenTelemetry Instrumentation

Add OTel tracing to all services. Propagate trace context through Kafka headers. Add video-specific Prometheus metrics:
- Encoding latency
- Segment duration drift
- Bitrate variance
- Worker utilization
- Time-to-first-segment
- Failover recovery time

---

### Story 7.2: Grafana Dashboards

Create pre-built dashboards:
- Pipeline overview (all streams, aggregate health)
- Per-stream detail (state, segment flow, encoder health)
- Kafka health (consumer lag, throughput)
- Encoder detail (FFmpeg resource usage, encoding speed)

Provision dashboards via Grafana provisioning in Docker Compose.

**Note from Epic 1:** Also add Grafana datasource auto-provisioning so Prometheus is pre-configured on startup (currently requires manual setup after `docker compose up`).

---

### Story 7.3: Helm Chart

Create Helm chart in `deployments/helm/streamline/` with templates for all services. Parameterize image tags, replica counts, resource limits.

---

### Story 7.4: OpenTofu Configuration

Create OpenTofu configs in `deployments/tofu/` for AWS:
- EKS cluster
- ECR repositories
- S3 for state
- Security groups and networking

**Note:** This story introduces real AWS credentials. Use Proton Pass CLI (`pass-cli`) per the secrets management decision in `00-overview.md`.
