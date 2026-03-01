# Epic 8: Low-Latency Delivery

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 7 complete. Observability in place to measure latency improvements.

**Goal:** Reduce glass-to-glass latency so live playback stays smooth without buffering stalls at the live edge.

**Milestone:** Live stream plays continuously without visible stalls in hls.js.

---

### Story 8.1: Shorter Segment Duration

Reduce HLS segment duration from 6 seconds to 2 seconds. Update FFmpeg GOP size (`-g`) to match, adjust packager manifest `EXT-X-TARGETDURATION`, and increase the sliding window size to maintain the same total buffer duration (~60s). Verify that segment production, push, and manifest update complete well within the 2-second budget.

---

### Story 8.2: Segment Watcher to fsnotify

Replace filesystem polling in the segment watcher with `fsnotify` inotify events for near-instant segment detection. Fall back to polling on platforms where inotify is unavailable. This eliminates the 200ms worst-case detection delay.

---

### Story 8.3: Asynchronous Segment Pipeline

Decouple the segment push, manifest update, and Kafka publish into an asynchronous pipeline so that a slow Kafka broker or network hiccup does not block subsequent segment detection and delivery. The packager's `onSegmentWritten` callback should return immediately, with manifest and Kafka work happening in background goroutines.

---

### Story 8.4: LL-HLS Partial Segments (Stretch)

Investigate and implement Low-Latency HLS (LL-HLS) with `EXT-X-PART` and `EXT-X-PRELOAD-HINT` tags. FFmpeg supports partial segment output via `-hls_fmp4_init_filename` and CMAF. This enables sub-segment latency where the player can start downloading a segment before FFmpeg finishes writing it. Requires hls.js `lowLatency: true` configuration.

---

### Story 8.5: Latency Benchmarking

Add a latency measurement harness that timestamps each pipeline stage (FFmpeg write → watcher detect → push start → push complete → manifest update → player fetch). Expose metrics via Prometheus and add a Grafana dashboard panel showing end-to-end segment delivery latency.

---
