# Epic 8: CDN Simulation + Low-Latency

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 7 complete. Pipeline Controller handles failover. Multi-rendition ABR working.

**Goal:** Add nginx as a CDN caching proxy demonstrating edge delivery concepts. Reduce latency with shorter segments and LL-HLS partial segments.

**Architecture:** An nginx reverse proxy sits between the player and the packager origin. It caches segments (immutable, long TTL) but not manifests (must be fresh for live). LL-HLS adds `EXT-X-PART` tags for sub-segment latency with blocking playlist requests.

**Epic Milestone:** Live stream plays through nginx CDN cache with LL-HLS partial segments, sub-3-second glass-to-glass latency.

---

## Story Dependency Graph

```
8.1 nginx CDN Proxy ──────────────────────────────────────────> 8.5 Latency Measurement
                                                                  ^
8.2 Shorter Segments ──> 8.3 fsnotify Watcher ──> 8.4 LL-HLS ──┘
```

**Stories 8.1 (nginx) and 8.2 (segment duration) can be developed in parallel.** nginx is Docker Compose + config, segment duration is encoder/packager config changes. Stories 8.3 and 8.4 are sequential (fsnotify helps LL-HLS latency). Story 8.5 measures everything.

---

### Story 8.1: nginx CDN Caching Proxy

**Branch:** `story/8.1-nginx-cdn`
**Parallel:** yes (independent of 8.2)

**Files:**
- Modify: `deployments/docker/docker-compose.yml`
- Create: `deployments/docker/nginx.conf`
- Modify: `web/verify.html`

**Step 1: Add nginx to Docker Compose**

```yaml
nginx:
  image: nginx:1.27-alpine
  ports:
    - "8080:80"
  volumes:
    - ./nginx.conf:/etc/nginx/nginx.conf:ro
  extra_hosts:
    - "host.docker.internal:host-gateway"
```

**Step 2: Write nginx.conf with caching**

CDN cache zone with content-aware rules:
- Manifests (`.m3u8`): BYPASS cache (must be fresh for live)
- Init segments (`.mp4`): cache long (rarely change)
- Media segments (`.m4s`): cache medium (immutable once written)
- `X-Cache-Status` header for debugging (HIT/MISS/BYPASS)

**Why this matters (interview talking points):**
- Manifest freshness is THE critical CDN problem for live streaming
- `stale-while-revalidate` reduces origin load while keeping content fresh
- Origin shielding reduces origin load from N*POPs to 1
- `X-Cache-Status` is how operators debug cache behavior in production

**Step 3: Update verify.html to use CDN URL**

Change source from `localhost:8082` to `localhost:8080` (nginx).

**Verify (before merge):**
- [ ] `make docker-up` starts nginx
- [ ] First segment request: `X-Cache-Status: MISS`
- [ ] Second same request: `X-Cache-Status: HIT`
- [ ] Manifest request: `X-Cache-Status: BYPASS`
- [ ] Video plays through nginx in `verify.html`
- [ ] Existing services unaffected

---

### Story 8.2: Shorter Segment Duration

**Branch:** `story/8.2-shorter-segments`
**Parallel:** yes (independent of 8.1)

**Files:**
- Modify: encoder and packager config defaults
- Modify: relevant tests

**Step 1: Reduce segment duration from 6s to 2s**

- FFmpeg: `-hls_time 2`, `-g framerate*2` (GOP = 60 frames at 30fps)
- Packager: `EXT-X-TARGETDURATION:2`
- Manifest window: increase from 10 to 30 segments (~60s buffer)

**Why 2 seconds:**
- 6s = 6-18s latency. 2s = 2-6s latency.
- Shorter segments = more HTTP requests/sec (this is why CDN caching matters)
- Too short (sub-1s) = overhead from HTTP headers dominates
- GOP size matches segment duration: every segment starts with IDR frame

**Step 2: Update tests**

Fix any hardcoded 6s assumptions.

**Step 3: Verify production rate**

With 2s segments, verify push + manifest update completes within 2s budget.

**Verify (before merge):**
- [ ] `go test ./... -race` passes
- [ ] FFmpeg produces segments every ~2s
- [ ] No segment queue buildup (push keeps up with production)

---

### Story 8.3: Segment Watcher to fsnotify

**Branch:** `story/8.3-fsnotify`
**Parallel:** no (depends on 8.2)

**Files:**
- Modify: `internal/encoder/segment_watcher.go`
- Modify: `internal/encoder/segment_watcher_test.go`

**Step 1: Write tests for fsnotify watcher**

Test near-instant detection of `.m4s` files via inotify events. Test polling fallback.

**Step 2: Replace polling with fsnotify**

Use `github.com/fsnotify/fsnotify`. Watch for `Create` events matching `*.m4s`. Eliminates 200ms worst-case polling delay. Keep polling as fallback.

**Verify (before merge):**
- [ ] `go test ./internal/encoder/... -race` passes
- [ ] Segment detection < 10ms (vs 0-200ms polling)
- [ ] Polling fallback works when fsnotify unavailable
- [ ] `goleak.VerifyNone(t)` passes

---

### Story 8.4: LL-HLS Partial Segments

**Branch:** `story/8.4-ll-hls`
**Parallel:** no (depends on 8.3)

**Files:**
- Modify: `internal/encoder/ffmpeg.go`
- Modify: `internal/packager/manifest.go`
- Create: `internal/packager/partial.go`
- Create: `internal/packager/partial_test.go`

**Step 1: Implement partial segment detection**

Detect CMAF fragment boundaries (`moof`+`mdat` box pairs) in incoming segments. Each fragment is an `EXT-X-PART`.

**Step 2: Update manifest for LL-HLS**

Add `EXT-X-PART-INF`, `EXT-X-PART`, `INDEPENDENT=YES`, `EXT-X-PRELOAD-HINT` tags. Bump HLS version to 9.

**Step 3: Blocking playlist requests**

Implement `_HLS_msn` and `_HLS_part` query parameter handling. Server holds HTTP response until the requested part is available — eliminates playlist polling.

**Step 4: Configure hls.js for LL-HLS**

```javascript
const hls = new Hls({ lowLatencyMode: true });
```

**Verify (before merge):**
- [ ] `go test ./internal/packager/... -race` passes
- [ ] Media playlist contains `EXT-X-PART` tags
- [ ] Blocking playlist request waits, then returns when part is available
- [ ] hls.js plays with `lowLatencyMode: true`
- [ ] Coverage >= 90% on partial segment code

---

### Story 8.5: Latency Measurement + Analysis

**Branch:** `story/8.5-latency-metrics`
**Parallel:** no (depends on 8.4; this is the epic milestone)

**Files:**
- Create: `internal/metrics/latency.go`
- Create: `internal/metrics/latency_test.go`
- Create: `docs/analysis/latency-analysis.md`

**Step 1: Implement pipeline stage timestamps**

Timestamp each stage: FFmpeg write -> watcher detect -> push start -> push complete -> manifest update -> player fetch.

**Step 2: Expose as Prometheus metrics**

```go
segmentDeliveryLatency = prometheus.NewHistogramVec(...)
```

**Step 3: Measure and analyze**

Run the full pipeline (source -> encode -> package -> CDN -> player) and measure actual glass-to-glass latency. Document findings in `docs/analysis/latency-analysis.md`:
- Where does the most time go? (encoding? push? manifest write? CDN fetch?)
- How does segment duration affect latency? (compare 2s vs 6s)
- Does LL-HLS (partial segments) measurably reduce latency vs regular HLS?
- What is the CDN cache impact on segment delivery time?

This analysis feeds into the scaling design document in Epic 9.

**Verify (before merge) — this is the Epic 8 milestone:**
- [ ] `go test ./... -race` passes
- [ ] Latency metrics exposed at `/metrics`
- [ ] End-to-end through nginx CDN: video plays with < 3s latency
- [ ] Cache HITs visible for segments, BYPASS for manifests
- [ ] LL-HLS parts visible in media playlist
- [ ] Latency analysis document written with findings
