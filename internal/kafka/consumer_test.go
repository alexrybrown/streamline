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

func TestConsumer_ReadMessage(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()

	// Produce a message first
	producer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(streamkafka.TopicSegments),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	record := &kgo.Record{Key: []byte("stream-1"), Value: []byte(`{"segment": 1}`)}
	if err := producer.ProduceSync(ctx, record).FirstErr(); err != nil {
		t.Fatal(err)
	}
	producer.Close()

	// Now consume via our wrapper
	consumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicSegments,
		GroupID: "test-group",
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	message, err := consumer.ReadMessage(readCtx)
	if err != nil {
		t.Fatal(err)
	}

	if string(message.Key) != "stream-1" {
		t.Errorf("expected key stream-1, got %s", string(message.Key))
	}
	if string(message.Value) != `{"segment": 1}` {
		t.Errorf("expected value {\"segment\": 1}, got %s", string(message.Value))
	}
}

func TestConsumer_ReadMessage_MultiRecordBuffering(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()
	ctx := context.Background()

	// Produce 3 records
	producer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(streamkafka.TopicSegments),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		t.Fatal(err)
	}

	records := []struct {
		key   string
		value string
	}{
		{"stream-1", `{"seq": 1}`},
		{"stream-1", `{"seq": 2}`},
		{"stream-1", `{"seq": 3}`},
	}
	for _, input := range records {
		if err := producer.ProduceSync(ctx, &kgo.Record{
			Key: []byte(input.key), Value: []byte(input.value),
		}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	producer.Close()

	// Consume all 3 via our wrapper — tests internal buffering
	consumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicSegments,
		GroupID: "test-buffering",
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	for i, want := range records {
		message, err := consumer.ReadMessage(readCtx)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if string(message.Value) != want.value {
			t.Errorf("record %d: expected value %s, got %s", i, want.value, string(message.Value))
		}
	}
}

func TestNewConsumer_NilLogger(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	consumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: cluster.ListenAddrs(),
		Topic:   streamkafka.TopicSegments,
		GroupID: "test-nil-logger",
		Log:     nil, // should default to zap.NewNop() internally
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	// Verify the consumer is functional: cancel immediately to prove it doesn't panic
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = consumer.ReadMessage(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestConsumer_ReadMessage_FetchError(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}

	brokers := cluster.ListenAddrs()

	consumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicSegments,
		GroupID: "test-fetch-error",
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	// Close the cluster to cause fetch errors on the next poll
	cluster.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = consumer.ReadMessage(ctx)
	if err == nil {
		t.Fatal("expected an error when cluster is closed, got nil")
	}
}

func TestConsumer_ReadMessage_ContextCancelled(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.AllowAutoTopicCreation())
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	brokers := cluster.ListenAddrs()

	consumer, err := streamkafka.NewConsumer(streamkafka.ConsumerConfig{
		Brokers: brokers,
		Topic:   streamkafka.TopicSegments,
		GroupID: "test-cancel",
		Log:     zap.NewNop(),
		Opts:    []kgo.Opt{kgo.ConsumeResetOffset(kgo.NewOffset().AtStart())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = consumer.ReadMessage(ctx)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
