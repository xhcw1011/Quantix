package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	httpSwagger "github.com/swaggo/http-swagger"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	_ "github.com/Quantix/quantix/docs" // Swagger generated docs
)

// Server is the Quantix HTTP API server.
type Server struct {
	store       *data.Store
	enc         *Encryptor
	manager     *EngineManager
	wsHub       *WSHub
	jwtSecret   string
	jwtExpiry   time.Duration
	rateLimiter *ipRateLimiter
	log         *zap.Logger
	logDir      string
	logLevel    string
}

// NewServer creates a configured Server.
// cfg is the full application configuration; smtpCfg is optional (zero-value disables email).
func NewServer(store *data.Store, enc *Encryptor, jwtSecret string, smtpCfg config.SMTPConfig, cfg *config.Config, rdb *redis.Client, log *zap.Logger) *Server {
	// Rate limiter: use config values, fall back to safe defaults.
	rps := cfg.Server.RateLimitRPS
	if rps <= 0 {
		rps = 10
	}
	burst := cfg.Server.RateLimitBurst
	if burst <= 0 {
		burst = 30
	}
	jwtExpiry := cfg.Server.JWTExpiry
	if jwtExpiry <= 0 {
		jwtExpiry = 24 * time.Hour
	}

	wsHub := NewWSHub(log)
	s := &Server{
		store:       store,
		enc:         enc,
		jwtSecret:   jwtSecret,
		jwtExpiry:   jwtExpiry,
		rateLimiter: newIPRateLimiter(rate.Limit(rps), burst),
		wsHub:       wsHub,
		log:         log,
		logDir:      cfg.App.LogDir,
		logLevel:    cfg.App.LogLevel,
	}
	s.manager = NewEngineManager(store, enc, smtpCfg, wsHub, cfg, rdb, log)
	return s
}

// StopAllEngines gracefully cancels all running engines across all users.
// Called before HTTP server shutdown to avoid orphaned orders.
// NOTE: sessions are intentionally NOT deactivated here so they are
// auto-restarted when the server comes back up.
func (s *Server) StopAllEngines() {
	s.manager.StopAllUsers()
}

// AutoRestart restores all active engine sessions persisted in the DB.
// Call once after server startup (typically in a goroutine with a small delay).
func (s *Server) AutoRestart(ctx context.Context) {
	s.manager.AutoRestart(ctx)
}

// handleHealth returns server and database health status.
//
//	@Summary		Health check
//	@Description	Returns API and database health status
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Failure		503	{object}	map[string]interface{}
//	@Router			/health [get]
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	dbOK := s.store.Ping(ctx) == nil
	status := "healthy"
	code := http.StatusOK
	if !dbOK {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	w.WriteHeader(code)
	jsonOK(w, map[string]any{
		"status":   status,
		"database": dbOK,
		"time":     time.Now().UTC(),
	})
}

// handleReady reports operational readiness including live-trading kill-switch status.
// Unlike /health (which checks infrastructure), /ready tells operators whether the
// server is configured to accept live trading requests.
//
//	@Summary		Readiness check
//	@Description	Reports operational readiness and live-trading gate status
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Failure		503	{object}	map[string]interface{}
//	@Router			/ready [get]
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	dbOK := s.store.Ping(ctx) == nil
	liveEnabled := s.manager.LiveEnabled()

	status := "ready"
	code := http.StatusOK
	if !dbOK {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	w.WriteHeader(code)
	jsonOK(w, map[string]any{
		"status":       status,
		"database":     dbOK,
		"live_enabled": liveEnabled,
		"time":         time.Now().UTC(),
	})
}

// Handler returns the top-level http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health check — no auth required
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// Readiness check — no auth required
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("GET /api/ready", s.handleReady)

	// Swagger UI — no auth required
	mux.Handle("/api/docs/", httpSwagger.WrapHandler)

	// Public endpoints
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Protected endpoints — wrapped individually below
	auth := s.authMiddleware

	mux.Handle("GET /api/users/me", auth(http.HandlerFunc(s.handleMe)))
	mux.Handle("PUT /api/users/me/password", auth(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("GET /api/users/me/notifications", auth(http.HandlerFunc(s.handleGetNotifications)))
	mux.Handle("PUT /api/users/me/notifications", auth(http.HandlerFunc(s.handleUpdateNotifications)))
	mux.Handle("POST /api/users/me/notifications/test", auth(http.HandlerFunc(s.handleTestNotification)))

	// Credentials
	mux.Handle("GET /api/credentials", auth(http.HandlerFunc(s.handleListCredentials)))
	mux.Handle("POST /api/credentials", auth(http.HandlerFunc(s.handleCreateCredential)))
	mux.Handle("PUT /api/credentials/{id}", auth(http.HandlerFunc(s.handleUpdateCredential)))
	mux.Handle("DELETE /api/credentials/{id}", auth(http.HandlerFunc(s.handleDeleteCredential)))
	mux.Handle("POST /api/credentials/{id}/test", auth(http.HandlerFunc(s.handleTestCredential)))

	// Trading data
	mux.Handle("GET /api/orders", auth(http.HandlerFunc(s.handleOrders)))
	mux.Handle("GET /api/fills", auth(http.HandlerFunc(s.handleFills)))
	mux.Handle("GET /api/equity", auth(http.HandlerFunc(s.handleEquity)))
	mux.Handle("GET /api/summary", auth(http.HandlerFunc(s.handleSummary)))
	mux.Handle("GET /api/positions", auth(http.HandlerFunc(s.handlePositions)))

	// Strategies
	mux.Handle("GET /api/strategies", auth(http.HandlerFunc(s.handleListStrategies)))

	// Engine control — multi-engine (new)
	mux.Handle("GET /api/engines", auth(http.HandlerFunc(s.listEngines)))
	mux.Handle("POST /api/engines", auth(http.HandlerFunc(s.startEngine)))
	mux.Handle("DELETE /api/engines/{id}", auth(http.HandlerFunc(s.stopEngineByID)))
	mux.Handle("GET /api/engines/{id}", auth(http.HandlerFunc(s.getEngineByID)))

	// Engine control — legacy (deprecated, kept for backward compat)
	mux.Handle("POST /api/engine/start", auth(http.HandlerFunc(s.handleEngineStart)))
	mux.Handle("POST /api/engine/stop", auth(http.HandlerFunc(s.handleEngineStop)))
	mux.Handle("GET /api/engine/status", auth(http.HandlerFunc(s.handleEngineStatus)))

	// Backtest
	mux.Handle("POST /api/backtest", auth(http.HandlerFunc(s.submitBacktest)))
	mux.Handle("GET /api/backtest", auth(http.HandlerFunc(s.listBacktests)))
	mux.Handle("GET /api/backtest/{id}", auth(http.HandlerFunc(s.getBacktest)))
	mux.Handle("DELETE /api/backtest/{id}", auth(http.HandlerFunc(s.deleteBacktest)))

	// Admin endpoints (require auth + admin role)
	admin := func(h http.HandlerFunc) http.Handler {
		return auth(s.adminOnly(http.HandlerFunc(h)))
	}
	mux.Handle("GET /api/admin/users", admin(s.adminListUsers))
	mux.Handle("PUT /api/admin/users/{id}/activate", admin(s.adminSetUserActive))
	mux.Handle("GET /api/admin/engines", admin(s.adminListEngines))
	mux.Handle("DELETE /api/admin/engines/{user_id}/{engine_id}", admin(s.adminForceStopEngine))

	// WebSocket
	mux.Handle("GET /api/ws", auth(http.HandlerFunc(s.handleWS)))

	return securityHeaders(corsMiddleware(rateLimitMiddleware(s.rateLimiter)(maxBodyBytes(1<<20)(mux))))
}

// ─── Shared response types (used in Swagger annotations) ─────────────────────

// errorResp is returned for any API error.
type errorResp struct {
	Error string `json:"error"`
}

// msgResp wraps a plain message string.
type msgResp struct {
	Message string `json:"message"`
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Connection likely closed by client; log at debug level to avoid noise.
		_, _ = w.Write(nil) // trigger write error detection
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		_, _ = w.Write(nil)
	}
}

func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
