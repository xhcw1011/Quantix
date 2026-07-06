// Package macross implements a dual moving-average crossover strategy.
//
// Long-only mode (EnableShort=false, default):
//
//	Golden cross (fast > slow) → BUY all-in
//	Death cross  (fast < slow) → SELL all
//
// Hedge mode (EnableShort=true, for futures/swap contracts):
//
//	Golden cross → close any short → open LONG (PositionSide=LONG)
//	Death cross  → close any long  → open SHORT (PositionSide=SHORT)
//
// Optional stop-loss and take-profit orders are attached to opening fills
// when StopLossPct > 0 or TakeProfitPct > 0.
package macross

import (
	"fmt"
	"math"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the tunable parameters for MACross.
type Config struct {
	Symbol        string
	FastPeriod    int     // default 10
	SlowPeriod    int     // default 30
	EnableShort   bool    // true = use hedge mode (LONG/SHORT PositionSide) for futures/swap
	StopLossPct   float64 // 0 = no stop loss; e.g. 0.02 = 2% from entry
	TakeProfitPct float64 // 0 = no take profit; e.g. 0.04 = 4% from entry
	// Trend filter (sit out chop). When TrendFilterMin > 0, a cross only OPENS a
	// new position if Kaufman's Efficiency Ratio over TrendFilterN bars is >=
	// TrendFilterMin (≈1 trending, ≈0 choppy). Crosses still CLOSE existing
	// positions regardless, so chop leaves us flat rather than whipsawing.
	// TrendFilterMin <= 0 disables the filter (default — unchanged behaviour).
	TrendFilterN   int
	TrendFilterMin float64
	// CrossBufferPct requires fast to clear slow by this fraction before a cross
	// counts, filtering marginal "touch" crosses that whipsaw in chop (e.g. the
	// 2026-07-05 fast/slow 0.0009% touch). 0 = raw cross. Default 0.0015 (0.15%).
	CrossBufferPct float64
}

// MACross is a dual-SMA crossover strategy with optional short-selling support.
type MACross struct {
	cfg    Config
	closes []float64

	// Internal position state (used in hedge mode to track LONG/SHORT legs).
	// Updated via OnFill so we don't rely on PortfolioView for hedge positions.
	hasLong  bool
	hasShort bool

	// Warmup priming: sawWarmup records that a backfill replay happened; primed
	// records that we've established the initial position from trend state on the
	// first live bar afterwards (see primeDirection).
	sawWarmup bool
	primed    bool
}

// New creates a new MACross strategy with the given configuration.
func New(cfg Config) *MACross {
	return &MACross{cfg: cfg}
}

// Name implements strategy.Strategy.
func (m *MACross) Name() string {
	if m.cfg.EnableShort {
		return fmt.Sprintf("MACross(%d,%d,hedge)", m.cfg.FastPeriod, m.cfg.SlowPeriod)
	}
	return fmt.Sprintf("MACross(%d,%d)", m.cfg.FastPeriod, m.cfg.SlowPeriod)
}

// OnBar implements strategy.Strategy.
func (m *MACross) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != m.cfg.Symbol {
		return
	}
	if bar.Warmup {
		m.sawWarmup = true
	}

	m.closes = append(m.closes, bar.Close)

	if len(m.closes) < m.cfg.SlowPeriod {
		return
	}

	fast := indicator.SMA(m.closes, m.cfg.FastPeriod)
	slow := indicator.SMA(m.closes, m.cfg.SlowPeriod)

	// Establish the position implied by the trend on the first live bar after a
	// warmup replay (hedge mode only — simple mode already re-checks Portfolio).
	if m.cfg.EnableShort {
		flat := !m.hasLong && !m.hasShort
		if dir := primeDirection(m.sawWarmup, bar.Warmup, m.primed, flat, indicator.Last(fast), indicator.Last(slow)); dir != 0 {
			// Only consume the prime once we actually enter. If the trend filter
			// blocks it (choppy ER), leave primed=false so we retry on later bars
			// and establish the position when the regime turns trending — otherwise
			// a one-off low-ER bar would leave us flat through a whole trend.
			if m.trendOK() {
				m.primed = true
				ctx.Log.Info("macross: priming position from trend state after warmup",
					zap.String("symbol", bar.Symbol), zap.Int("dir", dir),
					zap.Float64("fast", indicator.Last(fast)), zap.Float64("slow", indicator.Last(slow)))
				if dir > 0 {
					m.openLong(ctx, bar)
				} else {
					m.openShort(ctx, bar)
				}
			}
			return
		}
	}

	if m.cfg.EnableShort {
		m.onBarHedge(ctx, bar, fast, slow)
	} else {
		m.onBarSimple(ctx, bar, fast, slow)
	}
}

// efficiencyRatio returns Kaufman's Efficiency Ratio over the last n closes:
// |net change| / sum(|bar-to-bar change|). ~1 = clean trend, ~0 = chop. Returns
// 1 (don't filter) when there isn't enough data yet.
func (m *MACross) efficiencyRatio(n int) float64 {
	if n < 2 || len(m.closes) < n+1 {
		return 1
	}
	c := m.closes
	last := len(c) - 1
	net := math.Abs(c[last] - c[last-n])
	var vol float64
	for i := last - n + 1; i <= last; i++ {
		vol += math.Abs(c[i] - c[i-1])
	}
	if vol == 0 {
		return 0
	}
	return net / vol
}

// trendOK reports whether the market is trending enough to OPEN a new position.
// Always true when the filter is disabled (TrendFilterMin <= 0).
func (m *MACross) trendOK() bool {
	if m.cfg.TrendFilterMin <= 0 {
		return true
	}
	n := m.cfg.TrendFilterN
	if n <= 0 {
		n = m.cfg.SlowPeriod
	}
	return m.efficiencyRatio(n) >= m.cfg.TrendFilterMin
}

// onBarSimple handles the long-only (spot/net) mode.
func (m *MACross) onBarSimple(ctx *strategy.Context, bar exchange.Kline, fast, slow []float64) {
	_, _, hasPosition := ctx.Portfolio.Position(bar.Symbol)

	switch crossDir(fast, slow, m.cfg.CrossBufferPct) {
	case 1:
		if !hasPosition && m.trendOK() {
			ctx.Log.Info("golden cross — BUY",
				zap.String("symbol", bar.Symbol),
				zap.Float64("fast", indicator.Last(fast)),
				zap.Float64("slow", indicator.Last(slow)),
				zap.Float64("close", bar.Close),
			)
			req := strategy.OrderRequest{
				Symbol: bar.Symbol,
				Side:   strategy.SideBuy,
				Type:   strategy.OrderMarket,
				Qty:    0, // all-in
			}
			if m.cfg.StopLossPct > 0 {
				req.StopLoss = bar.Close * (1 - m.cfg.StopLossPct)
			}
			if m.cfg.TakeProfitPct > 0 {
				req.TakeProfit = bar.Close * (1 + m.cfg.TakeProfitPct)
			}
			ctx.PlaceOrder(req)
		}

	case -1:
		if hasPosition {
			ctx.Log.Info("death cross — SELL",
				zap.String("symbol", bar.Symbol),
				zap.Float64("fast", indicator.Last(fast)),
				zap.Float64("slow", indicator.Last(slow)),
				zap.Float64("close", bar.Close),
			)
			ctx.PlaceOrder(strategy.OrderRequest{
				Symbol: bar.Symbol,
				Side:   strategy.SideSell,
				Type:   strategy.OrderMarket,
				Qty:    0, // close all
			})
		}
	}
}

// onBarHedge handles the hedge mode (simultaneous LONG/SHORT for futures/swap).
func (m *MACross) onBarHedge(ctx *strategy.Context, bar exchange.Kline, fast, slow []float64) {
	switch crossDir(fast, slow, m.cfg.CrossBufferPct) {
	case 1:
		// Golden cross: close short (if open), then open long
		ctx.Log.Info("golden cross — close SHORT, open LONG",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasShort {
			ctx.PlaceOrder(strategy.CloseShort(bar.Symbol, 0))
		}
		if !m.hasLong && m.trendOK() {
			m.openLong(ctx, bar)
		}

	case -1:
		// Death cross: close long (if open), then open short
		ctx.Log.Info("death cross — close LONG, open SHORT",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasLong {
			ctx.PlaceOrder(strategy.CloseLong(bar.Symbol, 0))
		}
		if !m.hasShort && m.trendOK() {
			m.openShort(ctx, bar)
		}
	}
}

// openLong places a hedge-mode long entry with optional stop-loss / take-profit.
func (m *MACross) openLong(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.OpenLong(bar.Symbol, 0)
	if m.cfg.StopLossPct > 0 {
		req.StopLoss = bar.Close * (1 - m.cfg.StopLossPct)
	}
	if m.cfg.TakeProfitPct > 0 {
		req.TakeProfit = bar.Close * (1 + m.cfg.TakeProfitPct)
	}
	ctx.PlaceOrder(req)
}

// openShort places a hedge-mode short entry with optional stop-loss / take-profit.
func (m *MACross) openShort(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.OpenShort(bar.Symbol, 0)
	if m.cfg.StopLossPct > 0 {
		req.StopLoss = bar.Close * (1 + m.cfg.StopLossPct)
	}
	if m.cfg.TakeProfitPct > 0 {
		req.TakeProfit = bar.Close * (1 - m.cfg.TakeProfitPct)
	}
	ctx.PlaceOrder(req)
}

// OnFill implements strategy.Strategy.
// Updates internal position state for hedge mode tracking.
func (m *MACross) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	ctx.Log.Debug("fill received",
		zap.String("id", fill.ID),
		zap.String("side", string(fill.Side)),
		zap.String("position_side", string(fill.PositionSide)),
		zap.Float64("qty", fill.Qty),
		zap.Float64("price", fill.Price),
		zap.Float64("fee", fill.Fee),
	)

	if !m.cfg.EnableShort {
		return
	}

	// Update hedge position state
	switch {
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideBuy:
		m.hasLong = true
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideSell:
		m.hasLong = false
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideSell:
		m.hasShort = true
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideBuy:
		m.hasShort = false
	}
}

func init() {
	registry.Register("macross", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{}
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if v, ok := params["FastPeriod"]; ok {
			cfg.FastPeriod = toInt(v)
		}
		if v, ok := params["SlowPeriod"]; ok {
			cfg.SlowPeriod = toInt(v)
		}
		if v, ok := params["EnableShort"].(bool); ok {
			cfg.EnableShort = v
		}
		if v, ok := params["StopLossPct"]; ok {
			cfg.StopLossPct = toFloat(v)
		}
		if v, ok := params["TakeProfitPct"]; ok {
			cfg.TakeProfitPct = toFloat(v)
		}
		if v, ok := params["TrendFilterN"]; ok {
			cfg.TrendFilterN = toInt(v)
		} else {
			cfg.TrendFilterN = 10 // default: ER lookback
		}
		if v, ok := params["TrendFilterMin"]; ok {
			cfg.TrendFilterMin = toFloat(v) // explicit (incl. 0 = filter off)
		} else {
			cfg.TrendFilterMin = 0.20 // default ON — only open in trending regimes (backtest-validated)
		}
		if v, ok := params["CrossBufferPct"]; ok {
			cfg.CrossBufferPct = toFloat(v) // opt-in; default 0 (backtest showed a buffer hurts)
		}
		if cfg.FastPeriod == 0 {
			cfg.FastPeriod = 10
		}
		if cfg.SlowPeriod == 0 {
			cfg.SlowPeriod = 30
		}
		if cfg.FastPeriod >= cfg.SlowPeriod {
			return nil, fmt.Errorf("FastPeriod (%d) must be less than SlowPeriod (%d)",
				cfg.FastPeriod, cfg.SlowPeriod)
		}
		if cfg.StopLossPct < 0 {
			return nil, fmt.Errorf("StopLossPct must be >= 0 (got %.4f)", cfg.StopLossPct)
		}
		if cfg.TakeProfitPct < 0 {
			return nil, fmt.Errorf("TakeProfitPct must be >= 0 (got %.4f)", cfg.TakeProfitPct)
		}
		return New(cfg), nil
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}
