package health

import (
	"encoding/json"
	"net/http"
	"sync"
)

// CheckFunc returns nil if healthy, error otherwise.
type CheckFunc func() error

// Checker manages health check functions.
type Checker struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewChecker creates a new health checker.
func NewChecker() *Checker {
	return &Checker{checks: make(map[string]CheckFunc)}
}

// AddCheck registers a named health check.
func (c *Checker) AddCheck(name string, check CheckFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = check
}

// Handler returns an HTTP handler for health checks.
func (c *Checker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		defer c.mu.RUnlock()

		status := http.StatusOK
		results := make(map[string]string)

		for name, check := range c.checks {
			if err := check(); err != nil {
				status = http.StatusServiceUnavailable
				results[name] = err.Error()
			} else {
				results[name] = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(results)
	})
}
