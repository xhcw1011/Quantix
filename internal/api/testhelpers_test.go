package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/Quantix/quantix/internal/config"
)

// newTestServerNoStore returns a Server with nil store, a test JWT secret,
// a nop logger and an httptest.Server running its Handler().
func newTestServerNoStore(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			RateLimitRPS:   100,
			RateLimitBurst: 200,
			JWTExpiry:      time.Hour,
		},
	}

	log := zap.NewNop()
	wsHub := NewWSHub(log)

	s := &Server{
		store:       nil,
		enc:         nil,
		jwtSecret:   "test-secret-key",
		jwtExpiry:   cfg.Server.JWTExpiry,
		rateLimiter: newIPRateLimiter(rate.Limit(cfg.Server.RateLimitRPS), cfg.Server.RateLimitBurst),
		wsHub:       wsHub,
		log:         log,
	}
	s.manager = NewEngineManager(nil, nil, config.SMTPConfig{}, wsHub, cfg, nil, log)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// postJSON sends a POST request with a JSON body to the given URL.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	return resp
}

// getWithAuth sends a GET request with a Bearer token to the given URL.
func getWithAuth(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("getWithAuth: new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("getWithAuth: do: %v", err)
	}
	return resp
}
