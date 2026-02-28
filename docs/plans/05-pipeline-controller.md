# Epic 5: Pipeline Controller

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 4 complete. Stream Manager can start/stop streams via ConnectRPC.

**Goal:** State machine, layered failover, health monitoring.

**Milestone:** Kill an encoder → controller detects and recovers.

---

### Story 5.1: Stream State Machine

**Files:**
- Create: `internal/controller/state.go`
- Create: `internal/controller/state_test.go`

**Step 1: Write state machine tests**

Test valid transitions: provisioning → active, active → degraded, degraded → active (recovered), degraded → failed, active → stopping → stopped. Test invalid transitions are rejected.

**Step 2: Implement state machine**

Pure Go state machine with a transition table. No external dependencies. Returns error on invalid transitions. Emits state change events.

**Step 3: Run tests**

```bash
go test ./internal/controller/... -v -race
```

**Step 4: Commit**

```bash
git commit -m "feat: add stream state machine with valid transition enforcement"
```

---

### Story 5.2: Heartbeat Monitor

**Files:**
- Create: `internal/controller/heartbeat.go`
- Create: `internal/controller/heartbeat_test.go`

**Step 1: Write heartbeat monitor tests**

Test that a worker is marked healthy when heartbeats arrive within the timeout. Test that a worker is marked dead after the timeout elapses. Test that recovery is detected when heartbeats resume.

**Step 2: Implement heartbeat monitor**

Tracks last heartbeat timestamp per worker. Background goroutine checks for timeouts. Calls a configurable callback when a worker is detected as dead.

**Step 3: Run tests, commit**

```bash
git commit -m "feat: add heartbeat monitor with configurable timeout and dead worker detection"
```

---

### Story 5.3: Pipeline Controller Service

**Files:**
- Create: `internal/controller/service.go`
- Create: `cmd/pipeline-controller/main.go`

**Step 1: Implement controller service**

Consumes all Kafka topics (heartbeats, segments, state changes, errors). Routes events to the appropriate handler:
- Heartbeat → heartbeat monitor
- Segment events → update stream metadata in MongoDB
- State changes → state machine transitions

On dead worker detected:
1. Transition stream to DEGRADED
2. Call Stream Manager via ConnectRPC to provision replacement
3. On new worker heartbeat → transition back to ACTIVE

Persists stream state to MongoDB for crash recovery.

**Step 2: Main entry point**

**Step 3: Integration test — Layer 1 (FFmpeg crash)**

Start a stream. Kill the FFmpeg process (not the worker). Verify the worker's local supervisor restarts FFmpeg. Stream recovers without Pipeline Controller involvement.

**Step 4: Integration test — Layer 2 (Worker death)**

Start a stream. Kill the encoder worker process. Verify heartbeats stop. Verify Pipeline Controller detects timeout. Verify new worker is provisioned. Stream recovers.

This is the Epic 5 milestone.

**Step 5: Commit**

```bash
git commit -m "feat: add Pipeline Controller with layered failover and stream state management"
```
