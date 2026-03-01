package packager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
	"connectrpc.com/grpcreflect"
	"github.com/rs/cors"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
)

const (
	// hlsServePath is the URL path prefix for serving HLS content.
	hlsServePath = "/hls/"

	// kafkaPublishTimeout is the maximum time allowed for publishing
	// a single event to Kafka. Prevents indefinite blocking on broker issues.
	kafkaPublishTimeout = 5 * time.Second

	// httpShutdownTimeout is the maximum time to wait for in-flight HTTP
	// requests to complete during graceful shutdown.
	httpShutdownTimeout = 10 * time.Second

	// corsMaxAgeSeconds is the max age for CORS preflight cache.
	// 2 hours balances reducing preflight requests with allowing config changes.
	corsMaxAgeSeconds = 7200
)

// ServiceConfig holds configuration for the packager Service.
type ServiceConfig struct {
	// OutputDir is the base directory for segment files and HLS playlists.
	OutputDir string
	// TargetDuration is the EXT-X-TARGETDURATION value in seconds.
	TargetDuration int
	// WindowSize is the maximum number of segments in the sliding window playlist.
	WindowSize int
	// Log is the parent logger. Nil defaults to a no-op logger.
	Log *zap.Logger
	// Publisher is the Kafka publisher for SegmentAvailable events.
	Publisher publisher
	// AllowedOrigins is the list of allowed CORS origins for browser clients.
	// An empty slice disables CORS (no Access-Control headers are sent).
	AllowedOrigins []string
}

// Service is the packager ConnectRPC service. It wires together the segment
// receiver, manifest generator, Kafka publisher, and HLS file server.
// Service implements PackagerServiceHandler.
type Service struct {
	receiver       *Receiver
	manifest       *ManifestGenerator
	publisher      publisher
	log            *zap.Logger
	outputDir      string
	allowedOrigins []string
}

// Compile-time assertion that Service implements PackagerServiceHandler.
var _ streamlinev1connect.PackagerServiceHandler = (*Service)(nil)

// NewService creates a new packager Service. It panics if required config
// fields are missing or invalid.
func NewService(cfg ServiceConfig) *Service {
	if cfg.OutputDir == "" {
		panic("packager.ServiceConfig: OutputDir must not be empty")
	}
	if cfg.Publisher == nil {
		panic("packager.ServiceConfig: Publisher must not be nil")
	}
	if cfg.TargetDuration <= 0 {
		panic("packager.ServiceConfig: TargetDuration must be positive")
	}
	if cfg.WindowSize <= 0 {
		panic("packager.ServiceConfig: WindowSize must be positive")
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}

	log := cfg.Log.Named("service")

	manifest := NewManifestGenerator(ManifestGeneratorConfig{
		OutputDir:      cfg.OutputDir,
		TargetDuration: cfg.TargetDuration,
		WindowSize:     cfg.WindowSize,
		Log:            cfg.Log,
	})

	service := &Service{
		manifest:       manifest,
		publisher:      cfg.Publisher,
		log:            log,
		outputDir:      cfg.OutputDir,
		allowedOrigins: cfg.AllowedOrigins,
	}

	service.receiver = NewReceiver(ReceiverConfig{
		OutputDir:        cfg.OutputDir,
		Log:              cfg.Log,
		OnSegmentWritten: service.onSegmentWritten,
	})

	return service
}

// PushSegment implements PackagerServiceHandler by delegating to the receiver.
func (service *Service) PushSegment(ctx context.Context, stream *connect.ClientStream[streamlinev1.PushSegmentRequest]) (*connect.Response[streamlinev1.PushSegmentResponse], error) {
	return service.receiver.PushSegment(ctx, stream)
}

// onSegmentWritten is the callback invoked after the receiver writes a segment.
// It updates the HLS manifest and publishes a SegmentAvailable event to Kafka.
func (service *Service) onSegmentWritten(streamID string, segment SegmentInfo) {
	log := service.log.With(
		zap.String("method", "onSegmentWritten"),
		zap.String("streamID", streamID),
		zap.Int64("sequenceNumber", segment.SequenceNumber),
	)

	if err := service.manifest.AddSegment(streamID, segment); err != nil {
		log.Error("failed to update manifest", zap.Error(err))
		return
	}

	playlistPath := filepath.Join(streamID, playlistFilename)

	event := &streamlinev1.SegmentAvailable{
		StreamId:       streamID,
		SequenceNumber: segment.SequenceNumber,
		PlaylistPath:   playlistPath,
		Timestamp:      timestamppb.Now(),
	}

	eventBytes, err := proto.Marshal(event)
	if err != nil {
		log.Error("failed to marshal SegmentAvailable event", zap.Error(err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), kafkaPublishTimeout)
	defer cancel()

	if err := service.publisher.Publish(ctx, streamID, eventBytes); err != nil {
		log.Error("failed to publish SegmentAvailable event", zap.Error(err))
		return
	}

	log.Info("segment available event published")
}

// Handler returns an HTTP handler that serves the ConnectRPC packager service,
// gRPC server reflection (v1 and v1alpha), HLS file server, and CORS middleware.
func (service *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	connectPath, connectHandler := streamlinev1connect.NewPackagerServiceHandler(service)
	mux.Handle(connectPath, connectHandler)

	// Mount gRPC server reflection for tooling like grpcurl and buf curl.
	// Both v1 and v1alpha are needed because many tools still use v1alpha.
	reflector := grpcreflect.NewStaticReflector(streamlinev1connect.PackagerServiceName)
	reflectPathV1, reflectHandlerV1 := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(reflectPathV1, reflectHandlerV1)
	reflectPathV1Alpha, reflectHandlerV1Alpha := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(reflectPathV1Alpha, reflectHandlerV1Alpha)

	fileServer := http.StripPrefix(hlsServePath, http.FileServer(http.Dir(service.outputDir)))
	mux.Handle(hlsServePath, fileServer)

	return service.withCORS(mux)
}

// withCORS wraps an HTTP handler with CORS middleware configured for ConnectRPC
// browser clients. If no AllowedOrigins are configured, the handler is returned
// unchanged.
func (service *Service) withCORS(handler http.Handler) http.Handler {
	if len(service.allowedOrigins) == 0 {
		return handler
	}
	middleware := cors.New(cors.Options{
		AllowedOrigins: service.allowedOrigins,
		AllowedMethods: connectcors.AllowedMethods(),
		AllowedHeaders: connectcors.AllowedHeaders(),
		ExposedHeaders: connectcors.ExposedHeaders(),
		MaxAge:         corsMaxAgeSeconds,
	})
	return middleware.Handler(handler)
}

// Run starts the HTTP server with h2c (HTTP/2 cleartext) support and blocks
// until the context is cancelled. It performs graceful shutdown when the
// context is done. h2c is required for gRPC clients connecting without TLS.
func (service *Service) Run(ctx context.Context, address string) error {
	log := service.log.With(zap.String("method", "Run"))

	// Enable both HTTP/1.1 and unencrypted HTTP/2 (h2c) so the server can
	// accept gRPC clients (which require HTTP/2) without TLS, as well as
	// regular HTTP/1.1 clients for HLS content and Connect protocol requests.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              address,
		Handler:           service.Handler(),
		Protocols:         protocols,
		ReadHeaderTimeout: httpShutdownTimeout,
	}

	errChan := make(chan error, 1)
	go func() {
		log.Info("starting HTTP server", zap.String("address", address))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- fmt.Errorf("HTTP server error: %w", err)
		}
		close(errChan)
	}()

	<-ctx.Done()
	log.Info("shutting down HTTP server")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("HTTP server shutdown: %w", err)
	}

	// Check if the server returned an error before context cancellation.
	if err := <-errChan; err != nil {
		return err
	}

	return nil
}

// Close releases resources held by the service, including the Kafka publisher.
func (service *Service) Close() {
	service.publisher.Close()
}
