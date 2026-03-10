package packager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const (
	// hlsVersion is the HLS protocol version written to playlists.
	// Version 3 supports EXTINF with decimal durations (RFC 8216 Section 7).
	hlsVersion = 3

	// playlistFilename is the name of the HLS playlist file within each stream directory.
	playlistFilename = "stream.m3u8"

	// playlistFileMode is the permission for temp playlist files before atomic rename.
	// Owner-only read/write; the HTTP file server runs in the same process.
	playlistFileMode = 0o600
)

// SegmentInfo describes a segment for the playlist.
type SegmentInfo struct {
	Filename        string
	SequenceNumber  int64
	DurationSeconds float64
}

// ManifestGeneratorConfig holds configuration for the ManifestGenerator.
type ManifestGeneratorConfig struct {
	// OutputDir is the base directory for stream subdirectories and playlists.
	OutputDir string
	// TargetDuration is the EXT-X-TARGETDURATION value in seconds.
	// Must be >= the rounded-up duration of any segment.
	TargetDuration int
	// WindowSize is the maximum number of segments in the sliding window playlist.
	WindowSize int
	// Log is the parent logger. Nil defaults to a no-op logger.
	Log *zap.Logger
}

// ManifestGenerator creates and updates HLS playlists as segments arrive.
type ManifestGenerator struct {
	outputDir      string
	targetDuration int
	windowSize     int
	log            *zap.Logger

	mu       sync.Mutex
	segments map[string][]SegmentInfo // streamID -> segments in window
}

// NewManifestGenerator creates a new ManifestGenerator with the given configuration.
// OutputDir must be non-empty. A nil Log defaults to a no-op logger.
func NewManifestGenerator(cfg ManifestGeneratorConfig) *ManifestGenerator {
	if cfg.OutputDir == "" {
		panic("packager.ManifestGeneratorConfig: OutputDir must not be empty")
	}
	if cfg.TargetDuration <= 0 {
		panic("packager.ManifestGeneratorConfig: TargetDuration must be positive")
	}
	if cfg.WindowSize <= 0 {
		panic("packager.ManifestGeneratorConfig: WindowSize must be positive")
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	return &ManifestGenerator{
		outputDir:      cfg.OutputDir,
		targetDuration: cfg.TargetDuration,
		windowSize:     cfg.WindowSize,
		log:            cfg.Log.Named("manifest"),
		segments:       make(map[string][]SegmentInfo),
	}
}

// AddSegment appends a segment to the stream's playlist and regenerates the manifest file.
// The sliding window removes the oldest segments when the window exceeds WindowSize.
func (generator *ManifestGenerator) AddSegment(streamID string, segment SegmentInfo) error {
	log := generator.log.With(
		zap.String("method", "AddSegment"),
		zap.String("streamID", streamID),
		zap.Int64("sequenceNumber", segment.SequenceNumber),
	)

	generator.mu.Lock()
	defer generator.mu.Unlock()

	window := append(generator.segments[streamID], segment)

	// Apply sliding window: keep only the most recent WindowSize segments.
	if len(window) > generator.windowSize {
		window = window[len(window)-generator.windowSize:]
	}
	generator.segments[streamID] = window

	log.Info("segment added to playlist",
		zap.Int("windowSegments", len(window)),
	)

	return generator.writePlaylist(streamID)
}

// ResetStream clears the segment window for the given stream, so the next
// AddSegment call starts a fresh playlist. This is called when a stream is
// stopped and restarted.
func (generator *ManifestGenerator) ResetStream(streamID string) {
	generator.mu.Lock()
	delete(generator.segments, streamID)
	generator.mu.Unlock()

	generator.log.Info("stream manifest state reset",
		zap.String("method", "ResetStream"),
		zap.String("streamID", streamID),
	)
}

// writePlaylist writes the M3U8 playlist for the given stream using atomic rename.
// Caller must hold generator.mu.
func (generator *ManifestGenerator) writePlaylist(streamID string) error {
	segments := generator.segments[streamID]
	if len(segments) == 0 {
		return nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "#EXTM3U\n")
	fmt.Fprintf(&builder, "#EXT-X-VERSION:%d\n", hlsVersion)
	fmt.Fprintf(&builder, "#EXT-X-TARGETDURATION:%d\n", generator.targetDuration)
	fmt.Fprintf(&builder, "#EXT-X-MEDIA-SEQUENCE:%d\n", segments[0].SequenceNumber)
	fmt.Fprintf(&builder, "\n")

	for _, segment := range segments {
		fmt.Fprintf(&builder, "#EXTINF:%.3f,\n%s\n", segment.DurationSeconds, segment.Filename)
	}

	streamDir := filepath.Join(generator.outputDir, streamID)
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		return fmt.Errorf("create stream directory: %w", err)
	}

	// Atomic write: write to temp file, then rename to avoid serving partial playlists.
	finalPath := filepath.Join(streamDir, playlistFilename)
	tempPath := finalPath + tempFileSuffix

	if err := os.WriteFile(tempPath, []byte(builder.String()), playlistFileMode); err != nil {
		return fmt.Errorf("write temp playlist: %w", err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("rename temp playlist to final: %w", err)
	}

	return nil
}
