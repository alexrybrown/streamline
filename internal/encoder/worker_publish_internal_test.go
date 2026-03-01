package encoder

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/proto"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
)

// spyPublisher records Publish calls and optionally returns a custom error.
type spyPublisher struct {
	mu          sync.Mutex
	publishFunc func(ctx context.Context, key string, value []byte) error
	calls       []publishCall
	closed      bool
}

type publishCall struct {
	key   string
	value []byte
}

func (s *spyPublisher) Publish(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, publishCall{key: key, value: value})
	if s.publishFunc != nil {
		return s.publishFunc(ctx, key, value)
	}
	return nil
}

func (s *spyPublisher) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *spyPublisher) getCalls() []publishCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]publishCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// noopFactory is used in tests that don't exercise FFmpeg.
func noopFactory(_ FFmpegConfig) (FFmpegRunner, error) {
	return nil, errors.New("not used in publish tests")
}

func TestEmitHeartbeats_PublishError_LogsWarning(t *testing.T) {
	defer goleak.VerifyNone(t)

	core, logs := observer.New(zap.WarnLevel)

	spy := &spyPublisher{
		publishFunc: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("broker down")
		},
	}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID:          "w-1",
			StreamID:          "s-1",
			HeartbeatInterval: 50 * time.Millisecond,
			Log:               zap.New(core),
		},
		spy,
		&spyPublisher{},
		noopFactory,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	worker.emitHeartbeats(ctx)

	warnings := logs.FilterMessage("failed to publish heartbeat")
	if warnings.Len() == 0 {
		t.Fatal("expected at least one 'failed to publish heartbeat' warning log")
	}
}

func TestEmitHeartbeats_PublishSuccess_CorrectPayload(t *testing.T) {
	defer goleak.VerifyNone(t)

	spy := &spyPublisher{}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID:          "w-payload",
			StreamID:          "s-payload",
			HeartbeatInterval: 50 * time.Millisecond,
			Log:               zap.NewNop(),
		},
		spy,
		&spyPublisher{},
		noopFactory,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	worker.emitHeartbeats(ctx)

	calls := spy.getCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one publish call")
	}

	firstCall := calls[0]
	if firstCall.key != "s-payload" {
		t.Errorf("expected key %q, got %q", "s-payload", firstCall.key)
	}

	var heartbeat streamlinev1.Heartbeat
	if err := proto.Unmarshal(firstCall.value, &heartbeat); err != nil {
		t.Fatalf("failed to unmarshal heartbeat payload: %v", err)
	}

	if heartbeat.GetWorkerId() != "w-payload" {
		t.Errorf("worker_id = %q, want %q", heartbeat.GetWorkerId(), "w-payload")
	}
	if heartbeat.GetStreamId() != "s-payload" {
		t.Errorf("stream_id = %q, want %q", heartbeat.GetStreamId(), "s-payload")
	}
	if heartbeat.GetStatus() != streamlinev1.WorkerStatus_WORKER_STATUS_ENCODING {
		t.Errorf("status = %v, want ENCODING", heartbeat.GetStatus())
	}
	if heartbeat.GetTimestamp() == nil {
		t.Error("expected timestamp to be set")
	}
}

func TestPublishSegment_Error_LogsWarning(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)

	spy := &spyPublisher{
		publishFunc: func(_ context.Context, _ string, _ []byte) error {
			return errors.New("broker down")
		},
	}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID: "w-err",
			StreamID: "s-err",
			Log:      zap.New(core),
		},
		&spyPublisher{},
		spy,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00001.ts",
		SequenceNumber: 1,
		Size:           4096,
	}

	worker.publishSegment(context.Background(), segment)

	warnings := logs.FilterMessage("failed to publish segment event")
	if warnings.Len() == 0 {
		t.Fatal("expected 'failed to publish segment event' warning log")
	}
}

func TestPublishSegment_Success_LogsInfo(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)

	spy := &spyPublisher{}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID: "w-info",
			StreamID: "s-info",
			Log:      zap.New(core),
		},
		&spyPublisher{},
		spy,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00005.ts",
		SequenceNumber: 5,
		Size:           8192,
	}

	worker.publishSegment(context.Background(), segment)

	infoLogs := logs.FilterMessage("segment produced")
	if infoLogs.Len() == 0 {
		t.Fatal("expected 'segment produced' info log")
	}

	entry := infoLogs.All()[0]

	sequenceField := findField(entry.ContextMap(), "sequence")
	if sequenceField == nil {
		t.Fatal("missing 'sequence' field in log entry")
	}
	if *sequenceField != int64(5) {
		t.Errorf("expected sequence=5, got %v", *sequenceField)
	}

	sizeField := findField(entry.ContextMap(), "size")
	if sizeField == nil {
		t.Fatal("missing 'size' field in log entry")
	}
	if *sizeField != int64(8192) {
		t.Errorf("expected size=8192, got %v", *sizeField)
	}
}

func TestPublishSegment_CorrectPayload(t *testing.T) {
	spy := &spyPublisher{}

	worker := NewTestWorker(
		WorkerConfig{
			WorkerID: "w-pay",
			StreamID: "s-pay",
			Log:      zap.NewNop(),
		},
		&spyPublisher{},
		spy,
		noopFactory,
	)

	segment := Segment{
		Path:           "/tmp/segment_00010.ts",
		SequenceNumber: 10,
		Size:           16384,
	}

	worker.publishSegment(context.Background(), segment)

	calls := spy.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(calls))
	}

	call := calls[0]
	if call.key != "s-pay" {
		t.Errorf("expected key %q, got %q", "s-pay", call.key)
	}

	var event streamlinev1.SegmentProduced
	if err := proto.Unmarshal(call.value, &event); err != nil {
		t.Fatalf("failed to unmarshal segment payload: %v", err)
	}

	if event.GetStreamId() != "s-pay" {
		t.Errorf("stream_id = %q, want %q", event.GetStreamId(), "s-pay")
	}
	if event.GetWorkerId() != "w-pay" {
		t.Errorf("worker_id = %q, want %q", event.GetWorkerId(), "w-pay")
	}
	if event.GetSequenceNumber() != 10 {
		t.Errorf("sequence_number = %d, want 10", event.GetSequenceNumber())
	}
	if event.GetSizeBytes() != 16384 {
		t.Errorf("size_bytes = %d, want 16384", event.GetSizeBytes())
	}
	if event.GetTimestamp() == nil {
		t.Error("expected timestamp to be set")
	}
}

// findField looks up a named field in the context map returned by observer.
// The observer stores zap.Int64 fields as int64 values in the context map.
func findField(contextMap map[string]interface{}, name string) *int64 {
	value, ok := contextMap[name]
	if !ok {
		return nil
	}
	switch v := value.(type) {
	case int64:
		return &v
	case zapcore.Field:
		return &v.Integer
	default:
		return nil
	}
}
