# Epic 6: Multi-Rendition ABR

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 5 complete. CMAF/fMP4 segments stored in MinIO. Packager serves HLS from object storage.

**Goal:** Multi-rendition encoding with an ABR (Adaptive Bitrate) rendition ladder, master playlist generation, and adaptive quality switching in hls.js.

**Architecture:** The encoder worker spawns multiple FFmpeg processes per stream — one per rendition. Each rendition produces its own CMAF segments and pushes them independently to the packager. The packager maintains per-rendition media playlists and a master (multivariant) playlist that references all renditions. hls.js handles ABR switching automatically based on network conditions and buffer health.

**Epic Milestone:** hls.js switches between renditions based on network conditions. Master playlist references all rendition media playlists.

---

## Story Dependency Graph

```
6.1 Rendition Ladder ──> 6.2 Multi-Process Encoder ──> 6.3 Per-Rendition Playlists ──> 6.4 Master Playlist ──> 6.5 ABR Verification
```

**All stories are sequential.** Each builds directly on the previous — rendition definitions feed the encoder, encoder output feeds the packager, packager playlists feed the master, and verification exercises everything.

---

### Story 6.1: Rendition Ladder Definition

**Branch:** `story/6.1-rendition-ladder`
**Parallel:** no

**Files:**
- Modify: `proto/streamline/v1/stream.proto`
- Modify: `proto/streamline/v1/packager.proto`
- Create: `internal/encoder/rendition.go`
- Create: `internal/encoder/rendition_test.go`

**Step 1: Update protos for multi-rendition**

Add `Rendition` message and `repeated Rendition renditions` to `EncodingProfile`. Add `string rendition_name = 5;` to `SegmentMetadata`. Run `buf generate`.

**Step 2: Define the default rendition ladder**

```go
// DefaultRenditionLadder defines a 4-tier ABR ladder.
// Bitrates follow the rule-of-thumb: each step is ~40-60% of the next.
//
// The 360p rung ensures playback on constrained connections (mobile on 3G, congested WiFi).
// GOP size (keyframe interval) MUST be identical across all renditions
// so the player can switch at any segment boundary without glitches.
var DefaultRenditionLadder = []Rendition{
    {Name: "1080p", Width: 1920, Height: 1080, BitrateKbps: 4000, Codec: "libx264", Framerate: 30},
    {Name: "720p",  Width: 1280, Height: 720,  BitrateKbps: 2500, Codec: "libx264", Framerate: 30},
    {Name: "480p",  Width: 854,  Height: 480,  BitrateKbps: 1000, Codec: "libx264", Framerate: 30},
    {Name: "360p",  Width: 640,  Height: 360,  BitrateKbps: 600,  Codec: "libx264", Framerate: 30},
}
```

**Step 3: Write validation tests**

Table-driven: no duplicate names, all widths/heights positive, bitrates in descending order, GOP derived from `framerate * segment_duration_s`.

**Verify (before merge):**
- [ ] `buf lint` and `buf generate` pass
- [ ] `go test ./internal/encoder/... -race` passes
- [ ] Rendition validation catches invalid ladders (dups, zero values)
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 6.2: Multi-Process Encoder Worker

**Branch:** `story/6.2-multi-rendition-encoder`
**Parallel:** no (depends on 6.1)

**Files:**
- Modify: `internal/encoder/worker.go`
- Modify: `internal/encoder/ffmpeg.go`
- Modify: encoder test files

**Step 1: Write tests for multi-rendition encoding**

Test that when a worker is configured with 4 renditions:
- 4 FFmpeg processes are spawned (one per rendition)
- Each writes to `{outputDir}/{renditionName}/`
- Segments are pushed with rendition name in metadata
- Single rendition failure does not take down others

Use FFmpegFactory stub.

**Step 2: Update Worker.Run for multi-rendition**

Spawns N goroutines (one per rendition), each with its own FFmpeg supervisor, segment watcher, and pusher. Heartbeat remains one per worker (not per rendition).

**Step 3: Update FFmpeg args per rendition**

All renditions share the same GOP size (`-g framerate*segmentDuration`) for seamless ABR switching. Each differs in resolution and bitrate.

**Step 4: Backward compatibility**

If no renditions configured, fall back to single-rendition mode using existing `FFmpegConfig` fields. Existing tests pass unchanged.

**Verify (before merge):**
- [ ] `go test ./internal/encoder/... -race` passes
- [ ] Multi-rendition test confirms 3 processes created with correct configs
- [ ] Single-rendition backward compat test still passes
- [ ] `go test ./... -race` passes (no regressions)

---

### Story 6.3: Per-Rendition Media Playlists

**Branch:** `story/6.3-per-rendition-playlists`
**Parallel:** no (depends on 6.2)

**Files:**
- Modify: `internal/packager/manifest.go`
- Modify: `internal/packager/receiver.go`
- Modify: packager test files

**Step 1: Write tests for per-rendition playlists**

Test that segments arriving for different renditions produce separate playlists:
- `{streamID}/1080p/stream.m3u8` with 1080p segments
- `{streamID}/720p/stream.m3u8` with 720p segments
- Each has its own sliding window and sequence numbers.

**Step 2: Update Receiver for rendition-aware storage**

When `SegmentMetadata.rendition_name` is set, segment key becomes `{streamID}/{renditionName}/segment_{seq}.m4s`. Init segment: `{streamID}/{renditionName}/init.mp4`.

**Step 3: Update ManifestGenerator for per-rendition playlists**

Playlist key: `{streamID}/{renditionName}/stream.m3u8`. Internal segment map key: `streamID/renditionName`.

**Verify (before merge):**
- [ ] `go test ./internal/packager/... -race` passes
- [ ] Separate playlists generated per rendition
- [ ] Each playlist has correct `EXT-X-MAP` pointing to its own `init.mp4`
- [ ] Coverage >= 90% on `internal/packager`

---

### Story 6.4: Master (Multivariant) Playlist

**Branch:** `story/6.4-master-playlist`
**Parallel:** no (depends on 6.3)

**Files:**
- Create: `internal/packager/master_playlist.go`
- Create: `internal/packager/master_playlist_test.go`
- Modify: `internal/packager/service.go`

**Step 1: Write tests for master playlist generation**

Test that registering 4 renditions produces:
```
#EXTM3U
#EXT-X-VERSION:7

#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS="avc1.640028"
1080p/stream.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,CODECS="avc1.64001f"
720p/stream.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=854x480,CODECS="avc1.640015"
#EXT-X-STREAM-INF:BANDWIDTH=600000,RESOLUTION=640x360,CODECS="avc1.640015"
360p/stream.m3u8
480p/stream.m3u8
```

`BANDWIDTH` in bits/sec. `CODECS` strings encode H.264 profile/level — hls.js uses them to determine if the device can decode a rendition.

**Step 2: Implement MasterPlaylistGenerator**

Auto-discovers renditions: when the packager receives the first segment for a stream/rendition combo it hasn't seen, register the rendition and regenerate the master playlist. Written to MinIO at `{streamID}/master.m3u8`.

**Step 3: Update HLS serving**

Entry point changes from `{streamID}/stream.m3u8` to `{streamID}/master.m3u8`.

**Verify (before merge):**
- [ ] `go test ./internal/packager/... -race` passes
- [ ] Master playlist contains all 4 renditions with correct BANDWIDTH and CODECS
- [ ] `curl .../master.m3u8` returns valid multivariant playlist
- [ ] Media playlist URLs in master are relative and resolve correctly

---

### Story 6.5: hls.js ABR Verification

**Branch:** `story/6.5-abr-verification`
**Parallel:** no (depends on 6.4; this is the epic milestone)

**Files:**
- Modify: `web/verify.html`

**Step 1: Update verify.html for ABR**

Point hls.js at `master.m3u8`. Add quality level display and manual quality selector dropdown.

**Step 2: End-to-end test**

Start full pipeline with 4 renditions. Verify ABR switching by throttling network in browser DevTools.

**Verify (before merge) — this is the Epic 6 milestone:**
- [ ] Master playlist served with all 4 renditions
- [ ] hls.js loads master playlist and starts playback
- [ ] Quality indicator shows current rendition
- [ ] Network throttle in DevTools causes rendition switch-down
- [ ] Restoring network causes switch back up
- [ ] `go test ./... -race` passes (full suite)
