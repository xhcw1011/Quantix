package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type contextKey string

const ctxUserID contextKey = "userID"
const ctxUsername contextKey = "username"

// authMiddleware validates the JWT from Authorization header (or ?token= query param
// for WebSocket upgrades where browsers cannot set custom headers) and checks that
// the account is still active. On success it injects userID and username into
// the request context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r.Header.Get("Authorization"))
		// Fall back to ?token= query param (used by WebSocket upgrades from browsers).
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			jsonError(w, "missing authorization token", http.StatusUnauthorized)
			return
		}
		claims, err := ValidateToken(token, s.jwtSecret)
		if err != nil {
			jsonError(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		// Prevent deactivated users from using cached JWTs.
		active, err := s.store.IsUserActive(r.Context(), claims.UserID)
		if err != nil {
			s.log.Error("auth: failed to check user active status", zap.Int("user_id", claims.UserID), zap.Error(err))
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !active {
			jsonError(w, "account disabled", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxUsername, claims.Username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── Per-IP rate limiter ──────────────────────────────────────────────────────

// ipRateLimiter holds per-client token-bucket limiters with LRU-style cleanup.
type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipEntry
	r        rate.Limit
	b        int
}

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newIPRateLimiter creates a limiter allowing r requests/sec with burst b.
func newIPRateLimiter(r rate.Limit, b int) *ipRateLimiter {
	l := &ipRateLimiter{
		limiters: make(map[string]*ipEntry),
		r:        r,
		b:        b,
	}
	go l.cleanupLoop()
	return l
}

func (l *ipRateLimiter) get(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.limiters[ip]
	if !ok {
		e = &ipEntry{limiter: rate.NewLimiter(l.r, l.b)}
		l.limiters[ip] = e
	}
	e.lastSeen = time.Now()
	return e.limiter
}

// cleanupLoop removes entries not seen for 5 minutes to prevent memory growth.
func (l *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := time.Now().Add(-5 * time.Minute)
		for ip, e := range l.limiters {
			if e.lastSeen.Before(cutoff) {
				delete(l.limiters, ip)
			}
		}
		l.mu.Unlock()
	}
}

// rateLimitMiddleware returns a middleware that rejects requests from IPs
// exceeding the given limiter's rate. The limiter is configured at server
// startup from config (rate_limit_rps / rate_limit_burst).
func rateLimitMiddleware(limiter *ipRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.get(realIP(r)).Allow() {
				jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// realIP extracts the client IP. Proxy headers (X-Forwarded-For, X-Real-Ip) are
// only trusted when the direct connection comes from a loopback or private address
// (i.e. a reverse proxy). This prevents clients from spoofing their IP to bypass
// rate limiting.
func realIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// Only trust proxy headers when the connection is from a trusted (private/loopback) source.
	if isPrivateIP(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := indexByte(xff, ','); idx >= 0 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	return host
}

// isPrivateIP returns true if the IP is loopback or RFC-1918 private.
func isPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return parsed.IsLoopback() || parsed.IsPrivate()
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// maxBodyBytes returns a middleware that limits request bodies to limit bytes.
// Prevents oversized JSON payloads from causing OOM.
func maxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// allowedCORSOrigins parses QUANTIX_CORS_ORIGINS, falling back to localhost dev defaults.
// An empty Origin header (e.g. curl) is always allowed.
func allowedCORSOrigins() []string {
	if env := os.Getenv("QUANTIX_CORS_ORIGINS"); env != "" {
		var origins []string
		for _, o := range strings.Split(env, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
		return origins
	}
	return []string{"http://localhost:5173", "http://localhost:3000"}
}

// securityHeaders sets standard browser security headers on every response.
// These prevent clickjacking (X-Frame-Options), MIME-sniffing (X-Content-Type-Options),
// and encourage HTTPS (Strict-Transport-Security).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// HSTS: only set when served over TLS to avoid breaking plain-HTTP dev setups.
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware sets CORS headers based on the QUANTIX_CORS_ORIGINS whitelist.
// Requests from non-whitelisted origins receive no Allow-Origin header (browser blocks them).
// Empty Origin (curl, server-to-server) is always allowed.
func corsMiddleware(next http.Handler) http.Handler {
	allowed := allowedCORSOrigins()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Not a CORS request (curl, internal) — pass through.
		} else {
			for _, o := range allowed {
				if o == origin || o == "*" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					break
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// userIDFromCtx extracts the authenticated user ID from the request context.
func userIDFromCtx(r *http.Request) int {
	v, _ := r.Context().Value(ctxUserID).(int)
	return v
}

// adminOnly is a middleware that allows only users with role="admin" to proceed.
// It must be composed after authMiddleware so that userID is already in context.
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromCtx(r)
		role, err := s.store.GetUserRole(r.Context(), userID)
		if err != nil || role != "admin" {
			jsonError(w, "forbidden: admin access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
