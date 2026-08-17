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
	"github.com/Quantix/quantix/internal/position"
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

	// AsymmetricExit changes hedge-mode exit behavior (EnableShort only). Default
	// (false): a reverse cross always closes the current position and flips,
	// regardless of whether it's winning or losing.
	//
	// When true: a reverse cross only closes the position if it's currently
	// PROFITABLE (take-profit-on-reversal). While underwater, the position is
	// held instead of being flipped into a fresh loss on every whipsaw, and is
	// managed instead by ReduceTriggerPct/ReduceConfirmBars/ReduceFrac (a
	// one-time partial de-risk once the adverse move is confirmed) with
	// StopLossPct remaining the final backstop. TrailActivatePct/
	// TrailGivebackFrac additionally protects large winners from round-tripping
	// while waiting for the (laggy) SMA reverse cross to confirm.
	//
	// Backtest-validated across ETH (21 months, 2 real bear legs) and BTC (a
	// real -53% decline plus a calm control period): consistently and
	// substantially better than flip-on-every-cross on both assets and both
	// regimes tested. See docs/research/ for the investigation.
	AsymmetricExit bool

	// MinProfitToClosePct gates the "only close if profitable" half of
	// AsymmetricExit: a reverse cross only closes the current position if its
	// floating profit exceeds this fraction of entry, not merely > 0. A close
	// that's nominally profitable at the signal bar's close can still land a
	// net loss once round-trip taker fees and the gap to the actual fill price
	// are accounted for (2026-08-07 incident: 0.0224% floating profit at the
	// signal bar → -$1.12 realized after a 0.04%-per-side fee). Only used when
	// AsymmetricExit is true.
	MinProfitToClosePct float64

	// ReduceTriggerPct/ReduceConfirmBars/ReduceFrac: only used when
	// AsymmetricExit is true. A confirmed adverse move — floating loss >=
	// ReduceTriggerPct sustained for ReduceConfirmBars consecutive bars —
	// triggers a ONE-TIME partial reduce of ReduceFrac of the position.
	ReduceTriggerPct  float64
	ReduceConfirmBars int
	ReduceFrac        float64

	// TrailActivatePct/TrailGivebackFrac: only used when AsymmetricExit is true.
	// Once a position's floating profit reaches TrailActivatePct, its peak is
	// tracked; if profit retraces by TrailGivebackFrac of that peak, the
	// position is closed in full. TrailActivatePct <= 0 disables this (small/
	// medium winners are never touched — only large ones that have already run
	// well past typical trade size get this protection).
	TrailActivatePct  float64
	TrailGivebackFrac float64

	// EntryOrderType selects how new entries are placed: "" or "market"
	// (default) submits immediately at market; "limit" rests a maker-favoured
	// limit order first (see EntryLimitOffsetPct/EntryTimeoutBars) and falls
	// back to market if it doesn't fill in time. Exits/closes always stay
	// market regardless of this setting — a position that needs to come off
	// should come off decisively, not wait on a limit that may never fill.
	EntryOrderType string
	// EntryLimitOffsetPct is the fraction below (long) / above (short) the
	// signal bar's close that the limit entry is placed at — the further the
	// offset, the more likely it rests as a maker fill, but the less likely it
	// fills at all. Only used when EntryOrderType == "limit".
	EntryLimitOffsetPct float64
	// EntryTimeoutBars is how many bars a limit entry is given to fill before
	// it's cancelled and replaced with a market order. Only used when
	// EntryOrderType == "limit".
	EntryTimeoutBars int

	// CooldownBars blocks a new entry (hedge mode only) until this many live
	// bars have passed since the position last fully closed. <= 0 disables it
	// (default — unchanged behaviour). Unlike CrossBufferPct (which filters
	// marginal/hairline crosses by signal quality), this is a blunt time-based
	// debounce: it also blocks a clean, decisive reverse signal if it happens
	// to land within the cooldown window. Intended as a second line of defense
	// against rapid open/close/reopen whipsaws in chop (2026-08-17 finding).
	CooldownBars int
}

// MACross is a dual-SMA crossover strategy with optional short-selling support.
type MACross struct {
	cfg    Config
	closes []float64

	// Internal position state (used in hedge mode to track LONG/SHORT legs).
	// Updated via OnFill so we don't rely on PortfolioView for hedge positions.
	hasLong  bool
	hasShort bool

	// AsymmetricExit position state. hasLong and hasShort are mutually exclusive
	// in hedge mode (a reverse cross always closes the opposite side, whether
	// immediately or via the asymmetric-exit path), so a single set of fields
	// covers whichever side is currently open — no need to duplicate per side.
	posEntry      float64 // entry price of the currently open leg
	posQty        float64 // remaining qty of the currently open leg
	posReduced    bool    // the one-time confirmed-loss reduce has already fired
	posLossStreak int     // consecutive bars with floating loss >= ReduceTriggerPct
	posPeakFP     float64 // peak floating-profit fraction seen this leg (for trailing)

	// Warmup priming: sawWarmup records that a backfill replay happened; primed
	// records that we've established the initial position from trend state on the
	// first live bar afterwards (see primeDirection). hadPosition latches true
	// the first time hasLong/hasShort is observed true (restart-seeded or
	// opened live) and never resets — it distinguishes "genuinely flat, never
	// had a position" from "had one, our own exit logic already closed it",
	// so priming doesn't undo an intentional close (see primeDirection).
	sawWarmup   bool
	primed      bool
	hadPosition bool

	// CooldownBars bookkeeping: barsSinceClose counts live bars since the
	// position last fully closed (OnFill resets it to 0); everClosed latches
	// true the first time that happens, so a run that has never closed a
	// position yet is never blocked by the cooldown (see cooldownActive).
	barsSinceClose int
	everClosed     bool

	// reconciled records that we've seeded hasLong/hasShort from the live account
	// position once at startup. Without this, a restart resets the in-memory flags
	// to flat and priming re-opens a position the account already holds — the
	// "re-orders on every deploy" bug. See reconcilePosition.
	reconciled bool

	// pending tracks a resting limit entry order awaiting a fill (EntryOrderType
	// == "limit" only). One slot is enough — only one entry can ever be in
	// flight at a time (long-only and hedge mode both open at most one leg per
	// signal). See checkPendingEntry.
	pending strategy.PendingEntry

	// restart recovery for pending limit entries (optional; nil in backtest/paper).
	store        StateStore
	restoreTried bool // guards the one-shot restart cleanup below (once per process)
}

// positionReporter reports whether a live position exists on a side, and its
// cost basis. The live engine's position.Syncer satisfies it and is injected
// via ctx.Extra["position_syncer"]; absent in backtests (nil → no
// reconciliation).
type positionReporter interface {
	HasPosition(side string) bool
	GetLong() *position.StrategyPosition
	GetShort() *position.StrategyPosition
}

// reconcilePosition seeds hedge-mode hasLong/hasShort from the real account
// position (via the injected syncer) exactly once, so priming after a restart
// sees the true flat state and doesn't stack a duplicate entry onto a position
// the account already holds. No-op without a syncer (backtests unchanged).
func (m *MACross) reconcilePosition(ctx *strategy.Context) {
	if m.reconciled {
		return
	}
	m.reconciled = true
	pr, ok := ctx.Extra["position_syncer"].(positionReporter)
	if !ok || pr == nil {
		return
	}
	if pr.HasPosition(string(strategy.PositionSideLong)) {
		m.hasLong = true
		if p := pr.GetLong(); p != nil {
			m.posEntry = p.EntryPrice
			m.posQty = p.Qty
		}
	}
	if pr.HasPosition(string(strategy.PositionSideShort)) {
		m.hasShort = true
		if p := pr.GetShort(); p != nil {
			m.posEntry = p.EntryPrice
			m.posQty = p.Qty
		}
	}
	// posReduced/posLossStreak/posPeakFP intentionally stay at their zero
	// value here: the syncer doesn't carry these strategy-internal fields, and
	// forgetting them is safe-direction (worst case: one extra confirmed-loss
	// reduce on an already-reduced leg, or a trailing exit that re-earns its
	// peak from the current price instead of a pre-restart high) rather than
	// unsafe-direction like the posEntry==0 bug this restores.
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
	m.restoreState(ctx)
	if m.checkPendingEntry(ctx, bar) {
		return
	}
	if bar.Warmup {
		m.sawWarmup = true
	} else if m.cfg.EnableShort {
		m.barsSinceClose++
	}
	// Hedge mode: learn the real account position once, before any priming, so a
	// restart doesn't re-open a position the account already holds.
	if m.cfg.EnableShort {
		m.reconcilePosition(ctx)
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
		if !flat {
			m.hadPosition = true
		}
		if dir := primeDirection(m.sawWarmup, bar.Warmup, m.primed, flat, m.hadPosition, indicator.Last(fast), indicator.Last(slow)); dir != 0 {
			// Only consume the prime once we actually enter. If the trend filter
			// blocks it (choppy ER), leave primed=false so we retry on later bars
			// and establish the position when the regime turns trending — otherwise
			// a one-off low-ER bar would leave us flat through a whole trend.
			if m.trendOK() {
				m.primed = true
				ctx.Log.Info("macross：预热结束后按趋势方向建仓",
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

// checkPendingEntry advances a resting limit entry (if any) and reports
// whether this bar was consumed by it — the caller should return immediately
// in that case rather than also evaluating a fresh cross signal, so a limit
// order that hasn't resolved yet never gets a second entry stacked on top of
// it. Once the position appears (pendingFilled) the pending entry clears and
// this bar stays quiet; the next bar resumes normal signal evaluation. If the
// bar-count budget (EntryTimeoutBars) runs out first, the stale limit is
// cancelled and replaced with its market fallback.
func (m *MACross) checkPendingEntry(ctx *strategy.Context, bar exchange.Kline) bool {
	if !m.pending.Active() {
		return false
	}
	if m.pendingFilled(ctx) {
		m.pending.Clear()
		m.clearState()
		return true
	}
	if m.pending.Timeout(m.cfg.EntryTimeoutBars) {
		_ = ctx.CancelOrder(m.pending.OrderID)
		ctx.PlaceOrder(m.pending.Fallback)
		ctx.Log.Info("限价开仓超时，改市价单成交",
			zap.String("symbol", bar.Symbol), zap.Int("bars_waited", m.pending.Bars))
		m.pending.Clear()
		m.clearState()
	}
	return true
}

// pendingFilled reports whether the position the pending entry was opening
// has appeared. The two modes track position differently (see the OnFill /
// onBarSimple doc comments), so this can't be unified further than a branch.
func (m *MACross) pendingFilled(ctx *strategy.Context) bool {
	if m.cfg.EnableShort {
		return m.hasLong || m.hasShort
	}
	_, _, has := ctx.Portfolio.Position(m.cfg.Symbol)
	return has
}

// placeEntry submits req as-is when EntryOrderType isn't "limit" (unchanged
// market-order behaviour). Otherwise it rewrites req into a maker-favoured
// resting limit at closePrice ± EntryLimitOffsetPct and tracks it as a
// pending entry with fallback as the market order to fall back to; an instant
// reject (id == "", e.g. a MakerOnly order that would have crossed the book)
// falls back to market immediately instead of waiting out the timeout.
func (m *MACross) placeEntry(ctx *strategy.Context, fallback strategy.OrderRequest, closePrice float64) {
	if m.cfg.EntryOrderType != "limit" {
		ctx.PlaceOrder(fallback)
		return
	}
	offset := closePrice * m.cfg.EntryLimitOffsetPct
	price := closePrice - offset // favourable for a buy (long entry)
	if fallback.Side == strategy.SideSell {
		price = closePrice + offset // favourable for a sell (short entry)
	}
	req := fallback
	req.Type = strategy.OrderLimit
	req.Price = price
	req.MakerOnly = true
	id := ctx.PlaceOrder(req)
	if id == "" {
		ctx.PlaceOrder(fallback) // instantly rejected — don't wait out the timeout
		return
	}
	m.pending = strategy.PendingEntry{OrderID: id, Fallback: fallback}
	m.saveState()
}

// saveState persists the pending entry's order ID (best-effort) so a restart
// can at least clean it up. No-op without a wired StateStore.
func (m *MACross) saveState() {
	if m.store == nil {
		return
	}
	_ = m.store.Save(PendingEntryState{OrderID: m.pending.OrderID})
}

// clearState removes the persisted pending-entry record. Must be called
// whenever m.pending clears (filled or timed out) — otherwise a stale
// OrderID lingers and a later restart would try to cancel an order that
// isn't pending anymore (harmless — CancelOrder on an already-resolved order
// just errors and is ignored — but noisy and misleading in logs).
func (m *MACross) clearState() {
	if m.store == nil {
		return
	}
	_ = m.store.Clear()
}

// SetStateStore wires restart-recovery persistence for pending limit entries.
// The live engine calls this after construction; backtest/paper leave it nil.
func (m *MACross) SetStateStore(store StateStore) { m.store = store }

// restoreState runs once per process, at the very first bar. If a pending
// limit entry was left resting when the process last stopped, macross has no
// safe way to resume tracking it (unlike guardian, it can't distinguish "this
// order is still exactly what I'd place today" from "conditions changed") —
// so it just cancels the stale order and starts clean rather than leaving an
// orphaned resting order the strategy no longer tracks.
func (m *MACross) restoreState(ctx *strategy.Context) {
	if m.restoreTried || m.store == nil {
		return
	}
	m.restoreTried = true
	st, ok := m.store.Load()
	if !ok || st.OrderID == "" {
		return
	}
	_ = ctx.CancelOrder(st.OrderID)
	m.clearState()
	ctx.Log.Info("macross：重启后清理了一笔未成交的限价开仓单",
		zap.String("symbol", m.cfg.Symbol), zap.String("order_id", st.OrderID))
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
			ctx.Log.Info("金叉——买入",
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
			m.placeEntry(ctx, req, bar.Close)
		}

	case -1:
		if hasPosition {
			ctx.Log.Info("死叉——卖出",
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
	if m.cfg.AsymmetricExit && (m.hasLong || m.hasShort) {
		m.manageAsymmetricExit(ctx, bar)
	}

	// Track, for THIS bar's cross decision, whether a close order was just
	// queued above/below — fills are applied asynchronously (OnFill runs after
	// the broker processes this bar), so m.hasLong/m.hasShort won't reflect a
	// same-bar close yet. Using local flags instead of re-reading the (stale)
	// struct fields lets close-then-open fire correctly within one bar.
	stillShort := m.hasShort
	stillLong := m.hasLong

	switch crossDir(fast, slow, m.cfg.CrossBufferPct) {
	case 1:
		// Golden cross: close short (if open and, under AsymmetricExit, only if
		// profitable), then open long.
		ctx.Log.Info("金叉——平空开多",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasShort && (!m.cfg.AsymmetricExit || m.profitableEnoughToClose(bar.Close)) {
			req := strategy.CloseShort(bar.Symbol, 0)
			req.Reason = "cross_reversal"
			ctx.PlaceOrder(req)
			stillShort = false
		}
		if !m.hasLong && !stillShort && m.trendOK() && !cooldownActive(m.everClosed, m.barsSinceClose, m.cfg.CooldownBars) {
			m.openLong(ctx, bar)
		}

	case -1:
		// Death cross: close long (if open and, under AsymmetricExit, only if
		// profitable), then open short.
		ctx.Log.Info("死叉——平多开空",
			zap.String("symbol", bar.Symbol),
			zap.Float64("fast", indicator.Last(fast)),
			zap.Float64("slow", indicator.Last(slow)),
			zap.Float64("close", bar.Close),
		)
		if m.hasLong && (!m.cfg.AsymmetricExit || m.profitableEnoughToClose(bar.Close)) {
			req := strategy.CloseLong(bar.Symbol, 0)
			req.Reason = "cross_reversal"
			ctx.PlaceOrder(req)
			stillLong = false
		}
		if !m.hasShort && !stillLong && m.trendOK() && !cooldownActive(m.everClosed, m.barsSinceClose, m.cfg.CooldownBars) {
			m.openShort(ctx, bar)
		}
	}
}

// floatingPnLPct returns the currently open leg's unrealized P&L as a fraction
// of its entry price (positive = profit). Returns 0 when flat.
func (m *MACross) floatingPnLPct(price float64) float64 {
	if m.posEntry == 0 {
		return 0
	}
	switch {
	case m.hasLong:
		return (price - m.posEntry) / m.posEntry
	case m.hasShort:
		return (m.posEntry - price) / m.posEntry
	default:
		return 0
	}
}

// profitableEnoughToClose reports whether the currently open leg's floating
// profit clears MinProfitToClosePct — the gate AsymmetricExit uses to decide
// whether a reverse cross closes (take-profit-on-reversal) or holds. Requires
// strictly more than fees, not merely a nominal profit — see MinProfitToClosePct.
func (m *MACross) profitableEnoughToClose(price float64) bool {
	return m.floatingPnLPct(price) > m.cfg.MinProfitToClosePct
}

// manageAsymmetricExit runs the confirmed-loss reduce and large-winner trailing
// exit for the currently open leg. Only called when AsymmetricExit is on and a
// position is open. The hard StopLossPct backstop is unaffected — it's placed
// as an exchange-native protective order at entry time and fires independently.
func (m *MACross) manageAsymmetricExit(ctx *strategy.Context, bar exchange.Kline) {
	fp := m.floatingPnLPct(bar.Close)

	m.posLossStreak = updateLossStreak(m.posLossStreak, fp, m.cfg.ReduceTriggerPct)
	if m.cfg.ReduceTriggerPct > 0 && shouldReduce(m.posLossStreak, m.cfg.ReduceConfirmBars, m.posReduced) {
		m.reducePosition(ctx, bar, m.cfg.ReduceFrac)
		m.posReduced = true
		m.posLossStreak = 0
	}

	if m.cfg.TrailActivatePct > 0 {
		if fp > m.posPeakFP {
			m.posPeakFP = fp
		}
		if shouldTrailClose(m.posPeakFP, fp, m.cfg.TrailActivatePct, m.cfg.TrailGivebackFrac) {
			if m.hasLong {
				req := strategy.CloseLong(bar.Symbol, 0)
				req.Reason = "trail_giveback"
				ctx.PlaceOrder(req)
			} else if m.hasShort {
				req := strategy.CloseShort(bar.Symbol, 0)
				req.Reason = "trail_giveback"
				ctx.PlaceOrder(req)
			}
		}
	}
}

// reducePosition partially closes the currently open leg by frac (e.g. 0.5 =
// half). No-op if the leg's qty isn't known yet (shouldn't happen in practice —
// OnFill always sets it on open — but guards against div-by-zero-style orders).
func (m *MACross) reducePosition(ctx *strategy.Context, bar exchange.Kline, frac float64) {
	if m.posQty <= 0 {
		return
	}
	qty := m.posQty * frac
	if m.hasLong {
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol, Side: strategy.SideSell, PositionSide: strategy.PositionSideLong,
			Type: strategy.OrderMarket, Qty: qty, Reason: "asymmetric_reduce",
		})
	} else if m.hasShort {
		ctx.PlaceOrder(strategy.OrderRequest{
			Symbol: bar.Symbol, Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort,
			Type: strategy.OrderMarket, Qty: qty, Reason: "asymmetric_reduce",
		})
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
	m.placeEntry(ctx, req, bar.Close)
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
	m.placeEntry(ctx, req, bar.Close)
}

// positionEpsilon absorbs float rounding when a reduce fill's qty doesn't
// exactly zero out posQty.
const positionEpsilon = 1e-9

// resetPosState clears the per-leg asymmetric-exit bookkeeping on a fresh open.
func (m *MACross) resetPosState() {
	m.posReduced = false
	m.posLossStreak = 0
	m.posPeakFP = 0
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

	// Update hedge position state. Opening fills (re-)establish entry/qty for
	// the asymmetric-exit bookkeeping; closing fills decrement qty and only
	// clear the has* flag once the leg is fully flat — a partial reduce fill
	// must NOT be mistaken for a full close.
	switch {
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideBuy:
		m.hasLong = true
		m.posEntry = fill.Price
		m.posQty = fill.Qty
		m.resetPosState()
	case fill.PositionSide == strategy.PositionSideLong && fill.Side == strategy.SideSell:
		m.posQty -= fill.Qty
		if m.posQty <= positionEpsilon {
			m.hasLong = false
			m.posQty = 0
			m.barsSinceClose = 0
			m.everClosed = true
		}
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideSell:
		m.hasShort = true
		m.posEntry = fill.Price
		m.posQty = fill.Qty
		m.resetPosState()
	case fill.PositionSide == strategy.PositionSideShort && fill.Side == strategy.SideBuy:
		m.posQty -= fill.Qty
		if m.posQty <= positionEpsilon {
			m.hasShort = false
			m.posQty = 0
			m.barsSinceClose = 0
			m.everClosed = true
		}
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
			// default OFF as of 2026-08-05: the previous default-on 0.20 filter
			// (ER over just TrendFilterN=10 bars, ~2.5h at 15m) was re-tested
			// against a full 21-month ETH history (2 real bear legs) and a real
			// BTC -53% decline + calm control period. On BOTH assets, across
			// both regimes, leaving the filter OFF beat the shipped 0.20 default
			// -- the 10-bar ER window is too short relative to the FastPeriod/
			// SlowPeriod signal it was meant to gate, so it was filtering out
			// good entries more often than bad ones. See docs/research/.
			cfg.TrendFilterMin = 0
		}
		if v, ok := params["CrossBufferPct"]; ok {
			cfg.CrossBufferPct = toFloat(v) // opt-in; default 0 (backtest showed a buffer hurts)
		}
		if v, ok := params["CooldownBars"]; ok {
			cfg.CooldownBars = toInt(v) // opt-in; default 0 (disabled, unchanged behaviour)
		}
		if v, ok := params["EntryOrderType"].(string); ok {
			cfg.EntryOrderType = v
		} // default "" == market, unchanged behaviour
		if v, ok := params["EntryLimitOffsetPct"]; ok {
			cfg.EntryLimitOffsetPct = toFloat(v)
		} else {
			cfg.EntryLimitOffsetPct = 0.0005 // 0.05%
		}
		if v, ok := params["EntryTimeoutBars"]; ok {
			cfg.EntryTimeoutBars = toInt(v)
		} else {
			cfg.EntryTimeoutBars = 3
		}
		if v, ok := params["AsymmetricExit"].(bool); ok {
			cfg.AsymmetricExit = v
		} else {
			cfg.AsymmetricExit = true // default ON — see Config.AsymmetricExit doc for validation
		}
		if v, ok := params["MinProfitToClosePct"]; ok {
			cfg.MinProfitToClosePct = toFloat(v)
		} else {
			cfg.MinProfitToClosePct = 0.001 // 0.1% — clears the ~0.08% round-trip taker fee with a small buffer
		}
		if v, ok := params["ReduceTriggerPct"]; ok {
			cfg.ReduceTriggerPct = toFloat(v)
		} else {
			cfg.ReduceTriggerPct = 0.01
		}
		if v, ok := params["ReduceConfirmBars"]; ok {
			cfg.ReduceConfirmBars = toInt(v)
		} else {
			cfg.ReduceConfirmBars = 2
		}
		if v, ok := params["ReduceFrac"]; ok {
			cfg.ReduceFrac = toFloat(v)
		} else {
			cfg.ReduceFrac = 0.5
		}
		if v, ok := params["TrailActivatePct"]; ok {
			cfg.TrailActivatePct = toFloat(v)
		} else {
			cfg.TrailActivatePct = 0.05
		}
		if v, ok := params["TrailGivebackFrac"]; ok {
			cfg.TrailGivebackFrac = toFloat(v)
		} else {
			cfg.TrailGivebackFrac = 0.35
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
		if cfg.ReduceFrac < 0 || cfg.ReduceFrac > 1 {
			return nil, fmt.Errorf("ReduceFrac must be in [0, 1] (got %.4f)", cfg.ReduceFrac)
		}
		if cfg.TrailGivebackFrac < 0 || cfg.TrailGivebackFrac > 1 {
			return nil, fmt.Errorf("TrailGivebackFrac must be in [0, 1] (got %.4f)", cfg.TrailGivebackFrac)
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
