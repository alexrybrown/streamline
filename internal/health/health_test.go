package health_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexrybrown/streamline/internal/health"
)

func TestHealthHandler_Healthy(t *testing.T) {
	h := health.NewChecker()
	handler := h.Handler()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHealthHandler_WithFailingCheck(t *testing.T) {
	h := health.NewChecker()
	h.AddCheck("kafka", func() error {
		return fmt.Errorf("connection refused")
	})
	handler := h.Handler()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}
