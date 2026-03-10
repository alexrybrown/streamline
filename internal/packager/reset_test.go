package packager_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/packager"
)

func TestReceiver_ResetStream(t *testing.T) {
	tests := []struct {
		name           string
		resetStreamID  string
		verifyStreamID string
		expectDeduped  bool
	}{
		{
			name:           "clears dedup for target stream",
			resetStreamID:  "stream-a",
			verifyStreamID: "stream-a",
			expectDeduped:  false,
		},
		{
			name:           "does not affect other streams",
			resetStreamID:  "stream-a",
			verifyStreamID: "stream-b",
			expectDeduped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()

			callCount := 0
			receiver := packager.NewReceiver(packager.ReceiverConfig{
				OutputDir: outputDir,
				Log:       zap.NewNop(),
				OnSegmentWritten: func(streamID string, segment packager.SegmentInfo) {
					callCount++
				},
			})
			client := newClientForReceiver(t, receiver)

			// Push segment 0 for both streams.
			pushSegment(t, client, "stream-a", 0, 6.0, []byte("a-seg-0"))
			pushSegment(t, client, "stream-b", 0, 6.0, []byte("b-seg-0"))

			// Reset only the target stream.
			receiver.ResetStream(tt.resetStreamID)

			// Reset callback counter after setup.
			callCount = 0

			// Re-push segment 0 for the stream under verification.
			newData := []byte("new-data")
			pushSegment(t, client, tt.verifyStreamID, 0, 6.0, newData)

			if tt.expectDeduped && callCount != 0 {
				t.Errorf("expected segment to remain deduped, but OnSegmentWritten was called %d times", callCount)
			}
			if !tt.expectDeduped && callCount != 1 {
				t.Errorf("expected segment to be accepted after reset, but OnSegmentWritten was called %d times", callCount)
			}

			if !tt.expectDeduped {
				// Verify the new data was written to disk.
				segmentPath := filepath.Join(outputDir, tt.verifyStreamID, "segment_00000.ts")
				data, err := os.ReadFile(segmentPath)
				if err != nil {
					t.Fatalf("read segment after reset: %v", err)
				}
				if string(data) != string(newData) {
					t.Errorf("expected new data after reset, got %q", string(data))
				}
			}
		})
	}
}

func TestManifestGenerator_ResetStream(t *testing.T) {
	tests := []struct {
		name           string
		resetStreamID  string
		verifyStreamID string
		expectCleared  bool
	}{
		{
			name:           "clears segment window for target stream",
			resetStreamID:  "stream-a",
			verifyStreamID: "stream-a",
			expectCleared:  true,
		},
		{
			name:           "does not affect other streams",
			resetStreamID:  "stream-a",
			verifyStreamID: "stream-b",
			expectCleared:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outputDir := t.TempDir()
			generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
				OutputDir:      outputDir,
				TargetDuration: 6,
				WindowSize:     10,
				Log:            zap.NewNop(),
			})

			// Add segment 1 to both streams.
			for _, streamID := range []string{"stream-a", "stream-b"} {
				if err := generator.AddSegment(streamID, packager.SegmentInfo{
					Filename: "segment_00001.ts", SequenceNumber: 1, DurationSeconds: 6.0,
				}); err != nil {
					t.Fatalf("AddSegment %s: %v", streamID, err)
				}
			}

			// Reset only the target stream.
			generator.ResetStream(tt.resetStreamID)

			if tt.expectCleared {
				// Add a fresh segment 0 after reset; playlist should start fresh.
				if err := generator.AddSegment(tt.verifyStreamID, packager.SegmentInfo{
					Filename: "segment_00000.ts", SequenceNumber: 0, DurationSeconds: 6.0,
				}); err != nil {
					t.Fatalf("AddSegment after reset: %v", err)
				}

				playlistPath := filepath.Join(outputDir, tt.verifyStreamID, "stream.m3u8")
				data, err := os.ReadFile(playlistPath)
				if err != nil {
					t.Fatalf("read playlist: %v", err)
				}
				content := string(data)

				if !strings.Contains(content, "#EXT-X-MEDIA-SEQUENCE:0") {
					t.Errorf("expected media sequence 0 after reset, got:\n%s", content)
				}
				if strings.Contains(content, "segment_00001.ts") {
					t.Errorf("expected old segment cleared after reset, got:\n%s", content)
				}
			} else {
				// Verify the un-reset stream still has its segment.
				playlistPath := filepath.Join(outputDir, tt.verifyStreamID, "stream.m3u8")
				data, err := os.ReadFile(playlistPath)
				if err != nil {
					t.Fatalf("read playlist: %v", err)
				}
				if !strings.Contains(string(data), "segment_00001.ts") {
					t.Errorf("expected segment_00001.ts to remain after resetting a different stream")
				}
			}
		})
	}
}

func TestService_ResetStream_ClearsDedupAndManifest(t *testing.T) {
	env := newTestServiceEnv(t)

	// Push a segment.
	pushSegment(t, env.Client, "stream-reset", 0, 6.0, []byte("original"))

	// Reset via service.
	env.Service.ResetStream("stream-reset")

	// Re-push same sequence number with new data — should not be deduped.
	newData := []byte("after-reset")
	pushSegment(t, env.Client, "stream-reset", 0, 6.0, newData)

	segmentPath := filepath.Join(env.OutputDir, "stream-reset", "segment_00000.ts")
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	if string(data) != string(newData) {
		t.Errorf("expected new data after reset, got %q", string(data))
	}

	// Verify manifest has fresh media sequence.
	playlistPath := filepath.Join(env.OutputDir, "stream-reset", "stream.m3u8")
	playlistData, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	if !strings.Contains(string(playlistData), "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Errorf("expected fresh media sequence 0 after reset, got:\n%s", string(playlistData))
	}
}
