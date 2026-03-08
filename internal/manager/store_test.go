package manager_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/goleak"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/internal/manager"
)

// mongoContainerImage is the Docker image used for integration tests.
const mongoContainerImage = "mongo:8.2.5"

// testTimeout is the maximum duration for a single test operation against MongoDB.
const testTimeout = 10 * time.Second

// containerStartupTimeout bounds how long testcontainers waits for MongoDB to
// accept connections. Thirty seconds accommodates cold-pull in local dev.
const containerStartupTimeout = 30 * time.Second

// containerSetupTimeout bounds the overall container creation and startup.
// Sixty seconds covers image pull + container start on slow machines.
const containerSetupTimeout = 60 * time.Second

// containerCleanupTimeout bounds container termination and client disconnect.
const containerCleanupTimeout = 10 * time.Second

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// skipIfShort skips integration tests that require Docker unless running in CI.
func skipIfShort(t *testing.T) {
	t.Helper()

	if os.Getenv("CI") == "" && testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
}

func setupMongoDB(t *testing.T) *mongo.Database {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), containerSetupTimeout)
	defer cancel()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        mongoContainerImage,
			ExposedPorts: []string{"27017/tcp"},
			WaitingFor:   wait.ForListeningPort("27017/tcp").WithStartupTimeout(containerStartupTimeout),
		},
		Started: true,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
		defer cleanupCancel()
		_ = container.Terminate(cleanupCtx)
	})

	endpoint, err := container.Endpoint(ctx, "mongodb")
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(endpoint))
	require.NoError(t, err)

	t.Cleanup(func() {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
		defer disconnectCancel()
		_ = client.Disconnect(disconnectCtx)
	})

	databaseName := "streamline_test_" + t.Name()
	return client.Database(databaseName)
}

func TestStore_Create(t *testing.T) {
	skipIfShort(t)

	database := setupMongoDB(t)
	store := manager.NewStore(manager.StoreConfig{
		Database: database,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	input := &streamlinev1.Stream{
		SourceUri: "rtmp://localhost:1935/live/test",
		EncodingProfile: &streamlinev1.EncodingProfile{
			Codec:            "h264",
			Width:            1920,
			Height:           1080,
			BitrateKbps:      4500,
			Framerate:        30,
			SegmentDurationS: 6,
		},
	}

	created, err := store.Create(ctx, input)
	require.NoError(t, err)

	assert.NotEmpty(t, created.GetId(), "ID should be populated")
	assert.Equal(t, streamlinev1.StreamState_STREAM_STATE_PROVISIONING, created.GetState(), "state should be PROVISIONING")
	assert.NotNil(t, created.GetCreatedAt(), "CreatedAt should not be nil")
	assert.NotNil(t, created.GetUpdatedAt(), "UpdatedAt should not be nil")
	assert.Equal(t, input.GetSourceUri(), created.GetSourceUri())
	assert.Equal(t, input.GetEncodingProfile().GetCodec(), created.GetEncodingProfile().GetCodec())
}

func TestStore_Get(t *testing.T) {
	skipIfShort(t)

	database := setupMongoDB(t)
	store := manager.NewStore(manager.StoreConfig{
		Database: database,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	seeded, err := store.Create(ctx, &streamlinev1.Stream{
		SourceUri: "rtmp://localhost:1935/live/get-test",
		EncodingProfile: &streamlinev1.EncodingProfile{
			Codec:  "h264",
			Width:  1280,
			Height: 720,
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name        string
		streamID    string
		expectError error
	}{
		{
			name:     "existing stream",
			streamID: seeded.GetId(),
		},
		{
			name:        "not found",
			streamID:    "nonexistent-id-12345",
			expectError: manager.ErrStreamNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.Get(ctx, tt.streamID)

			if tt.expectError != nil {
				require.ErrorIs(t, err, tt.expectError)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.streamID, result.GetId())
		})
	}
}

func TestStore_UpdateState(t *testing.T) {
	skipIfShort(t)

	database := setupMongoDB(t)
	store := manager.NewStore(manager.StoreConfig{
		Database: database,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	tests := []struct {
		name        string
		newState    streamlinev1.StreamState
		setupStream bool
		streamID    string
		expectError error
	}{
		{
			name:        "transitions to active",
			newState:    streamlinev1.StreamState_STREAM_STATE_ACTIVE,
			setupStream: true,
		},
		{
			name:        "transitions to stopped",
			newState:    streamlinev1.StreamState_STREAM_STATE_STOPPED,
			setupStream: true,
		},
		{
			name:        "not found",
			newState:    streamlinev1.StreamState_STREAM_STATE_ACTIVE,
			streamID:    "nonexistent-id-12345",
			expectError: manager.ErrStreamNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetID := tt.streamID
			if tt.setupStream {
				created, err := store.Create(ctx, &streamlinev1.Stream{
					SourceUri: "rtmp://localhost:1935/live/" + tt.name,
					EncodingProfile: &streamlinev1.EncodingProfile{
						Codec:  "h264",
						Width:  1280,
						Height: 720,
					},
				})
				require.NoError(t, err)
				targetID = created.GetId()
			}

			err := store.UpdateState(ctx, targetID, tt.newState)

			if tt.expectError != nil {
				require.ErrorIs(t, err, tt.expectError)
				return
			}

			require.NoError(t, err)

			updated, err := store.Get(ctx, targetID)
			require.NoError(t, err)
			assert.Equal(t, tt.newState, updated.GetState())
		})
	}
}

func TestStore_FindBySourceURI(t *testing.T) {
	skipIfShort(t)

	database := setupMongoDB(t)
	store := manager.NewStore(manager.StoreConfig{
		Database: database,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	seeded, err := store.Create(ctx, &streamlinev1.Stream{
		SourceUri: "rtmp://localhost:1935/live/find-test",
		EncodingProfile: &streamlinev1.EncodingProfile{
			Codec:  "h264",
			Width:  1920,
			Height: 1080,
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		sourceURI string
		expectNil bool
		expectID  string
	}{
		{
			name:      "found",
			sourceURI: "rtmp://localhost:1935/live/find-test",
			expectID:  seeded.GetId(),
		},
		{
			name:      "not found returns nil",
			sourceURI: "rtmp://localhost:1935/live/nonexistent",
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.FindBySourceURI(ctx, tt.sourceURI)
			require.NoError(t, err)

			if tt.expectNil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expectID, result.GetId())
		})
	}
}
