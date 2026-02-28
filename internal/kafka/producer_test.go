package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	streamkafka "github.com/alexrybrown/streamline/internal/kafka"
)

func TestNewProducer_NilLogger(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	producer, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cluster.ListenAddrs(),
		Topic:   streamkafka.TopicSegments,
		Log:     nil, // should default to zap.NewNop() internally
		Opts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	// Verify the producer is functional with a nil logger
	err = producer.Publish(context.Background(), "key", []byte("value"))
	if err != nil {
		t.Fatalf("expected publish to succeed with nil logger, got %v", err)
	}
}

func TestProducer_Publish_CancelledContext(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	producer, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: cluster.ListenAddrs(),
		Topic:   streamkafka.TopicSegments,
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to trigger error path in Publish

	err = producer.Publish(ctx, "stream-1", []byte(`{"test": true}`))
	if err == nil {
		t.Fatal("expected error when publishing with cancelled context, got nil")
	}
}

func TestProducer_PublishAndConsume(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()

	producer, err := streamkafka.NewProducer(streamkafka.ProducerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicSegments,
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.AllowAutoTopicCreation()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	ctx := context.Background()

	err = producer.Publish(ctx, "stream-1", []byte(`{"test": true}`))
	if err != nil {
		t.Fatal(err)
	}

	// Consume and verify using a direct consumer (no group, for simplicity in test)
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(streamkafka.TopicSegments),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	fetches := consumer.PollFetches(readCtx)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatalf("fetch errors: %v", errs)
	}

	var found bool
	fetches.EachRecord(func(record *kgo.Record) {
		if string(record.Key) != "stream-1" {
			t.Errorf("expected key stream-1, got %s", string(record.Key))
		}
		if string(record.Value) != `{"test": true}` {
			t.Errorf("expected value {\"test\": true}, got %s", string(record.Value))
		}
		found = true
	})
	if !found {
		t.Fatal("no records consumed")
	}
}
