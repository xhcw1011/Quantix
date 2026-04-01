package risk

import (
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// ErrCircuitBreaker is returned when the global drawdown limit is breached.
var ErrCircuitBreaker = errors.New("circuit breaker triggered: max drawdown exceeded")

// Config holds the risk parameters (mirrors config.RiskConfig).
type Config struct {
	MaxPositionPct   float64 // max fraction of equity per position, e.g. 0.10
	MaxDrawdownPct   float64 // circuit breaker threshold, e.g. 0.15
	MaxSingleLossPct float64 // max allowed loss per trade, e.g. 0.02
}

// Manager enforces risk rules on every order before it reaches the broker.
type Manager struct {
	cfg        Config
	peakEquity float64
	halted     bool
	mu         sync.Mutex
	log        *zap.Logger
}

// New creates a RiskManager. initialEquity sets the baseline for drawdown tracking.
func New(cfg Config, initialEquity float64, log *zap.Logger) *Manager {
	return &Manager{
		cfg:        cfg,
		peakEquity: initialEquity,
		log:        log,
	}
}

// Halted returns true if the circuit breaker has fired.
func (m *Manager) Halted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.halted
}

// UpdateEquity tracks the equity curve and fires the circuit breaker if
// the drawdown from the peak exceeds MaxDrawdownPct.
// Returns ErrCircuitBreaker the first time the threshold is crossed.
func (m *Manager) UpdateEquity(equity float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if equity > m.peakEquity {
		m.peakEquity = equity
	}

	if m.peakEquity > 0 {
		drawdown := (m.peakEquity - equity) / m.peakEquity
		if drawdown >= m.cfg.MaxDrawdownPct && !m.halted {
			m.halted = true
			m.log.Error("⚡ CIRCUIT BREAKER TRIGGERED",
				zap.Float64("peak_equity", m.peakEquity),
				zap.Float64("current_equity", equity),
				zap.Float64("drawdown_pct", drawdown*100),
				zap.Float64("limit_pct", m.cfg.MaxDrawdownPct*100),
			)
			return ErrCircuitBreaker
		}
	}
	return nil
}

// Check validates an order against all risk rules.
// currentPrice is the current market price of the symbol being ordered.
// equity is the current total portfolio value.
// positionValue is the current dollar value held in this symbol (0 if flat).
//
// Returns a non-nil error if any rule is violated.
func (m *Manager) Check(
	req strategy.OrderRequest,
	equity float64,
	positionValue float64,
	currentPrice float64,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.halted {
		return ErrCircuitBreaker
	}

	// Apply position-size and single-trade checks to orders that open or increase positions:
	// BUY (long/net), or SELL with PositionSide=SHORT (opening a short).
	isOpening := req.Side == strategy.SideBuy ||
		(req.Side == strategy.SideSell && req.PositionSide == strategy.PositionSideShort)

	if isOpening {
		// Rule 1: max position size
		// Estimate the cost of this order
		orderCost := req.Qty * currentPrice
		if req.Qty == 0 {
			// all-in case: will use ~all available cash; check against equity
			orderCost = equity
		}
		newPositionValue := positionValue + orderCost
		if equity > 0 && newPositionValue/equity > m.cfg.MaxPositionPct {
			return fmt.Errorf("position size %.2f exceeds max %.0f%% of equity (%.2f)",
				newPositionValue, m.cfg.MaxPositionPct*100, equity)
		}

		// Rule 2: max single trade size as fraction of equity
		if equity > 0 && orderCost/equity > m.cfg.MaxSingleLossPct {
			return fmt.Errorf("order size $%.2f exceeds max single-trade limit %.0f%% of equity",
				orderCost, m.cfg.MaxSingleLossPct*100)
		}
	}

	return nil
}

// Reset clears the circuit breaker (for testing / manual override).
func (m *Manager) Reset(equity float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.halted = false
	m.peakEquity = equity
	m.log.Warn("risk manager reset", zap.Float64("equity", equity))
}
