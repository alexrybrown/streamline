package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"github.com/alexrybrown/streamline/internal/config"
	"github.com/alexrybrown/streamline/internal/encoder"
	"github.com/alexrybrown/streamline/internal/logging"
)

func main() {
	log := logging.New("encoder-worker")
	cfg := config.Load()

	workerID := envOrDefault("WORKER_ID", "worker-1")
	streamID := envOrDefault("STREAM_ID", "stream-1")
	inputURI := envOrDefault("INPUT_URI", "file:///app/assets/bbb.mp4")
	// Empty means segment pushing to packager is disabled.
	packagerURL := os.Getenv("PACKAGER_URL")

	worker, err := encoder.NewWorker(encoder.WorkerConfig{
		WorkerID:     workerID,
		StreamID:     streamID,
		KafkaBrokers: strings.Split(cfg.KafkaBrokers, ","),
		PackagerURL:  packagerURL,
		Log:          log,
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-re", "-i", inputURI},
			OutputDir:        "/tmp/segments",
			Codec:            "libx264",
			Width:            1920,
			Height:           1080,
			BitrateKbps:      4000,
			Framerate:        30,
			SegmentDurationS: 6,
		},
	})
	if err != nil {
		log.Fatal("failed to create worker", zap.Error(err))
	}
	defer worker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		log.Info("shutting down")
		cancel()
	}()

	log.Info("starting encoder worker",
		zap.String("worker_id", workerID),
		zap.String("stream_id", streamID),
	)
	if err := worker.Run(ctx); err != nil {
		log.Fatal("worker exited with error", zap.Error(err))
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
