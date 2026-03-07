# Epic 7: Pipeline Controller + Fault Tolerance + ADRs

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 6 complete. Multi-rendition ABR working. Master + media playlists served from MinIO.

**Goal:** Stream state machine, heartbeat-based health monitoring, layered failover with segment continuity on encoder restart. This is the differentiator — handling the hard part of live streaming fault tolerance.

**Architecture:** The Pipeline Controller is a Kafka consumer that watches heartbeats and segment events. It maintains a state machine per stream and triggers recovery actions when workers die. The critical innovation is **segment continuity**: when an encoder restarts, the replacement must produce segments that continue the sequence without gaps or overlaps, and the packager must handle the transition seamlessly.

**Epic Milestone:** Kill an encoder worker -> controller detects via heartbeat timeout -> provisions replacement -> stream recovers with continuous segment sequence. 2 ADRs document the failover design decisions.

---

## Story Dependency Graph

```
7.1 State Machine ──────────┐
                             ├──> 7.4 Controller Service ──> 7.5 Failover Integration Tests
7.2 Heartbeat Monitor ──────┤
                             │
7.3 Sequence Coordinator ───┘
```

**Stories 7.1, 7.2, and 7.3 can be developed in parallel.** They are pure logic with no shared files — state machine, heartbeat tracking, and sequence coordination are independent concerns. Story 7.4 wires them together. Story 7.5 is the integration milestone.

---

### Story 7.1: Stream State Machine

**Branch:** `story/7.1-state-machine`
**Parallel:** yes (independent of 7.2, 7.3)

**Files:**
- Create: `internal/controller/state.go`
- Create: `internal/controller/state_test.go`

**Step 1: Write state machine tests**

Table-driven tests for valid transitions:

| From | To | Trigger |
|------|----|---------|
| PROVISIONING | ACTIVE | First heartbeat received |
| ACTIVE | DEGRADED | Heartbeat timeout |
| DEGRADED | ACTIVE | Heartbeat resumed (recovery) |
| DEGRADED | FAILED | Max recovery attempts exceeded |
| ACTIVE | STOPPING | StopStream called |
| STOPPING | STOPPED | All workers confirmed stopped |

Test invalid transitions are rejected with an error (e.g., STOPPED -> ACTIVE).

**Step 2: Implement state machine**

Pure Go, no external dependencies. Transition table maps `(currentState, event) -> nextState`. Returns error on invalid transitions. Emits `StreamStateChanged` events.

**Verify (before merge):**
- [ ] `go test ./internal/controller/... -race` passes
- [ ] All valid transitions succeed
- [ ] All invalid transitions return error
- [ ] Coverage >= 90% on state machine
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 7.2: Heartbeat Monitor

**Branch:** `story/7.2-heartbeat-monitor`
**Parallel:** yes (independent of 7.1, 7.3)

**Files:**
- Create: `internal/controller/heartbeat.go`
- Create: `internal/controller/heartbeat_test.go`

**Step 1: Write heartbeat monitor tests**

- Worker healthy: heartbeats arrive within timeout -> no callback fired
- Worker dies: no heartbeat for `timeout` duration -> `onWorkerDead` fires
- Worker recovers: heartbeats resume after death -> `onWorkerRecovered` fires
- Multiple workers: each tracked independently
- Injectable clock for deterministic tests

**Step 2: Implement heartbeat monitor**

Background goroutine checks at `CheckInterval`. Uses injectable `time.Now` for testability. Callbacks for `OnWorkerDead` and `OnWorkerRecovered`.

**Verify (before merge):**
- [ ] `go test ./internal/controller/... -race` passes
- [ ] Dead worker detection works within expected timing
- [ ] Recovery detection works when heartbeats resume
- [ ] `goleak.VerifyNone(t)` on async tests
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 7.3: Segment Sequence Coordinator

**Branch:** `story/7.3-sequence-coordinator`
**Parallel:** yes (independent of 7.1, 7.2)

**Files:**
- Create: `internal/controller/sequence.go`
- Create: `internal/controller/sequence_test.go`

**Step 1: Write tests for sequence coordination**

When an encoder dies mid-stream, the last segment was sequence N. The replacement encoder starts FFmpeg at sequence 0. The coordinator translates 0 -> N+1.

Table-driven tests:
- Normal operation: sequences 1, 2, 3 -> no translation
- Encoder restart: old produced up to seq 5, new starts at 0 -> translated to 6
- Multiple restarts: each bumps the offset
- Per-rendition tracking: each rendition has its own counter

**Step 2: Implement SequenceCoordinator**

Tracks highest sequence number seen per `streamID/renditionName`. Detects sequence reset (worker sequence < high-water mark) and applies offset.

**Verify (before merge):**
- [ ] `go test ./internal/controller/... -race` passes
- [ ] Normal operation: no translation applied
- [ ] Restart detection: sequences continue from high-water mark + 1
- [ ] Per-rendition isolation: restart in one rendition doesn't affect others
- [ ] Coverage >= 90%

---

### Story 7.4: Pipeline Controller Service

**Branch:** `story/7.4-controller-service`
**Parallel:** no (depends on 7.1 + 7.2 + 7.3 merged to main)

**Files:**
- Create: `internal/controller/service.go`
- Create: `internal/controller/service_test.go`
- Create: `cmd/pipeline-controller/main.go`

**Step 1: Implement controller service**

Kafka consumer that routes events:
- `Heartbeat` -> heartbeat monitor
- `SegmentProduced` -> sequence coordinator (track high-water mark)
- `StreamStateChanged` -> state machine

On dead worker detected:
1. Transition stream to DEGRADED
2. Record high-water marks for all renditions
3. Call Stream Manager via ConnectRPC to provision replacement
4. On new worker heartbeat -> transition to ACTIVE

**Step 2: Create main entry point**

Standard service binary consuming from all relevant Kafka topics.

**Step 3: Persist state to MongoDB**

Store state machine state and sequence high-water marks. On controller restart, reload from MongoDB.

**Verify (before merge):**
- [ ] `go test ./internal/controller/... -race` passes
- [ ] `go build ./cmd/pipeline-controller` succeeds
- [ ] Event routing: heartbeats reach monitor, segments reach coordinator
- [ ] Dead worker -> DEGRADED -> replacement provisioned -> ACTIVE
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 7.5: Failover Integration Tests

**Branch:** `story/7.5-failover-tests`
**Parallel:** no (depends on 7.4; this is the epic milestone)

**Files:**
- Create: `internal/controller/integration_test.go` (or `test/integration/`)

**Step 1: Layer 1 test — FFmpeg crash recovery**

Start a stream. Kill the FFmpeg process (not the worker). Verify:
- Worker supervisor restarts FFmpeg
- Segments continue (no heartbeat interruption)
- Stream stays ACTIVE

**Step 2: Layer 2 test — Worker death recovery**

Start a stream. Kill the encoder worker. Verify:
- Heartbeats stop -> DEGRADED
- Controller provisions replacement
- New worker produces segments with continuous sequence numbers
- Stream returns to ACTIVE

**Step 3: Multi-rendition failover test**

Kill worker on 3-rendition stream. Verify all renditions resume with continuous sequences. Master playlist unchanged. hls.js continues without visible interruption.

**Verify (before merge) — this is the Epic 7 milestone:**
- [ ] Layer 1: FFmpeg crash -> restart -> no state change
- [ ] Layer 2: Worker death -> DEGRADED -> replacement -> ACTIVE
- [ ] Segment sequence is continuous across failover (no gaps in playlist)
- [ ] Multi-rendition: all renditions recover independently
- [ ] `go test ./... -race` passes (full suite)

---

### Story 7.6: ADR — Layered Failover Design

**Branch:** `story/7.6-adr-failover`
**Parallel:** yes (can parallel with 7.7)

**Files:**
- Create: `docs/adrs/003-layered-failover-design.md`

**Context:** The pipeline uses three layers of failover for encoder recovery.

**Key points to cover:**
- Layer 1: FFmpeg supervisor (local process restart) — handles transient FFmpeg crashes without pipeline disruption
- Layer 2: Heartbeat timeout — detects worker death when process-level supervision is insufficient
- Layer 3: Worker replacement — controller provisions a new encoder when heartbeats stop
- Trade-offs at each layer: restart latency vs detection confidence vs blast radius
- How this maps to production patterns: container restart policies (Layer 1), K8s liveness probes (Layer 2), orchestrator scaling (Layer 3)
- How live sports platforms handle failover during high-stakes events (redundant encoders, hot standby)
- Industry references: Twitch stream health monitoring, ESPN+ live sports redundancy, YouTube Live reliability

**Verify (before merge):**
- [ ] ADR follows format: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References
- [ ] References practices across multiple companies

---

### Story 7.7: ADR — Segment Continuity on Encoder Restart

**Branch:** `story/7.7-adr-segment-continuity`
**Parallel:** yes (can parallel with 7.6)

**Files:**
- Create: `docs/adrs/004-segment-continuity-on-restart.md`

**Context:** When an encoder restarts, the replacement must produce segments that continue the sequence without gaps or overlaps.

**Key points to cover:**
- Why sequence gaps cause player stalls (HLS spec requires consecutive media sequence numbers)
- The sequence coordinator: tracks high-water marks per stream/rendition, translates new-encoder sequences
- GOP alignment constraints: every segment must start with an IDR frame for seamless ABR switching
- Per-rendition isolation: a restart in one rendition does not affect others
- Production complexity: multi-region redundancy adds cross-region sequence coordination
- Epoch locking: how companies prevent duplicate segments from redundant pipelines
- Trade-off: sequence translation adds a coordinator dependency vs allowing gaps and relying on player resilience
- Industry references: Netflix epoch locking, redundant encoding pipelines, DVR window handling during failover

**Verify (before merge) — this is the Epic 7 milestone:**
- [ ] ADR follows format: Context, Decision, Why, Trade-offs, Alternatives Considered, Industry References
- [ ] References practices across multiple companies
