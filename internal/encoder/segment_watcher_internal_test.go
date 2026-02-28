package encoder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestParseSequenceNumber(t *testing.T) {
	tests := []struct {
		filename string
		want     int64
	}{
		{"segment_00001.ts", 1},
		{"segment_00000.ts", 0},
		{"segment_00042.ts", 42},
		{"segment_99999.ts", 99999},
		{"multi_part_00007.ts", 7},
		{"noseparator.ts", 0},
		{"segment_abc.ts", 0},
		{".ts", 0},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := parseSequenceNumber(tt.filename)
			if got != tt.want {
				t.Errorf("parseSequenceNumber(%q) = %d, want %d", tt.filename, got, tt.want)
			}
		})
	}
}

func TestNewSegmentWatcher_Defaults(t *testing.T) {
	tests := []struct {
		name             string
		config           SegmentWatcherConfig
		wantPollInterval time.Duration
	}{
		{
			name: "zero PollInterval uses default",
			config: SegmentWatcherConfig{
				Dir: t.TempDir(),
				Log: zap.NewNop(),
			},
			wantPollInterval: defaultPollInterval,
		},
		{
			name: "nil Log uses nop logger",
			config: SegmentWatcherConfig{
				Dir:          t.TempDir(),
				PollInterval: 100 * time.Millisecond,
			},
			wantPollInterval: 100 * time.Millisecond,
		},
		{
			name: "both defaults applied",
			config: SegmentWatcherConfig{
				Dir: t.TempDir(),
			},
			wantPollInterval: defaultPollInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			watcher := NewSegmentWatcher(tt.config)
			if watcher.pollInterval != tt.wantPollInterval {
				t.Errorf("pollInterval = %v, want %v", watcher.pollInterval, tt.wantPollInterval)
			}
			if watcher.log == nil {
				t.Error("expected log to be non-nil even when config.Log is nil")
			}
			if watcher.dir != tt.config.Dir {
				t.Errorf("dir = %q, want %q", watcher.dir, tt.config.Dir)
			}
			if watcher.seen == nil {
				t.Error("expected seen map to be initialized")
			}
		})
	}
}

func TestScan_StatError_LogsWarningAndContinues(t *testing.T) {
	directory := t.TempDir()

	// Create a segment file, then remove it after Glob finds it but before Stat.
	// We simulate this by creating a symlink to a non-existent target.
	segmentPath := filepath.Join(directory, "segment_00001.ts")
	if err := os.Symlink("/nonexistent/path/file.ts", segmentPath); err != nil {
		t.Fatal(err)
	}

	// Also create a valid segment so we can verify scan continues after the error.
	validPath := filepath.Join(directory, "segment_00002.ts")
	if err := os.WriteFile(validPath, []byte("valid data"), 0644); err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zap.WarnLevel)

	watcher := &SegmentWatcher{
		dir:          directory,
		pollInterval: defaultPollInterval,
		log:          zap.New(core).Named("segment-watcher"),
		seen:         make(map[string]bool),
	}

	channel := make(chan Segment, 10)
	ctx := context.Background()

	watcher.scan(ctx, channel)

	// Verify warning was logged for the broken symlink
	warnings := logs.FilterMessage("failed to stat segment")
	if warnings.Len() == 0 {
		t.Fatal("expected 'failed to stat segment' warning log")
	}

	// Verify the valid segment was still emitted
	select {
	case segment := <-channel:
		if segment.SequenceNumber != 2 {
			t.Errorf("expected sequence number 2, got %d", segment.SequenceNumber)
		}
	default:
		t.Fatal("expected valid segment to be emitted after stat error")
	}
}

func TestScan_ContextCancelledDuringSend(t *testing.T) {
	directory := t.TempDir()

	// Create a segment file
	segmentPath := filepath.Join(directory, "segment_00001.ts")
	if err := os.WriteFile(segmentPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	watcher := &SegmentWatcher{
		dir:          directory,
		pollInterval: defaultPollInterval,
		log:          zap.NewNop().Named("segment-watcher"),
		seen:         make(map[string]bool),
	}

	// Use an unbuffered channel and a cancelled context to hit the ctx.Done case
	// in the select during send.
	channel := make(chan Segment)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	watcher.scan(ctx, channel)

	// The segment should be marked as seen even though we couldn't send it.
	// The function should return without blocking.
	if !watcher.seen[segmentPath] {
		t.Error("expected segment to be marked as seen")
	}
}

func TestScan_ContextCancelledDuringBackpressureFallback(t *testing.T) {
	directory := t.TempDir()

	// Create a segment file
	segmentPath := filepath.Join(directory, "segment_00001.ts")
	if err := os.WriteFile(segmentPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	watcher := &SegmentWatcher{
		dir:          directory,
		pollInterval: defaultPollInterval,
		log:          zap.NewNop().Named("segment-watcher"),
		seen:         make(map[string]bool),
	}

	// Use an unbuffered channel that no one reads from. The first select will
	// hit the time.After(sendTimeout) case. Then in the fallback select, we
	// cancel the context to hit the ctx.Done branch.
	channel := make(chan Segment) // unbuffered, no reader
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after sendTimeout fires but before the fallback send can complete.
	// sendTimeout is 200ms, so cancel at ~250ms.
	go func() {
		time.Sleep(sendTimeout + 50*time.Millisecond)
		cancel()
	}()

	watcher.scan(ctx, channel)

	// scan should have returned via the ctx.Done branch in the fallback select.
	if !watcher.seen[segmentPath] {
		t.Error("expected segment to be marked as seen")
	}
}
