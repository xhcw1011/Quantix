package data

import "time"

// User represents an authenticated system user.
type User struct {
	ID           int
	Username     string
	Email        string
	PasswordHash string
	Role         string // "user" | "admin"
	IsActive     bool
	CreatedAt    time.Time

	// Per-user Telegram notification settings (added by migration 004).
	TgBotToken string
	TgChatID   int64
}

// Credential stores (encrypted) exchange API credentials for a user.
type Credential struct {
	ID         int
	UserID     int
	Exchange   string // "binance" | "okx" | "bybit"
	Label      string // user-defined name
	APIKey     string // AES-256-GCM encrypted, base64
	APISecret  string // AES-256-GCM encrypted, base64
	Passphrase string // OKX only, encrypted; empty otherwise
	Testnet    bool
	Demo       bool
	MarketType string
	IsActive   bool
	CreatedAt  time.Time
}

// Fill records a single trade execution persisted to the database.
type Fill struct {
	ID              int64
	UserID          int
	StrategyID      string
	Symbol          string
	Side            string // "BUY" | "SELL"
	PositionSide    string // hedge mode: "LONG", "SHORT", or "" (Phase 16)
	Qty             float64
	Price           float64
	Fee             float64
	RealizedPnL     float64
	ExchangeOrderID string
	Mode            string // "live" | "paper"
	FilledAt        time.Time
}

// EquitySnapshot captures a point-in-time equity value for charting.
type EquitySnapshot struct {
	ID            int64
	UserID        int
	StrategyID    string
	Equity        float64
	Cash          float64
	UnrealizedPnL float64
	RealizedPnL   float64
	SnapshottedAt time.Time
}

// OrderRecord mirrors the orders table for API queries and OMS persistence.
type OrderRecord struct {
	ID             string
	ExchangeID     string
	Symbol         string
	Side           string
	Type           string
	Status         string
	Quantity       float64
	Price          float64
	FilledQuantity float64
	AvgFillPrice   float64
	Commission     float64
	StrategyID     string
	Mode           string
	UserID         int
	CredentialID   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Phase 15: OMS persistence fields
	PositionSide  string  // hedge mode direction: "LONG", "SHORT", or ""
	StopPrice     float64 // stop trigger price (STOP_MARKET / STOP_LIMIT)
	RejectReason  string  // reason for REJECTED status
	ClientOrderID string  // 32-char UUID without dashes (idempotency key)
	// Phase 16: protective order role
	OrderRole string // "" | "stop_loss" | "take_profit"
}

// EngineSessionRow is a row from engine_sessions used by AutoRestart.
type EngineSessionRow struct {
	UserID      int
	EngineID    string
	RequestJSON []byte
}
