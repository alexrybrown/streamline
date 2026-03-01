package packager

import "context"

// publisher is the interface Service requires for Kafka publishing.
type publisher interface {
	Publish(ctx context.Context, key string, value []byte) error
	Close()
}
