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
	ID              int64     `json:"id"`
	UserID          int       `json:"user_id"`
	StrategyID      string    `json:"strategy_id"`
	Symbol          string    `json:"symbol"`
	Side            string    `json:"side"` // "BUY" | "SELL"
	PositionSide    string    `json:"position_side"` // hedge mode: "LONG", "SHORT", or "" (Phase 16)
	Qty             float64   `json:"qty"`
	Price           float64   `json:"price"`
	Fee             float64   `json:"fee"`
	RealizedPnL     float64   `json:"realized_pnl"`
	ExchangeOrderID string    `json:"exchange_order_id"`
	Mode            string    `json:"mode"` // "live" | "paper"
	FilledAt        time.Time `json:"filled_at"`
}

// EquitySnapshot captures a point-in-time equity value for charting.
type EquitySnapshot struct {
	ID            int64     `json:"id"`
	UserID        int       `json:"user_id"`
	StrategyID    string    `json:"strategy_id"`
	Equity        float64   `json:"equity"`
	Cash          float64   `json:"cash"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
	RealizedPnL   float64   `json:"realized_pnl"`
	SnapshottedAt time.Time `json:"snapshotted_at"`
}

// OrderRecord mirrors the orders table for API queries and OMS persistence.
type OrderRecord struct {
	ID             string    `json:"id"`
	ExchangeID     string    `json:"exchange_id"`
	Symbol         string    `json:"symbol"`
	Side           string    `json:"side"`
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	Quantity       float64   `json:"quantity"`
	Price          float64   `json:"price"`
	FilledQuantity float64   `json:"filled_quantity"`
	AvgFillPrice   float64   `json:"avg_fill_price"`
	Commission     float64   `json:"commission"`
	StrategyID     string    `json:"strategy_id"`
	Mode           string    `json:"mode"`
	UserID         int       `json:"user_id"`
	CredentialID   int       `json:"credential_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Phase 15: OMS persistence fields
	PositionSide  string  `json:"position_side"` // hedge mode direction: "LONG", "SHORT", or ""
	StopPrice     float64 `json:"stop_price"`    // stop trigger price (STOP_MARKET / STOP_LIMIT)
	RejectReason  string  `json:"reject_reason"` // reason for REJECTED status
	ClientOrderID string  `json:"client_order_id"` // 32-char UUID without dashes (idempotency key)
	// Phase 16: protective order role
	OrderRole string `json:"order_role"` // "" | "stop_loss" | "take_profit"
}

// EngineSessionRow is a row from engine_sessions used by AutoRestart.
type EngineSessionRow struct {
	UserID      int
	EngineID    string
	RequestJSON []byte
}
