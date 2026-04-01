package monitor

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	tradingMetricsOnce     sync.Once
	tradingMetricsSingleton *TradingMetrics
)

// TradingMetrics holds Prometheus metrics specific to trading activity.
type TradingMetrics struct {
	// EquityGauge tracks current portfolio equity in USD.
	EquityGauge *prometheus.GaugeVec

	// RealizedPnL tracks cumulative realized profit/loss.
	RealizedPnL *prometheus.GaugeVec

	// UnrealizedPnL tracks current mark-to-market unrealized P&L.
	UnrealizedPnL *prometheus.GaugeVec

	// FillsTotal counts order fills.
	FillsTotal *prometheus.CounterVec

	// TradeLatency measures time from bar-close to fill (ms).
	TradeLatency *prometheus.HistogramVec

	// OpenPositions tracks the number of open positions.
	OpenPositions *prometheus.GaugeVec

	// WinRate tracks the rolling win rate (0–100).
	WinRate *prometheus.GaugeVec

	// RiskHalted is 1 when the circuit breaker is active, 0 otherwise.
	RiskHalted *prometheus.GaugeVec

	// NATSPublished counts events published to NATS.
	NATSPublished *prometheus.CounterVec

	// AlertsSent counts Telegram alerts by type.
	AlertsSent *prometheus.CounterVec
}

// NewTradingMetrics returns the singleton TradingMetrics, registering Prometheus
// collectors on first call. Safe to call multiple times (e.g. engine restart).
func NewTradingMetrics() *TradingMetrics {
	tradingMetricsOnce.Do(func() {
		tradingMetricsSingleton = newTradingMetrics()
	})
	return tradingMetricsSingleton
}

func newTradingMetrics() *TradingMetrics {
	return &TradingMetrics{
		EquityGauge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_equity_usd",
			Help: "Current portfolio equity in USD.",
		}, []string{"strategy"}),

		RealizedPnL: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_realized_pnl_usd",
			Help: "Cumulative realized profit/loss in USD.",
		}, []string{"strategy"}),

		UnrealizedPnL: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_unrealized_pnl_usd",
			Help: "Current unrealized P&L in USD.",
		}, []string{"strategy"}),

		FillsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_fills_total",
			Help: "Total number of order fills.",
		}, []string{"strategy", "symbol", "side", "mode"}),

		TradeLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "quantix_trade_latency_ms",
			Help:    "Time from bar close to fill, in milliseconds.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
		}, []string{"strategy"}),

		OpenPositions: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_open_positions",
			Help: "Number of currently open positions.",
		}, []string{"strategy"}),

		WinRate: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_win_rate_pct",
			Help: "Rolling win rate percentage (0-100).",
		}, []string{"strategy"}),

		RiskHalted: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "quantix_risk_halted",
			Help: "1 if the circuit breaker is active, 0 otherwise.",
		}, []string{"strategy"}),

		NATSPublished: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_nats_published_total",
			Help: "Total NATS messages published by topic.",
		}, []string{"topic"}),

		AlertsSent: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_alerts_sent_total",
			Help: "Total Telegram alerts sent by type.",
		}, []string{"type", "level"}),
	}
}
