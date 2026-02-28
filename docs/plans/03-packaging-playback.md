# Epic 3: Packaging + Playback

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
>
> **Context:** Read `00-overview.md` first for project-wide decisions and conventions.
>
> **Prerequisites:** Epic 2 complete. Encoder worker produces segments and Kafka events.

**Goal:** Packager accepts segment pushes, generates HLS manifests, serves content. Video plays in a browser.

**Milestone:** BBB plays in a bare hls.js page.

---

### Story 3.1: Segment Receiver (HTTP)

The Packager's HTTP endpoint that accepts segment pushes from encoder workers.

**Files:**
- Create: `internal/packager/receiver.go`
- Create: `internal/packager/receiver_test.go`

**Step 1: Write test for segment receiver**

Test duplicate handling (idempotent by stream ID + sequence number) and gap detection.

```go
// internal/packager/receiver_test.go
package packager_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrybrown/streamline/internal/packager"
)

func TestReceiver_AcceptsSegment(t *testing.T) {
	dir := t.TempDir()
	r := packager.NewReceiver(dir)
	handler := r.Handler()

	body := bytes.NewReader([]byte("fake segment data"))
	req := httptest.NewRequest(http.MethodPut, "/segments/stream-1/3", body)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify file was written
	segPath := filepath.Join(dir, "stream-1", "segment_00003.ts")
	if _, err := os.Stat(segPath); err != nil {
		t.Errorf("segment file not found: %v", err)
	}
}

func TestReceiver_DeduplicatesSegment(t *testing.T) {
	dir := t.TempDir()
	r := packager.NewReceiver(dir)
	handler := r.Handler()

	// Push same segment twice
	for i := 0; i < 2; i++ {
		body := bytes.NewReader([]byte("fake segment data"))
		req := httptest.NewRequest(http.MethodPut, "/segments/stream-1/3", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	// Should still only have one file (idempotent)
	segPath := filepath.Join(dir, "stream-1", "segment_00003.ts")
	info, err := os.Stat(segPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len("fake segment data")) {
		t.Errorf("unexpected file size: %d", info.Size())
	}
}
```

**Step 2: Implement receiver**

```go
// internal/packager/receiver.go
package packager

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Receiver handles incoming segment pushes via HTTP.
type Receiver struct {
	outputDir string
	mu        sync.Mutex
	received  map[string]bool // "streamID/seq" → true
}

// NewReceiver creates a segment receiver writing to the given directory.
func NewReceiver(outputDir string) *Receiver {
	return &Receiver{
		outputDir: outputDir,
		received:  make(map[string]bool),
	}
}

// Handler returns an HTTP handler for segment pushes.
// Route: PUT /segments/{streamID}/{sequenceNumber}
func (r *Receiver) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse path: /segments/{streamID}/{seq}
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "segments" {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		streamID := parts[1]
		seq, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			http.Error(w, "invalid sequence number", http.StatusBadRequest)
			return
		}

		// Dedup check
		key := fmt.Sprintf("%s/%d", streamID, seq)
		r.mu.Lock()
		if r.received[key] {
			r.mu.Unlock()
			w.WriteHeader(http.StatusOK) // idempotent — no-op
			return
		}
		r.received[key] = true
		r.mu.Unlock()

		// Write segment to disk
		streamDir := filepath.Join(r.outputDir, streamID)
		os.MkdirAll(streamDir, 0755)

		filename := fmt.Sprintf("segment_%05d.ts", seq)
		segPath := filepath.Join(streamDir, filename)

		f, err := os.Create(segPath)
		if err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		defer f.Close()

		if _, err := io.Copy(f, req.Body); err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
	})
}
```

**Step 3: Run tests**

```bash
go test ./internal/packager/... -v -race
```

**Step 4: Commit**

```bash
git add internal/packager/
git commit -m "feat: add segment receiver with deduplication"
```

---

### Story 3.2: HLS Manifest Generator

Generates and updates HLS playlists as segments arrive.

**Files:**
- Create: `internal/packager/manifest.go`
- Create: `internal/packager/manifest_test.go`

**Step 1: Write test for manifest generation**

```go
// internal/packager/manifest_test.go
package packager_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexrybrown/streamline/internal/packager"
)

func TestManifest_GeneratesValidHLS(t *testing.T) {
	dir := t.TempDir()

	m := packager.NewManifestGenerator(dir, packager.ManifestConfig{
		TargetDuration: 6,
		WindowSize:     10,
	})

	m.AddSegment("stream-1", packager.SegmentInfo{
		Filename:       "segment_00001.ts",
		SequenceNumber: 1,
		DurationS:      6.0,
	})

	m.AddSegment("stream-1", packager.SegmentInfo{
		Filename:       "segment_00002.ts",
		SequenceNumber: 2,
		DurationS:      6.0,
	})

	playlistPath := filepath.Join(dir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "#EXTM3U") {
		t.Error("missing #EXTM3U header")
	}
	if !strings.Contains(content, "#EXT-X-TARGETDURATION:6") {
		t.Error("missing target duration")
	}
	if !strings.Contains(content, "segment_00001.ts") {
		t.Error("missing segment 1")
	}
	if !strings.Contains(content, "segment_00002.ts") {
		t.Error("missing segment 2")
	}
}

func TestManifest_SlidingWindow(t *testing.T) {
	dir := t.TempDir()

	m := packager.NewManifestGenerator(dir, packager.ManifestConfig{
		TargetDuration: 2,
		WindowSize:     3, // Only keep 3 segments in playlist
	})

	for i := int64(1); i <= 5; i++ {
		m.AddSegment("stream-1", packager.SegmentInfo{
			Filename:       fmt.Sprintf("segment_%05d.ts", i),
			SequenceNumber: i,
			DurationS:      2.0,
		})
	}

	playlistPath := filepath.Join(dir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	// Should NOT contain segments 1-2 (slid out of window)
	if strings.Contains(content, "segment_00001.ts") {
		t.Error("segment 1 should have been removed from window")
	}
	// Should contain segments 3-5
	if !strings.Contains(content, "segment_00003.ts") {
		t.Error("segment 3 should be in window")
	}
	if !strings.Contains(content, "segment_00005.ts") {
		t.Error("segment 5 should be in window")
	}
}
```

**Step 2: Implement manifest generator**

Implement with atomic writes (write to temp file, rename).

```go
// internal/packager/manifest.go
package packager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ManifestConfig controls playlist generation.
type ManifestConfig struct {
	TargetDuration int // EXT-X-TARGETDURATION in seconds
	WindowSize     int // Max segments in the sliding window
}

// SegmentInfo describes a segment for the playlist.
type SegmentInfo struct {
	Filename       string
	SequenceNumber int64
	DurationS      float64
}

// ManifestGenerator creates and updates HLS playlists.
type ManifestGenerator struct {
	outputDir string
	cfg       ManifestConfig
	mu        sync.Mutex
	segments  map[string][]SegmentInfo // streamID → segments
}

// NewManifestGenerator creates a new manifest generator.
func NewManifestGenerator(outputDir string, cfg ManifestConfig) *ManifestGenerator {
	return &ManifestGenerator{
		outputDir: outputDir,
		cfg:       cfg,
		segments:  make(map[string][]SegmentInfo),
	}
}

// AddSegment adds a segment to the playlist and regenerates the manifest.
func (m *ManifestGenerator) AddSegment(streamID string, seg SegmentInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.segments[streamID] = append(m.segments[streamID], seg)

	// Sliding window
	if len(m.segments[streamID]) > m.cfg.WindowSize {
		m.segments[streamID] = m.segments[streamID][len(m.segments[streamID])-m.cfg.WindowSize:]
	}

	return m.writePlaylist(streamID)
}

func (m *ManifestGenerator) writePlaylist(streamID string) error {
	segs := m.segments[streamID]
	if len(segs) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString(fmt.Sprintf("#EXT-X-VERSION:3\n"))
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", m.cfg.TargetDuration))
	b.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", segs[0].SequenceNumber))
	b.WriteString("\n")

	for _, seg := range segs {
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.DurationS))
		b.WriteString(seg.Filename + "\n")
	}

	streamDir := filepath.Join(m.outputDir, streamID)
	os.MkdirAll(streamDir, 0755)

	// Atomic write: write to temp file, then rename
	playlistPath := filepath.Join(streamDir, "stream.m3u8")
	tmpPath := playlistPath + ".tmp"

	if err := os.WriteFile(tmpPath, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, playlistPath)
}
```

**Step 3: Run tests**

```bash
go test ./internal/packager/... -v -race
```

**Step 4: Commit**

```bash
git add internal/packager/
git commit -m "feat: add HLS manifest generator with sliding window and atomic writes"
```

---

### Story 3.3: Packager Service

Wire receiver, manifest generator, Kafka consumer/producer, and HTTP server into a runnable service.

**Files:**
- Create: `internal/packager/service.go`
- Create: `cmd/packager/main.go`

**Step 1: Implement packager service**

The packager service:
1. Listens for segment pushes via HTTP
2. Writes segments to disk
3. Updates HLS manifests
4. Publishes segment-available events to Kafka
5. Serves HLS content (segments + manifests) via HTTP

```go
// internal/packager/service.go
package packager

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
	"github.com/alexrybrown/streamline/internal/logging"
)

// ServiceConfig holds packager service configuration.
type ServiceConfig struct {
	Port           string
	OutputDir      string
	KafkaBrokers   []string
	TargetDuration int
	WindowSize     int
}

// Service is the packager HTTP server.
type Service struct {
	cfg      ServiceConfig
	log      *zap.Logger
	receiver *Receiver
	manifest *ManifestGenerator
	producer *streamkafka.Producer
}

// NewService creates a new packager service.
func NewService(cfg ServiceConfig) (*Service, error) {
	producer, err := streamkafka.NewProducer(cfg.KafkaBrokers, streamkafka.TopicSegments)
	if err != nil {
		return nil, err
	}

	return &Service{
		cfg:      cfg,
		log:      logging.New("packager"),
		receiver: NewReceiver(cfg.OutputDir),
		manifest: NewManifestGenerator(cfg.OutputDir, ManifestConfig{
			TargetDuration: cfg.TargetDuration,
			WindowSize:     cfg.WindowSize,
		}),
		producer: producer,
	}, nil
}

// Run starts the packager HTTP server.
func (s *Service) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Segment push endpoint
	mux.Handle("/segments/", s.segmentHandler())

	// HLS content serving
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.FileServer(http.Dir(s.cfg.OutputDir))))

	srv := &http.Server{Addr: ":" + s.cfg.Port, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	s.log.Info("packager listening", zap.String("port", s.cfg.Port))
	return srv.ListenAndServe()
}

func (s *Service) segmentHandler() http.Handler {
	// Wrap the receiver to also update manifests and publish events
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delegate to receiver for disk write + dedup
		rec := s.receiver.Handler()
		rw := &responseCapture{ResponseWriter: w}
		rec.ServeHTTP(rw, r)

		if rw.statusCode != http.StatusOK {
			return
		}

		// NOTE: The receiver + manifest + kafka publish integration
		// will need refinement during implementation. Consider having
		// the receiver return structured info about the accepted segment
		// so the service layer can update the manifest and publish events.
	})
}

type responseCapture struct {
	http.ResponseWriter
	statusCode int
}

func (r *responseCapture) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
```

**Step 2: Create main entry point**

```go
// cmd/packager/main.go
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/config"
	"github.com/alexrybrown/streamline/internal/logging"
	"github.com/alexrybrown/streamline/internal/packager"
)

func main() {
	log := logging.New("packager")
	cfg := config.Load()

	svc, err := packager.NewService(packager.ServiceConfig{
		Port:           getEnv("PACKAGER_PORT", "8082"),
		OutputDir:      getEnv("OUTPUT_DIR", "/tmp/packager"),
		KafkaBrokers:   strings.Split(cfg.KafkaBrokers, ","),
		TargetDuration: 6,
		WindowSize:     10,
	})
	if err != nil {
		log.Fatal("failed to create packager", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("shutting down")
		cancel()
	}()

	log.Info("starting packager")
	if err := svc.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatal("packager exited with error", zap.Error(err))
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Step 3: Build and verify**

```bash
go build ./cmd/packager
```

**Step 4: Commit**

```bash
git add internal/packager/ cmd/packager/
git commit -m "feat: add packager service with segment receiver, manifest generation, and HLS serving"
```

---

### Story 3.4: Encoder → Packager Integration

Update the encoder worker to push segments to the Packager via HTTP instead of just writing to local disk.

**Files:**
- Modify: `internal/encoder/worker.go`
- Create: `internal/encoder/segment_pusher.go`
- Create: `internal/encoder/segment_pusher_test.go`

**Step 1: Write test for segment pusher**

```go
// internal/encoder/segment_pusher_test.go
package encoder_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrybrown/streamline/internal/encoder"
)

func TestSegmentPusher_PushesSegment(t *testing.T) {
	// Set up a mock packager
	var receivedPath string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create a fake segment file
	dir := t.TempDir()
	segPath := filepath.Join(dir, "segment_00001.ts")
	os.WriteFile(segPath, []byte("test segment data"), 0644)

	pusher := encoder.NewSegmentPusher(server.URL, "stream-1")
	err := pusher.Push(segPath, 1)
	if err != nil {
		t.Fatal(err)
	}

	if receivedPath != "/segments/stream-1/1" {
		t.Errorf("unexpected path: %s", receivedPath)
	}
	if string(receivedBody) != "test segment data" {
		t.Errorf("unexpected body: %s", string(receivedBody))
	}
}
```

**Step 2: Implement segment pusher**

```go
// internal/encoder/segment_pusher.go
package encoder

import (
	"fmt"
	"net/http"
	"os"
)

// SegmentPusher pushes encoded segments to the Packager via HTTP.
type SegmentPusher struct {
	packagerURL string
	streamID    string
	client      *http.Client
}

// NewSegmentPusher creates a pusher targeting the given packager URL.
func NewSegmentPusher(packagerURL, streamID string) *SegmentPusher {
	return &SegmentPusher{
		packagerURL: packagerURL,
		streamID:    streamID,
		client:      &http.Client{},
	}
}

// Push sends a segment file to the packager.
func (p *SegmentPusher) Push(filePath string, sequenceNumber int64) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()

	url := fmt.Sprintf("%s/segments/%s/%d", p.packagerURL, p.streamID, sequenceNumber)
	req, err := http.NewRequest(http.MethodPut, url, f)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("push segment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("packager returned %d", resp.StatusCode)
	}
	return nil
}
```

**Step 3: Update worker to use segment pusher**

Modify `internal/encoder/worker.go`:
- Add `PackagerURL string` to `WorkerConfig`
- Update `publishSegment` method to push first, then publish the Kafka event

**Step 4: Run tests**

```bash
go test ./internal/encoder/... -v -race
```

**Step 5: End-to-end smoke test**

```bash
make docker-up
# Terminal 1: run packager
go run ./cmd/packager
# Terminal 2: run encoder (pointing at packager)
PACKAGER_URL=http://localhost:8082 INPUT_URI="file://$PWD/assets/bbb.mp4" go run ./cmd/encoder-worker
# Terminal 3: check HLS output
curl http://localhost:8082/hls/stream-1/stream.m3u8
# Should see a valid M3U8 playlist
```

**Step 6: Commit**

```bash
git add internal/encoder/ cmd/encoder-worker/
git commit -m "feat: add segment pusher and wire encoder → packager pipeline"
```

---

### Story 3.5: Bare hls.js Verification Page

Minimal HTML page to verify the HLS output is playable.

**Files:**
- Create: `web/verify.html`

**Step 1: Create verification page**

```html
<!-- web/verify.html -->
<!DOCTYPE html>
<html>
<head>
  <title>streamline — HLS Verification</title>
  <script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
  <style>
    body { font-family: monospace; background: #1a1a1a; color: #eee; padding: 20px; }
    video { max-width: 800px; width: 100%; background: #000; }
    #status { margin-top: 10px; }
  </style>
</head>
<body>
  <h1>streamline — HLS Verification</h1>
  <video id="video" controls></video>
  <div id="status">Connecting...</div>

  <script>
    const video = document.getElementById('video');
    const status = document.getElementById('status');
    const src = 'http://localhost:8082/hls/stream-1/stream.m3u8';

    if (Hls.isSupported()) {
      const hls = new Hls();
      hls.loadSource(src);
      hls.attachMedia(video);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        status.textContent = 'Playing';
        video.play();
      });
      hls.on(Hls.Events.ERROR, (event, data) => {
        status.textContent = 'Error: ' + data.type + ' - ' + data.details;
      });
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = src;
    }
  </script>
</body>
</html>
```

**Step 2: Verify end-to-end**

With Kafka, encoder, and packager running, open `web/verify.html` in a browser. Video should play.

This is the Epic 3 milestone: **video plays in a browser**.

**Step 3: Commit**

```bash
git add web/verify.html
git commit -m "feat: add bare hls.js verification page for end-to-end playback test"
```
