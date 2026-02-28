package config_test

import (
	"testing"

	"github.com/alexrybrown/streamline/internal/config"
)

func TestDefaults(t *testing.T) {
	cfg := config.Load()
	if cfg.KafkaBrokers == "" {
		t.Error("expected default Kafka broker")
	}
	if cfg.MongoURI == "" {
		t.Error("expected default Mongo URI")
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:29092")
	t.Setenv("MONGO_URI", "mongodb://mongo:27017")

	cfg := config.Load()
	if cfg.KafkaBrokers != "kafka:29092" {
		t.Errorf("expected kafka:29092, got %s", cfg.KafkaBrokers)
	}
	if cfg.MongoURI != "mongodb://mongo:27017" {
		t.Errorf("expected mongodb://mongo:27017, got %s", cfg.MongoURI)
	}
}
