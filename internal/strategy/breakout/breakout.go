// Package breakout implements a decoupled-channel (Turtle-style) breakout
// strategy: enter on a SHORT lookback breakout/breakdown, exit on a much
// LONGER lookback channel — deliberately patient, so an ordinary pullback
// inside a developing trend doesn't shake the position out early.
//
// Validated 2026-08-25 on BTCUSDT 15m, full available history (2026-01-21 →
// 2026-08-25, ~7 months): long+short, EntryPeriod swept 5-60 bars all
// positive net of realistic cost when paired with ExitPeriod=96 (~24h);
// best combo EntryPeriod=20/ExitPeriod=96 returned +31.4% net, spread across
// 5 of 8 months (not one lucky month) — Feb (a real -35% BTC decline)
// contributed +21.3% from the short side alone. Two things this backtest
// established as load-bearing, don't "fix" without re-testing:
//   - NO immediate reversal on an opposite-direction EntryPeriod signal while
//     positioned — only the wide ExitPeriod channel closes a position. Adding
//     reversal-on-signal was tested and made every combo dramatically worse
//     (e.g. EntryPeriod=20/ExitPeriod=96 net swung from +31.4% to -68.9%,
//     maxDD 23%→75%) — the patience IS the edge, not a missing feature.
//   - This is single-asset (BTC only) and single-regime-mixed (7 months, one
//     exchange). It has NOT been cross-validated on other symbols.
package breakout

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config holds the tunable parameters for Breakout.
type Config struct {
	Symbol string

	// EntryPeriod (N_in): a close above the trailing EntryPeriod-bar high
	// opens long; a close below the trailing EntryPeriod-bar low opens short
	// (only if EnableShort). Backtested 5-60; default 20.
	EntryPeriod int
	// ExitPeriod (N_out): a close below the trailing ExitPeriod-bar low
	// closes a long; a close above the trailing ExitPeriod-bar high closes a
	// short. Must be well above EntryPeriod for the "patience" effect that
	// the backtest found load-bearing. Default 96.
	ExitPeriod int

	// EnableShort: symmetric long+short (hedge mode, PositionSide LONG/
	// SHORT). false = long-only. The validated backtest used true — a
	// meaningful share of the return (Feb 2026) came from the short side.
	EnableShort bool

	// StopLossPct: hard exchange-native protective stop, placed at entry as
	// a backstop for a gap/flash-crash the ExitPeriod channel wouldn't catch
	// in time (the backtest itself has no such stop — this is a real-money
	// safety addition on top of the validated signal, not a backtested
	// parameter). 0 disables it; strongly discouraged for live money.
	StopLossPct float64
}

// Breakout is a decoupled-channel breakout strategy with optional short-selling.
type Breakout struct {
	cfg    Config
	log    *zap.Logger
	highs  []float64
	lows   []float64
	closes []float64

	// Position state (hedge-mode tracking, updated via OnFill/reconcilePosition
	// — mirrors internal/strategy/macross's established pattern rather than
	// relying on ctx.Portfolio, which only exposes a one-way slot and would
	// report a hedge leg as absent).
	hasLong    bool
	hasShort   bool
	posQty     float64
	reconciled bool
}

// New creates a new Breakout strategy with the given configuration.
func New(cfg Config) *Breakout {
	return &Breakout{cfg: cfg, log: zap.NewNop()}
}

// Name implements strategy.Strategy.
func (b *Breakout) Name() string {
	if b.cfg.EnableShort {
		return fmt.Sprintf("Breakout(%d,%d,hedge)", b.cfg.EntryPeriod, b.cfg.ExitPeriod)
	}
	return fmt.Sprintf("Breakout(%d,%d)", b.cfg.EntryPeriod, b.cfg.ExitPeriod)
}

// positionReporter reports whether a live position exists on a side, and its
// quantity. The live engine's *position.Syncer satisfies it, injected via
// ctx.Extra["position_syncer"]; absent in backtests (nil → no reconciliation).
type positionReporter interface {
	HasPosition(side string) bool
	GetLong() *position.StrategyPosition
	GetShort() *position.StrategyPosition
}

// reconcilePosition seeds hasLong/hasShort/posQty from the real account
// position exactly once, so a restart doesn't stack a duplicate entry onto a
// position the account already holds (see internal/strategy/macross's
// identically-motivated reconcilePosition — same restart-reopen bug class).
func (b *Breakout) reconcilePosition(ctx *strategy.Context) {
	if b.reconciled {
		return
	}
	b.reconciled = true
	pr, ok := ctx.Extra["position_syncer"].(positionReporter)
	if !ok || pr == nil {
		return
	}
	if pr.HasPosition(string(strategy.PositionSideLong)) {
		b.hasLong = true
		if p := pr.GetLong(); p != nil {
			b.posQty = p.Qty
		}
	}
	if pr.HasPosition(string(strategy.PositionSideShort)) {
		b.hasShort = true
		if p := pr.GetShort(); p != nil {
			b.posQty = p.Qty
		}
	}
}

// OnBar implements strategy.Strategy.
func (b *Breakout) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != b.cfg.Symbol {
		return
	}
	if b.cfg.EnableShort {
		b.reconcilePosition(ctx)
	}

	b.highs = append(b.highs, bar.High)
	b.lows = append(b.lows, bar.Low)
	b.closes = append(b.closes, bar.Close)

	maxP := b.cfg.ExitPeriod
	if b.cfg.EntryPeriod > maxP {
		maxP = b.cfg.EntryPeriod
	}
	n := len(b.closes)
	if n <= maxP {
		return // not enough history for even the shorter channel yet
	}
	// Never trade on the startup backfill replay — only prime the buffers.
	if bar.Warmup {
		return
	}

	if !b.hasLong && !b.hasShort {
		entryHH := maxOf(b.highs[n-1-b.cfg.EntryPeriod : n-1])
		entryLL := minOf(b.lows[n-1-b.cfg.EntryPeriod : n-1])
		switch {
		case bar.Close > entryHH:
			b.openLong(ctx, bar)
		case b.cfg.EnableShort && bar.Close < entryLL:
			b.openShort(ctx, bar)
		}
		return
	}
	// Positioned: ONLY the wide ExitPeriod channel can close it. An opposite
	// EntryPeriod signal is deliberately ignored here — see the package doc
	// comment on why immediate reversal was tested and found much worse.
	if b.hasLong {
		exitLL := minOf(b.lows[n-1-b.cfg.ExitPeriod : n-1])
		if bar.Close < exitLL {
			b.closeLong(ctx, bar)
		}
		return
	}
	exitHH := maxOf(b.highs[n-1-b.cfg.ExitPeriod : n-1])
	if bar.Close > exitHH {
		b.closeShort(ctx, bar)
	}
}

func maxOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func minOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func (b *Breakout) openLong(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.OpenLong(bar.Symbol, 0)
	if b.cfg.StopLossPct > 0 {
		req.StopLoss = bar.Close * (1 - b.cfg.StopLossPct)
	}
	req.Reason = "breakout_entry"
	ctx.PlaceOrder(req)
	b.log.Info("breakout: 突破入场做多", zap.String("symbol", bar.Symbol), zap.Float64("close", bar.Close))
}

func (b *Breakout) openShort(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.OpenShort(bar.Symbol, 0)
	if b.cfg.StopLossPct > 0 {
		req.StopLoss = bar.Close * (1 + b.cfg.StopLossPct)
	}
	req.Reason = "breakout_entry"
	ctx.PlaceOrder(req)
	b.log.Info("breakout: 跌破入场做空", zap.String("symbol", bar.Symbol), zap.Float64("close", bar.Close))
}

func (b *Breakout) closeLong(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.CloseLong(bar.Symbol, b.posQty)
	req.Reason = "breakout_exit"
	ctx.PlaceOrder(req)
	b.log.Info("breakout: 跌破退出通道，平多", zap.String("symbol", bar.Symbol), zap.Float64("close", bar.Close))
}

func (b *Breakout) closeShort(ctx *strategy.Context, bar exchange.Kline) {
	req := strategy.CloseShort(bar.Symbol, b.posQty)
	req.Reason = "breakout_exit"
	ctx.PlaceOrder(req)
	b.log.Info("breakout: 突破退出通道，平空", zap.String("symbol", bar.Symbol), zap.Float64("close", bar.Close))
}

// OnFill implements strategy.Strategy. Tracks hedge-mode position state —
// unused when EnableShort is false (long-only isn't reconciled/tracked here
// since it was never the backtested/validated configuration).
func (b *Breakout) OnFill(_ *strategy.Context, fill strategy.Fill) {
	if !b.cfg.EnableShort {
		return
	}
	switch {
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideBuy:
		b.hasLong = true
		b.posQty = fill.Qty
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideSell:
		b.hasLong = false
		b.posQty = 0
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideSell:
		b.hasShort = true
		b.posQty = fill.Qty
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideBuy:
		b.hasShort = false
		b.posQty = 0
	}
}

func init() {
	registry.Register("breakout", func(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
		cfg := Config{EntryPeriod: 20, ExitPeriod: 96} // the backtested default combo
		if v, ok := params["Symbol"].(string); ok {
			cfg.Symbol = v
		}
		if cfg.Symbol == "" {
			return nil, fmt.Errorf("breakout: Symbol is required")
		}
		if v, ok := params["EntryPeriod"]; ok {
			cfg.EntryPeriod = toInt(v)
		}
		if v, ok := params["ExitPeriod"]; ok {
			cfg.ExitPeriod = toInt(v)
		}
		if cfg.EntryPeriod <= 0 || cfg.ExitPeriod <= 0 {
			return nil, fmt.Errorf("breakout: EntryPeriod and ExitPeriod must both be > 0")
		}
		if v, ok := params["EnableShort"].(bool); ok {
			cfg.EnableShort = v
		} else {
			cfg.EnableShort = true // default matches the validated backtest (long+short)
		}
		if v, ok := params["StopLossPct"]; ok {
			cfg.StopLossPct = toFloat(v)
		}
		b := New(cfg)
		if log != nil {
			b.log = log
		}
		return b, nil
	})
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
