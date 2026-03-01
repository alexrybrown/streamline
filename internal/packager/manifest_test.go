package packager_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/packager"
)

func TestManifestGenerator_GeneratesValidHLS(t *testing.T) {
	outputDir := t.TempDir()
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     10,
		Log:            zap.NewNop(),
	})

	if err := generator.AddSegment("stream-1", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	}); err != nil {
		t.Fatalf("AddSegment 1: %v", err)
	}

	if err := generator.AddSegment("stream-1", packager.SegmentInfo{
		Filename:        "segment_00002.ts",
		SequenceNumber:  2,
		DurationSeconds: 6.0,
	}); err != nil {
		t.Fatalf("AddSegment 2: %v", err)
	}

	playlistPath := filepath.Join(outputDir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}

	content := string(data)

	tests := []struct {
		name     string
		contains string
	}{
		{"EXTM3U header", "#EXTM3U"},
		{"version tag", "#EXT-X-VERSION:3"},
		{"target duration", "#EXT-X-TARGETDURATION:6"},
		{"media sequence", "#EXT-X-MEDIA-SEQUENCE:1"},
		{"segment 1", "segment_00001.ts"},
		{"segment 2", "segment_00002.ts"},
		{"segment 1 duration", "#EXTINF:6.000,"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(content, tt.contains) {
				t.Errorf("playlist missing %q:\n%s", tt.contains, content)
			}
		})
	}
}

func TestManifestGenerator_SlidingWindow(t *testing.T) {
	outputDir := t.TempDir()
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      outputDir,
		TargetDuration: 2,
		WindowSize:     3, // Only keep 3 segments in playlist
		Log:            zap.NewNop(),
	})

	for i := int64(1); i <= 5; i++ {
		if err := generator.AddSegment("stream-1", packager.SegmentInfo{
			Filename:        fmt.Sprintf("segment_%05d.ts", i),
			SequenceNumber:  i,
			DurationSeconds: 2.0,
		}); err != nil {
			t.Fatalf("AddSegment %d: %v", i, err)
		}
	}

	playlistPath := filepath.Join(outputDir, "stream-1", "stream.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}

	content := string(data)

	tests := []struct {
		name    string
		segment string
		present bool
	}{
		{"segment 1 removed", "segment_00001.ts", false},
		{"segment 2 removed", "segment_00002.ts", false},
		{"segment 3 present", "segment_00003.ts", true},
		{"segment 4 present", "segment_00004.ts", true},
		{"segment 5 present", "segment_00005.ts", true},
		{"media sequence updated", "#EXT-X-MEDIA-SEQUENCE:3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Contains(content, tt.segment); got != tt.present {
				t.Errorf("playlist contains %q = %v, want %v:\n%s", tt.segment, got, tt.present, content)
			}
		})
	}
}

func TestManifestGenerator_MultipleStreams(t *testing.T) {
	outputDir := t.TempDir()
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     10,
		Log:            zap.NewNop(),
	})

	// Add segments for two different streams
	if err := generator.AddSegment("stream-a", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	}); err != nil {
		t.Fatalf("AddSegment stream-a: %v", err)
	}

	if err := generator.AddSegment("stream-b", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	}); err != nil {
		t.Fatalf("AddSegment stream-b: %v", err)
	}

	// Both playlists should exist independently
	for _, streamID := range []string{"stream-a", "stream-b"} {
		playlistPath := filepath.Join(outputDir, streamID, "stream.m3u8")
		data, err := os.ReadFile(playlistPath)
		if err != nil {
			t.Fatalf("read playlist for %s: %v", streamID, err)
		}
		if !strings.Contains(string(data), "segment_00001.ts") {
			t.Errorf("playlist for %s missing segment_00001.ts", streamID)
		}
	}
}

func TestNewManifestGenerator_PanicsOnInvalidConfig(t *testing.T) {
	tests := []struct {
		name            string
		config          packager.ManifestGeneratorConfig
		expectedMessage string
	}{
		{
			name: "empty OutputDir",
			config: packager.ManifestGeneratorConfig{
				OutputDir:      "",
				TargetDuration: 6,
				WindowSize:     10,
				Log:            zap.NewNop(),
			},
			expectedMessage: "packager.ManifestGeneratorConfig: OutputDir must not be empty",
		},
		{
			name: "zero TargetDuration",
			config: packager.ManifestGeneratorConfig{
				OutputDir:      t.TempDir(),
				TargetDuration: 0,
				WindowSize:     10,
				Log:            zap.NewNop(),
			},
			expectedMessage: "packager.ManifestGeneratorConfig: TargetDuration must be positive",
		},
		{
			name: "negative WindowSize",
			config: packager.ManifestGeneratorConfig{
				OutputDir:      t.TempDir(),
				TargetDuration: 6,
				WindowSize:     -1,
				Log:            zap.NewNop(),
			},
			expectedMessage: "packager.ManifestGeneratorConfig: WindowSize must be positive",
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

			packager.NewManifestGenerator(tt.config)
		})
	}
}

func TestManifestGenerator_AddSegmentReturnsErrorOnInvalidDir(t *testing.T) {
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      "/dev/null/impossible",
		TargetDuration: 6,
		WindowSize:     10,
		Log:            zap.NewNop(),
	})

	err := generator.AddSegment("stream-1", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	})
	if err == nil {
		t.Fatal("expected error for invalid output directory")
	}
}

func TestManifestGenerator_AddSegmentReturnsErrorOnUnwritableDir(t *testing.T) {
	outputDir := t.TempDir()
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     10,
		Log:            zap.NewNop(),
	})

	// Create the stream directory, then make it read-only so WriteFile fails.
	streamDir := filepath.Join(outputDir, "stream-1")
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(streamDir, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(streamDir, 0o750) })

	err := generator.AddSegment("stream-1", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	})
	if err == nil {
		t.Fatal("expected error when directory is unwritable")
	}
}

func TestNewManifestGenerator_NilLogDefaultsToNop(t *testing.T) {
	outputDir := t.TempDir()

	// Should not panic with nil Log
	generator := packager.NewManifestGenerator(packager.ManifestGeneratorConfig{
		OutputDir:      outputDir,
		TargetDuration: 6,
		WindowSize:     10,
		Log:            nil,
	})

	// Verify it works by adding a segment
	if err := generator.AddSegment("stream-1", packager.SegmentInfo{
		Filename:        "segment_00001.ts",
		SequenceNumber:  1,
		DurationSeconds: 6.0,
	}); err != nil {
		t.Fatalf("AddSegment: %v", err)
	}

	playlistPath := filepath.Join(outputDir, "stream-1", "stream.m3u8")
	if _, err := os.Stat(playlistPath); err != nil {
		t.Fatalf("playlist not written: %v", err)
	}
}
