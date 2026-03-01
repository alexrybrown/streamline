package encoder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/goleak"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	streamlinev1 "github.com/alexrybrown/streamline/gen/go/streamline/v1"
	"github.com/alexrybrown/streamline/internal/encoder"
	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

// blockingProcess is a stub FFmpegRunner that blocks until context cancellation.
type blockingProcess struct{}

func (p *blockingProcess) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// blockingFactory returns a factory that creates blockingProcess instances.
func blockingFactory(cfg encoder.FFmpegConfig) (encoder.FFmpegRunner, error) {
	return &blockingProcess{}, nil
}

func TestNewWorker_DefaultsApplied(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()

	// Create a worker with nil Log, zero HeartbeatInterval, and zero BackoffBase
	// to exercise all default branches in NewWorker.
	worker, err := encoder.NewWorker(encoder.WorkerConfig{
		WorkerID:     "default-w",
		StreamID:     "default-s",
		KafkaBrokers: brokers,
		KafkaOpts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
			OutputDir:        t.TempDir(),
			Codec:            "libx264",
			Framerate:        30,
			SegmentDurationS: 2,
		},
		// Log, HeartbeatInterval, BackoffBase intentionally omitted
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer worker.Close()
}

func TestNewWorker_ValidatesConfig(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()

	tests := []struct {
		name    string
		cfg     encoder.WorkerConfig
		wantErr bool
	}{
		{
			name:    "empty config",
			cfg:     encoder.WorkerConfig{},
			wantErr: true,
		},
		{
			name: "missing StreamID",
			cfg: encoder.WorkerConfig{
				WorkerID:     "w-1",
				KafkaBrokers: brokers,
			},
			wantErr: true,
		},
		{
			name: "missing WorkerID",
			cfg: encoder.WorkerConfig{
				StreamID:     "s-1",
				KafkaBrokers: brokers,
			},
			wantErr: true,
		},
		{
			name: "missing KafkaBrokers",
			cfg: encoder.WorkerConfig{
				WorkerID: "w-1",
				StreamID: "s-1",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			cfg: encoder.WorkerConfig{
				WorkerID:     "w-1",
				StreamID:     "s-1",
				KafkaBrokers: brokers,
				Log:          zap.NewNop(),
				KafkaOpts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
				FFmpeg: encoder.FFmpegConfig{
					InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
					OutputDir:        t.TempDir(),
					Codec:            "libx264",
					Framerate:        30,
					SegmentDurationS: 2,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := encoder.NewWorker(tt.cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w != nil {
				w.Close()
			}
		})
	}
}

func TestWorker_PublishesHeartbeats(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kfake.(*Cluster).run"),
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kgo.(*Client).waitShardRetries"),
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kgo.(*Client).repin"),
	)

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()
	outputDir := t.TempDir()

	worker, err := encoder.NewWorker(encoder.WorkerConfigWithProcessFactory(encoder.WorkerConfig{
		WorkerID:          "hb-worker",
		StreamID:          "hb-stream",
		KafkaBrokers:      brokers,
		Log:               zap.NewNop(),
		KafkaOpts:         []kgo.Opt{kgo.AllowAutoTopicCreation()},
		HeartbeatInterval: 100 * time.Millisecond, // fast heartbeats for test
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
			OutputDir:        outputDir,
			Codec:            "libx264",
			Framerate:        30,
			SegmentDurationS: 2,
		},
	}, blockingFactory))
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = worker.Run(ctx) }()

	// Consume heartbeat from Kafka
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(streamkafka.TopicHeartbeats),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	fetches := consumer.PollFetches(readCtx)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatalf("fetch errors: %v", errs)
	}

	var found bool
	fetches.EachRecord(func(record *kgo.Record) {
		if string(record.Key) != "hb-stream" {
			t.Errorf("expected key hb-stream, got %s", string(record.Key))
		}
		var heartbeat streamlinev1.Heartbeat
		if err := proto.Unmarshal(record.Value, &heartbeat); err != nil {
			t.Fatalf("invalid heartbeat protobuf: %v", err)
		}
		if heartbeat.GetWorkerId() != "hb-worker" {
			t.Errorf("expected worker_id hb-worker, got %v", heartbeat.GetWorkerId())
		}
		if heartbeat.GetStreamId() != "hb-stream" {
			t.Errorf("expected stream_id hb-stream, got %v", heartbeat.GetStreamId())
		}
		if heartbeat.GetStatus() != streamlinev1.WorkerStatus_WORKER_STATUS_ENCODING {
			t.Errorf("expected status ENCODING, got %v", heartbeat.GetStatus())
		}
		if heartbeat.GetTimestamp() == nil {
			t.Error("expected timestamp to be set")
		}
		found = true
	})
	if !found {
		t.Fatal("no heartbeat records consumed")
	}
}

func TestWorker_PublishesSegmentEvents(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kfake.(*Cluster).run"),
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kgo.(*Client).waitShardRetries"),
		goleak.IgnoreTopFunction("github.com/twmb/franz-go/pkg/kgo.(*Client).repin"),
	)

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()
	outputDir := t.TempDir()

	worker, err := encoder.NewWorker(encoder.WorkerConfigWithProcessFactory(encoder.WorkerConfig{
		WorkerID:     "seg-worker",
		StreamID:     "seg-stream",
		KafkaBrokers: brokers,
		Log:          zap.NewNop(),
		KafkaOpts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
		FFmpeg: encoder.FFmpegConfig{
			InputArgs:        []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
			OutputDir:        outputDir,
			Codec:            "libx264",
			Framerate:        30,
			SegmentDurationS: 2,
		},
	}, blockingFactory))
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = worker.Run(ctx) }()

	// Give the segment watcher time to start polling
	time.Sleep(200 * time.Millisecond)

	// Create a fake segment file to trigger the watcher
	segmentPath := filepath.Join(outputDir, "segment_00001.ts")
	if err := os.WriteFile(segmentPath, []byte("fake segment data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Consume segment event from Kafka
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(streamkafka.TopicSegments),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	fetches := consumer.PollFetches(readCtx)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatalf("fetch errors: %v", errs)
	}

	var found bool
	fetches.EachRecord(func(record *kgo.Record) {
		if string(record.Key) != "seg-stream" {
			t.Errorf("expected key seg-stream, got %s", string(record.Key))
		}
		var event streamlinev1.SegmentProduced
		if err := proto.Unmarshal(record.Value, &event); err != nil {
			t.Fatalf("invalid segment event protobuf: %v", err)
		}
		if event.GetWorkerId() != "seg-worker" {
			t.Errorf("expected worker_id seg-worker, got %v", event.GetWorkerId())
		}
		if event.GetStreamId() != "seg-stream" {
			t.Errorf("expected stream_id seg-stream, got %v", event.GetStreamId())
		}
		if event.GetSequenceNumber() != 1 {
			t.Errorf("expected sequence_number 1, got %v", event.GetSequenceNumber())
		}
		if event.GetTimestamp() == nil {
			t.Error("expected timestamp to be set")
		}
		found = true
	})
	if !found {
		t.Fatal("no segment event records consumed")
	}
}
