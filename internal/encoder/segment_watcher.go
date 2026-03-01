package encoder

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// sendTimeout is how long scan waits for the consumer to accept a segment
// before logging a backpressure warning. Kept short so stalls surface quickly
// without silently blocking through entire poll intervals.
const sendTimeout = 200 * time.Millisecond

// defaultPollInterval is the fallback polling frequency for segment detection
// when none is configured. 200ms keeps detection latency well under 5% of a
// typical 6-second HLS segment while adding negligible CPU overhead (one
// filepath.Glob per tick).
const defaultPollInterval = 200 * time.Millisecond

// Segment represents a detected HLS segment file.
type Segment struct {
	Path           string
	SequenceNumber int64
	Size           int64
}

// SegmentWatcherConfig holds configuration for a SegmentWatcher.
type SegmentWatcherConfig struct {
	Dir          string
	PollInterval time.Duration
	Log          *zap.Logger
}

// SegmentWatcher monitors a directory for new .ts segment files.
type SegmentWatcher struct {
	dir          string
	pollInterval time.Duration
	log          *zap.Logger
	seen         map[string]bool
}

// NewSegmentWatcher creates a new watcher for the given directory.
func NewSegmentWatcher(cfg SegmentWatcherConfig) *SegmentWatcher {
	pollInterval := cfg.PollInterval
	if pollInterval == 0 {
		pollInterval = defaultPollInterval
	}
	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	return &SegmentWatcher{
		dir:          cfg.Dir,
		pollInterval: pollInterval,
		log:          log.Named("segment-watcher"),
		seen:         make(map[string]bool),
	}
}

// Watch returns a channel that emits new segments as they appear.
// The channel is closed when the context is cancelled.
func (w *SegmentWatcher) Watch(ctx context.Context) <-chan Segment {
	ch := make(chan Segment)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.scan(ctx, ch)
			}
		}
	}()

	return ch
}

func (w *SegmentWatcher) scan(ctx context.Context, ch chan<- Segment) {
	matches, _ := filepath.Glob(filepath.Join(w.dir, "segment_*.ts"))
	sort.Strings(matches)

	for _, path := range matches {
		if w.seen[path] {
			continue
		}
		w.seen[path] = true

		info, err := os.Stat(path)
		if err != nil {
			w.log.Warn("failed to stat segment",
				zap.String("method", "scan"),
				zap.String("path", path),
				zap.Error(err))
			continue
		}

		sequence := parseSequenceNumber(filepath.Base(path))
		segment := Segment{
			Path:           path,
			SequenceNumber: sequence,
			Size:           info.Size(),
		}

		select {
		case ch <- segment:
			w.log.Debug("segment detected",
				zap.String("method", "scan"),
				zap.Int64("sequence", sequence),
				zap.Int64("size_bytes", info.Size()))
		case <-ctx.Done():
			return
		case <-time.After(sendTimeout):
			w.log.Warn("segment send blocked — consumer may be slow",
				zap.String("method", "scan"),
				zap.Int64("sequence", sequence),
				zap.Duration("blocked_for", sendTimeout),
			)
			// Still deliver — block until consumer reads or context cancels.
			select {
			case ch <- segment:
			case <-ctx.Done():
				return
			}
		}
	}
}

func parseSequenceNumber(filename string) int64 {
	// segment_00001.ts → 1
	name := strings.TrimSuffix(filename, ".ts")
	parts := strings.Split(name, "_")
	if len(parts) < 2 {
		return 0
	}
	number, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	return number
}
