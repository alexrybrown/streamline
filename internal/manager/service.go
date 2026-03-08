package manager

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
)

// streamStore defines the persistence operations the service requires.
type streamStore interface {
	Create(ctx context.Context, stream *streamlinev1.Stream) (*streamlinev1.Stream, error)
	Get(ctx context.Context, streamID string) (*streamlinev1.Stream, error)
	UpdateState(ctx context.Context, streamID string, state streamlinev1.StreamState) error
	FindBySourceURI(ctx context.Context, sourceURI string) (*streamlinev1.Stream, error)
}

// ServiceConfig holds the dependencies for creating a Service.
type ServiceConfig struct {
	Store streamStore
	Log   *zap.Logger
}

// Service implements the StreamManagerService ConnectRPC handler.
type Service struct {
	store streamStore
	log   *zap.Logger
}

// Compile-time assertion that Service implements StreamManagerServiceHandler.
var _ streamlinev1connect.StreamManagerServiceHandler = (*Service)(nil)

// NewService creates a new stream manager Service. It panics if Store is nil
// and defaults Log to a no-op logger when not provided.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Store == nil {
		panic("manager.NewService: Store is required")
	}

	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	log = log.Named("service")

	return &Service{
		store: cfg.Store,
		log:   log,
	}
}

// StartStream creates a new stream or returns an existing one for the same
// source URI (idempotent).
func (service *Service) StartStream(ctx context.Context, request *connect.Request[streamlinev1.StartStreamRequest]) (*connect.Response[streamlinev1.StartStreamResponse], error) {
	log := service.log.With(zap.String("method", "StartStream"))

	sourceURI := request.Msg.GetSourceUri()

	// Check for an existing stream with the same source URI (idempotent).
	existing, err := service.store.FindBySourceURI(ctx, sourceURI)
	if err != nil {
		log.Error("failed to find stream by source URI", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("find stream by source URI: %w", err))
	}
	if existing != nil {
		log.Info("returning existing stream", zap.String("streamID", existing.GetId()))
		return connect.NewResponse(&streamlinev1.StartStreamResponse{
			Stream: existing,
		}), nil
	}

	// Create a new stream record.
	stream, err := service.store.Create(ctx, &streamlinev1.Stream{
		SourceUri:       sourceURI,
		EncodingProfile: request.Msg.GetEncodingProfile(),
	})
	if err != nil {
		log.Error("failed to create stream", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create stream: %w", err))
	}

	log.Info("created stream", zap.String("streamID", stream.GetId()))

	return connect.NewResponse(&streamlinev1.StartStreamResponse{
		Stream: stream,
	}), nil
}

// StopStream transitions a stream to the STOPPED state.
func (service *Service) StopStream(ctx context.Context, request *connect.Request[streamlinev1.StopStreamRequest]) (*connect.Response[streamlinev1.StopStreamResponse], error) {
	stream, err := service.updateStateAndGet(ctx, "StopStream", request.Msg.GetStreamId(), streamlinev1.StreamState_STREAM_STATE_STOPPED)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&streamlinev1.StopStreamResponse{
		Stream: stream,
	}), nil
}

// RestartStream transitions a stream back to the PROVISIONING state.
func (service *Service) RestartStream(ctx context.Context, request *connect.Request[streamlinev1.RestartStreamRequest]) (*connect.Response[streamlinev1.RestartStreamResponse], error) {
	stream, err := service.updateStateAndGet(ctx, "RestartStream", request.Msg.GetStreamId(), streamlinev1.StreamState_STREAM_STATE_PROVISIONING)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&streamlinev1.RestartStreamResponse{
		Stream: stream,
	}), nil
}

// updateStateAndGet transitions a stream to the given state and returns the
// updated stream. It maps store errors to appropriate ConnectRPC error codes.
func (service *Service) updateStateAndGet(ctx context.Context, method string, streamID string, state streamlinev1.StreamState) (*streamlinev1.Stream, error) {
	log := service.log.With(zap.String("method", method))

	if err := service.store.UpdateState(ctx, streamID, state); err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("stream %s not found", streamID))
		}
		log.Error("failed to update stream state", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update stream state: %w", err))
	}

	stream, err := service.store.Get(ctx, streamID)
	if err != nil {
		log.Error("failed to get stream after state update", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get stream: %w", err))
	}

	log.Info("updated stream state", zap.String("streamID", streamID), zap.String("newState", state.String()))

	return stream, nil
}
