package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/config"
	"github.com/alexrybrown/streamline/internal/kafka"
	"github.com/alexrybrown/streamline/internal/logging"
	"github.com/alexrybrown/streamline/internal/packager"
)

const (
	// defaultPort is the default HTTP listen port for the packager service.
	// Chosen to avoid conflicts with the API gateway (8080) and encoder (8081).
	defaultPort = "8082"

	// defaultOutputDir is the default directory for HLS segment files and playlists.
	// Uses a temp directory to avoid requiring volume mounts in development.
	defaultOutputDir = "/tmp/packager"

	// defaultTargetDuration is the default HLS EXT-X-TARGETDURATION in seconds.
	// 6 seconds is a common HLS segment length that balances latency with encoding efficiency.
	defaultTargetDuration = 6

	// defaultWindowSize is the default number of segments in the sliding window playlist.
	// 10 segments at 6s each gives a ~60s playback buffer, sufficient for live DVR scrubbing.
	defaultWindowSize = 10
)

func main() {
	log := logging.New("packager")
	cfg := config.Load()

	port := envOrDefault("PACKAGER_PORT", defaultPort)
	outputDir := envOrDefault("OUTPUT_DIR", defaultOutputDir)
	targetDuration := envOrDefaultInt(log, "TARGET_DURATION", defaultTargetDuration)
	windowSize := envOrDefaultInt(log, "WINDOW_SIZE", defaultWindowSize)

	producer, err := kafka.NewProducer(kafka.ProducerConfig{
		Brokers: strings.Split(cfg.KafkaBrokers, ","),
		Topic:   kafka.TopicSegments,
		Log:     log,
	})
	if err != nil {
		log.Fatal("failed to create kafka producer", zap.Error(err))
	}

	service := packager.NewService(packager.ServiceConfig{
		OutputDir:      outputDir,
		TargetDuration: targetDuration,
		WindowSize:     windowSize,
		Log:            log,
		Publisher:      producer,
	})
	defer service.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		log.Info("shutting down")
		cancel()
	}()

	address := ":" + port
	log.Info("starting packager",
		zap.String("address", address),
		zap.String("output_dir", outputDir),
		zap.Int("target_duration", targetDuration),
		zap.Int("window_size", windowSize),
	)
	if err := service.Run(ctx, address); err != nil {
		log.Fatal("packager exited with error", zap.Error(err))
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envOrDefaultInt(log *zap.Logger, key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Warn("invalid integer env var, using default",
			zap.String("key", key),
			zap.String("value", value),
			zap.Int("default", fallback),
		)
		return fallback
	}
	return parsed
}
