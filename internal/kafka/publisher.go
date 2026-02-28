package kafka

import "context"

// Publisher sends a keyed message to a Kafka topic.
type Publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
}

// PublisherCloser combines publishing and resource cleanup.
type PublisherCloser interface {
	Publisher
	Close()
}
