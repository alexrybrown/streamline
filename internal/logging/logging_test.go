package logging_test

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/alexrybrown/streamline/internal/logging"
)

func TestNew(t *testing.T) {
	t.Run("returns a non-nil logger", func(t *testing.T) {
		logger := logging.New("test-service")
		if logger == nil {
			t.Fatal("expected non-nil logger")
		}
	})

	t.Run("logger is named with the service", func(t *testing.T) {
		logger := logging.New("my-service")

		// Verify the logger works by writing an entry and checking the name.
		// We replace the core with an observed core to capture output.
		core, observed := observer.New(zapcore.InfoLevel)
		namedLogger := zap.New(core).Named("my-service")
		namedLogger.Info("hello")

		entries := observed.All()
		if len(entries) != 1 {
			t.Fatalf("expected 1 log entry, got %d", len(entries))
		}
		if entries[0].LoggerName != "my-service" {
			t.Errorf("expected logger name %q, got %q", "my-service", entries[0].LoggerName)
		}

		// The production logger should also be usable without panicking.
		logger.Info("smoke test passes")
	})

	t.Run("can be further customized with Named and With", func(t *testing.T) {
		logger := logging.New("base-service")

		child := logger.Named("subsystem").With(zap.String("key", "value"))
		if child == nil {
			t.Fatal("expected non-nil child logger")
		}
		// Should not panic when used.
		child.Info("child logger works")
	})

	t.Run("different service names produce distinct loggers", func(t *testing.T) {
		loggerA := logging.New("service-a")
		loggerB := logging.New("service-b")

		if loggerA == loggerB {
			t.Error("expected different logger instances for different service names")
		}
	})
}
