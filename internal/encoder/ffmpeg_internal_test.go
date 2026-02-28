package encoder

import (
	"strings"
	"testing"
)

func TestDefaultFFmpegFactory_ValidConfig(t *testing.T) {
	config := FFmpegConfig{
		InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
		OutputDir:        t.TempDir(),
		Codec:            "libx264",
		Width:            320,
		Height:           240,
		BitrateKbps:      500,
		Framerate:        30,
		SegmentDurationS: 2,
	}

	runner, err := defaultFFmpegFactory(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestDefaultFFmpegFactory_InvalidConfig(t *testing.T) {
	config := FFmpegConfig{} // empty config should fail validation

	_, err := defaultFFmpegFactory(config)
	if err == nil {
		t.Fatal("expected error for empty config, got nil")
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := FFmpegConfig{
		InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
		OutputDir:        "/tmp/out",
		Codec:            "libx264",
		Width:            320,
		Height:           240,
		BitrateKbps:      500,
		Framerate:        30,
		SegmentDurationS: 2,
	}

	process := &FFmpegProcess{cfg: cfg}
	args := process.buildArgs()
	joined := strings.Join(args, " ")

	tests := []struct {
		name string
		want string
	}{
		{"input args passed through", "-f lavfi -i testsrc=size=320x240:rate=30"},
		{"codec", "-c:v libx264"},
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
}
