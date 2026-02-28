package encoder

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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
	Width            int
	Height           int
	BitrateKbps      int
	Framerate        int
	SegmentDurationS int
}

// FFmpegProcess manages an FFmpeg subprocess.
type FFmpegProcess struct {
	cfg FFmpegConfig
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
	return &FFmpegProcess{cfg: cfg}, nil
}

// Start launches FFmpeg and blocks until it exits or context is cancelled.
func (p *FFmpegProcess) Start(ctx context.Context) error {
	args := p.buildArgs()
	p.cmd = exec.CommandContext(ctx, "ffmpeg", args...)
	return p.cmd.Run()
}

func (p *FFmpegProcess) buildArgs() []string {
	outputPattern := filepath.Join(p.cfg.OutputDir, "segment_%05d.ts")
	playlistPath := filepath.Join(p.cfg.OutputDir, "stream.m3u8")

	var args []string

	// Input (caller-provided)
	args = append(args, p.cfg.InputArgs...)

	// Encoding
	args = append(args,
		"-c:v", p.cfg.Codec,
		"-b:v", fmt.Sprintf("%dk", p.cfg.BitrateKbps),
		"-s", fmt.Sprintf("%dx%d", p.cfg.Width, p.cfg.Height),
		"-r", fmt.Sprintf("%d", p.cfg.Framerate),
		"-preset", defaultPreset,
		"-g", fmt.Sprintf("%d", p.cfg.Framerate*p.cfg.SegmentDurationS),
	)

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
