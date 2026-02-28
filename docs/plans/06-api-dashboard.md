# Epic 6: API + Dashboard

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 5 complete. Pipeline Controller manages stream lifecycle and failover.

**Goal:** Read API, unified UI with player and health views.

**Milestone:** Full end-to-end in the dashboard.

---

### Story 6.1: API Service

**Files:**
- Create: `internal/api/service.go`
- Create: `internal/api/service_test.go`
- Create: `cmd/api-server/main.go`

Implement `StreamlineAPIService` from the generated ConnectRPC code. Queries MongoDB for stream state. Serves GetStream (by ID) and ListStreams (with pagination).

Test with unit tests (mock MongoDB) and integration tests (real MongoDB via testcontainers).

---

### Story 6.2: Dashboard — Vite + React Setup

**Files:**
- Create: `web/dashboard/` (Vite + React project)

**Step 1: Scaffold Vite + React project**

```bash
cd web
npm create vite@latest dashboard -- --template react-ts
cd dashboard
npm install
npm install hls.js @connectrpc/connect @connectrpc/connect-web @bufbuild/protobuf
npm install -D tailwindcss @tailwindcss/vite
```

**Step 2: Configure TailwindCSS**

Follow Tailwind Vite plugin setup.

**Step 3: Generate TypeScript ConnectRPC client**

Add TypeScript generation to `buf.gen.yaml`:
```yaml
  - remote: buf.build/connectrpc/es
    out: web/dashboard/src/gen
    opt: target=ts
  - remote: buf.build/bufbuild/es
    out: web/dashboard/src/gen
    opt: target=ts
```

```bash
buf generate
```

**Step 4: Commit**

```bash
git commit -m "feat: scaffold Vite + React dashboard with ConnectRPC client generation"
```

---

### Story 6.3: Dashboard — Player View

**Files:**
- Create/modify: `web/dashboard/src/components/Player.tsx`
- Create/modify: `web/dashboard/src/components/StreamSelector.tsx`

Implement hls.js player component. Stream selector dropdown populated from ListStreams API. Player connects to Packager's HLS endpoint for the selected stream.

---

### Story 6.4: Dashboard — Pipeline Health View

**Files:**
- Create/modify: `web/dashboard/src/components/HealthView.tsx`

Display stream state, active workers, recent events. Poll API Service at interval for updates. Show state machine visualization (current state highlighted).

---

### Story 6.5: Dashboard Integration

Wire player and health views into a single-page layout. Add navigation or side-by-side view.

This is the Epic 6 milestone: **video playing alongside live health data in the dashboard**.

```bash
git commit -m "feat: add Dashboard with player view and pipeline health view"
```
