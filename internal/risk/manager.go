package risk

import (
	"errors"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// ErrCircuitBreaker is returned when the global drawdown limit is breached.
var ErrCircuitBreaker = errors.New("熔断已触发：超过最大回撤限制")

// Config holds the risk parameters (mirrors config.RiskConfig).
type Config struct {
	MaxPositionPct   float64 // max fraction of equity per position, e.g. 0.10
	MaxDrawdownPct   float64 // circuit breaker threshold, e.g. 0.15
	MaxSingleLossPct float64 // max allowed loss per trade, e.g. 0.02
}

// StateStore persists the circuit breaker's halted/peak-equity state so it
// survives an engine/server restart. The live engine's API layer supplies a
// DB-backed implementation; tests use an in-memory fake.
type StateStore interface {
	Save(halted bool, peakEquity float64) error
	Load() (halted bool, peakEquity float64, ok bool)
}

// Manager enforces risk rules on every order before it reaches the broker.
type Manager struct {
	cfg        Config
	peakEquity float64
	halted     bool
	mu         sync.Mutex
	log        *zap.Logger
	store      StateStore // nil = no persistence (tests, backtests)
}

// New creates a RiskManager. initialEquity sets the baseline for drawdown tracking.
func New(cfg Config, initialEquity float64, log *zap.Logger) *Manager {
	return &Manager{
		cfg:        cfg,
		peakEquity: initialEquity,
		log:        log,
	}
}

// Cfg returns the risk configuration (thresholds), so other layers such as the
// Order Risk Gateway can build rules from the same numbers.
func (m *Manager) Cfg() Config { return m.cfg }

// SetStateStore wires persistence and immediately restores any prior state.
// A persisted halt is NEVER silently cleared by this — a real, still-in-force
// circuit-breaker trip must survive a restart, or the account could resume
// trading past its own drawdown cap (2026-08-06 finding: Manager is
// recreated fresh on every engine start, restart or not, so without this the
// halted flag and peak-equity baseline were lost on every restart). The peak
// is restored as max(persisted, current) so a legitimate equity increase
// while the process was down still counts, but the baseline never regresses
// to a lower, already-drawn-down value.
func (m *Manager) SetStateStore(store StateStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
	if store == nil {
		return
	}
	halted, peak, ok := store.Load()
	if !ok {
		return
	}
	if halted {
		m.halted = true
	}
	if peak > m.peakEquity {
		m.peakEquity = peak
	}
}

// persistLocked saves the current state via the store, if any. Caller must
// hold m.mu.
func (m *Manager) persistLocked() {
	if m.store == nil {
		return
	}
	if err := m.store.Save(m.halted, m.peakEquity); err != nil {
		m.log.Warn("风控：熔断器状态持久化失败", zap.Error(err))
	}
}

// PeakEquity returns the running peak equity used for drawdown tracking.
func (m *Manager) PeakEquity() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peakEquity
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
		m.persistLocked()
	}

	if m.peakEquity > 0 {
		drawdown := (m.peakEquity - equity) / m.peakEquity
		if drawdown >= m.cfg.MaxDrawdownPct && !m.halted {
			m.halted = true
			m.persistLocked()
			m.log.Error("⚡ 熔断已触发",
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
			return fmt.Errorf("仓位大小 %.2f 超过权益上限 %.0f%%（权益 %.2f）",
				newPositionValue, m.cfg.MaxPositionPct*100, equity)
		}

		// Rule 2: max single trade size as fraction of equity
		if equity > 0 && orderCost/equity > m.cfg.MaxSingleLossPct {
			return fmt.Errorf("单笔订单 $%.2f 超过单笔限额 %.0f%%（占权益比例）",
				orderCost, m.cfg.MaxSingleLossPct*100)
		}
	}

	return nil
}

// Reset clears the circuit breaker (for testing / manual override). Persists
// the cleared state so an operator's explicit reset isn't undone by the next
// restart re-loading the old halted=true row.
func (m *Manager) Reset(equity float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.halted = false
	m.peakEquity = equity
	m.persistLocked()
	m.log.Warn("风控：熔断器已重置", zap.Float64("equity", equity))
}

// AdjustDayStart updates the peak equity baseline after a non-trade balance change
// (e.g., transfer in/out, funding fee). Prevents false circuit-breaker triggers.
func (m *Manager) AdjustDayStart(newEquity float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.peakEquity
	// Deposits (equity up): raise baseline so drawdown is measured from new high.
	// Withdrawals (equity down): keep old baseline — don't allow smaller baseline
	// to bypass drawdown limits.
	if newEquity > m.peakEquity {
		m.peakEquity = newEquity
	}
	if m.halted && newEquity > old {
		// Un-halt only if equity increased (deposit)
		m.halted = false
	}
	m.persistLocked()
	m.log.Info("风控：权益基准已因转账/资金费调整",
		zap.Float64("old_peak", old), zap.Float64("new_equity", newEquity),
		zap.Float64("peak", m.peakEquity))
}
