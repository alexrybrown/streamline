package encoder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/alexrybrown/streamline/internal/encoder"
)

func TestSegmentWatcher_DetectsNewSegments(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watcher := encoder.NewSegmentWatcher(encoder.SegmentWatcherConfig{
		Dir:          dir,
		PollInterval: 100 * time.Millisecond,
		Log:          zap.NewNop(),
	})
	segments := watcher.Watch(ctx)

	// Create a segment file
	content := []byte("fake segment data")
	segmentPath := filepath.Join(dir, "segment_00001.ts")
	if err := os.WriteFile(segmentPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case seg := <-segments:
		if seg.Path != segmentPath {
			t.Errorf("expected path %s, got %s", segmentPath, seg.Path)
		}
		if seg.SequenceNumber != 1 {
			t.Errorf("expected sequence 1, got %d", seg.SequenceNumber)
		}
		if seg.Size != int64(len(content)) {
			t.Errorf("expected size %d, got %d", len(content), seg.Size)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for segment notification")
	}
}

func TestSegmentWatcher_IgnoresDuplicates(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watcher := encoder.NewSegmentWatcher(encoder.SegmentWatcherConfig{
		Dir:          dir,
		PollInterval: 100 * time.Millisecond,
		Log:          zap.NewNop(),
	})
	segments := watcher.Watch(ctx)

	// Create the same segment — should only get one notification
	segmentPath := filepath.Join(dir, "segment_00001.ts")
	if err := os.WriteFile(segmentPath, []byte("fake segment"), 0644); err != nil {
		t.Fatal(err)
	}

	// First notification
	select {
	case <-segments:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first segment")
	}

	// Wait a poll cycle — should NOT get another notification for the same file
	select {
	case seg := <-segments:
		t.Fatalf("got unexpected duplicate segment: %s", seg.Path)
	case <-time.After(1 * time.Second):
		// Expected — no duplicate
	}
}

func TestSegmentWatcher_EmitsInSortedOrder(t *testing.T) {
	dir := t.TempDir()

	// Create segments out of order
	for _, name := range []string{"segment_00003.ts", "segment_00001.ts", "segment_00002.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watcher := encoder.NewSegmentWatcher(encoder.SegmentWatcherConfig{
		Dir:          dir,
		PollInterval: 100 * time.Millisecond,
		Log:          zap.NewNop(),
	})
	segments := watcher.Watch(ctx)

	want := []int64{1, 2, 3}
	for i, wantSeq := range want {
		select {
		case seg := <-segments:
			if seg.SequenceNumber != wantSeq {
				t.Errorf("segment[%d]: expected sequence %d, got %d", i, wantSeq, seg.SequenceNumber)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for segment %d", i)
		}
	}
}

func TestSegmentWatcher_BackpressureLogsWarning(t *testing.T) {
	dir := t.TempDir()

	// Use an observed logger to capture warnings
	core, logs := observer.New(zap.WarnLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watcher := encoder.NewSegmentWatcher(encoder.SegmentWatcherConfig{
		Dir:          dir,
		PollInterval: 100 * time.Millisecond,
		Log:          zap.New(core),
	})
	segments := watcher.Watch(ctx)

	// Create a segment but don't read from the channel — triggers backpressure
	if err := os.WriteFile(filepath.Join(dir, "segment_00001.ts"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for the backpressure timeout to fire (sendTimeout = 200ms + poll margin)
	time.Sleep(500 * time.Millisecond)

	// Now drain the channel so the watcher can deliver
	select {
	case <-segments:
	case <-ctx.Done():
		t.Fatal("timed out draining segment")
	}

	// Check that a backpressure warning was logged
	found := false
	for _, entry := range logs.All() {
		if entry.Level == zap.WarnLevel && entry.Message == "segment send blocked — consumer may be slow" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected backpressure warning log, got none")
	}
}
