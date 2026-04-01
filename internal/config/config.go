// Package config loads and exposes typed application configuration via Viper.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration structure.
type Config struct {
	App       AppConfig       `mapstructure:"app"`
	Exchange  ExchangeConfig  `mapstructure:"exchange"`
	Data      DataConfig      `mapstructure:"data"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
	NATS      NATSConfig      `mapstructure:"nats"`
	Trading   TradingConfig   `mapstructure:"trading"`
	Risk      RiskConfig      `mapstructure:"risk"`
	Monitor   MonitorConfig   `mapstructure:"monitor"`
	Server    ServerConfig    `mapstructure:"server"`
	Telegram  TelegramConfig  `mapstructure:"telegram"`
	SMTP      SMTPConfig      `mapstructure:"smtp"`
	Paper     PaperConfig     `mapstructure:"paper"`
	Live      LiveConfig      `mapstructure:"live"`
	Portfolio PortfolioConfig `mapstructure:"portfolio"`
	OpenAI    OpenAIConfig    `mapstructure:"openai"`
}

// OpenAIConfig holds settings for GPT-powered AI strategies.
type OpenAIConfig struct {
	APIKey string `mapstructure:"api_key"` // OpenAI API key; also via QUANTIX_OPENAI_API_KEY env
	Model  string `mapstructure:"model"`   // default "gpt-5.4-mini"
}

type AppConfig struct {
	Name     string `mapstructure:"name"`
	Env      string `mapstructure:"env"`
	LogLevel string `mapstructure:"log_level"`
}

type ExchangeConfig struct {
	Active  string        `mapstructure:"active"`
	Binance BinanceConfig `mapstructure:"binance"`
	OKX     OKXConfig     `mapstructure:"okx"`
	Bybit   BybitConfig   `mapstructure:"bybit"`
}

type BinanceConfig struct {
	APIKey         string `mapstructure:"api_key"`
	APISecret      string `mapstructure:"api_secret"`
	Testnet        bool   `mapstructure:"testnet"`
	Demo           bool   `mapstructure:"demo"`              // true → use demo-api.binance.com (official demo trading)
	MarketType     string `mapstructure:"market_type"`      // "spot" (default) | "futures"
	KeyType        string `mapstructure:"key_type"`          // "HMAC" (default) | "RSA" | "ED25519"
	PrivateKeyPath string `mapstructure:"private_key_path"`  // path to PEM file (required for RSA/ED25519)
}

type OKXConfig struct {
	APIKey     string `mapstructure:"api_key"`
	APISecret  string `mapstructure:"api_secret"`
	Passphrase string `mapstructure:"passphrase"`
	Demo       bool   `mapstructure:"demo"`        // true → x-simulated-trading: 1
	MarketType string `mapstructure:"market_type"` // "spot" or "swap"
}

type BybitConfig struct {
	APIKey    string `mapstructure:"api_key"`
	APISecret string `mapstructure:"api_secret"`
}

type DataConfig struct {
	Symbols       []string `mapstructure:"symbols"`
	Intervals     []string `mapstructure:"intervals"`
	BackfillLimit int      `mapstructure:"backfill_limit"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Name     string `mapstructure:"name"`
	SSLMode  string `mapstructure:"ssl_mode"`
	MaxConns int    `mapstructure:"max_conns"`
}

// DSN returns a pgx-compatible connection string.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s pool_max_conns=%d",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode, d.MaxConns,
	)
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type NATSConfig struct {
	URL string `mapstructure:"url"`
}

type TradingConfig struct {
	Mode         string `mapstructure:"mode"`
	BaseCurrency string `mapstructure:"base_currency"`
}

type RiskConfig struct {
	MaxPositionPct   float64 `mapstructure:"max_position_pct"`
	MaxDrawdownPct   float64 `mapstructure:"max_drawdown_pct"`
	MaxSingleLossPct float64 `mapstructure:"max_single_loss_pct"`
}

type MonitorConfig struct {
	PrometheusPort          int           `mapstructure:"prometheus_port"`
	Enabled                 bool          `mapstructure:"enabled"`
	MarginWarnThreshold     float64       `mapstructure:"margin_warn_threshold"`     // default 0.20
	MarginCriticalThreshold float64       `mapstructure:"margin_critical_threshold"` // default 0.12
	MarginCheckInterval     time.Duration `mapstructure:"margin_check_interval"`     // default 60s
}

// ServerConfig holds HTTP server and API operational parameters.
type ServerConfig struct {
	RateLimitRPS   float64       `mapstructure:"rate_limit_rps"`   // requests/sec per IP; default 10
	RateLimitBurst int           `mapstructure:"rate_limit_burst"` // burst size; default 30
	JWTExpiry      time.Duration `mapstructure:"jwt_expiry"`       // token validity; default 24h
	ReadTimeout    time.Duration `mapstructure:"read_timeout"`     // default 15s
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`    // default 30s
	IdleTimeout    time.Duration `mapstructure:"idle_timeout"`     // default 60s
}

type TelegramConfig struct {
	BotToken string `mapstructure:"bot_token"`
	ChatID   int64  `mapstructure:"chat_id"`
	// Enabled is true when both BotToken and ChatID are set.
}

// SMTPConfig holds outbound mail server credentials for email notifications.
// If Host is empty, email notifications are disabled.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`     // 587 STARTTLS or 465 TLS; default 587
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"` // sender address, e.g. "Quantix <alerts@example.com>"
}

type PaperConfig struct {
	InitialCapital float64 `mapstructure:"initial_capital"`
	FeeRate        float64 `mapstructure:"fee_rate"`
	Slippage       float64 `mapstructure:"slippage"`
	StrategyID     string  `mapstructure:"strategy_id"`
}

// LiveConfig controls the live trading engine.
type LiveConfig struct {
	StrategyID string `mapstructure:"strategy_id"` // registry name (e.g. "macross", "grid")
	Symbol     string `mapstructure:"symbol"`      // primary symbol (e.g. "BTCUSDT")
	Interval   string `mapstructure:"interval"`    // kline interval (e.g. "1h")
	// Enabled is a kill-switch for live trading. When false (default),
	// all live engine starts are rejected. Paper mode is unaffected.
	// Override via config live.enabled or env QUANTIX_LIVE_ENABLED.
	Enabled bool `mapstructure:"enabled"`
}

// PortfolioConfig controls the multi-strategy portfolio manager.
type PortfolioConfig struct {
	Enabled bool         `mapstructure:"enabled"`
	Slots   []SlotConfig `mapstructure:"allocations"`
}

// SlotConfig defines one strategy slot within the portfolio.
type SlotConfig struct {
	Strategy    string         `mapstructure:"strategy"`
	Symbol      string         `mapstructure:"symbol"`
	Interval    string         `mapstructure:"interval"`
	Params      map[string]any `mapstructure:"params"`
	FracCapital float64        `mapstructure:"capital_frac"`
}

// Load reads config from configPath (YAML) and overlays environment variables.
// Env var format: QUANTIX_<SECTION>_<KEY> (e.g. QUANTIX_DATABASE_PASSWORD).
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// File-based config
	v.SetConfigFile(configPath)

	// Environment variable overrides
	v.SetEnvPrefix("QUANTIX")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return &cfg, nil
}

// Validate checks that critical configuration fields are set.
func (cfg *Config) Validate() error {
	if cfg.Database.Host == "" {
		return fmt.Errorf("database.host is required")
	}
	if cfg.Database.Name == "" {
		return fmt.Errorf("database.name is required")
	}
	if cfg.Database.User == "" {
		return fmt.Errorf("database.user is required")
	}
	if cfg.Database.Port <= 0 || cfg.Database.Port > 65535 {
		return fmt.Errorf("database.port must be between 1 and 65535, got %d", cfg.Database.Port)
	}
	return nil
}

// applyDefaults fills zero-valued config fields with safe production defaults.
func applyDefaults(cfg *Config) {
	// Database
	if cfg.Database.MaxConns <= 0 {
		cfg.Database.MaxConns = 16
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}

	// Risk — ensure non-zero limits
	if cfg.Risk.MaxPositionPct <= 0 {
		cfg.Risk.MaxPositionPct = 0.10
	}
	if cfg.Risk.MaxDrawdownPct <= 0 {
		cfg.Risk.MaxDrawdownPct = 0.15
	}
	if cfg.Risk.MaxSingleLossPct <= 0 {
		cfg.Risk.MaxSingleLossPct = 0.02
	}

	// Server
	if cfg.Server.RateLimitRPS <= 0 {
		cfg.Server.RateLimitRPS = 10
	}
	if cfg.Server.RateLimitBurst <= 0 {
		cfg.Server.RateLimitBurst = 30
	}
	if cfg.Server.JWTExpiry <= 0 {
		cfg.Server.JWTExpiry = 24 * time.Hour
	}
	if cfg.Server.JWTExpiry > 7*24*time.Hour {
		cfg.Server.JWTExpiry = 7 * 24 * time.Hour
	}
	if cfg.Server.ReadTimeout <= 0 {
		cfg.Server.ReadTimeout = 15 * time.Second
	}
	if cfg.Server.WriteTimeout <= 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.IdleTimeout <= 0 {
		cfg.Server.IdleTimeout = 60 * time.Second
	}

	// Monitor
	if cfg.Monitor.MarginCheckInterval <= 0 {
		cfg.Monitor.MarginCheckInterval = 60 * time.Second
	}

	// Data
	if cfg.Data.BackfillLimit <= 0 {
		cfg.Data.BackfillLimit = 500
	}

	// Paper
	if cfg.Paper.InitialCapital <= 0 {
		cfg.Paper.InitialCapital = 10000
	}
	if cfg.Paper.FeeRate <= 0 {
		cfg.Paper.FeeRate = 0.001
	}

	// SMTP
	if cfg.SMTP.Port <= 0 {
		cfg.SMTP.Port = 587
	}
}
