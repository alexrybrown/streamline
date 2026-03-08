package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	connectcors "connectrpc.com/cors"
	"connectrpc.com/grpcreflect"
	"github.com/rs/cors"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/gen/go/streamline/v1/streamlinev1connect"
	"github.com/alexrybrown/streamline/internal/config"
	"github.com/alexrybrown/streamline/internal/health"
	"github.com/alexrybrown/streamline/internal/logging"
	"github.com/alexrybrown/streamline/internal/manager"
)

const (
	// mongoTimeout is the maximum time allowed for MongoDB operations (connect,
	// ping, disconnect). Ten seconds accommodates cold-start latency in
	// containerised environments.
	mongoTimeout = 10 * time.Second

	// httpShutdownTimeout is the maximum time to wait for in-flight HTTP
	// requests to complete during graceful shutdown.
	httpShutdownTimeout = 10 * time.Second

	// readHeaderTimeout is the maximum time to read request headers from a
	// client. Ten seconds guards against slowloris-style attacks.
	readHeaderTimeout = 10 * time.Second

	// corsMaxAgeSeconds is the max age for CORS preflight cache.
	// 7200 seconds (2 hours) reduces preflight request frequency for browser clients.
	corsMaxAgeSeconds = 7200

	// mongoPingTimeout is the maximum time allowed for a MongoDB health check ping.
	// Two seconds prevents slow pings from blocking the health endpoint.
	mongoPingTimeout = 2 * time.Second
)

func main() {
	log := logging.New("stream-manager")
	cfg := config.Load()

	// Connect to MongoDB.
	mongoClient, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		log.Fatal("failed to create mongo client", zap.Error(err))
	}

	connectCtx, connectCancel := context.WithTimeout(context.Background(), mongoTimeout)
	defer connectCancel()

	if err := mongoClient.Ping(connectCtx, nil); err != nil {
		log.Fatal("failed to ping mongo", zap.Error(err))
	}
	log.Info("connected to MongoDB", zap.String("uri", cfg.MongoURI))

	database := mongoClient.Database(cfg.MongoDBName)

	store := manager.NewStore(manager.StoreConfig{
		Database: database,
		Log:      log,
	})

	service := manager.NewService(manager.ServiceConfig{
		Store: store,
		Log:   log,
	})

	// Build HTTP mux with ConnectRPC handler, gRPC reflection, and health check.
	mux := http.NewServeMux()

	connectPath, connectHandler := streamlinev1connect.NewStreamManagerServiceHandler(service)
	mux.Handle(connectPath, connectHandler)

	// Mount gRPC server reflection for tooling like grpcurl and buf curl.
	// Both v1 and v1alpha are needed because many tools still use v1alpha.
	reflector := grpcreflect.NewStaticReflector(streamlinev1connect.StreamManagerServiceName)
	reflectPathV1, reflectHandlerV1 := grpcreflect.NewHandlerV1(reflector)
	mux.Handle(reflectPathV1, reflectHandlerV1)
	reflectPathV1Alpha, reflectHandlerV1Alpha := grpcreflect.NewHandlerV1Alpha(reflector)
	mux.Handle(reflectPathV1Alpha, reflectHandlerV1Alpha)

	// Health check with MongoDB ping.
	checker := health.NewChecker()
	checker.AddCheck("mongodb", func() error {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), mongoPingTimeout)
		defer pingCancel()
		return mongoClient.Ping(pingCtx, nil)
	})
	mux.Handle("/healthz", checker.Handler())

	// CORS middleware allowing all origins for ConnectRPC browser clients.
	middleware := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: connectcors.AllowedMethods(),
		AllowedHeaders: connectcors.AllowedHeaders(),
		ExposedHeaders: connectcors.ExposedHeaders(),
		MaxAge:         corsMaxAgeSeconds,
	})
	handler := middleware.Handler(mux)

	// Enable both HTTP/1.1 and unencrypted HTTP/2 (h2c) so the server can
	// accept gRPC clients (which require HTTP/2) without TLS, as well as
	// regular HTTP/1.1 clients for Connect protocol requests.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	address := ":" + cfg.ServicePort
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		cancel()
	}()

	errChan := make(chan error, 1)
	go func() {
		log.Info("starting stream-manager", zap.String("address", address))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- fmt.Errorf("HTTP server error: %w", err)
		}
		close(errChan)
	}()

	<-ctx.Done()
	log.Info("shutting down HTTP server")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP server shutdown failed", zap.Error(err))
	}

	if err := <-errChan; err != nil {
		log.Error("server error", zap.Error(err))
	}

	// Disconnect MongoDB client.
	disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), mongoTimeout)
	defer disconnectCancel()

	if err := mongoClient.Disconnect(disconnectCtx); err != nil {
		log.Error("failed to disconnect mongo client", zap.Error(err))
	}
}
