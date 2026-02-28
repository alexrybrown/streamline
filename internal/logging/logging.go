package logging

import "go.uber.org/zap"

// New creates a production JSON logger with the service name baked in.
// Callers customize further with .Named() and .With() on the returned logger.
func New(service string) *zap.Logger {
	logger, err := zap.NewProduction()
	if err != nil {
		panic("failed to create logger: " + err.Error())
	}
	return logger.Named(service)
}
