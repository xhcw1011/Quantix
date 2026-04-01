// Package bus provides a NATS-backed event bus for decoupling Quantix components.
//
// Topic hierarchy:
//
//	quantix.kline.{symbol}.{interval}   – closed OHLCV bar
//	quantix.fill.{strategy_id}          – order fill from paper/live engine
//	quantix.alert                       – risk, system and trade alerts
//	quantix.status.{strategy_id}        – periodic heartbeat / P&L snapshot
package bus

import (
	"fmt"
	"time"
)

// ─── Topic helpers ────────────────────────────────────────────────────────────

func TopicKline(symbol, interval string) string {
	return fmt.Sprintf("quantix.kline.%s.%s", symbol, interval)
}

func TopicFill(strategyID string) string {
	return fmt.Sprintf("quantix.fill.%s", strategyID)
}

func TopicAlert() string { return "quantix.alert" }

func TopicStatus(strategyID string) string {
	return fmt.Sprintf("quantix.status.%s", strategyID)
}

// ─── Message types ─────────────────────────────────────────────────────────

// KlineMsg is published when a kline bar closes.
type KlineMsg struct {
	Symbol    string    `json:"symbol"`
	Interval  string    `json:"interval"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
	OpenTime  time.Time `json:"open_time"`
	CloseTime time.Time `json:"close_time"`
}

// FillMsg is published after an order is filled.
type FillMsg struct {
	StrategyID  string    `json:"strategy_id"`
	OrderID     string    `json:"order_id"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	Qty         float64   `json:"qty"`
	Price       float64   `json:"price"`
	Fee         float64   `json:"fee"`
	RealizedPnL float64   `json:"realized_pnl"`
	Timestamp   time.Time `json:"timestamp"`
}

// AlertLevel indicates severity.
type AlertLevel string

const (
	AlertInfo  AlertLevel = "INFO"
	AlertWarn  AlertLevel = "WARN"
	AlertError AlertLevel = "ERROR"
)

// AlertType categorises the alert source.
type AlertType string

const (
	AlertTrade  AlertType = "TRADE"
	AlertRisk   AlertType = "RISK"
	AlertSystem AlertType = "SYSTEM"
)

// AlertMsg is published for any noteworthy event.
type AlertMsg struct {
	Level      AlertLevel `json:"level"`
	Type       AlertType  `json:"type"`
	StrategyID string     `json:"strategy_id,omitempty"`
	Message    string     `json:"message"`
	Timestamp  time.Time  `json:"timestamp"`
}

// StatusMsg is a periodic P&L snapshot.
type StatusMsg struct {
	StrategyID     string    `json:"strategy_id"`
	Cash           float64   `json:"cash"`
	Equity         float64   `json:"equity"`
	RealizedPnL    float64   `json:"realized_pnl"`
	UnrealizedPnL  float64   `json:"unrealized_pnl"`
	TotalReturnPct float64   `json:"total_return_pct"`
	OpenPositions  int       `json:"open_positions"`
	RiskHalted     bool      `json:"risk_halted"`
	Timestamp      time.Time `json:"timestamp"`
}
