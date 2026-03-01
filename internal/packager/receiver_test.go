package packager_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
	"github.com/alexrybrown/streamline/internal/packager"
)

func newTestClient(t *testing.T, outputDir string) streamlinev1connect.PackagerServiceClient {
	t.Helper()
	receiver := packager.NewReceiver(packager.ReceiverConfig{
		OutputDir: outputDir,
		Log:       zap.NewNop(),
	})
	mux := http.NewServeMux()
	path, handler := streamlinev1connect.NewPackagerServiceHandler(receiver)
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.Start()
	t.Cleanup(server.Close)
	return streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)
}

func pushSegment(t *testing.T, client streamlinev1connect.PackagerServiceClient, streamID string, sequenceNumber int64, duration float64, data []byte) *connect.Response[streamlinev1.PushSegmentResponse] {
	t.Helper()
	ctx := context.Background()
	stream := client.PushSegment(ctx)

	// Send metadata first
	if err := stream.Send(&streamlinev1.PushSegmentRequest{
		Payload: &streamlinev1.PushSegmentRequest_Metadata{
			Metadata: &streamlinev1.SegmentMetadata{
				StreamId:        streamID,
				SequenceNumber:  sequenceNumber,
				DurationSeconds: duration,
			},
		},
	}); err != nil {
		t.Fatalf("send metadata: %v", err)
	}

	// Send data in chunks (64KiB)
	const chunkSize = 64 * 1024
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := stream.Send(&streamlinev1.PushSegmentRequest{
			Payload: &streamlinev1.PushSegmentRequest_Chunk{
				Chunk: data[offset:end],
			},
		}); err != nil {
			t.Fatalf("send chunk at offset %d: %v", offset, err)
		}
	}

	response, err := stream.CloseAndReceive()
	if err != nil {
		t.Fatalf("close and receive: %v", err)
	}
	return response
}

func TestReceiver_AcceptsSegment(t *testing.T) {
	outputDir := t.TempDir()
	client := newTestClient(t, outputDir)

	segmentData := []byte("fake segment data for testing")
	response := pushSegment(t, client, "stream-1", 3, 6.0, segmentData)

	if response.Msg.BytesReceived != int64(len(segmentData)) {
		t.Errorf("expected %d bytes received, got %d", len(segmentData), response.Msg.BytesReceived)
	}

	// Verify file was written
	segmentPath := filepath.Join(outputDir, "stream-1", "segment_00003.ts")
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("segment file not found: %v", err)
	}
	if string(data) != string(segmentData) {
		t.Errorf("unexpected segment content: %s", string(data))
	}
}

func TestReceiver_DeduplicatesSegment(t *testing.T) {
	outputDir := t.TempDir()
	client := newTestClient(t, outputDir)

	segmentData := []byte("fake segment data")

	// Push same segment twice
	for i := 0; i < 2; i++ {
		pushSegment(t, client, "stream-1", 3, 6.0, segmentData)
	}

	// Should still have the original content (idempotent)
	segmentPath := filepath.Join(outputDir, "stream-1", "segment_00003.ts")
	info, err := os.Stat(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(segmentData)) {
		t.Errorf("unexpected file size: %d", info.Size())
	}
}

func TestReceiver_RejectsMissingMetadata(t *testing.T) {
	outputDir := t.TempDir()
	client := newTestClient(t, outputDir)

	// Send chunk without metadata first
	stream := client.PushSegment(context.Background())
	err := stream.Send(&streamlinev1.PushSegmentRequest{
		Payload: &streamlinev1.PushSegmentRequest_Chunk{
			Chunk: []byte("data without metadata"),
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	_, err = stream.CloseAndReceive()
	if err == nil {
		t.Fatal("expected error for missing metadata, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", connect.CodeOf(err))
	}
}

func TestNewReceiver_PanicsOnEmptyOutputDir(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic for empty OutputDir, but none occurred")
		}
		message, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", recovered, recovered)
		}
		const expectedMessage = "packager.ReceiverConfig: OutputDir must not be empty"
		if message != expectedMessage {
			t.Errorf("unexpected panic message:\n  got:  %s\n  want: %s", message, expectedMessage)
		}
	}()

	packager.NewReceiver(packager.ReceiverConfig{
		OutputDir: "",
		Log:       zap.NewNop(),
	})
}

func TestNewReceiver_NilLogDefaultsToNop(t *testing.T) {
	outputDir := t.TempDir()

	// Create a receiver with nil Log to exercise the default nop-logger path.
	receiver := packager.NewReceiver(packager.ReceiverConfig{
		OutputDir: outputDir,
		Log:       nil,
	})

	mux := http.NewServeMux()
	path, handler := streamlinev1connect.NewPackagerServiceHandler(receiver)
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.Start()
	t.Cleanup(server.Close)
	client := streamlinev1connect.NewPackagerServiceClient(server.Client(), server.URL)

	// Verify the receiver works without a logger by pushing a segment.
	segmentData := []byte("data with nil log")
	response := pushSegment(t, client, "stream-nil-log", 1, 6.0, segmentData)

	if response.Msg.BytesReceived != int64(len(segmentData)) {
		t.Errorf("expected %d bytes received, got %d", len(segmentData), response.Msg.BytesReceived)
	}
}

func TestReceiver_EmptyStreamReturnsInvalidArgument(t *testing.T) {
	outputDir := t.TempDir()
	client := newTestClient(t, outputDir)

	// Open the stream, send nothing, and close immediately.
	stream := client.PushSegment(context.Background())
	_, err := stream.CloseAndReceive()

	if err == nil {
		t.Fatal("expected error for empty stream, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %v", connect.CodeOf(err))
	}
}

func TestReceiver_LargeSegmentChunked(t *testing.T) {
	outputDir := t.TempDir()
	client := newTestClient(t, outputDir)

	// Create a segment larger than one chunk (64KiB)
	segmentData := make([]byte, 200*1024) // 200KiB
	for i := range segmentData {
		segmentData[i] = byte(i % 256)
	}

	response := pushSegment(t, client, "stream-1", 1, 6.0, segmentData)

	if response.Msg.BytesReceived != int64(len(segmentData)) {
		t.Errorf("expected %d bytes received, got %d", len(segmentData), response.Msg.BytesReceived)
	}

	// Verify file content matches
	segmentPath := filepath.Join(outputDir, "stream-1", "segment_00001.ts")
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatalf("segment file not found: %v", err)
	}
	if !bytes.Equal(data, segmentData) {
		t.Fatalf("segment content mismatch: expected %d bytes, got %d bytes", len(segmentData), len(data))
	}
}
