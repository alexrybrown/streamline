package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
)

// collectionName is the MongoDB collection used for stream documents.
const collectionName = "streams"

// indexCreationTimeout bounds how long the constructor waits for MongoDB to
// acknowledge the unique index on source_uri. Five seconds is sufficient for a
// single-field index on an empty or small collection.
const indexCreationTimeout = 5 * time.Second

// mongoIndexAscending avoids a magic number in index creation (MongoDB convention: 1 = ascending).
const mongoIndexAscending = 1

// ErrStreamNotFound is returned when a stream cannot be located by its ID.
var ErrStreamNotFound = errors.New("stream not found")

// encodingProfileDocument is the BSON-serializable representation of an encoding profile.
type encodingProfileDocument struct {
	Codec            string `bson:"codec"`
	Width            int32  `bson:"width"`
	Height           int32  `bson:"height"`
	BitrateKbps      int32  `bson:"bitrate_kbps"`
	Framerate        int32  `bson:"framerate"`
	SegmentDurationS int32  `bson:"segment_duration_s"`
}

// streamDocument is the BSON-serializable representation of a stream.
type streamDocument struct {
	ID              string                   `bson:"_id"`
	SourceURI       string                   `bson:"source_uri"`
	State           int32                    `bson:"state"`
	EncodingProfile *encodingProfileDocument `bson:"encoding_profile,omitempty"`
	CreatedAtNano   int64                    `bson:"created_at_nano"`
	UpdatedAtNano   int64                    `bson:"updated_at_nano"`
}

// toDocument converts a proto Stream to a BSON-serializable streamDocument.
func toDocument(stream *streamlinev1.Stream) streamDocument {
	document := streamDocument{
		ID:            stream.GetId(),
		SourceURI:     stream.GetSourceUri(),
		State:         int32(stream.GetState()),
		CreatedAtNano: stream.GetCreatedAt().AsTime().UnixNano(),
		UpdatedAtNano: stream.GetUpdatedAt().AsTime().UnixNano(),
	}

	if profile := stream.GetEncodingProfile(); profile != nil {
		document.EncodingProfile = &encodingProfileDocument{
			Codec:            profile.GetCodec(),
			Width:            profile.GetWidth(),
			Height:           profile.GetHeight(),
			BitrateKbps:      profile.GetBitrateKbps(),
			Framerate:        profile.GetFramerate(),
			SegmentDurationS: profile.GetSegmentDurationS(),
		}
	}

	return document
}

// fromDocument converts a BSON streamDocument back to a proto Stream.
func fromDocument(document streamDocument) *streamlinev1.Stream {
	stream := &streamlinev1.Stream{
		Id:        document.ID,
		SourceUri: document.SourceURI,
		State:     streamlinev1.StreamState(document.State),
		CreatedAt: timestamppb.New(time.Unix(0, document.CreatedAtNano)),
		UpdatedAt: timestamppb.New(time.Unix(0, document.UpdatedAtNano)),
	}

	if document.EncodingProfile != nil {
		stream.EncodingProfile = &streamlinev1.EncodingProfile{
			Codec:            document.EncodingProfile.Codec,
			Width:            document.EncodingProfile.Width,
			Height:           document.EncodingProfile.Height,
			BitrateKbps:      document.EncodingProfile.BitrateKbps,
			Framerate:        document.EncodingProfile.Framerate,
			SegmentDurationS: document.EncodingProfile.SegmentDurationS,
		}
	}

	return stream
}

// StoreConfig holds the dependencies for creating a Store.
type StoreConfig struct {
	Database *mongo.Database
	Log      *zap.Logger
}

// Store persists stream metadata in MongoDB.
type Store struct {
	collection *mongo.Collection
	log        *zap.Logger
}

// NewStore creates a Store backed by the given MongoDB database. It panics if
// Database is nil and defaults Log to a no-op logger when not provided. A
// unique index on source_uri is created to prevent duplicate streams.
func NewStore(cfg StoreConfig) *Store {
	if cfg.Database == nil {
		panic("manager.NewStore: Database is required")
	}

	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	log = log.Named("store")

	collection := cfg.Database.Collection(collectionName)

	// Create a unique index on source_uri to enforce one stream per source.
	indexModel := mongo.IndexModel{
		Keys:    bson.D{{Key: "source_uri", Value: mongoIndexAscending}},
		Options: options.Index().SetUnique(true),
	}

	// Use a background context because the constructor is synchronous and has
	// no caller-provided context. The index creation is idempotent.
	ctx, cancel := context.WithTimeout(context.Background(), indexCreationTimeout)
	defer cancel()

	if _, err := collection.Indexes().CreateOne(ctx, indexModel); err != nil {
		log.Warn("failed to create unique index on source_uri", zap.Error(err))
	}

	return &Store{
		collection: collection,
		log:        log,
	}
}

// Create inserts a new stream document into MongoDB. It generates an ObjectID
// hex string as the stream ID, sets the state to PROVISIONING, and populates
// timestamps. The fully populated stream is returned without mutating the input.
func (store *Store) Create(ctx context.Context, stream *streamlinev1.Stream) (*streamlinev1.Stream, error) {
	log := store.log.With(zap.String("method", "Create"))

	now := time.Now()
	created := &streamlinev1.Stream{
		Id:              bson.NewObjectID().Hex(),
		SourceUri:       stream.GetSourceUri(),
		State:           streamlinev1.StreamState_STREAM_STATE_PROVISIONING,
		EncodingProfile: stream.GetEncodingProfile(),
		CreatedAt:       timestamppb.New(now),
		UpdatedAt:       timestamppb.New(now),
	}

	document := toDocument(created)

	if _, err := store.collection.InsertOne(ctx, document); err != nil {
		return nil, fmt.Errorf("insert stream: %w", err)
	}

	log.Info("created stream", zap.String("streamID", created.GetId()))

	return created, nil
}

// Get retrieves a stream by its ID. It returns ErrStreamNotFound when no
// document matches the given ID.
func (store *Store) Get(ctx context.Context, streamID string) (*streamlinev1.Stream, error) {
	var document streamDocument

	err := store.collection.FindOne(ctx, bson.D{{Key: "_id", Value: streamID}}).Decode(&document)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrStreamNotFound
		}
		return nil, fmt.Errorf("find stream: %w", err)
	}

	return fromDocument(document), nil
}

// UpdateState transitions a stream to the given state. It returns
// ErrStreamNotFound when no document matches the given ID.
func (store *Store) UpdateState(ctx context.Context, streamID string, state streamlinev1.StreamState) error {
	log := store.log.With(zap.String("method", "UpdateState"), zap.String("streamID", streamID))

	now := time.Now()

	result, err := store.collection.UpdateOne(
		ctx,
		bson.D{{Key: "_id", Value: streamID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "state", Value: int32(state)},
			{Key: "updated_at_nano", Value: now.UnixNano()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update stream state: %w", err)
	}

	if result.MatchedCount == 0 {
		return ErrStreamNotFound
	}

	log.Info("updated stream state", zap.String("newState", state.String()))

	return nil
}

// FindBySourceURI looks up a stream by its source URI. It returns (nil, nil)
// when no document matches, following the convention that absence is not an error.
func (store *Store) FindBySourceURI(ctx context.Context, sourceURI string) (*streamlinev1.Stream, error) {
	var document streamDocument

	err := store.collection.FindOne(ctx, bson.D{{Key: "source_uri", Value: sourceURI}}).Decode(&document)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("find stream by source URI: %w", err)
	}

	return fromDocument(document), nil
}
