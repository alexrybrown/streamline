package encoder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"go.uber.org/zap"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
)

const (
	// pushChunkSize is the size of each data chunk sent to the packager via
	// client-streaming RPC. 64 KiB balances throughput with memory usage,
	// matching the packager's expected chunk size.
	pushChunkSize = 64 * 1024
)

// segmentPusher pushes encoded segment data to the packager service.
type segmentPusher interface {
	PushSegment(ctx context.Context, streamID string, segment Segment, durationSeconds float64) error
}

// SegmentPusherConfig holds configuration for a ConnectSegmentPusher.
type SegmentPusherConfig struct {
	// PackagerURL is the base URL of the packager ConnectRPC service.
	PackagerURL string
	// Log is the parent logger. Nil defaults to a no-op logger.
	Log *zap.Logger
}

// ConnectSegmentPusher pushes segment files to the packager via ConnectRPC
// client-streaming. It reads the segment file from disk, sends metadata as the
// first message, then streams 64 KiB data chunks.
type ConnectSegmentPusher struct {
	client streamlinev1connect.PackagerServiceClient
	log    *zap.Logger
}

// NewConnectSegmentPusher creates a segment pusher targeting the given packager URL.
func NewConnectSegmentPusher(cfg SegmentPusherConfig) (*ConnectSegmentPusher, error) {
	if cfg.PackagerURL == "" {
		return nil, errors.New("encoder.SegmentPusherConfig: PackagerURL must not be empty")
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}

	client := streamlinev1connect.NewPackagerServiceClient(
		&http.Client{},
		cfg.PackagerURL,
	)

	return &ConnectSegmentPusher{
		client: client,
		log:    cfg.Log.Named("segment-pusher"),
	}, nil
}

// PushSegment reads the segment file from disk and streams it to the packager.
// The first message contains metadata (stream ID, sequence number, duration).
// Subsequent messages carry raw segment data in 64 KiB chunks.
func (pusher *ConnectSegmentPusher) PushSegment(ctx context.Context, streamID string, segment Segment, durationSeconds float64) error {
	log := pusher.log.With(
		zap.String("method", "PushSegment"),
		zap.String("streamID", streamID),
		zap.Int64("sequenceNumber", segment.SequenceNumber),
	)

	file, err := os.Open(segment.Path)
	if err != nil {
		return fmt.Errorf("open segment file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Warn("failed to close segment file", zap.Error(closeErr))
		}
	}()

	stream := pusher.client.PushSegment(ctx)

	// Send metadata as the first message.
	if err := stream.Send(&streamlinev1.PushSegmentRequest{
		Payload: &streamlinev1.PushSegmentRequest_Metadata{
			Metadata: &streamlinev1.SegmentMetadata{
				StreamId:        streamID,
				SequenceNumber:  segment.SequenceNumber,
				DurationSeconds: durationSeconds,
			},
		},
	}); err != nil {
		return fmt.Errorf("send metadata: %w", err)
	}

	// Stream file data in chunks.
	buffer := make([]byte, pushChunkSize)
	for {
		bytesRead, readErr := file.Read(buffer)
		if bytesRead > 0 {
			if sendErr := stream.Send(&streamlinev1.PushSegmentRequest{
				Payload: &streamlinev1.PushSegmentRequest_Chunk{
					Chunk: buffer[:bytesRead],
				},
			}); sendErr != nil {
				return fmt.Errorf("send chunk: %w", sendErr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read segment file: %w", readErr)
		}
	}

	response, err := stream.CloseAndReceive()
	if err != nil {
		return fmt.Errorf("close stream: %w", err)
	}

	log.Info("segment pushed",
		zap.Int64("bytesReceived", response.Msg.GetBytesReceived()),
	)

	return nil
}
