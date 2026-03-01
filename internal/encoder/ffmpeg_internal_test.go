package encoder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// testInputArgs is a lavfi test source used across all FFmpeg tests.
var testInputArgs = []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"}

// validTestConfig returns an FFmpegConfig with valid defaults for testing.
// The caller can override fields after receiving the config.
func validTestConfig(t *testing.T) FFmpegConfig {
	t.Helper()
	return FFmpegConfig{
		InputArgs:        testInputArgs,
		OutputDir:        t.TempDir(),
		Codec:            "libx264",
		Width:            320,
		Height:           240,
		BitrateKbps:      500,
		Framerate:        30,
		SegmentDurationS: 2,
	}
}

func TestDefaultFFmpegFactory_ValidConfig(t *testing.T) {
	runner, err := defaultFFmpegFactory(validTestConfig(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestDefaultFFmpegFactory_InvalidConfig(t *testing.T) {
	_, err := defaultFFmpegFactory(FFmpegConfig{})
	if err == nil {
		t.Fatal("expected error for empty config, got nil")
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.OutputDir = "/tmp/out"
	cfg.AudioCodec = "aac"

	process := &FFmpegProcess{cfg: cfg}
	args := process.buildArgs()
	joined := strings.Join(args, " ")

	tests := []struct {
		name string
		want string
	}{
		{"loglevel suppresses banner and progress", "-loglevel error"},
		{"input args passed through", "-f lavfi -i testsrc=size=320x240:rate=30"},
		{"codec", "-c:v libx264"},
		{"audio codec", "-c:a aac"},
		{"bitrate formatted with k suffix", "-b:v 500k"},
		{"resolution", "-s 320x240"},
		{"framerate", "-r 30"},
		{"preset", "-preset veryfast"},
		{"GOP = framerate * segment duration", "-g 60"},
		{"HLS format", "-f hls"},
		{"segment duration", "-hls_time 2"},
		{"list size", "-hls_list_size 10"},
		{"HLS flags", "-hls_flags delete_segments+append_list"},
		{"segment filename pattern", "-hls_segment_filename /tmp/out/segment_%05d.ts"},
		{"playlist path", "/tmp/out/stream.m3u8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(joined, tt.want) {
				t.Errorf("expected args to contain %q, got: %s", tt.want, joined)
			}
		})
	}

	t.Run("audio codec omitted when empty", func(t *testing.T) {
		noAudioCfg := validTestConfig(t)
		noAudioCfg.OutputDir = "/tmp/out"
		noAudioProcess := &FFmpegProcess{cfg: noAudioCfg}
		noAudioArgs := strings.Join(noAudioProcess.buildArgs(), " ")
		if strings.Contains(noAudioArgs, "-c:a") {
			t.Errorf("expected -c:a to be omitted when AudioCodec is empty, got: %s", noAudioArgs)
		}
	})
}

// writeFakeFFmpeg creates a shell script named "ffmpeg" in a temp directory
// and prepends it to PATH so exec.LookPath finds it first.
func writeFakeFFmpeg(t *testing.T, script string) {
	t.Helper()
	directory := t.TempDir()
	fakePath := filepath.Join(directory, "ffmpeg")
	if err := os.WriteFile(fakePath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake ffmpeg: %v", err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", directory+":"+originalPath)
}

func TestStart_CreatesOutputDirectory(t *testing.T) {
	writeFakeFFmpeg(t, "#!/bin/sh\nexit 0\n")

	outputDir := filepath.Join(t.TempDir(), "nested", "output")
	cfg := validTestConfig(t)
	cfg.OutputDir = outputDir

	process, err := NewFFmpegProcess(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := process.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, statErr := os.Stat(outputDir)
	if statErr != nil {
		t.Fatalf("output directory was not created: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatal("expected output path to be a directory")
	}
}

func TestStart_ReturnsErrorWhenOutputDirCannotBeCreated(t *testing.T) {
	// Create a regular file that blocks MkdirAll from creating a directory through it.
	blockingFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := validTestConfig(t)
	cfg.OutputDir = filepath.Join(blockingFile, "sub", "dir")

	process, err := NewFFmpegProcess(cfg)
	if err != nil {
		t.Fatal(err)
	}

	err = process.Start(context.Background())
	if err == nil {
		t.Fatal("expected error when output directory cannot be created")
	}
	if !strings.Contains(err.Error(), "create output dir") {
		t.Errorf("expected error to mention 'create output dir', got: %v", err)
	}
}

const (
	// stderrUnderLimit is a stderr size comfortably below maxStderrBytes,
	// used to verify that small output is preserved without truncation.
	stderrUnderLimit = 100
)

func TestStart_StderrLogging(t *testing.T) {
	tests := []struct {
		name           string
		stderrSize     int
		wantLoggedSize int
	}{
		{
			name:           "truncated when over limit",
			stderrSize:     maxStderrBytes * 2,
			wantLoggedSize: maxStderrBytes,
		},
		{
			name:           "preserved when under limit",
			stderrSize:     stderrUnderLimit,
			wantLoggedSize: stderrUnderLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := fmt.Sprintf("#!/bin/sh\nhead -c %d /dev/zero | tr '\\0' 'E' >&2\nexit 1\n", tt.stderrSize)
			writeFakeFFmpeg(t, script)

			core, observedLogs := observer.New(zapcore.ErrorLevel)

			cfg := validTestConfig(t)
			cfg.Log = zap.New(core)

			process, err := NewFFmpegProcess(cfg)
			if err != nil {
				t.Fatal(err)
			}

			if err := process.Start(context.Background()); err == nil {
				t.Fatal("expected subprocess to fail")
			}

			logs := observedLogs.FilterMessage("FFmpeg stderr output").All()
			if len(logs) != 1 {
				t.Fatalf("expected 1 log entry, got %d", len(logs))
			}

			loggedStderr := logs[0].ContextMap()["stderr"].(string)
			if len(loggedStderr) != tt.wantLoggedSize {
				t.Errorf("expected stderr to be %d bytes, got %d", tt.wantLoggedSize, len(loggedStderr))
			}
		})
	}
}

func TestStart_NoStderrLogWhenContextCancelled(t *testing.T) {
	writeFakeFFmpeg(t, "#!/bin/sh\nsleep 10\n")

	core, observedLogs := observer.New(zapcore.ErrorLevel)

	cfg := validTestConfig(t)
	cfg.Log = zap.New(core)

	process, err := NewFFmpegProcess(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_ = process.Start(ctx)

	logs := observedLogs.FilterMessage("FFmpeg stderr output").All()
	if len(logs) != 0 {
		t.Errorf("expected no stderr log when context cancelled, got %d entries", len(logs))
	}
}
