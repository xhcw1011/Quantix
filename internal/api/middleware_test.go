package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// TestRealIP
// ---------------------------------------------------------------------------

func TestRealIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		want       string
	}{
		{
			name:       "XFF trusted when RemoteAddr is private",
			xff:        "1.2.3.4",
			remoteAddr: "127.0.0.1:1234",
			want:       "1.2.3.4",
		},
		{
			name:       "XFF multiple IPs takes first (private remote)",
			xff:        "10.0.0.1, 10.0.0.2, 10.0.0.3",
			remoteAddr: "127.0.0.1:1234",
			want:       "10.0.0.1",
		},
		{
			name:       "X-Real-Ip used when no XFF (private remote)",
			xri:        "5.6.7.8",
			remoteAddr: "10.0.0.1:1234",
			want:       "5.6.7.8",
		},
		{
			name:       "RemoteAddr fallback with port",
			remoteAddr: "192.168.1.1:12345",
			want:       "192.168.1.1",
		},
		{
			name:       "RemoteAddr fallback without port",
			remoteAddr: "192.168.1.1",
			want:       "192.168.1.1",
		},
		{
			name:       "XFF takes priority over XRI (private remote)",
			xff:        "1.1.1.1",
			xri:        "2.2.2.2",
			remoteAddr: "127.0.0.1:80",
			want:       "1.1.1.1",
		},
		{
			name:       "XFF ignored when RemoteAddr is public",
			xff:        "1.1.1.1",
			remoteAddr: "3.3.3.3:80",
			want:       "3.3.3.3",
		},
		{
			name:       "X-Real-Ip ignored when RemoteAddr is public",
			xri:        "5.6.7.8",
			remoteAddr: "8.8.8.8:1234",
			want:       "8.8.8.8",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.xri != "" {
				r.Header.Set("X-Real-Ip", tc.xri)
			}
			if tc.remoteAddr != "" {
				r.RemoteAddr = tc.remoteAddr
			}
			got := realIP(r)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestRateLimiter_AllowsUnderLimit
// ---------------------------------------------------------------------------

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	limiter := &ipRateLimiter{
		limiters: make(map[string]*ipEntry),
		r:        rate.Limit(100),
		b:        100,
	}
	handler := rateLimitMiddleware(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 50; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		handler.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code, "request %d should succeed", i)
	}
}

// ---------------------------------------------------------------------------
// TestRateLimiter_BlocksOverLimit
// ---------------------------------------------------------------------------

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	limiter := &ipRateLimiter{
		limiters: make(map[string]*ipEntry),
		r:        rate.Limit(1),
		b:        1,
	}
	handler := rateLimitMiddleware(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request should succeed.
	rr1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(rr1, req1)
	assert.Equal(t, http.StatusOK, rr1.Code)

	// Second request from same IP should be rate-limited.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.1:1234"
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusTooManyRequests, rr2.Code)

	// Request from a different IP should succeed.
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.RemoteAddr = "10.0.0.2:1234"
	handler.ServeHTTP(rr3, req3)
	assert.Equal(t, http.StatusOK, rr3.Code)
}

// ---------------------------------------------------------------------------
// TestCORSMiddleware_AllowedOrigin
// ---------------------------------------------------------------------------

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	t.Setenv("QUANTIX_CORS_ORIGINS", "http://example.com")

	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Allowed origin gets the header.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "http://example.com")
	handler.ServeHTTP(rr, req)
	assert.Equal(t, "http://example.com", rr.Header().Get("Access-Control-Allow-Origin"))

	// Disallowed origin does NOT get the header.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Origin", "http://evil.com")
	handler.ServeHTTP(rr2, req2)
	assert.Empty(t, rr2.Header().Get("Access-Control-Allow-Origin"))
}

// ---------------------------------------------------------------------------
// TestCORSMiddleware_Preflight
// ---------------------------------------------------------------------------

func TestCORSMiddleware_Preflight(t *testing.T) {
	t.Setenv("QUANTIX_CORS_ORIGINS", "http://example.com")

	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "http://example.com")
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "http://example.com", rr.Header().Get("Access-Control-Allow-Origin"))
}

// ---------------------------------------------------------------------------
// TestMaxBodyBytes
// ---------------------------------------------------------------------------

func TestMaxBodyBytes(t *testing.T) {
	handler := maxBodyBytes(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Body larger than limit should trigger error.
	rr := httptest.NewRecorder()
	body := strings.NewReader("this body is definitely longer than 10 bytes")
	req := httptest.NewRequest(http.MethodPost, "/", body)
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

// ---------------------------------------------------------------------------
// TestUserIDFromCtx
// ---------------------------------------------------------------------------

func TestUserIDFromCtx(t *testing.T) {
	// With a userID in context.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), ctxUserID, 42)
	req = req.WithContext(ctx)
	assert.Equal(t, 42, userIDFromCtx(req))

	// Without a userID in context — should return zero value.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, 0, userIDFromCtx(req2))
}
