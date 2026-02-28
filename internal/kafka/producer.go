package kafka

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
)

// ProducerConfig holds configuration for creating a Producer.
type ProducerConfig struct {
	Brokers []string
	Topic   string
	Log     *zap.Logger
	Opts    []kgo.Opt
}

// Producer wraps a franz-go client for publishing messages to a single topic.
type Producer struct {
	client *kgo.Client
	log    *zap.Logger
}

// NewProducer creates a Kafka producer bound to the given topic.
func NewProducer(cfg ProducerConfig) (*Producer, error) {
	baseOpts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.DefaultProduceTopic(cfg.Topic),
	}

	client, err := kgo.NewClient(append(baseOpts, cfg.Opts...)...)
	if err != nil {
		return nil, err
	}

	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	log = log.Named("producer").With(zap.String("topic", cfg.Topic))

	return &Producer{client: client, log: log}, nil
}

// Publish sends a message with the given partition key.
func (p *Producer) Publish(ctx context.Context, key string, value []byte) error {
	err := p.client.ProduceSync(ctx, &kgo.Record{
		Key:   []byte(key),
		Value: value,
	}).FirstErr()
	if err != nil {
		p.log.Error("failed to publish message",
			zap.String("method", "publish"),
			zap.String("key", key),
			zap.Error(err))
		return err
	}

	p.log.Debug("message published",
		zap.String("method", "publish"),
		zap.String("key", key),
		zap.Int("value_bytes", len(value)))
	return nil
}

// Close flushes pending writes and closes the underlying client.
func (p *Producer) Close() {
	p.log.Info("closing producer")
	p.client.Close()
}
