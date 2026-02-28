package config

import "os"

// Config holds service configuration loaded from environment variables.
type Config struct {
	KafkaBrokers string
	MongoURI     string
	MongoDBName  string
	ServicePort  string
}

// Load reads configuration from environment variables with defaults.
func Load() Config {
	return Config{
		KafkaBrokers: getEnv("KAFKA_BROKERS", "localhost:9092"),
		MongoURI:     getEnv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDBName:  getEnv("MONGO_DB_NAME", "streamline"),
		ServicePort:  getEnv("SERVICE_PORT", "8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
