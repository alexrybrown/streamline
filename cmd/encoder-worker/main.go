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

const (
	// defaultWorkerID identifies this encoder instance in heartbeats and logs.
	defaultWorkerID = "worker-1"

	// defaultStreamID is the stream key used when none is provided via env.
	defaultStreamID = "stream-1"

	// defaultInputURI is the default FFmpeg input source.
	// Points to the Big Buck Bunny asset bundled in the Docker image.
	defaultInputURI = "file:///app/assets/bbb.mp4"

	// defaultOutputDir is the directory where FFmpeg writes HLS segments.
	// Uses a temp directory to avoid requiring volume mounts in development.
	defaultOutputDir = "/tmp/segments"

	// defaultVideoCodec uses H.264 for broad browser and device compatibility.
	defaultVideoCodec = "libx264"

	// defaultAudioCodec re-encodes audio to AAC so segment boundaries align
	// cleanly. Stream-copying audio causes HLS discontinuities.
	defaultAudioCodec = "aac"

	// defaultWidth and defaultHeight define the 1080p output resolution.
	defaultWidth  = 1920
	defaultHeight = 1080

	// defaultBitrateKbps targets 4 Mbps, appropriate for 1080p live content
	// with the veryfast preset.
	defaultBitrateKbps = 4000

	// defaultFramerate is the output frame rate in frames per second.
	defaultFramerate = 30

	// defaultSegmentDurationS is the HLS segment length in seconds.
	// 6 seconds balances latency with encoding efficiency.
	defaultSegmentDurationS = 6
)

func main() {
	log := logging.New("encoder-worker")
	cfg := config.Load()

	workerID := envOrDefault("WORKER_ID", defaultWorkerID)
	streamID := envOrDefault("STREAM_ID", defaultStreamID)
	inputURI := envOrDefault("INPUT_URI", defaultInputURI)
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
			OutputDir:        defaultOutputDir,
			Codec:            defaultVideoCodec,
			AudioCodec:       defaultAudioCodec,
			Width:            defaultWidth,
			Height:           defaultHeight,
			BitrateKbps:      defaultBitrateKbps,
			Framerate:        defaultFramerate,
			SegmentDurationS: defaultSegmentDurationS,
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
