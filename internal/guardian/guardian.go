package guardian

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

// Retry pacing for rejected orders (see rejectBackoff in backoff.go). Entry
// and partial-TP rejections aren't naked-position emergencies, so they back
// off slower and cap higher; a rejected close leaves the position exposed
// right now, so it retries sooner and caps lower. All three notify once
// immediately and at most every notifyEvery thereafter while the rejection
// persists — never every single retry (2026-08-24 incident: a persistent
// leverage-cap rejection retried every 30s for 10 hours, one Telegram
// message each time, before backoff existed).
const (
	entryRetryBase   = 30 * time.Second
	entryRetryMax    = 10 * time.Minute
	entryNotifyEvery = 10 * time.Minute

	closeRetryBase   = 5 * time.Second
	closeRetryMax    = 5 * time.Minute
	closeNotifyEvery = 5 * time.Minute

	partialTPRetryBase   = 30 * time.Second
	partialTPRetryMax    = 10 * time.Minute
	partialTPNotifyEvery = 10 * time.Minute
)

// fmtPrice formats a price as a plain number with thousands separators (never
// scientific notation), so user notifications read "64,127.70" not "6.413e+04".
func fmtPrice(v float64) string {
	dec := 2
	if v > 0 && v < 1 {
		dec = 6 // sub-$1 assets need more precision
	}
	s := strconv.FormatFloat(v, 'f', dec, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		dot = len(s)
	}
	intPart, frac := s[:dot], s[dot:]
	neg := strings.HasPrefix(intPart, "-")
	if neg {
		intPart = intPart[1:]
	}
	var b strings.Builder
	n := len(intPart)
	for i := 0; i < n; i++ {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(intPart[i])
	}
	out := b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}

// livePositionSource reports the live account position per side. The live
// engine's *position.Syncer satisfies it and is injected via
// ctx.Extra["position_syncer"]; absent in backtests. The guardian uses it to
// detect an already-open position on restart (so arm-with-entry adopts instead
// of re-placing the market entry).
type livePositionSource interface {
	GetLong() *position.StrategyPosition
	GetShort() *position.StrategyPosition
}

// Phase is the Guardian's lifecycle stage — the single source of truth for
// Status()'s "state" wire value. These 4 strings are a fixed contract with
// web/src/pages/Engine.tsx's LiveStatus display (arming/watching/closing/
// closed → 准备中/守护中/平仓中/已结束) and must never change.
type Phase string

const (
	PhaseArming   Phase = "arming"
	PhaseWatching Phase = "watching"
	PhaseClosing  Phase = "closing"
	PhaseClosed   Phase = "closed"
)

// Mode selects how the Guardian arms itself when it starts unarmed
// (prot == nil). Set once at construction; never changes for the life of the
// instance. See ResolveMode (config.go) for how a start request's params
// resolve to one of these.
type Mode int

const (
	ModeAdopt    Mode = iota // arm from whatever the account already holds
	ModeEntry                // place the entry first, arm from its fill (or a live-position match)
	ModeExplicit             // pre-armed at construction (API-only); never re-arms
)

// Guardian is a protective strategy: it never opens (the user does, unless
// Mode is ModeEntry) and never re-enters. It watches one position, ratchets a
// trailing stop, closes on stop or take-profit, and exposes live status. It
// implements strategy.Strategy, strategy.TickReceiver and
// strategy.StatusReporter.
type Guardian struct {
	symbol string
	prot   *Protection
	cfg    ProtectionConfig // used to arm when adopting
	mode   Mode
	phase  Phase
	atr    *ATR
	log    *zap.Logger

	prevClose float64
	lastPrice float64
	barsHeld  int

	// alerts (optional)
	alerts   *AlertEngine
	dispatch Dispatcher
	sma      *SMA
	avgATR   float64
	avgAlpha float64

	// resting-stop reliability (opt-in; live). When on, the protective stop is an
	// exchange-native STOP_MARKET that survives bot/server death; the bot only
	// advances it. If placement fails, restingMode stays false and the tick-based
	// close is used as a fallback.
	wantRestingStop  bool
	restingMode      bool
	restingTried     bool
	stopOrderID      string
	restingStopPrice float64 // stop price currently resting on the exchange

	// resting-take-profit (opt-in; live). When on and a take-profit is
	// configured, it is an exchange-native reduce-only LIMIT order at the TP
	// price placed as soon as the position is armed — it fills exactly there
	// (maker, no slippage) instead of a market close fired after a tick
	// crosses the target. If placement fails, restingTPMode stays false and
	// the tick-based market close (checkExitBar/OnTick) is used as a fallback.
	wantRestingTP  bool
	restingTPMode  bool
	tpTried        bool
	tpOrderID      string
	restingTPPrice float64 // TP price currently resting on the exchange

	// restart recovery (optional). restoreLoaded/restoredState guard the ONE
	// store.Load() call (across both OnBar/OnTick call sites and however many
	// bars arming takes); trailApplied guards the post-arm overlay, which may
	// happen on a later bar than the load itself (e.g. ATR warmup delays arming).
	store         StateStore
	stateKey      string
	restoreLoaded bool
	restoredState *GuardianState
	trailApplied  bool

	armNotified bool // arm-summary (risk preview) sent once
	partialDone bool // partial take-profit already fired

	// arm-with-entry (optional, Mode == ModeEntry): place the entry, then arm from its fill.
	entrySide   string
	entryQty    float64
	entryType   string  // "market" (default) or "limit"
	entryPrice  float64 // limit price when entryType == "limit"
	entryPlaced bool
	// {entry,close,partialTP}Backoff pace retries of a rejected order (see
	// rejectBackoff in backoff.go) — growing wait, throttled notification.
	entryBackoff     rejectBackoff
	closeBackoff     rejectBackoff
	partialTPBackoff rejectBackoff
}

// NewGuardian builds a Guardian for an already-armed Protection (ModeExplicit,
// prot != nil). Also used internally by NewAdoptGuardian/NewEntryGuardian with
// prot == nil, which overwrite mode immediately after.
func NewGuardian(symbol string, prot *Protection, atrWindow int, log *zap.Logger) *Guardian {
	if log == nil {
		log = zap.NewNop()
	}
	// AvgATR baseline = EMA of ATR over ~4× the ATR window (for vol-spike detection).
	alpha := 2.0 / (float64(4*atrWindow) + 1)
	phase := PhaseArming
	if prot != nil {
		phase = PhaseWatching
	}
	return &Guardian{symbol: symbol, prot: prot, mode: ModeExplicit, phase: phase, atr: NewATR(atrWindow), log: log, avgAlpha: alpha}
}

// NewAdoptGuardian builds a Guardian that arms itself from the live account
// position on the first bar (for "I already opened on the exchange, guard it").
func NewAdoptGuardian(symbol string, cfg ProtectionConfig, atrWindow int, log *zap.Logger) *Guardian {
	g := NewGuardian(symbol, nil, atrWindow, log)
	g.cfg = cfg
	g.mode = ModeAdopt
	return g
}

// NewEntryGuardian builds a Guardian that first PLACES the entry order, then arms
// protection from its fill — open and protect in one action.
func NewEntryGuardian(symbol, side string, qty float64, cfg ProtectionConfig, atrWindow int, log *zap.Logger) *Guardian {
	g := NewGuardian(symbol, nil, atrWindow, log)
	g.cfg = cfg
	g.mode = ModeEntry
	g.entrySide = side
	g.entryQty = qty
	g.entryType = "market"
	return g
}

// UpdateParams applies a live change to the protective setup (止损/止盈/移动止损/
// 落袋 等) on a running guardian — without stopping the engine or touching the
// position. The stop recomputes (tighten-only once trailing; free before), the
// take-profit updates immediately, and the exchange resting stop is re-placed at
// the new level. Implements strategy.LiveUpdatable; the engine calls it from its
// run-loop goroutine, so no extra locking is needed.
func (g *Guardian) UpdateParams(ctx *strategy.Context, params map[string]any) error {
	newCfg := parseProtection(params)
	g.cfg = newCfg // future re-arm (adopt/restart) uses the new config
	if g.prot == nil {
		return nil // not armed yet — applies when protection arms
	}
	g.prot.UpdateConfig(newCfg, g.atr.Value())
	g.forceResyncRestingStop(ctx) // exchange stop must match the new level (either direction)
	g.forceResyncRestingTP(ctx)   // exchange TP must match the new target (new price, or on/off)
	tp := "不设"
	if g.prot.TPPrice() > 0 {
		tp = fmtPrice(g.prot.TPPrice())
	}
	g.log.Info("guardian：参数已实时更新",
		zap.String("symbol", g.symbol),
		zap.Float64("stop", g.prot.Stop), zap.Float64("tp", g.prot.TPPrice()))
	g.notifyAction("updated", fmt.Sprintf("已更新守护参数:止损→%s,止盈→%s", fmtPrice(g.prot.Stop), tp))
	return nil
}

// forceResyncRestingStop re-places the exchange resting stop at the current
// protection stop regardless of direction (a live edit may widen or tighten it).
// No-op outside resting mode, where the tick-based close reads g.prot.Stop live.
func (g *Guardian) forceResyncRestingStop(ctx *strategy.Context) {
	if !g.restingMode || g.stopOrderID == "" {
		return
	}
	_ = ctx.CancelOrder(g.stopOrderID)
	g.stopOrderID = ""
	g.placeRestingStop(ctx)
}

// forceResyncRestingTP re-places the resting take-profit limit at the current
// TP price after a live parameter edit, which may change the target price,
// turn take-profit on, or turn it off entirely. No-op when resting-TP was
// never enabled for this guardian (backtest/paper, or wantRestingTP off).
func (g *Guardian) forceResyncRestingTP(ctx *strategy.Context) {
	if !g.wantRestingTP || g.prot == nil {
		return
	}
	if g.tpOrderID != "" {
		_ = ctx.CancelOrder(g.tpOrderID)
		g.tpOrderID = ""
		g.restingTPMode = false
	}
	if g.prot.TPPrice() > 0 {
		g.tpTried = true // mark handled so ensureRestingTP doesn't also place it
		g.placeRestingTP(ctx)
	}
}

// SetLimitEntry switches arm-with-entry to a resting LIMIT order at price (0 or
// non-"limit" type keeps the default market entry). The protective exit stays
// market regardless.
func (g *Guardian) SetLimitEntry(price float64) {
	if price > 0 {
		g.entryType = "limit"
		g.entryPrice = price
	}
}

// livePosition returns the live account position for this symbol (preferring the
// side we intended to open), or nil if none / no syncer. Used to detect an
// already-open position on restart so arm-with-entry adopts instead of re-opening.
func (g *Guardian) livePosition(ctx *strategy.Context) *position.StrategyPosition {
	src, ok := ctx.Extra["position_syncer"].(livePositionSource)
	if !ok || src == nil {
		return nil
	}
	var pos *position.StrategyPosition
	if g.entrySide == SideShort {
		if pos = src.GetShort(); pos == nil {
			pos = src.GetLong()
		}
	} else {
		if pos = src.GetLong(); pos == nil {
			pos = src.GetShort()
		}
	}
	// Accept even without an entry price — a position recovered from the exchange
	// may lack a cost basis; enterOrAdopt falls back to the current price so we
	// still adopt & protect it rather than trying to re-open.
	if pos == nil || pos.Qty <= 0 {
		return nil
	}
	return pos
}

// enterOrAdopt drives arm-with-entry idempotently. If the account already holds
// the position (e.g. after a restart) it adopts it — arming protection from the
// live position instead of re-placing the market entry (the "re-orders on every
// deploy" bug). Otherwise it opens once and arms later from the fill.
//
// isWarmup gates only the OPEN: during the startup backfill replay the broker
// suppresses orders, so placing the entry then would latch entryPlaced without
// ever really opening. Adoption is safe any time (it places no order).
//
// Returns true only when protection is now armed (caller continues guarding);
// false means "wait" — the entry is pending/deferred, or ATR is still warming up.
func (g *Guardian) enterOrAdopt(ctx *strategy.Context, atr float64, isWarmup bool) bool {
	pos := g.livePosition(ctx)
	if pos == nil {
		if isWarmup || g.entryPlaced || !g.entryBackoff.ready() {
			// isWarmup: don't open during warmup replay — the order is suppressed.
			// entryPlaced: the one-shot entry already fired in a prior call/run —
			// don't place a second one. (If it already filled, the branch above
			// would have caught it via the live position; if it's still pending,
			// wait rather than duplicate it.)
			// entryBackoff: a prior attempt was just rejected (placeEntry resets
			// entryPlaced so it isn't stuck forever) — back off before retrying
			// instead of re-submitting on every tick.
			return false
		}
		g.placeEntry(ctx) // first run, live bar: no existing position → open it
		return false
	}
	// Restart: position already open → adopt, never re-enter.
	if g.cfg.StopMode == StopATR && !g.atr.Ready() {
		return false // ATR warming up; retry next bar (still no new entry)
	}
	side := SideLong
	sideCN := "多"
	if pos.Side == "SHORT" {
		side, sideCN = SideShort, "空"
	}
	// A position recovered from the exchange may lack a cost basis; protect from
	// the current price rather than leaving it unprotected.
	entry := pos.EntryPrice
	if entry <= 0 {
		entry = g.lastPrice
	}
	if entry <= 0 {
		return false // no price yet — retry next bar/tick
	}
	g.prot = NewProtection(side, entry, pos.Qty, g.cfg, atr)
	g.phase = PhaseWatching
	g.log.Info("guardian：接管已有仓位，不重复开仓",
		zap.String("symbol", g.symbol), zap.String("side", side),
		zap.Float64("entry", pos.EntryPrice), zap.Float64("qty", pos.Qty),
		zap.Float64("stop", g.prot.Stop))
	g.notifyAction("adopt", fmt.Sprintf("检测到账户已有%s仓,直接接管保护(未重复开仓)", sideCN))
	return true
}

// placeEntry submits the market entry order once (armed later via OnFill).
func (g *Guardian) placeEntry(ctx *strategy.Context) {
	g.entryPlaced = true
	g.persistEntryPlaced()
	var req strategy.OrderRequest
	dir := "空"
	if g.entrySide == SideLong {
		req = strategy.OpenLong(g.symbol, g.entryQty)
		dir = "多"
	} else {
		req = strategy.OpenShort(g.symbol, g.entryQty)
	}
	// Optional resting LIMIT entry (protective exit stays market).
	kind := "市价"
	if g.entryType == "limit" && g.entryPrice > 0 {
		req.Type = strategy.OrderLimit
		req.Price = g.entryPrice
		kind = fmt.Sprintf("限价%s", fmtPrice(g.entryPrice))
	}
	req.Reason = "guardian_entry"
	id := ctx.PlaceOrder(req)
	if id == "" {
		// PlaceOrder submits synchronously up through the exchange call (only
		// FILL confirmation is async), so an empty ID means the order was
		// rejected right here — by the exchange (e.g. insufficient margin) or
		// by our own risk gate (e.g. leverage cap) — and nothing is live on
		// the exchange. The one-shot guard above must not stick in that case:
		// entryPlaced=true with no order and no position left every future
		// restart/retry (even with corrected params) silently no-op forever,
		// with only a log line to show for it (confirmed live twice in one
		// day, 2026-08-21). Backing off — rather than a flat cooldown —
		// matters because some rejection reasons never resolve on their own
		// (a leverage cap doesn't get less exceeded by waiting): a flat 30s
		// retry hammered the exchange and paged Telegram every 30s for 10
		// hours straight (2026-08-24) before this existed.
		g.entryPlaced = false
		g.clearState()
		notify := g.entryBackoff.record(entryRetryBase, entryRetryMax, entryNotifyEvery)
		g.log.Warn("guardian：开仓单被拒绝，已重置一次性开仓标记，可以重新尝试",
			zap.String("symbol", g.symbol), zap.Int("reject_count", g.entryBackoff.count))
		if notify {
			g.notifyAction("entry_failed", "开仓单被拒绝(交易所或风控,比如保证金/杠杆超限),已重置,调整好参数后可以重新开始。会自动重试,持续失败只会间隔性提醒")
		}
		return
	}
	g.entryBackoff.reset()
	g.notifyAction("entry", fmt.Sprintf("已按你的指令%s开%s单 %.6g,成交后自动挂上保护", kind, dir, g.entryQty))
}

// tryArm initialises Protection from the live account position; returns whether armed.
func (g *Guardian) tryArm(ctx *strategy.Context, atr float64) bool {
	if g.mode != ModeAdopt || ctx.Portfolio == nil {
		return false
	}
	qty, avg, ok := ctx.Portfolio.Position(g.symbol)
	if !ok || qty == 0 || avg <= 0 {
		return false
	}
	if g.cfg.StopMode == StopATR && !g.atr.Ready() {
		return false // need a valid ATR before an ATR-based stop
	}
	side := SideLong
	if qty < 0 {
		side = SideShort
	}
	g.prot = NewProtection(side, avg, math.Abs(qty), g.cfg, atr)
	g.phase = PhaseWatching
	g.log.Info("guardian：已接管仓位",
		zap.String("symbol", g.symbol), zap.String("side", side),
		zap.Float64("entry", avg), zap.Float64("qty", math.Abs(qty)),
		zap.Float64("stop", g.prot.Stop))
	return true
}

// SetAlerts attaches an alert engine and dispatcher. maPeriod>0 enables the MA
// reference used by MAState (0 = no MA rules). Safe to call once before running.
func (g *Guardian) SetAlerts(engine *AlertEngine, d Dispatcher) {
	g.alerts = engine
	g.dispatch = d
}

// SetMAPeriod enables the reference moving average for MA-cross fact alerts.
func (g *Guardian) SetMAPeriod(period int) {
	if period > 0 {
		g.sma = NewSMA(period)
	}
}

// SetDispatcher injects the live alert dispatcher (the engine layer wires the
// notifier here after constructing the strategy from the registry).
func (g *Guardian) SetDispatcher(d Dispatcher) { g.dispatch = d }

// SetRestingStop enables placing an exchange-native resting STOP_MARKET as the
// protective stop (survives bot/server death). The live engine turns this on; it
// stays off in backtest/paper, where the tick-based close is used.
func (g *Guardian) SetRestingStop(on bool) { g.wantRestingStop = on }

// SetRestingTP enables placing an exchange-native resting reduce-only LIMIT
// order at the take-profit price as soon as the position is armed, so it
// fills exactly there instead of via a tick-triggered market close. The live
// engine turns this on; it stays off in backtest/paper. No-op when no
// take-profit is configured (TPMode/TPValue unset).
func (g *Guardian) SetRestingTP(on bool) { g.wantRestingTP = on }

// SetStateStore wires persistence so the trailed stop survives a restart. key is
// the engine id; the live engine supplies a DB-backed store.
func (g *Guardian) SetStateStore(store StateStore, key string) {
	g.store = store
	g.stateKey = key
}

// restoreState is called at the top of OnBar and OnTick, both before AND after
// the arm attempt. It loads the persisted blob exactly once (restoreLoaded)
// and applies the prot-independent parts (EntryPlaced, Closing) immediately;
// the prot-dependent trail overlay applies once prot exists, which may be a
// later call if arming is delayed (e.g. ATR warmup).
func (g *Guardian) restoreState() {
	if g.store == nil || g.stateKey == "" {
		return
	}
	if !g.restoreLoaded {
		g.restoreLoaded = true
		if st, ok := g.store.Load(g.stateKey); ok {
			g.restoredState = &st
			if g.mode == ModeEntry && st.EntryPlaced {
				g.entryPlaced = true
				g.log.Info("guardian：一次性开仓指令此前已执行过，重启后不再重复开仓",
					zap.String("symbol", g.symbol))
			}
			if st.Closing {
				g.phase = PhaseClosing
			}
		}
	}
	if !g.trailApplied && g.restoredState != nil && g.prot != nil {
		g.trailApplied = true
		st := g.restoredState
		g.prot.Stop = st.Stop
		g.prot.PeakR = st.PeakR
		g.prot.SetActivated(st.Activated)
		g.stopOrderID = st.StopOrderID
		g.restingStopPrice = st.RestingStopPrice
		g.partialDone = st.PartialDone
		if st.StopOrderID != "" {
			// The resting stop still lives on the exchange; don't place a duplicate.
			g.restingMode = true
			g.restingTried = true
		}
		g.tpOrderID = st.TPOrderID
		g.restingTPPrice = st.RestingTPPrice
		if st.TPOrderID != "" {
			// The resting take-profit still lives on the exchange; don't duplicate it.
			g.restingTPMode = true
			g.tpTried = true
		}
		g.log.Info("guardian：重启后已恢复跟踪止损状态",
			zap.String("symbol", g.symbol), zap.Float64("stop", st.Stop))
	}
}

// saveState persists the current trail state (best-effort). EntryPlaced is
// carried forward so a later trail-state save (which writes the whole JSON
// document) never resets the one-shot-entry flag back to false.
func (g *Guardian) saveState() {
	if g.store == nil || g.stateKey == "" || g.prot == nil {
		return
	}
	_ = g.store.Save(g.stateKey, GuardianState{
		Stop:             g.prot.Stop,
		PeakR:            g.prot.PeakR,
		Activated:        g.prot.Activated(),
		StopOrderID:      g.stopOrderID,
		RestingStopPrice: g.restingStopPrice,
		TPOrderID:        g.tpOrderID,
		RestingTPPrice:   g.restingTPPrice,
		EntryPlaced:      g.entryPlaced,
		PartialDone:      g.partialDone,
		Closing:          g.phase == PhaseClosing,
	})
}

// clearState removes the persisted state entirely. Must be called the moment
// g.phase becomes PhaseClosed — by ANY path (self-retire or externally
// closed) — otherwise a stale record (Closing:true above all) would poison a
// future, unrelated guardian that later reuses this exact engine_id
// (engine_id is deterministic — "Symbol-Interval-guardian" — with no session
// nonce, and rows are never deleted anywhere else).
func (g *Guardian) clearState() {
	if g.store == nil || g.stateKey == "" {
		return
	}
	_ = g.store.Delete(g.stateKey)
}

// persistEntryPlaced records immediately that the one-shot entry has fired, so
// even if the process dies before any trail state exists, a subsequent restart
// still knows not to re-enter.
func (g *Guardian) persistEntryPlaced() {
	if g.store == nil || g.stateKey == "" {
		return
	}
	_ = g.store.Save(g.stateKey, GuardianState{EntryPlaced: true})
}

// notifyAction sends a factual notification about something the guardian did
// (placed/moved a stop, closed, retired). No-op when no dispatcher is wired.
func (g *Guardian) notifyAction(event, msg string) {
	if g.dispatch != nil {
		_ = g.dispatch.Send(event, "["+g.symbol+"] "+msg)
	}
}

// maybePartialTP closes a fraction of the position once P&L reaches the partial
// take-profit trigger (once). The account position then shrinks; resyncPosition
// re-sizes the resting stop on the next bar (single source of truth = account qty).
func (g *Guardian) maybePartialTP(ctx *strategy.Context, price float64) {
	if g.partialDone || g.prot == nil || g.inactive() || !g.prot.PartialTPReady(price) {
		return
	}
	if !g.partialTPBackoff.ready() {
		return
	}
	g.partialDone = true
	g.saveState() // persist immediately so a restart before the next trail-save doesn't forget
	qty := g.prot.PartialTPQty()
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.CloseLong(g.symbol, qty)
	} else {
		req = strategy.CloseShort(g.symbol, qty)
	}
	req.Reason = "guardian_partial_tp"
	id := ctx.PlaceOrder(req)
	if id == "" {
		// Rejected — unlike placeClose, the stop-loss is untouched here, so
		// this isn't a naked-position emergency, just a silently missed
		// profit-bank if left alone (2026-08-21 audit finding: partialDone
		// was set true before the result was known, with no reset AND no
		// notification on failure — the user would believe they'd banked
		// profit they never actually banked).
		g.partialDone = false
		g.saveState()
		notify := g.partialTPBackoff.record(partialTPRetryBase, partialTPRetryMax, partialTPNotifyEvery)
		g.log.Warn("guardian：分批止盈单被拒绝，已重置，将在冷却后重试",
			zap.String("symbol", g.symbol), zap.Int("reject_count", g.partialTPBackoff.count))
		if notify {
			g.notifyAction("partial_tp_failed", "分批止盈单被拒绝,还没有落袋,会自动重试(持续失败只会间隔性提醒)")
		}
		return
	}
	g.partialTPBackoff.reset()
	g.notifyAction("partial_tp", fmt.Sprintf("已到分批止盈点,先平掉 %.6g(约一半)落袋,剩下继续让止损守着跑", qty))
}

// notifyArmSummary sends the risk preview (and any sanity warnings) once, when the
// guardian first starts protecting a position.
func (g *Guardian) notifyArmSummary() {
	if g.armNotified || g.prot == nil {
		return
	}
	g.armNotified = true
	p := g.prot
	dir := "多"
	if p.Side == SideShort {
		dir = "空"
	}
	g.notifyAction("armed", fmt.Sprintf(
		"开始守护 %s %s单 @%s,止损 @%s(%.1f%%),这单最多亏 ~$%.2f",
		g.symbol, dir, fmtPrice(p.Entry), fmtPrice(p.Stop), p.RiskPct(), p.RiskUSD()))
	for _, warn := range RiskWarnings(p) {
		g.notifyAction("warning", "⚠️ "+warn)
	}
}

// ensureRestingStop places the resting stop once, the first time the position is armed.
func (g *Guardian) ensureRestingStop(ctx *strategy.Context) {
	if !g.wantRestingStop || g.restingTried || g.prot == nil || g.inactive() {
		return
	}
	g.restingTried = true
	g.placeRestingStop(ctx)
	if g.restingMode {
		g.notifyAction("stop_placed", fmt.Sprintf("已挂交易所止损单 @ %s,即使程序掉线也会替你止损", fmtPrice(g.prot.Stop)))
	}
}

// placeRestingStop submits a reduce-only STOP_MARKET at the current stop. On success
// the exchange owns the trigger (the bot only advances it); on failure restingMode
// stays false and the tick-based close takes over.
func (g *Guardian) placeRestingStop(ctx *strategy.Context) {
	id := ctx.PlaceOrder(g.stopReq())
	if id == "" {
		g.restingMode = false
		return
	}
	g.stopOrderID = id
	g.restingStopPrice = g.prot.Stop
	g.restingMode = true
	g.saveState()
	g.log.Info("guardian：已挂出交易所止损单",
		zap.String("symbol", g.symbol), zap.Float64("stop", g.prot.Stop),
		zap.String("order", id))
}

// syncRestingStop advances the resting stop when the trail has moved it materially
// in the trade's favour (cancel-then-replace, matching the broker's ReplaceSLOrder
// convention). Sub-epsilon moves are ignored to avoid churn.
func (g *Guardian) syncRestingStop(ctx *strategy.Context) {
	if !g.restingMode || g.stopOrderID == "" {
		return
	}
	eps := math.Max(g.prot.Stop*0.0005, 1e-9) // 0.05% of price
	improved := g.prot.Stop-g.restingStopPrice >= eps
	if g.prot.Side == SideShort {
		improved = g.restingStopPrice-g.prot.Stop >= eps
	}
	if !improved {
		return
	}
	_ = ctx.CancelOrder(g.stopOrderID)
	g.stopOrderID = ""
	g.placeRestingStop(ctx)
	g.notifyAction("stop_advanced", fmt.Sprintf("止损已上移到 %s,锁住更多利润", fmtPrice(g.prot.Stop)))
}

// stopReq builds the reduce-only STOP_MARKET order for the current stop.
func (g *Guardian) stopReq() strategy.OrderRequest {
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.OrderRequest{Symbol: g.symbol, Side: strategy.SideSell, PositionSide: strategy.PositionSideLong}
	} else {
		req = strategy.OrderRequest{Symbol: g.symbol, Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort}
	}
	req.Type = strategy.OrderStopMarket
	req.Qty = g.prot.Qty
	req.StopPrice = g.prot.Stop
	req.Reason = "guardian_resting_stop"
	return req
}

// ensureRestingTP places the resting take-profit limit once, the first time
// the position is armed. No-op when no take-profit is configured — checked
// every call (not latched) so a TP added later via UpdateParams still gets
// picked up promptly through forceResyncRestingTP instead.
func (g *Guardian) ensureRestingTP(ctx *strategy.Context) {
	if !g.wantRestingTP || g.tpTried || g.prot == nil || g.inactive() || g.prot.TPPrice() <= 0 {
		return
	}
	g.tpTried = true
	g.placeRestingTP(ctx)
	if g.restingTPMode {
		g.notifyAction("tp_placed", fmt.Sprintf("已挂交易所止盈单 @ %s,到价自动成交(限价,省滑点)", fmtPrice(g.prot.TPPrice())))
	}
}

// placeRestingTP submits a reduce-only LIMIT at the take-profit price. On
// success the exchange owns the fill; on failure restingTPMode stays false
// and the tick-based TP check (OnTick/checkExitBar) takes over.
func (g *Guardian) placeRestingTP(ctx *strategy.Context) {
	id := ctx.PlaceOrder(g.tpReq())
	if id == "" {
		g.restingTPMode = false
		return
	}
	g.tpOrderID = id
	g.restingTPPrice = g.prot.TPPrice()
	g.restingTPMode = true
	g.saveState()
	g.log.Info("guardian：已挂出交易所止盈单",
		zap.String("symbol", g.symbol), zap.Float64("tp", g.prot.TPPrice()),
		zap.String("order", id))
}

// tpReq builds the reduce-only LIMIT order for the current take-profit price.
func (g *Guardian) tpReq() strategy.OrderRequest {
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.OrderRequest{Symbol: g.symbol, Side: strategy.SideSell, PositionSide: strategy.PositionSideLong}
	} else {
		req = strategy.OrderRequest{Symbol: g.symbol, Side: strategy.SideBuy, PositionSide: strategy.PositionSideShort}
	}
	req.Type = strategy.OrderLimit
	req.Qty = g.prot.Qty
	req.Price = g.prot.TPPrice()
	req.Reason = "guardian_resting_tp"
	return req
}

// Name identifies the strategy type.
func (g *Guardian) Name() string { return "guardian" }

// OnBar updates ATR + trailing on each closed candle and enforces the stop/TP
// against the bar's intrabar extremes (the stop that was in force during the bar).
func (g *Guardian) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if g.phase == PhaseClosed {
		return
	}
	g.restoreState()
	pc := g.prevClose
	if pc == 0 {
		pc = bar.Open
	}
	atr := g.atr.Update(bar.High, bar.Low, pc)
	g.updateAvgATR(atr)
	if g.sma != nil {
		g.sma.Add(bar.Close)
	}
	g.prevClose = bar.Close
	g.lastPrice = bar.Close

	if g.prot == nil {
		if g.phase == PhaseClosing {
			// Restored Closing while still unarmed this run -- our own close
			// (placed by a prior process) may have resolved, fully or
			// partially, while we were down. Confirm flatness before ever
			// arming: a fully-resolved close must retire, not quietly start
			// fresh protection underneath a position that's already gone.
			if bar.Warmup {
				return
			}
			if qty, known := g.liveQty(ctx); !known || qty == 0 {
				g.retire(ctx)
				return
			}
			// Nonzero remainder: our close likely only partially filled (or
			// never went through at all). Fall through and arm it fresh
			// below rather than leaving it unprotected indefinitely.
		}
		switch g.mode {
		case ModeEntry: // open first (or adopt if already open), arm from there.
			// Always re-checks the live position, even after entryPlaced fires --
			// a fill that lands as an "unmatched fill" (OMS lost track of the
			// order's client-order-ID, e.g. after an intervening restart while a
			// resting LIMIT entry was still pending) never reaches OnFill, so
			// waiting for that notification alone can wait forever while a real,
			// unprotected position sits on the exchange (2026-08-05 incident).
			if !g.enterOrAdopt(ctx, atr, bar.Warmup) {
				return
			}
		default: // ModeAdopt (ModeExplicit never reaches here — prot is set at construction)
			if !g.tryArm(ctx, atr) {
				return
			}
		}
	}
	// During the startup backfill replay, only prime indicators / adopt — never run
	// the trail/exit logic on historical prices (it would false-trigger a stop and
	// submit a suppressed close, leaving the guardian stuck "closing"). Live ticks
	// (OnTick) place the resting stop and enforce protection right after warmup.
	if bar.Warmup {
		return
	}
	g.restoreState()
	if g.resyncPosition(ctx) { // position closed externally → retired, or partial fill re-armed
		return
	}
	if g.phase == PhaseClosing {
		// Our own close hasn't resolved yet — resyncPosition already checked
		// whether it's actually gone (→ retire) or partially filled (→ re-arm
		// above); if we're still here it's genuinely still open. Don't
		// re-trail/re-check exit on a position we've already told the exchange
		// to close.
		return
	}
	g.notifyArmSummary()
	g.ensureRestingStop(ctx)
	g.ensureRestingTP(ctx)
	g.barsHeld++
	g.evalAlerts(bar.Close)
	g.maybePartialTP(ctx, bar.Close)

	// Exit check first, using the stop as it stood during the bar.
	if g.checkExitBar(ctx, bar) {
		return
	}
	// Then trail for the next bar, advancing the resting stop if it moved.
	g.prot.UpdateStop(bar.Close, atr)
	g.syncRestingStop(ctx)
}

// engineLive reports whether the engine has finished replaying the warmup
// backfill and is now trading real-time (set by the live engine via ctx.Extra).
// Until then order execution is suppressed, so we must not place the entry.
func engineLive(ctx *strategy.Context) bool {
	v, _ := ctx.Extra["engine_live"].(bool)
	return v
}

// OnTick enforces the stop/TP precisely between bars and trails on favourable ticks.
func (g *Guardian) OnTick(ctx *strategy.Context, price float64) {
	if g.phase == PhaseClosed {
		return
	}
	g.restoreState()
	g.lastPrice = price
	// arm-with-entry: open as soon as the engine is live (real-time), rather than
	// waiting for the next closed bar. Skip if a position already exists — OnBar
	// adopts that case. This is what makes "顺便帮我开仓" open promptly.
	if g.prot == nil {
		if g.phase == PhaseClosing {
			// Restored Closing while still unarmed -- see the matching branch
			// in OnBar. Only OnBar's per-bar resync confirms/re-arms; never
			// treat this as "need a fresh entry" here.
			return
		}
		if g.mode == ModeEntry {
			// Mirrors OnBar: always re-check the live position, even after
			// entryPlaced fires or before engineLive flips -- a fill that lands
			// as an "unmatched fill" never reaches OnFill, so waiting for that
			// notification alone can wait forever while a real, unprotected
			// position sits on the exchange (2026-08-05 incident). A brand-new
			// entry still waits for engineLive, same as before.
			g.enterOrAdopt(ctx, g.atr.Value(), !engineLive(ctx))
		}
		return
	}
	g.restoreState()
	if g.phase == PhaseClosing {
		// See the matching comment in OnBar — OnBar's per-bar resyncPosition is
		// what actually confirms/re-arms; OnTick just waits quietly in between.
		return
	}
	g.notifyArmSummary()
	g.ensureRestingStop(ctx)
	g.ensureRestingTP(ctx)
	g.lastPrice = price
	g.evalAlerts(price)
	g.maybePartialTP(ctx, price)
	// In resting-TP mode the exchange owns the take-profit fill; the bot only
	// needs to react once it's filled (via OnFill), not race it on ticks.
	if !g.restingTPMode && g.prot.TPHit(price) {
		g.placeClose(ctx, "take_profit")
		return
	}
	// In resting mode the exchange owns the stop trigger; the bot only trails it.
	if !g.restingMode && g.prot.StopHit(price) {
		g.placeClose(ctx, g.stopReason())
		return
	}
	g.prot.UpdateStop(price, g.atr.Value())
	g.syncRestingStop(ctx)
}

// liveQty returns the live account position size (unsigned) for this symbol
// and whether it's known. Prefers the position syncer — it tracks hedge LONG/
// SHORT legs, whereas ctx.Portfolio.Position only exposes the one-way slot and
// would report a hedge position as gone (falsely retiring the guardian).
// Falls back to the portfolio only when no syncer is present (backtest/paper).
func (g *Guardian) liveQty(ctx *strategy.Context) (float64, bool) {
	if pos := g.livePosition(ctx); pos != nil {
		return pos.Qty, true
	}
	if _, ok := ctx.Extra["position_syncer"]; !ok && ctx.Portfolio != nil {
		if q, _, pok := ctx.Portfolio.Position(g.symbol); pok {
			return math.Abs(q), true
		}
	}
	return 0, false
}

// resyncPosition reconciles against the live account position each bar. If the
// position vanished (user closed / liquidated, or our own close finally
// confirmed) it retires; if the size changed it updates the protected qty and
// re-sizes the resting stop — including the case where we were Closing and the
// live qty shrank but didn't reach zero (our close only partially filled), in
// which case the remainder is re-armed back into Watching rather than left
// unprotected. Returns true if the guardian retired this cycle.
func (g *Guardian) resyncPosition(ctx *strategy.Context) bool {
	if g.prot == nil {
		return false
	}
	qty, known := g.liveQty(ctx)
	if !known || qty == 0 {
		g.retire(ctx)
		return true
	}
	if absQty := qty; absQty != g.prot.Qty {
		if g.phase == PhaseClosing {
			g.phase = PhaseWatching
			g.notifyAction("resync", fmt.Sprintf("平仓单疑似只部分成交,剩余 %.6g 已重新纳入保护", absQty))
		}
		g.prot.Qty = absQty
		if g.restingMode && g.stopOrderID != "" {
			_ = ctx.CancelOrder(g.stopOrderID)
			g.stopOrderID = ""
			g.placeRestingStop(ctx)
		}
		if g.restingTPMode && g.tpOrderID != "" {
			_ = ctx.CancelOrder(g.tpOrderID)
			g.tpOrderID = ""
			g.placeRestingTP(ctx)
		}
		g.log.Info("guardian：仓位数量有变化，止损数量已同步调整",
			zap.String("symbol", g.symbol), zap.Float64("qty", absQty))
		g.notifyAction("resized", fmt.Sprintf("检测到仓位变动,止损数量已调整为 %.6g", absQty))
	}
	return false
}

// retire cancels the resting stop, marks the guardian closed (position gone —
// either our own protective close finally confirmed via position resync, or
// closed externally), and clears persisted state so a future, unrelated
// guardian reusing this engine_id starts clean (see clearState doc comment).
func (g *Guardian) retire(ctx *strategy.Context) {
	wasClosing := g.phase == PhaseClosing
	if g.stopOrderID != "" {
		_ = ctx.CancelOrder(g.stopOrderID)
		g.stopOrderID = ""
	}
	if g.tpOrderID != "" {
		_ = ctx.CancelOrder(g.tpOrderID)
		g.tpOrderID = ""
	}
	g.phase = PhaseClosed
	g.clearState()
	if wasClosing {
		g.log.Info("guardian：平仓已确认(通过持仓核对发现)，守护结束",
			zap.String("symbol", g.symbol))
		g.notifyAction("closed", "已按保护单平仓,守护结束")
		return
	}
	g.log.Info("guardian：检测到仓位已被外部平掉，退出守护",
		zap.String("symbol", g.symbol))
	g.notifyAction("retired", "检测到你已手动平掉这个仓位,已撤掉止损单、停止守护")
}

func (g *Guardian) updateAvgATR(atr float64) {
	if atr <= 0 {
		return
	}
	if g.avgATR == 0 {
		g.avgATR = atr
		return
	}
	g.avgATR += g.avgAlpha * (atr - g.avgATR)
}

// evalAlerts builds the snapshot, runs the rules, and dispatches what fired.
func (g *Guardian) evalAlerts(price float64) {
	if g.alerts == nil || g.alerts.Len() == 0 {
		return
	}
	r := g.prot.R
	stopDistR := 0.0
	if r > 0 {
		stopDistR = math.Abs(price-g.prot.Stop) / r
	}
	c := AlertCtx{
		Price:     price,
		PnlR:      g.prot.PnlR(price),
		StopDistR: stopDistR,
		ATR:       g.atr.Value(),
		AvgATR:    g.avgATR,
		BarsHeld:  g.barsHeld,
	}
	if g.sma != nil && g.sma.Ready() {
		c.MA, c.HasMA = g.sma.Value(), true
	}
	for _, a := range g.alerts.Evaluate(c) {
		if g.dispatch != nil {
			_ = g.dispatch.Send(a.Title, "["+g.symbol+"] "+a.Msg)
		}
	}
}

// OnFill marks the guardian closed once its protective order (resting stop,
// resting take-profit, or a tick-based close) has filled, and arms protection
// from an entry fill.
func (g *Guardian) OnFill(ctx *strategy.Context, fill strategy.Fill) {
	// A partial take-profit fill shrinks the position but does NOT end the guardian;
	// resyncPosition re-sizes the stop from the account qty on the next bar.
	if fill.Reason == "guardian_partial_tp" {
		return
	}
	// The entry fill (arm-with-entry) arms protection from the fill price/qty.
	if fill.Reason == "guardian_entry" && g.prot == nil {
		g.prot = NewProtection(g.entrySide, fill.Price, fill.Qty, g.cfg, g.atr.Value())
		g.phase = PhaseWatching
		g.log.Info("guardian：开仓成交，已开始保护",
			zap.String("symbol", g.symbol), zap.Float64("entry", fill.Price),
			zap.Float64("qty", fill.Qty), zap.Float64("stop", g.prot.Stop))
		return
	}
	// Gating on stopOrderID != "" alone (the old check) misfires when
	// enterOrAdopt arms protection (setting stopOrderID) via the position
	// syncer BEFORE the entry order's own fill notification arrives: that
	// late "guardian_entry" fill would then hit this branch and get
	// mistaken for the protective stop filling, retiring the guardian on a
	// position that was never closed (2026-08-21 incident — 3 of 4 same-day
	// retirements on a real-money account were this race, not real closes).
	// Explicitly excluding the two fill reasons that are never a close is
	// enough to close that race without requiring every close path to be
	// enumerated positively.
	notAClose := fill.Reason == "guardian_entry" || fill.Reason == "guardian_partial_tp"
	if g.phase == PhaseClosed || notAClose {
		return
	}
	if g.phase != PhaseClosing && g.stopOrderID == "" && g.tpOrderID == "" {
		return // not a close-related fill
	}
	// Stop and take-profit can both be resting on the exchange at once (an
	// OCO pair): whichever fills, cancel the sibling so it doesn't orphan
	// now the position is gone. The Reason (set on stopReq/tpReq, propagated
	// onto the live Fill from its OrderRequest) says precisely which one
	// this fill is, so we cancel only the untouched sibling. If Reason isn't
	// available (some fill paths, e.g. restart recovery, don't carry it —
	// and a tick-triggered market close via placeClose already cancelled
	// both before submitting) fall back to cancelling both defensively:
	// re-cancelling the one that already filled is a harmless no-op, but
	// skipping a genuine sibling would leave it orphaned on the exchange.
	switch fill.Reason {
	case "guardian_resting_stop":
		g.stopOrderID = ""
		if g.tpOrderID != "" {
			_ = ctx.CancelOrder(g.tpOrderID)
			g.tpOrderID = ""
		}
	case "guardian_resting_tp":
		g.tpOrderID = ""
		if g.stopOrderID != "" {
			_ = ctx.CancelOrder(g.stopOrderID)
			g.stopOrderID = ""
		}
	default:
		if g.stopOrderID != "" {
			_ = ctx.CancelOrder(g.stopOrderID)
			g.stopOrderID = ""
		}
		if g.tpOrderID != "" {
			_ = ctx.CancelOrder(g.tpOrderID)
			g.tpOrderID = ""
		}
	}
	g.phase = PhaseClosed
	// Without this, EVERY normal close-completion leaves the persisted
	// Closing:true parked forever — poisoning the next, unrelated guardian
	// that ever reuses this engine_id (deterministic, no session nonce).
	g.clearState()
	g.log.Info("guardian：保护性平仓单已成交",
		zap.String("symbol", g.symbol),
		zap.Float64("price", fill.Price))
	g.notifyAction("closed", fmt.Sprintf("已按保护单平仓 @ %s,守护结束", fmtPrice(fill.Price)))
}

// Status exposes live state for the operator dashboard.
func (g *Guardian) Status() map[string]any {
	if g.prot == nil {
		return map[string]any{"state": string(g.phase), "symbol": g.symbol}
	}
	tpPct := 0.0
	if g.cfg.TPMode == TPPct {
		tpPct = g.cfg.TPValue * 100
	}
	trailPct := 0.0 // 0 = trailing off; else the profit % at which it starts
	if g.cfg.TrailEnabled {
		trailPct = g.cfg.ActivateR * g.cfg.StopValue * 100
	}
	return map[string]any{
		"state":        string(g.phase),
		"symbol":       g.symbol,
		"side":         g.prot.Side,
		"entry":        g.prot.Entry,
		"qty":          g.prot.Qty,
		"stop":         g.prot.Stop,
		"tp":           g.prot.TPPrice(),
		"pnl_r":        g.prot.PnlR(g.lastPrice),
		"peak_r":       g.prot.PeakR,
		"trail_active": g.prot.Activated(),
		"bars_held":    g.barsHeld,
		// Current editable risk levels as plain percentages, for pre-filling the
		// live "修改参数" form so a save preserves the fields the user didn't change.
		"edit": map[string]any{
			"StopValue":        g.cfg.StopValue * 100,
			"BreakEvenPct":     g.cfg.BreakEvenAtR * g.cfg.StopValue * 100,
			"TrailActivatePct": trailPct,
			"PeakLockPct":      g.cfg.PeakProfitLockFrac * 100,
			"PartialTPPct":     g.cfg.PartialTPAtR * g.cfg.StopValue * 100,
			"TPValue":          tpPct,
		},
	}
}

// Prot exposes the underlying protection (for adoption/persistence wiring).
func (g *Guardian) Prot() *Protection { return g.prot }

// inactive reports whether the guardian should skip its normal per-bar/tick
// watching work — still closing (waiting for its own close to resolve) or
// already fully done.
func (g *Guardian) inactive() bool { return g.phase == PhaseClosing || g.phase == PhaseClosed }

// Retired implements strategy.Retired: once the guarded position is gone for
// good (closed by the guardian's own protective order, or externally and
// detected via resyncPosition→retire), the engine hosting this guardian
// should stop itself rather than keep polling/subscribing forever and being
// resurrected on the next server restart. Never true for a ModeEntry guardian
// still waiting to open — PhaseClosed is only ever reached once a position
// that WAS being protected is confirmed gone.
func (g *Guardian) Retired() bool { return g.phase == PhaseClosed }

func (g *Guardian) stopReason() string {
	if g.prot.Activated() {
		return "trailing"
	}
	return "stop_loss"
}

func (g *Guardian) checkExitBar(ctx *strategy.Context, bar exchange.Kline) bool {
	if g.prot.Side == SideLong {
		if !g.restingTPMode && g.prot.TPHit(bar.High) {
			g.placeClose(ctx, "take_profit")
			return true
		}
		if !g.restingMode && g.prot.StopHit(bar.Low) {
			g.placeClose(ctx, g.stopReason())
			return true
		}
	} else {
		if !g.restingTPMode && g.prot.TPHit(bar.Low) {
			g.placeClose(ctx, "take_profit")
			return true
		}
		if !g.restingMode && g.prot.StopHit(bar.High) {
			g.placeClose(ctx, g.stopReason())
			return true
		}
	}
	return false
}

func (g *Guardian) placeClose(ctx *strategy.Context, reason string) {
	if g.phase == PhaseClosing || g.phase == PhaseClosed {
		return
	}
	if !g.closeBackoff.ready() {
		return
	}
	// Cancel any resting protective orders first so they don't orphan on the
	// exchange once this market close fills.
	if g.stopOrderID != "" {
		_ = ctx.CancelOrder(g.stopOrderID)
		g.stopOrderID = ""
	}
	if g.tpOrderID != "" {
		_ = ctx.CancelOrder(g.tpOrderID)
		g.tpOrderID = ""
	}
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.CloseLong(g.symbol, g.prot.Qty)
	} else {
		req = strategy.CloseShort(g.symbol, g.prot.Qty)
	}
	req.Reason = "guardian_" + reason
	verb := "止损"
	if reason == "take_profit" {
		verb = "止盈"
	}
	id := ctx.PlaceOrder(req)
	if id == "" {
		// Rejected — same shape as placeEntry's fix (PlaceOrder submits
		// synchronously up through the exchange call, so an empty ID means
		// the rejection is known right here). We already cancelled the
		// resting stop above, so the position is genuinely naked at this
		// instant: try to restore protection immediately instead of leaving
		// phase stuck at Watching with no stop and no retry (2026-08-21
		// audit finding — same failure class as the entry-rejection bug,
		// found before it bit anyone live). Cooldown keeps a persistent
		// rejection (rate limit, margin) from resubmitting every tick.
		notify := g.closeBackoff.record(closeRetryBase, closeRetryMax, closeNotifyEvery)
		if g.wantRestingStop {
			g.placeRestingStop(ctx) // best-effort re-arm; tick fallback covers failure
		}
		if g.wantRestingTP && g.prot.TPPrice() > 0 {
			g.placeRestingTP(ctx) // best-effort re-arm; tick fallback covers failure
		}
		g.log.Error("guardian：平仓单被拒绝，已尝试恢复保护",
			zap.String("symbol", g.symbol), zap.String("reason", reason), zap.Int("reject_count", g.closeBackoff.count))
		if notify {
			g.notifyAction("close_failed", fmt.Sprintf("⚠️ %s单被拒绝,已尝试恢复保护,请立即检查仓位是否安全!会自动重试,持续失败只会间隔性提醒", verb))
		}
		return
	}
	g.closeBackoff.reset()
	g.phase = PhaseClosing
	// Persist Closing immediately — without this, a restart before the next
	// trail-save would forget we're mid-close and re-arm from scratch as if
	// still Watching (this write also picks up the now-cleared stopOrderID,
	// closing a second, pre-existing gap: a restart used to be able to restore
	// a stale stopOrderID pointing at an order we'd just cancelled above).
	g.saveState()
	g.notifyAction("closing", fmt.Sprintf("已触发%s,正在平仓(约 %s)", verb, fmtPrice(g.lastPrice)))
	g.log.Info("guardian：保护性平仓单已提交",
		zap.String("symbol", g.symbol),
		zap.String("side", g.prot.Side),
		zap.String("reason", reason),
		zap.Float64("stop", g.prot.Stop),
		zap.Float64("price", g.lastPrice))
}
