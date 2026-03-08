package manager_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/internal/manager"
)

// fakeStore is a hand-written test double that implements manager.StreamStore
// using an in-memory map.
type fakeStore struct {
	mu        sync.Mutex
	idCounter int
	streams   map[string]*streamlinev1.Stream
}

var _ manager.StreamStore = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		streams: make(map[string]*streamlinev1.Stream),
	}
}

func (store *fakeStore) Create(_ context.Context, stream *streamlinev1.Stream) (*streamlinev1.Stream, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.idCounter++
	created := &streamlinev1.Stream{
		Id:              fmt.Sprintf("fake-id-%d", store.idCounter),
		SourceUri:       stream.GetSourceUri(),
		State:           streamlinev1.StreamState_STREAM_STATE_PROVISIONING,
		EncodingProfile: stream.GetEncodingProfile(),
	}

	store.streams[created.GetId()] = created

	return created, nil
}

func (store *fakeStore) Get(_ context.Context, streamID string) (*streamlinev1.Stream, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	stream, exists := store.streams[streamID]
	if !exists {
		return nil, manager.ErrStreamNotFound
	}

	return stream, nil
}

func (store *fakeStore) UpdateState(_ context.Context, streamID string, state streamlinev1.StreamState) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	stream, exists := store.streams[streamID]
	if !exists {
		return manager.ErrStreamNotFound
	}

	stream.State = state

	return nil
}

func (store *fakeStore) FindBySourceURI(_ context.Context, sourceURI string) (*streamlinev1.Stream, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	for _, stream := range store.streams {
		if stream.GetSourceUri() == sourceURI {
			return stream, nil
		}
	}

	return nil, nil
}

func TestService_StartStream(t *testing.T) {
	tests := []struct {
		name            string
		seedSourceURI   string
		requestURI      string
		expectNewStream bool
	}{
		{
			name:            "creates new stream",
			requestURI:      "rtmp://localhost:1935/live/new-stream",
			expectNewStream: true,
		},
		{
			name:          "idempotent for same source URI",
			seedSourceURI: "rtmp://localhost:1935/live/existing",
			requestURI:    "rtmp://localhost:1935/live/existing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			service := manager.NewService(manager.ServiceConfig{
				Store: store,
			})

			ctx := context.Background()

			// Seed an existing stream if specified.
			if tt.seedSourceURI != "" {
				seedRequest := connect.NewRequest(&streamlinev1.StartStreamRequest{
					SourceUri: tt.seedSourceURI,
				})
				_, err := service.StartStream(ctx, seedRequest)
				require.NoError(t, err)
			}

			request := connect.NewRequest(&streamlinev1.StartStreamRequest{
				SourceUri: tt.requestURI,
			})

			response, err := service.StartStream(ctx, request)
			require.NoError(t, err)

			stream := response.Msg.GetStream()
			assert.NotEmpty(t, stream.GetId(), "stream ID should not be empty")
			assert.Equal(t, streamlinev1.StreamState_STREAM_STATE_PROVISIONING, stream.GetState())

			if !tt.expectNewStream {
				// For idempotent case, calling again should return the same ID.
				secondResponse, err := service.StartStream(ctx, request)
				require.NoError(t, err)
				assert.Equal(t, stream.GetId(), secondResponse.Msg.GetStream().GetId(),
					"idempotent call should return same stream ID")
			}
		})
	}
}

func TestService_StopStream(t *testing.T) {
	tests := []struct {
		name        string
		setupStream bool
		streamID    string
		expectCode  connect.Code
	}{
		{
			name:        "stops running stream",
			setupStream: true,
		},
		{
			name:       "not found",
			streamID:   "nonexistent-id",
			expectCode: connect.CodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			service := manager.NewService(manager.ServiceConfig{
				Store: store,
			})

			ctx := context.Background()

			targetID := tt.streamID
			if tt.setupStream {
				startRequest := connect.NewRequest(&streamlinev1.StartStreamRequest{
					SourceUri: "rtmp://localhost:1935/live/stop-test",
				})
				startResponse, err := service.StartStream(ctx, startRequest)
				require.NoError(t, err)
				targetID = startResponse.Msg.GetStream().GetId()
			}

			request := connect.NewRequest(&streamlinev1.StopStreamRequest{
				StreamId: targetID,
			})

			response, err := service.StopStream(ctx, request)

			if tt.expectCode != 0 {
				require.Error(t, err)
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				assert.Equal(t, tt.expectCode, connectErr.Code())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, streamlinev1.StreamState_STREAM_STATE_STOPPED, response.Msg.GetStream().GetState())
		})
	}
}

func TestService_RestartStream(t *testing.T) {
	tests := []struct {
		name        string
		setupStream bool
		streamID    string
		expectCode  connect.Code
	}{
		{
			name:        "restarts existing stream",
			setupStream: true,
		},
		{
			name:       "not found",
			streamID:   "nonexistent-id",
			expectCode: connect.CodeNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			service := manager.NewService(manager.ServiceConfig{
				Store: store,
			})

			ctx := context.Background()

			targetID := tt.streamID
			if tt.setupStream {
				startRequest := connect.NewRequest(&streamlinev1.StartStreamRequest{
					SourceUri: "rtmp://localhost:1935/live/restart-test",
				})
				startResponse, err := service.StartStream(ctx, startRequest)
				require.NoError(t, err)
				targetID = startResponse.Msg.GetStream().GetId()
			}

			request := connect.NewRequest(&streamlinev1.RestartStreamRequest{
				StreamId: targetID,
			})

			response, err := service.RestartStream(ctx, request)

			if tt.expectCode != 0 {
				require.Error(t, err)
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				assert.Equal(t, tt.expectCode, connectErr.Code())
				return
			}

			require.NoError(t, err)
			assert.Equal(t, streamlinev1.StreamState_STREAM_STATE_PROVISIONING, response.Msg.GetStream().GetState())
		})
	}
}
