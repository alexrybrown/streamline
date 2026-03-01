package encoder_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
	"github.com/alexrybrown/streamline/internal/encoder"
)

// fakePackagerHandler is a hand-written test double that captures PushSegment
// calls for assertion. It implements PackagerServiceHandler directly — no
// embedding of UnimplementedPackagerServiceHandler since the interface has
// only one method.
type fakePackagerHandler struct {
	// Captured fields from the last PushSegment call.
	metadata     *streamlinev1.SegmentMetadata
	receivedData []byte
	callCount    int
	returnError  error
}

// Compile-time assertion that fakePackagerHandler implements the handler interface.
var _ streamlinev1connect.PackagerServiceHandler = (*fakePackagerHandler)(nil)

func (handler *fakePackagerHandler) PushSegment(
	ctx context.Context,
	stream *connect.ClientStream[streamlinev1.PushSegmentRequest],
) (*connect.Response[streamlinev1.PushSegmentResponse], error) {
	handler.callCount++

	if handler.returnError != nil {
		return nil, handler.returnError
	}

	// First message must be metadata.
	if !stream.Receive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("empty stream"))
	}
	handler.metadata = stream.Msg().GetMetadata()

	// Read all chunks.
	var buffer bytes.Buffer
	for stream.Receive() {
		chunk := stream.Msg().GetChunk()
		buffer.Write(chunk)
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}

	handler.receivedData = buffer.Bytes()

	return connect.NewResponse(&streamlinev1.PushSegmentResponse{
		BytesReceived: int64(buffer.Len()),
		Timestamp:     timestamppb.Now(),
	}), nil
}

func newFakePackagerServer(handler *fakePackagerHandler) *httptest.Server {
	mux := http.NewServeMux()
	path, handlerHTTP := streamlinev1connect.NewPackagerServiceHandler(handler)
	mux.Handle(path, handlerHTTP)
	return httptest.NewUnstartedServer(mux)
}

// makeLargeSegmentData creates a deterministic byte slice of the given size
// for testing chunked streaming.
func makeLargeSegmentData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return data
}

func TestConnectSegmentPusher_HappyPath(t *testing.T) {
	// largeSegmentSize requires multiple 64 KiB chunks to verify chunked streaming.
	const largeSegmentSize = 200 * 1024

	tests := []struct {
		name           string
		segmentData    []byte
		sequenceNumber int64
		streamID       string
		duration       float64
	}{
		{
			name:           "PushesSegment",
			segmentData:    []byte("test segment data for push verification"),
			sequenceNumber: 1,
			streamID:       "stream-1",
			duration:       6.0,
		},
		{
			name:           "LargeSegment",
			segmentData:    makeLargeSegmentData(largeSegmentSize),
			sequenceNumber: 2,
			streamID:       "stream-1",
			duration:       6.0,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := &fakePackagerHandler{}
			server := newFakePackagerServer(handler)
			server.EnableHTTP2 = true
			server.Start()
			defer server.Close()

			// Create a fake segment file.
			segmentDir := t.TempDir()
			segmentPath := filepath.Join(segmentDir, fmt.Sprintf("segment_%05d.ts", testCase.sequenceNumber))
			if err := os.WriteFile(segmentPath, testCase.segmentData, 0o644); err != nil {
				t.Fatal(err)
			}

			pusher, err := encoder.NewConnectSegmentPusher(encoder.SegmentPusherConfig{
				PackagerURL: server.URL,
			})
			if err != nil {
				t.Fatalf("unexpected constructor error: %v", err)
			}

			segment := encoder.Segment{
				Path:           segmentPath,
				SequenceNumber: testCase.sequenceNumber,
				Size:           int64(len(testCase.segmentData)),
			}

			err = pusher.PushSegment(context.Background(), testCase.streamID, segment, testCase.duration)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify metadata.
			if handler.metadata == nil {
				t.Fatal("expected metadata, got nil")
			}
			if handler.metadata.GetStreamId() != testCase.streamID {
				t.Errorf("stream_id: got %q, want %q", handler.metadata.GetStreamId(), testCase.streamID)
			}
			if handler.metadata.GetSequenceNumber() != testCase.sequenceNumber {
				t.Errorf("sequence_number: got %d, want %d", handler.metadata.GetSequenceNumber(), testCase.sequenceNumber)
			}
			if handler.metadata.GetDurationSeconds() != testCase.duration {
				t.Errorf("duration_seconds: got %f, want %f", handler.metadata.GetDurationSeconds(), testCase.duration)
			}

			// Verify received data matches the file.
			if !bytes.Equal(handler.receivedData, testCase.segmentData) {
				t.Errorf("received data mismatch: got %d bytes, want %d bytes",
					len(handler.receivedData), len(testCase.segmentData))
			}
		})
	}
}

func TestConnectSegmentPusher_Errors(t *testing.T) {
	tests := []struct {
		name         string
		segmentPath  string
		writeFile    bool
		fileData     []byte
		handlerError error
	}{
		{
			name:        "FileNotFound",
			segmentPath: "/nonexistent/segment_00001.ts",
			writeFile:   false,
		},
		{
			name:         "ServerError",
			writeFile:    true,
			fileData:     []byte("data"),
			handlerError: connect.NewError(connect.CodeInternal, fmt.Errorf("disk full")),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			handler := &fakePackagerHandler{
				returnError: testCase.handlerError,
			}
			server := newFakePackagerServer(handler)
			server.EnableHTTP2 = true
			server.Start()
			defer server.Close()

			segmentPath := testCase.segmentPath
			if testCase.writeFile {
				segmentDir := t.TempDir()
				segmentPath = filepath.Join(segmentDir, "segment_00001.ts")
				if err := os.WriteFile(segmentPath, testCase.fileData, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			pusher, err := encoder.NewConnectSegmentPusher(encoder.SegmentPusherConfig{
				PackagerURL: server.URL,
			})
			if err != nil {
				t.Fatalf("unexpected constructor error: %v", err)
			}

			segment := encoder.Segment{
				Path:           segmentPath,
				SequenceNumber: 1,
				Size:           int64(len(testCase.fileData)),
			}

			err = pusher.PushSegment(context.Background(), "stream-1", segment, 6.0)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
