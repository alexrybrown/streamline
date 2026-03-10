package packager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
)

const (
	// segmentFilenameFormat is the format string for segment filenames.
	// Uses 5-digit zero-padded sequence numbers (e.g., segment_00003.ts).
	segmentFilenameFormat = "segment_%05d.ts"

	// tempFileSuffix is appended to segment filenames during writing
	// to enable atomic rename on completion.
	tempFileSuffix = ".tmp"
)

// SegmentWrittenFunc is called after a segment is successfully written to disk.
// It receives the stream ID and segment metadata. This enables the service
// layer to update manifests and publish events without tight coupling.
type SegmentWrittenFunc func(streamID string, segment SegmentInfo)

// ReceiverConfig holds configuration for the segment Receiver.
type ReceiverConfig struct {
	OutputDir        string
	Log              *zap.Logger
	OnSegmentWritten SegmentWrittenFunc
}

// Receiver implements the PackagerServiceHandler interface. It receives
// streamed segment data from encoder workers and writes segments to disk.
type Receiver struct {
	outputDir        string
	log              *zap.Logger
	onSegmentWritten SegmentWrittenFunc

	// dedup tracks segments that have already been written to disk.
	// Key format: "streamID/sequenceNumber".
	dedup   map[string]struct{}
	dedupMu sync.Mutex
}

// NewReceiver creates a new Receiver with the given configuration.
// OutputDir must be non-empty. A nil Log defaults to a no-op logger.
func NewReceiver(cfg ReceiverConfig) *Receiver {
	if cfg.OutputDir == "" {
		panic("packager.ReceiverConfig: OutputDir must not be empty")
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	return &Receiver{
		outputDir:        cfg.OutputDir,
		log:              cfg.Log.Named("receiver"),
		onSegmentWritten: cfg.OnSegmentWritten,
		dedup:            make(map[string]struct{}),
	}
}

// ResetStream clears all dedup entries for the given stream, allowing
// segments with previously-seen sequence numbers to be accepted again.
// This is called when a stream is stopped and restarted.
func (receiver *Receiver) ResetStream(streamID string) {
	receiver.dedupMu.Lock()
	prefix := streamID + "/"
	for key := range receiver.dedup {
		if strings.HasPrefix(key, prefix) {
			delete(receiver.dedup, key)
		}
	}
	receiver.dedupMu.Unlock()

	receiver.log.Info("stream dedup state reset",
		zap.String("method", "ResetStream"),
		zap.String("streamID", streamID),
	)
}

// PushSegment receives a client-streaming RPC containing segment metadata
// followed by one or more data chunks. The first message must contain
// SegmentMetadata; subsequent messages must contain chunk bytes.
func (receiver *Receiver) PushSegment(ctx context.Context, stream *connect.ClientStream[streamlinev1.PushSegmentRequest]) (*connect.Response[streamlinev1.PushSegmentResponse], error) {
	log := receiver.log.With(zap.String("method", "PushSegment"))

	// First message must be metadata.
	if !stream.Receive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("empty stream: expected metadata as first message"))
	}
	metadata := stream.Msg().GetMetadata()
	if metadata == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first message must contain segment metadata"))
	}

	streamID := metadata.GetStreamId()
	sequenceNumber := metadata.GetSequenceNumber()
	dedupKey := fmt.Sprintf("%s/%d", streamID, sequenceNumber)

	log = log.With(
		zap.String("streamID", streamID),
		zap.Int64("sequenceNumber", sequenceNumber),
	)

	log.Info("receiving segment")

	// Check for duplicate segment.
	receiver.dedupMu.Lock()
	_, duplicate := receiver.dedup[dedupKey]
	if !duplicate {
		receiver.dedup[dedupKey] = struct{}{}
	}
	receiver.dedupMu.Unlock()

	if duplicate {
		// Drain the stream but skip disk writes.
		var bytesReceived int64
		for stream.Receive() {
			chunk := stream.Msg().GetChunk()
			bytesReceived += int64(len(chunk))
		}
		if err := stream.Err(); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("stream error during dedup drain: %w", err))
		}
		log.Info("duplicate segment skipped")
		return connect.NewResponse(&streamlinev1.PushSegmentResponse{
			BytesReceived: bytesReceived,
			Timestamp:     timestamppb.New(time.Now()),
		}), nil
	}

	// Create the stream output directory.
	streamDir := filepath.Join(receiver.outputDir, streamID)
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create stream directory: %w", err))
	}

	// Write chunks to a temp file for atomic rename on completion.
	segmentFilename := fmt.Sprintf(segmentFilenameFormat, sequenceNumber)
	finalPath := filepath.Join(streamDir, segmentFilename)
	tempPath := finalPath + tempFileSuffix

	tempFile, err := os.Create(filepath.Clean(tempPath))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create temp file: %w", err))
	}
	defer func() {
		// Clean up temp file on error; on success it will have been renamed.
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	var bytesReceived int64
	for stream.Receive() {
		chunk := stream.Msg().GetChunk()
		written, writeErr := tempFile.Write(chunk)
		if writeErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write chunk: %w", writeErr))
		}
		bytesReceived += int64(written)
	}
	if err := stream.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("stream error: %w", err))
	}

	// Sync and close before rename to ensure data is flushed.
	if err := tempFile.Sync(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sync temp file: %w", err))
	}
	if err := tempFile.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("close temp file: %w", err))
	}

	// Atomic rename from temp to final path.
	if err := os.Rename(tempPath, finalPath); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("rename temp file to final: %w", err))
	}

	if receiver.onSegmentWritten != nil {
		receiver.onSegmentWritten(streamID, SegmentInfo{
			Filename:        segmentFilename,
			SequenceNumber:  sequenceNumber,
			DurationSeconds: metadata.GetDurationSeconds(),
		})
	}

	log.Info("segment written",
		zap.Int64("bytesReceived", bytesReceived),
	)

	return connect.NewResponse(&streamlinev1.PushSegmentResponse{
		BytesReceived: bytesReceived,
		Timestamp:     timestamppb.New(time.Now()),
	}), nil
}
