package kafka

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
)

// Message is a consumed Kafka record's key and value.
type Message struct {
	Key   []byte
	Value []byte
}

// ConsumerConfig holds configuration for creating a Consumer.
type ConsumerConfig struct {
	Brokers []string
	Topic   string
	GroupID string
	Log     *zap.Logger
	Opts    []kgo.Opt
}

// Consumer wraps a franz-go client for consuming messages from a topic.
type Consumer struct {
	client *kgo.Client
	log    *zap.Logger
	buf    []*kgo.Record
}

// NewConsumer creates a Kafka consumer for the given topic and group.
func NewConsumer(cfg ConsumerConfig) (*Consumer, error) {
	baseOpts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.ConsumerGroup(cfg.GroupID),
	}

	client, err := kgo.NewClient(append(baseOpts, cfg.Opts...)...)
	if err != nil {
		return nil, err
	}

	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	log = log.Named("consumer").With(
		zap.String("topic", cfg.Topic),
		zap.String("group", cfg.GroupID),
	)

	return &Consumer{client: client, log: log}, nil
}

// ReadMessage blocks until a message is available or the context is cancelled.
// Multi-record fetches are buffered internally so no records are lost.
func (c *Consumer) ReadMessage(ctx context.Context) (Message, error) {
	for len(c.buf) == 0 {
		fetches := c.client.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			c.log.Error("fetch error",
				zap.String("method", "read"),
				zap.Error(errs[0].Err))
			return Message{}, fmt.Errorf("fetch error: %w", errs[0].Err)
		}
		c.buf = fetches.Records()
		c.log.Debug("fetched records",
			zap.String("method", "read"),
			zap.Int("count", len(c.buf)))
	}

	record := c.buf[0]
	c.buf = c.buf[1:]
	return Message{Key: record.Key, Value: record.Value}, nil
}

// Close leaves the consumer group and closes the underlying client.
func (c *Consumer) Close() {
	c.log.Info("closing consumer")
	c.client.Close()
}
