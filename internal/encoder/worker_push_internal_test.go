package encoder

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
)

func TestPublishSegment_PushSucceeds_ThenPublishesKafkaEvent(t *testing.T) {
	pusher := &TestSegmentPusher{}
	segmentSpy := &spyPublisher{}

	const segmentDurationSeconds = 4

	worker := NewTestWorkerWithPusher(
		WorkerConfig{
			WorkerID: "w-push",
			StreamID: "s-push",
			FFmpeg:   FFmpegConfig{SegmentDurationS: segmentDurationSeconds},
			Log:      zap.NewNop(),
		},
		&spyPublisher{}, // heartbeat publisher (unused)
		segmentSpy,
		pusher,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00001.ts",
		SequenceNumber: 1,
		Size:           4096,
	}

	worker.publishSegment(context.Background(), segment)

	// Verify pusher was called with correct arguments.
	if len(pusher.Calls) != 1 {
		t.Fatalf("expected 1 push call, got %d", len(pusher.Calls))
	}
	call := pusher.Calls[0]
	if call.StreamID != "s-push" {
		t.Errorf("push stream_id = %q, want %q", call.StreamID, "s-push")
	}
	if call.Segment != segment {
		t.Errorf("push segment = %+v, want %+v", call.Segment, segment)
	}
	if call.DurationSeconds != float64(segmentDurationSeconds) {
		t.Errorf("push duration_seconds = %f, want %f", call.DurationSeconds, float64(segmentDurationSeconds))
	}

	// Verify Kafka publish was also called.
	kafkaCalls := segmentSpy.getCalls()
	if len(kafkaCalls) != 1 {
		t.Fatalf("expected 1 Kafka publish call, got %d", len(kafkaCalls))
	}
	if kafkaCalls[0].key != "s-push" {
		t.Errorf("Kafka publish key = %q, want %q", kafkaCalls[0].key, "s-push")
	}
}

func TestPublishSegment_PushFails_SkipsKafkaPublish(t *testing.T) {
	pusher := &TestSegmentPusher{
		ReturnErr: errors.New("packager unavailable"),
	}
	segmentSpy := &spyPublisher{}

	worker := NewTestWorkerWithPusher(
		WorkerConfig{
			WorkerID: "w-pushfail",
			StreamID: "s-pushfail",
			FFmpeg:   FFmpegConfig{SegmentDurationS: 4},
			Log:      zap.NewNop(),
		},
		&spyPublisher{},
		segmentSpy,
		pusher,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00002.ts",
		SequenceNumber: 2,
		Size:           8192,
	}

	worker.publishSegment(context.Background(), segment)

	// Verify pusher was called.
	if len(pusher.Calls) != 1 {
		t.Fatalf("expected 1 push call, got %d", len(pusher.Calls))
	}

	// Verify Kafka publish was NOT called.
	kafkaCalls := segmentSpy.getCalls()
	if len(kafkaCalls) != 0 {
		t.Fatalf("expected 0 Kafka publish calls after push failure, got %d", len(kafkaCalls))
	}
}

func TestPublishSegment_NilPusher_StillPublishesKafkaEvent(t *testing.T) {
	segmentSpy := &spyPublisher{}

	// Use NewTestWorker (no pusher) to get a worker with nil segmentPusher.
	worker := NewTestWorker(
		WorkerConfig{
			WorkerID: "w-nopush",
			StreamID: "s-nopush",
			Log:      zap.NewNop(),
		},
		&spyPublisher{},
		segmentSpy,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00003.ts",
		SequenceNumber: 3,
		Size:           2048,
	}

	worker.publishSegment(context.Background(), segment)

	// Verify Kafka publish still happened despite no pusher.
	kafkaCalls := segmentSpy.getCalls()
	if len(kafkaCalls) != 1 {
		t.Fatalf("expected 1 Kafka publish call, got %d", len(kafkaCalls))
	}
	if kafkaCalls[0].key != "s-nopush" {
		t.Errorf("Kafka publish key = %q, want %q", kafkaCalls[0].key, "s-nopush")
	}
}
