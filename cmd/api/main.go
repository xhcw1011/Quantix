// Command api starts the Quantix REST API server.
// It provides user authentication, exchange credential management,
// live engine control, and trading data endpoints.
//
// Required environment variables:
//
//	QUANTIX_ENCRYPTION_KEY — 32-byte hex string for AES-256-GCM (e.g. openssl rand -hex 32)
//	QUANTIX_JWT_SECRET     — secret for JWT signing (any non-empty string)
//
// Optional:
//
//	QUANTIX_API_ADDR — listen address (default ":8080")
//	QUANTIX_API_CONFIG — path to config.yaml for DB DSN (default "config/config.yaml")
//
//	@title						Quantix Trading API
//	@version					1.0
//	@description				REST API for the Quantix algorithmic crypto trading platform.
//	@host						localhost:8080
//	@BasePath					/
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/api"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/logger"

	// Strategy side-effect registrations
	_ "github.com/Quantix/quantix/internal/guardian"
	_ "github.com/Quantix/quantix/internal/strategy/aistrat"
	_ "github.com/Quantix/quantix/internal/strategy/aistrat_v4"
	_ "github.com/Quantix/quantix/internal/strategy/breakout"
	_ "github.com/Quantix/quantix/internal/strategy/composite"
	_ "github.com/Quantix/quantix/internal/strategy/dca"
	_ "github.com/Quantix/quantix/internal/strategy/dipdca"
	_ "github.com/Quantix/quantix/internal/strategy/grid"
	_ "github.com/Quantix/quantix/internal/strategy/macross"
	_ "github.com/Quantix/quantix/internal/strategy/meanreversion"
	_ "github.com/Quantix/quantix/internal/strategy/mlstrat"
	_ "github.com/Quantix/quantix/internal/strategy/rebalance"
	_ "github.com/Quantix/quantix/internal/strategy/spotgrid"
	_ "github.com/Quantix/quantix/internal/strategy/spottrend"
	_ "github.com/Quantix/quantix/internal/strategy/trendradar"
)

func main() {
	cfgPath := flag.String("config", "", "path to config file (overrides QUANTIX_API_CONFIG env)")
	addr := flag.String("addr", "", "listen address (overrides QUANTIX_API_ADDR)")
	createAdmin := flag.Bool("create-admin", false, "create an admin user and exit")
	adminUser := flag.String("admin-username", "admin", "admin username (used with -create-admin)")
	adminPass := flag.String("admin-password", "", "admin password (used with -create-admin)")
	adminEmail := flag.String("admin-email", "admin@quantix.local", "admin email (used with -create-admin)")
	flag.Parse()

	// Resolve config path: -config flag > QUANTIX_API_CONFIG env > default
	if *cfgPath == "" {
		if env := os.Getenv("QUANTIX_API_CONFIG"); env != "" {
			*cfgPath = env
		} else {
			*cfgPath = "config/config.yaml"
		}
	}

	if *createAdmin {
		if err := createAdminUser(*cfgPath, *adminUser, *adminEmail, *adminPass); err != nil {
			fmt.Fprintf(os.Stderr, "create-admin failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*cfgPath, *addr); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// createAdminUser connects to the DB and inserts an admin-role user.
func createAdminUser(cfgPath, username, email, password string) error {
	if username == "" || password == "" {
		return fmt.Errorf("--admin-username and --admin-password are required")
	}
	if len(password) < 8 {
		return fmt.Errorf("admin password must be at least 8 characters")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log, _ := logger.New("production", "info", cfg.App.LogDir)
	defer log.Sync() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer store.Close()

	hash, err := api.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	id, err := store.CreateAdminUser(ctx, username, email, hash)
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	fmt.Printf("Admin user created: id=%d username=%s\n", id, username)
	return nil
}

func run(cfgPath, addrFlag string) error {
	// Config
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Logger
	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, cfg.App.LogDir)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck

	// Encryption key from env
	encKeyHex := os.Getenv("QUANTIX_ENCRYPTION_KEY")
	if encKeyHex == "" {
		return fmt.Errorf("QUANTIX_ENCRYPTION_KEY env var is required (32 bytes hex)")
	}
	encKeyBytes, err := hex.DecodeString(encKeyHex)
	if err != nil || len(encKeyBytes) != 32 {
		return fmt.Errorf("QUANTIX_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}
	enc, err := api.NewEncryptor(encKeyBytes)
	if err != nil {
		return fmt.Errorf("create encryptor: %w", err)
	}

	// JWT secret from env (required)
	jwtSecret := os.Getenv("QUANTIX_JWT_SECRET")
	if jwtSecret == "" {
		return fmt.Errorf("QUANTIX_JWT_SECRET env var is required (use a long random string, e.g. openssl rand -hex 32)")
	}
	if len(jwtSecret) < 32 {
		return fmt.Errorf("QUANTIX_JWT_SECRET must be at least 32 characters for security")
	}

	// Database
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer store.Close()

	// Run DB migrations automatically on startup
	if err := store.RunMigrations(ctx, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Redis (optional — used for position syncer)
	var rdb *redis.Client
	if cfg.Redis.Addr != "" {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Warn("Redis not available — position syncer disabled", zap.Error(err))
			rdb = nil
		} else {
			log.Info("Redis connected", zap.String("addr", cfg.Redis.Addr))
			defer rdb.Close()
		}
	}

	// Server
	srv := api.NewServer(store, enc, jwtSecret, cfg.SMTP, cfg, rdb, log)

	listenAddr := os.Getenv("QUANTIX_API_ADDR")
	if addrFlag != "" {
		listenAddr = addrFlag
	}
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	readTimeout := cfg.Server.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 15 * time.Second
	}
	writeTimeout := cfg.Server.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 30 * time.Second
	}
	idleTimeout := cfg.Server.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Second
	}

	httpServer := &http.Server{
		Addr:         listenAddr,
		Handler:      srv.Handler(),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	log.Info("API server starting", zap.String("addr", listenAddr))

	// After a brief delay (to allow WS connections to establish), auto-restart
	// any engines that were running before the last server shutdown.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		srv.AutoRestart(ctx)
	}()

	go func() {
		<-ctx.Done()
		// Stop all running engines first to prevent orphaned exchange orders.
		srv.StopAllEngines()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutCancel()
		if err := httpServer.Shutdown(shutCtx); err != nil {
			log.Error("graceful shutdown failed", zap.Error(err))
		}
	}()

	tlsCert := os.Getenv("QUANTIX_TLS_CERT")
	tlsKey := os.Getenv("QUANTIX_TLS_KEY")

	var listenErr error
	if tlsCert != "" && tlsKey != "" {
		log.Info("TLS enabled", zap.String("cert", tlsCert))
		listenErr = httpServer.ListenAndServeTLS(tlsCert, tlsKey)
	} else {
		listenErr = httpServer.ListenAndServe()
	}
	if listenErr != nil && listenErr != http.ErrServerClosed {
		return fmt.Errorf("listen: %w", listenErr)
	}
	log.Info("API server stopped")
	return nil
}
