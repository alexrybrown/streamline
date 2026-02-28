package encoder

import "context"

// publisher is the interface Worker requires for Kafka publishing.
type publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
	Close()
}
