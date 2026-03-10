package encoder_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexrybrown/streamline/internal/encoder"
)

func TestNewFFmpegProcess_ValidatesConfig(t *testing.T) {
	validInputArgs := []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"}

	tests := []struct {
		name    string
		cfg     func() encoder.FFmpegConfig
		wantErr bool
	}{
		{
			name:    "empty config",
			cfg:     func() encoder.FFmpegConfig { return encoder.FFmpegConfig{} },
			wantErr: true,
		},
		{
			name: "missing OutputDir",
			cfg: func() encoder.FFmpegConfig {
				return encoder.FFmpegConfig{InputArgs: validInputArgs}
			},
			wantErr: true,
		},
		{
			name: "zero Framerate",
			cfg: func() encoder.FFmpegConfig {
				return encoder.FFmpegConfig{
					InputArgs: validInputArgs,
					OutputDir: t.TempDir(),
				}
			},
			wantErr: true,
		},
		{
			name: "zero SegmentDurationS",
			cfg: func() encoder.FFmpegConfig {
				return encoder.FFmpegConfig{
					InputArgs: validInputArgs,
					OutputDir: t.TempDir(),
					Framerate: 30,
				}
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: func() encoder.FFmpegConfig {
				return encoder.FFmpegConfig{
					InputArgs:        validInputArgs,
					OutputDir:        t.TempDir(),
					Codec:            "libx264",
					Framerate:        30,
					SegmentDurationS: 2,
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := encoder.NewFFmpegProcess(tt.cfg())
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFFmpegProcess_ProducesSegments(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	outputDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	process, err := encoder.NewFFmpegProcess(encoder.FFmpegConfig{
		InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
		OutputDir:        outputDir,
		Codec:            "libx264",
		Width:            320,
		Height:           240,
		BitrateKbps:      500,
		Framerate:        30,
		SegmentDurationS: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- process.Start(ctx) }()

	// Wait for at least one segment to appear
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for segments")
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				t.Fatalf("ffmpeg exited with error: %v", err)
			}
		default:
			matches, _ := filepath.Glob(filepath.Join(outputDir, "*.ts"))
			if len(matches) > 0 {
				cancel()
				// Wait for FFmpeg to fully exit before t.TempDir() cleanup
				// removes the output directory.
				<-errCh
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
}
