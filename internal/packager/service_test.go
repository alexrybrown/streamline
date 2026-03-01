package packager_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
	"github.com/alexrybrown/streamline/internal/packager"
)

// publishCall records a single call to spyPublisher.Publish.
type publishCall struct {
	Key   string
	Value []byte
}

// spyPublisher records Publish calls for test assertions.
type spyPublisher struct {
	mu    sync.Mutex
	calls []publishCall
}

func (spy *spyPublisher) Publish(_ context.Context, key string, value []byte) error {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls = append(spy.calls, publishCall{Key: key, Value: value})
	return nil
}

func (spy *spyPublisher) Close() {}

func (spy *spyPublisher) publishCalls() []publishCall {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	result := make([]publishCall, len(spy.calls))
	copy(result, spy.calls)
	return result
}

// testServiceEnv holds the components created for a service integration test.
type testServiceEnv struct {
	Client    streamlinev1connect.PackagerServiceClient
	Spy       *spyPublisher
	OutputDir string
	ServerURL string
}

// newTestServiceEnv creates a Service backed by httptest.Server and returns all
// test components needed for assertions.
func newTestServiceEnv(t *testing.T) testServiceEnv {
	t.Helper()
	outputDir := t.TempDir()
	spy := &spyPublisher{}

	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     5,
		Log:            zap.NewNop(),
		Publisher:      spy,
	})
	t.Cleanup(service.Close)

	server := httptest.NewUnstartedServer(service.Handler())
	server.Start()
	t.Cleanup(server.Close)

	client := streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)
	return testServiceEnv{
		Client:    client,
		Spy:       spy,
		OutputDir: outputDir,
		ServerURL: server.URL,
	}
}

func TestService_SegmentPipelineUpdatesManifestAndPublishesEvent(t *testing.T) {
	env := newTestServiceEnv(t)

	segmentData := []byte("test segment content for pipeline")
	pushSegment(t, env.Client, "stream-1", 3, 6.0, segmentData)

	// Verify segment file was written to disk.
	segmentPath := filepath.Join(env.OutputDir, "stream-1", "segment_00003.ts")
	fileData, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("segment file not found: %v", err)
	}
	if string(fileData) != string(segmentData) {
		t.Errorf("segment content mismatch:\n  got:  %q\n  want: %q", string(fileData), string(segmentData))
	}

	// Verify manifest was generated with the segment entry.
	playlistPath := filepath.Join(env.OutputDir, "stream-1", "stream.m3u8")
	playlistData, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("playlist file not found: %v", err)
	}
	playlist := string(playlistData)
	if !strings.Contains(playlist, "#EXTM3U") {
		t.Error("playlist missing #EXTM3U header")
	}
	if !strings.Contains(playlist, "segment_00003.ts") {
		t.Error("playlist missing segment_00003.ts entry")
	}
	if !strings.Contains(playlist, "#EXTINF:6.000,") {
		t.Errorf("playlist missing expected EXTINF entry, got:\n%s", playlist)
	}

	// Verify Kafka SegmentAvailable event was published.
	calls := env.Spy.publishCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(calls))
	}
	if calls[0].Key != "stream-1" {
		t.Errorf("expected publish key %q, got %q", "stream-1", calls[0].Key)
	}

	var event streamlinev1.SegmentAvailable
	if err := proto.Unmarshal(calls[0].Value, &event); err != nil {
		t.Fatalf("unmarshal SegmentAvailable: %v", err)
	}
	if event.StreamId != "stream-1" {
		t.Errorf("expected StreamId %q, got %q", "stream-1", event.StreamId)
	}
	if event.SequenceNumber != 3 {
		t.Errorf("expected SequenceNumber 3, got %d", event.SequenceNumber)
	}
	expectedPlaylistPath := "stream-1/stream.m3u8"
	if event.PlaylistPath != expectedPlaylistPath {
		t.Errorf("expected PlaylistPath %q, got %q", expectedPlaylistPath, event.PlaylistPath)
	}
	if event.Timestamp == nil {
		t.Error("expected non-nil Timestamp")
	}
}

func TestService_HLSContentServedViaHTTP(t *testing.T) {
	env := newTestServiceEnv(t)

	segmentData := []byte("hls content test segment")
	pushSegment(t, env.Client, "stream-1", 1, 6.0, segmentData)

	// GET the HLS playlist via HTTP.
	playlistURL := env.ServerURL + "/hls/stream-1/stream.m3u8"
	response, err := http.Get(playlistURL)
	if err != nil {
		t.Fatalf("GET playlist: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "#EXTM3U") {
		t.Errorf("expected HLS playlist content, got:\n%s", string(body))
	}
	if !strings.Contains(string(body), "segment_00001.ts") {
		t.Errorf("expected segment entry in playlist, got:\n%s", string(body))
	}

	// GET the segment file via HTTP.
	segmentURL := env.ServerURL + "/hls/stream-1/segment_00001.ts"
	segmentResponse, err := http.Get(segmentURL)
	if err != nil {
		t.Fatalf("GET segment: %v", err)
	}
	defer func() { _ = segmentResponse.Body.Close() }()

	if segmentResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for segment, got %d", segmentResponse.StatusCode)
	}
	segmentBody, err := io.ReadAll(segmentResponse.Body)
	if err != nil {
		t.Fatalf("read segment body: %v", err)
	}
	if string(segmentBody) != string(segmentData) {
		t.Errorf("segment content mismatch:\n  got:  %q\n  want: %q", string(segmentBody), string(segmentData))
	}
}

// failingPublisher returns a configured error from Publish. It records calls
// so tests can verify the method was actually invoked.
type failingPublisher struct {
	mu        sync.Mutex
	calls     []publishCall
	publishFn func(ctx context.Context, key string, value []byte) error
}

func newFailingPublisher(err error) *failingPublisher {
	return &failingPublisher{
		publishFn: func(_ context.Context, _ string, _ []byte) error {
			return err
		},
	}
}

func (publisher *failingPublisher) Publish(ctx context.Context, key string, value []byte) error {
	publisher.mu.Lock()
	publisher.calls = append(publisher.calls, publishCall{Key: key, Value: value})
	publisher.mu.Unlock()
	return publisher.publishFn(ctx, key, value)
}

func (publisher *failingPublisher) Close() {}

func (publisher *failingPublisher) publishCalls() []publishCall {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	result := make([]publishCall, len(publisher.calls))
	copy(result, publisher.calls)
	return result
}

func TestService_OnSegmentWritten_ManifestFailureSkipsPublish(t *testing.T) {
	outputDir := t.TempDir()
	spy := &spyPublisher{}

	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     5,
		Log:            zap.NewNop(),
		Publisher:      spy,
	})
	t.Cleanup(service.Close)

	server := httptest.NewUnstartedServer(service.Handler())
	server.Start()
	t.Cleanup(server.Close)

	client := streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)

	// Push one segment successfully so the stream directory exists.
	pushSegment(t, client, "stream-manifest-fail", 1, 6.0, []byte("first segment"))

	// Make the stream directory read-only so the manifest write fails on the next segment.
	streamDir := filepath.Join(outputDir, "stream-manifest-fail")
	if err := os.Chmod(streamDir, 0o444); err != nil {
		t.Fatalf("chmod stream dir: %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions so t.TempDir cleanup succeeds.
		_ = os.Chmod(streamDir, 0o755)
	})

	// Push a second segment. The receiver writes to a new temp file which will fail
	// because the directory is read-only. The segment push itself will fail with an
	// internal error, and the onSegmentWritten callback will never fire.
	// However, receiver creates the stream dir with MkdirAll which won't fail if it
	// already exists — but os.Create for the temp file will fail in read-only dir.
	// So let's instead make a subdirectory approach: we need the segment write to
	// succeed but the manifest write to fail. We can do that by pushing to a stream
	// whose directory we pre-create as read-only for the playlist file.

	// Restore write permission so receiver can write segment file.
	if err := os.Chmod(streamDir, 0o755); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}

	// Remove the playlist file and make the directory read-only for new files
	// won't work — we need a different approach. Let's create a file at the
	// playlist temp path to block the atomic rename.
	playlistTmpPath := filepath.Join(streamDir, "stream.m3u8.tmp")
	if err := os.MkdirAll(playlistTmpPath, 0o755); err != nil {
		t.Fatalf("create blocking dir: %v", err)
	}

	// Reset spy to count only new publish calls.
	spy.mu.Lock()
	spy.calls = nil
	spy.mu.Unlock()

	// Push second segment. The receiver will write the segment file, then
	// onSegmentWritten fires. The manifest generator will try to write
	// stream.m3u8.tmp but it's now a directory, so WriteFile fails.
	pushSegment(t, client, "stream-manifest-fail", 2, 6.0, []byte("second segment"))

	// Verify no Kafka event was published because manifest update failed.
	calls := spy.publishCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 publish calls after manifest failure, got %d", len(calls))
	}
}

func TestService_OnSegmentWritten_PublishFailureStillWritesSegmentAndManifest(t *testing.T) {
	outputDir := t.TempDir()
	publisher := newFailingPublisher(errors.New("kafka broker unavailable"))

	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     5,
		Log:            zap.NewNop(),
		Publisher:      publisher,
	})
	t.Cleanup(service.Close)

	server := httptest.NewUnstartedServer(service.Handler())
	server.Start()
	t.Cleanup(server.Close)

	client := streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)

	segmentData := []byte("segment with publish failure")
	pushSegment(t, client, "stream-pub-fail", 1, 6.0, segmentData)

	// Verify the segment file was still written to disk.
	segmentPath := filepath.Join(outputDir, "stream-pub-fail", "segment_00001.ts")
	fileData, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("segment file not found: %v", err)
	}
	if string(fileData) != string(segmentData) {
		t.Errorf("segment content mismatch:\n  got:  %q\n  want: %q", string(fileData), string(segmentData))
	}

	// Verify the manifest was still updated (manifest write happens before publish).
	playlistPath := filepath.Join(outputDir, "stream-pub-fail", "stream.m3u8")
	playlistData, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("playlist file not found: %v", err)
	}
	if !strings.Contains(string(playlistData), "segment_00001.ts") {
		t.Errorf("playlist missing segment entry, got:\n%s", string(playlistData))
	}

	// Verify Publish was called (even though it failed).
	calls := publisher.publishCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(calls))
	}
}

func TestService_WithCORS_PreflightReturnsHeaders(t *testing.T) {
	outputDir := t.TempDir()
	spy := &spyPublisher{}

	allowedOrigin := "http://localhost:3000"
	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     5,
		Log:            zap.NewNop(),
		Publisher:      spy,
		AllowedOrigins: []string{allowedOrigin},
	})
	t.Cleanup(service.Close)

	server := httptest.NewUnstartedServer(service.Handler())
	server.Start()
	t.Cleanup(server.Close)

	// Send a CORS preflight OPTIONS request to the ConnectRPC endpoint.
	connectEndpoint := server.URL + "/streamline.v1.PackagerService/PushSegment"
	request, err := http.NewRequest(http.MethodOptions, connectEndpoint, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Origin", allowedOrigin)
	request.Header.Set("Access-Control-Request-Method", "POST")
	// The Fetch standard guarantees that browsers send header names lowercased
	// and sorted lexicographically in CORS preflight requests.
	request.Header.Set("Access-Control-Request-Headers", "connect-protocol-version,content-type")

	// Use a plain http.Client rather than server.Client() to avoid any
	// transport-level header stripping.
	httpClient := &http.Client{}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("send preflight: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	// Verify CORS headers are present in the response.
	accessControlOrigin := response.Header.Get("Access-Control-Allow-Origin")
	if accessControlOrigin != allowedOrigin {
		t.Errorf("expected Access-Control-Allow-Origin %q, got %q", allowedOrigin, accessControlOrigin)
	}

	accessControlMethods := response.Header.Get("Access-Control-Allow-Methods")
	if accessControlMethods == "" {
		t.Error("expected non-empty Access-Control-Allow-Methods header")
	}

	accessControlHeaders := response.Header.Get("Access-Control-Allow-Headers")
	if accessControlHeaders == "" {
		t.Error("expected non-empty Access-Control-Allow-Headers header")
	}
}

func TestNewService_NilLogDefaultsToNop(t *testing.T) {
	outputDir := t.TempDir()
	spy := &spyPublisher{}

	// NewService with nil Log should not panic and should work normally.
	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     5,
		Log:            nil,
		Publisher:      spy,
	})
	t.Cleanup(service.Close)

	server := httptest.NewUnstartedServer(service.Handler())
	server.Start()
	t.Cleanup(server.Close)

	client := streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)

	segmentData := []byte("nil log test")
	pushSegment(t, client, "stream-nil-log", 1, 6.0, segmentData)

	calls := spy.publishCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(calls))
	}
}

func TestNewService_PanicsOnInvalidConfig(t *testing.T) {
	tests := []struct {
		name            string
		config          packager.ServiceConfig
		expectedMessage string
	}{
		{
			name: "empty OutputDir",
			config: packager.ServiceConfig{
				OutputDir:      "",
				TargetDuration: 6,
				WindowSize:     5,
				Publisher:      &spyPublisher{},
			},
			expectedMessage: "packager.ServiceConfig: OutputDir must not be empty",
		},
		{
			name: "nil Publisher",
			config: packager.ServiceConfig{
				OutputDir:      "/tmp/test",
				TargetDuration: 6,
				WindowSize:     5,
				Publisher:      nil,
			},
			expectedMessage: "packager.ServiceConfig: Publisher must not be nil",
		},
		{
			name: "zero TargetDuration",
			config: packager.ServiceConfig{
				OutputDir:      "/tmp/test",
				TargetDuration: 0,
				WindowSize:     5,
				Publisher:      &spyPublisher{},
			},
			expectedMessage: "packager.ServiceConfig: TargetDuration must be positive",
		},
		{
			name: "zero WindowSize",
			config: packager.ServiceConfig{
				OutputDir:      "/tmp/test",
				TargetDuration: 6,
				WindowSize:     0,
				Publisher:      &spyPublisher{},
			},
			expectedMessage: "packager.ServiceConfig: WindowSize must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					t.Fatal("expected panic, but none occurred")
				}
				message, ok := recovered.(string)
				if !ok {
					t.Fatalf("expected string panic, got %T: %v", recovered, recovered)
				}
				if message != tt.expectedMessage {
					t.Errorf("unexpected panic message:\n  got:  %s\n  want: %s", message, tt.expectedMessage)
				}
			}()

			packager.NewService(tt.config)
		})
	}
}
