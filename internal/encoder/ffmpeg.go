package encoder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.uber.org/zap"
)

const (
	// defaultPreset balances encoding speed vs. quality for live streaming.
	defaultPreset = "veryfast"

	// defaultHLSListSize is the number of segments kept in the playlist.
	// 10 segments provides a reasonable sliding window for live playback.
	defaultHLSListSize = 10

	// hlsFlags configures segment lifecycle: delete old segments from disk
	// and append to the existing playlist rather than overwriting it.
	hlsFlags = "delete_segments+append_list"

	// outputDirPermissions is the permission mode for the segment output directory.
	// Owner rwx + group r-x; no world access since segments are served by the packager, not directly.
	outputDirPermissions = 0o750

	// maxStderrBytes is the maximum number of stderr bytes logged on FFmpeg failure.
	// Keeps the tail of output where the actual error message appears and prevents
	// unbounded memory growth during long-running encodes.
	maxStderrBytes = 4096
)

// FFmpegRunner starts an FFmpeg process and blocks until it exits.
type FFmpegRunner interface {
	Start(ctx context.Context) error
}

// FFmpegFactory creates a new FFmpegRunner from config. Used for dependency injection in Worker.
type FFmpegFactory func(FFmpegConfig) (FFmpegRunner, error)

// defaultFFmpegFactory creates a real FFmpegProcess.
func defaultFFmpegFactory(cfg FFmpegConfig) (FFmpegRunner, error) {
	return NewFFmpegProcess(cfg)
}

// FFmpegConfig defines encoding parameters.
type FFmpegConfig struct {
	// InputArgs are the raw FFmpeg arguments for the input source.
	// For a file: []string{"-re", "-i", "file:///path/to/video.mp4"}
	// For RTMP:  []string{"-re", "-i", "rtmp://host/app/key"}
	// For test:  []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"}
	InputArgs        []string
	OutputDir        string
	Codec            string
	AudioCodec       string
	Width            int
	Height           int
	BitrateKbps      int
	Framerate        int
	SegmentDurationS int
	Log              *zap.Logger
}

// FFmpegProcess manages an FFmpeg subprocess.
type FFmpegProcess struct {
	cfg FFmpegConfig
	log *zap.Logger
	cmd *exec.Cmd
}

// NewFFmpegProcess creates a new FFmpeg process manager.
// Returns an error if required config fields are missing.
func NewFFmpegProcess(cfg FFmpegConfig) (*FFmpegProcess, error) {
	if len(cfg.InputArgs) == 0 {
		return nil, errors.New("encoder: InputArgs must not be empty")
	}
	if cfg.OutputDir == "" {
		return nil, errors.New("encoder: OutputDir must not be empty")
	}
	if cfg.Framerate == 0 {
		return nil, errors.New("encoder: Framerate must not be zero")
	}
	if cfg.SegmentDurationS == 0 {
		return nil, errors.New("encoder: SegmentDurationS must not be zero")
	}
	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	return &FFmpegProcess{cfg: cfg, log: log.Named("ffmpeg")}, nil
}

// Start launches FFmpeg and blocks until it exits or context is cancelled.
// Creates the output directory if it does not exist.
func (p *FFmpegProcess) Start(ctx context.Context) error {
	if err := os.MkdirAll(p.cfg.OutputDir, outputDirPermissions); err != nil {
		return fmt.Errorf("encoder: create output dir: %w", err)
	}

	args := p.buildArgs()
	p.cmd = exec.CommandContext(ctx, "ffmpeg", args...)

	var stderr bytes.Buffer
	p.cmd.Stderr = &stderr

	err := p.cmd.Run()
	if err != nil && ctx.Err() == nil {
		output := stderr.String()
		if len(output) > maxStderrBytes {
			output = output[len(output)-maxStderrBytes:]
		}
		p.log.Error("FFmpeg stderr output",
			zap.String("method", "Start"),
			zap.String("stderr", output),
			zap.Error(err),
		)
	}
	return err
}

func (p *FFmpegProcess) buildArgs() []string {
	outputPattern := filepath.Join(p.cfg.OutputDir, "segment_%05d.ts")
	playlistPath := filepath.Join(p.cfg.OutputDir, "stream.m3u8")

	var args []string

	// Suppress FFmpeg banner and progress output; only emit errors.
	// Reduces stderr volume to prevent unbounded buffer growth.
	args = append(args, "-loglevel", "error")

	// Input (caller-provided)
	args = append(args, p.cfg.InputArgs...)

	// Video encoding
	args = append(args,
		"-c:v", p.cfg.Codec,
		"-b:v", fmt.Sprintf("%dk", p.cfg.BitrateKbps),
		"-s", fmt.Sprintf("%dx%d", p.cfg.Width, p.cfg.Height),
		"-r", fmt.Sprintf("%d", p.cfg.Framerate),
		"-preset", defaultPreset,
		"-g", fmt.Sprintf("%d", p.cfg.Framerate*p.cfg.SegmentDurationS),
	)

	// Audio encoding — re-encode to ensure proper alignment at segment
	// boundaries. Stream-copying audio causes discontinuities in HLS.
	if p.cfg.AudioCodec != "" {
		args = append(args, "-c:a", p.cfg.AudioCodec)
	}

	// HLS output
	args = append(args,
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", p.cfg.SegmentDurationS),
		"-hls_list_size", fmt.Sprintf("%d", defaultHLSListSize),
		"-hls_flags", hlsFlags,
		"-hls_segment_filename", outputPattern,
		playlistPath,
	)

	return args
}
