package guardian

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/position"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
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

// Guardian is a protective strategy: it never opens (the user does) and never
// re-enters. It watches one position, ratchets a trailing stop, closes on stop
// or take-profit, and exposes live status. It implements strategy.Strategy,
// strategy.TickReceiver and strategy.StatusReporter.
type Guardian struct {
	symbol string
	prot   *Protection
	cfg    ProtectionConfig // used to arm when adopting
	adopt  bool             // arm from the live account position on first bar
	atr    *ATR
	log    *zap.Logger

	prevClose   float64
	lastPrice   float64
	barsHeld    int
	closePlaced bool // a protective close has been submitted (prevents dupes)
	done        bool // the protective close has filled

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

	// restart recovery (optional)
	store        StateStore
	stateKey     string
	restoreTried bool

	armNotified bool // arm-summary (risk preview) sent once
	partialDone bool // partial take-profit already fired

	// arm-with-entry (optional): place the entry, then arm from its fill.
	wantEntry   bool
	entrySide   string
	entryQty    float64
	entryType   string  // "market" (default) or "limit"
	entryPrice  float64 // limit price when entryType == "limit"
	entryPlaced bool
}

// NewGuardian builds a Guardian for an already-armed Protection.
func NewGuardian(symbol string, prot *Protection, atrWindow int, log *zap.Logger) *Guardian {
	if log == nil {
		log = zap.NewNop()
	}
	// AvgATR baseline = EMA of ATR over ~4× the ATR window (for vol-spike detection).
	alpha := 2.0 / (float64(4*atrWindow) + 1)
	return &Guardian{symbol: symbol, prot: prot, atr: NewATR(atrWindow), log: log, avgAlpha: alpha}
}

// NewAdoptGuardian builds a Guardian that arms itself from the live account
// position on the first bar (for "I already opened on the exchange, guard it").
func NewAdoptGuardian(symbol string, cfg ProtectionConfig, atrWindow int, log *zap.Logger) *Guardian {
	g := NewGuardian(symbol, nil, atrWindow, log)
	g.cfg = cfg
	g.adopt = true
	return g
}

// NewEntryGuardian builds a Guardian that first PLACES the entry order, then arms
// protection from its fill — open and protect in one action.
func NewEntryGuardian(symbol, side string, qty float64, cfg ProtectionConfig, atrWindow int, log *zap.Logger) *Guardian {
	g := NewGuardian(symbol, nil, atrWindow, log)
	g.cfg = cfg
	g.wantEntry = true
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
	tp := "不设"
	if g.prot.TPPrice() > 0 {
		tp = fmtPrice(g.prot.TPPrice())
	}
	g.log.Info("guardian: params updated live",
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
		if isWarmup {
			return false // don't open during warmup replay — the order is suppressed
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
	g.log.Info("guardian: adopted existing position instead of re-entering",
		zap.String("symbol", g.symbol), zap.String("side", side),
		zap.Float64("entry", pos.EntryPrice), zap.Float64("qty", pos.Qty),
		zap.Float64("stop", g.prot.Stop))
	g.notifyAction("adopt", fmt.Sprintf("检测到账户已有%s仓,直接接管保护(未重复开仓)", sideCN))
	return true
}

// placeEntry submits the market entry order once (armed later via OnFill).
func (g *Guardian) placeEntry(ctx *strategy.Context) {
	g.entryPlaced = true
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
	ctx.PlaceOrder(req)
	g.notifyAction("entry", fmt.Sprintf("已按你的指令%s开%s单 %.6g,成交后自动挂上保护", kind, dir, g.entryQty))
}

// tryArm initialises Protection from the live account position; returns whether armed.
func (g *Guardian) tryArm(ctx *strategy.Context, atr float64) bool {
	if !g.adopt || ctx.Portfolio == nil {
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
	g.log.Info("guardian: adopted position",
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

// SetStateStore wires persistence so the trailed stop survives a restart. key is
// the engine id; the live engine supplies a DB-backed store.
func (g *Guardian) SetStateStore(store StateStore, key string) {
	g.store = store
	g.stateKey = key
}

// maybeRestore loads persisted trail state once, overriding the freshly-armed
// protection so a restart resumes the advanced stop instead of resetting it.
func (g *Guardian) maybeRestore() {
	if g.restoreTried || g.store == nil || g.stateKey == "" || g.prot == nil {
		return
	}
	g.restoreTried = true
	st, ok := g.store.Load(g.stateKey)
	if !ok {
		return
	}
	g.prot.Stop = st.Stop
	g.prot.PeakR = st.PeakR
	g.prot.SetActivated(st.Activated)
	g.stopOrderID = st.StopOrderID
	g.restingStopPrice = st.RestingStopPrice
	if st.StopOrderID != "" {
		// The resting stop still lives on the exchange; don't place a duplicate.
		g.restingMode = true
		g.restingTried = true
	}
	g.log.Info("guardian: restored trail state after restart",
		zap.String("symbol", g.symbol), zap.Float64("stop", st.Stop))
}

// saveState persists the current trail state (best-effort).
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
	})
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
	g.partialDone = true
	qty := g.prot.PartialTPQty()
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.CloseLong(g.symbol, qty)
	} else {
		req = strategy.CloseShort(g.symbol, qty)
	}
	req.Reason = "guardian_partial_tp"
	ctx.PlaceOrder(req)
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
	g.log.Info("guardian: resting stop placed",
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

// Name identifies the strategy type.
func (g *Guardian) Name() string { return "guardian" }

// OnBar updates ATR + trailing on each closed candle and enforces the stop/TP
// against the bar's intrabar extremes (the stop that was in force during the bar).
func (g *Guardian) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if g.inactive() {
		return
	}
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
		if g.wantEntry { // open first (or adopt if already open), arm from there
			if !g.entryPlaced {
				if !g.enterOrAdopt(ctx, atr, bar.Warmup) {
					return
				}
			} else {
				return // entry placed, waiting for its fill to arm
			}
		} else if !g.tryArm(ctx, atr) {
			return
		}
	}
	// During the startup backfill replay, only prime indicators / adopt — never run
	// the trail/exit logic on historical prices (it would false-trigger a stop and
	// submit a suppressed close, leaving the guardian stuck "closing"). Live ticks
	// (OnTick) place the resting stop and enforce protection right after warmup.
	if bar.Warmup {
		return
	}
	g.maybeRestore()
	if g.resyncPosition(ctx) { // position closed externally → retired
		return
	}
	g.notifyArmSummary()
	g.ensureRestingStop(ctx)
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
	if g.inactive() {
		return
	}
	g.lastPrice = price
	// arm-with-entry: open as soon as the engine is live (real-time), rather than
	// waiting for the next closed bar. Skip if a position already exists — OnBar
	// adopts that case. This is what makes "顺便帮我开仓" open promptly.
	if g.prot == nil {
		if g.wantEntry && !g.entryPlaced && engineLive(ctx) && g.livePosition(ctx) == nil {
			g.placeEntry(ctx)
		}
		return
	}
	g.maybeRestore()
	g.notifyArmSummary()
	g.ensureRestingStop(ctx)
	g.lastPrice = price
	g.evalAlerts(price)
	g.maybePartialTP(ctx, price)
	if g.prot.TPHit(price) {
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

// resyncPosition reconciles against the live account position each bar. If the
// position vanished (user closed / liquidated) it retires; if the size changed it
// updates the protected qty and re-sizes the resting stop. Returns true if the
// guardian retired this cycle.
func (g *Guardian) resyncPosition(ctx *strategy.Context) bool {
	if g.prot == nil || g.inactive() {
		return false
	}
	// Determine the live position size. Prefer the syncer — it tracks hedge LONG/
	// SHORT legs, whereas ctx.Portfolio.Position only exposes the one-way slot and
	// would report a hedge position as gone (falsely retiring the guardian). Fall
	// back to the portfolio only when no syncer is present (backtest/paper).
	var qty float64
	var known bool
	if pos := g.livePosition(ctx); pos != nil {
		qty, known = pos.Qty, true
	} else if _, ok := ctx.Extra["position_syncer"]; !ok && ctx.Portfolio != nil {
		if q, _, pok := ctx.Portfolio.Position(g.symbol); pok {
			qty, known = math.Abs(q), true
		}
	}
	if !known || qty == 0 {
		g.retire(ctx)
		return true
	}
	if absQty := qty; absQty != g.prot.Qty {
		g.prot.Qty = absQty
		if g.restingMode && g.stopOrderID != "" {
			_ = ctx.CancelOrder(g.stopOrderID)
			g.stopOrderID = ""
			g.placeRestingStop(ctx)
		}
		g.log.Info("guardian: position resized, stop re-sized",
			zap.String("symbol", g.symbol), zap.Float64("qty", absQty))
		g.notifyAction("resized", fmt.Sprintf("检测到仓位变动,止损数量已调整为 %.6g", absQty))
	}
	return false
}

// retire cancels the resting stop and marks the guardian done (position gone).
func (g *Guardian) retire(ctx *strategy.Context) {
	if g.stopOrderID != "" {
		_ = ctx.CancelOrder(g.stopOrderID)
		g.stopOrderID = ""
	}
	g.done = true
	g.saveState()
	g.log.Info("guardian: position closed externally, retiring",
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

// OnFill marks the guardian done once its protective order (resting stop or a
// tick-based close) has filled.
func (g *Guardian) OnFill(_ *strategy.Context, fill strategy.Fill) {
	// A partial take-profit fill shrinks the position but does NOT end the guardian;
	// resyncPosition re-sizes the stop from the account qty on the next bar.
	if fill.Reason == "guardian_partial_tp" {
		return
	}
	// The entry fill (arm-with-entry) arms protection from the fill price/qty.
	if fill.Reason == "guardian_entry" && g.prot == nil {
		g.prot = NewProtection(g.entrySide, fill.Price, fill.Qty, g.cfg, g.atr.Value())
		g.log.Info("guardian: armed from entry fill",
			zap.String("symbol", g.symbol), zap.Float64("entry", fill.Price),
			zap.Float64("qty", fill.Qty), zap.Float64("stop", g.prot.Stop))
		return
	}
	if !g.done && (g.closePlaced || g.stopOrderID != "") {
		g.done = true
		g.stopOrderID = ""
		g.log.Info("guardian: protective exit filled",
			zap.String("symbol", g.symbol),
			zap.Float64("price", fill.Price))
		g.notifyAction("closed", fmt.Sprintf("已按保护单平仓 @ %s,守护结束", fmtPrice(fill.Price)))
	}
}

// Status exposes live state for the operator dashboard.
func (g *Guardian) Status() map[string]any {
	if g.prot == nil {
		return map[string]any{"state": "arming", "symbol": g.symbol}
	}
	state := "watching"
	if g.done {
		state = "closed"
	} else if g.closePlaced {
		state = "closing"
	}
	tpPct := 0.0
	if g.cfg.TPMode == TPPct {
		tpPct = g.cfg.TPValue * 100
	}
	return map[string]any{
		"state":        state,
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
			"StopValue":    g.cfg.StopValue * 100,
			"BreakEvenPct": g.cfg.BreakEvenAtR * g.cfg.StopValue * 100,
			"PartialTPPct": g.cfg.PartialTPAtR * g.cfg.StopValue * 100,
			"TPValue":      tpPct,
		},
	}
}

// Prot exposes the underlying protection (for adoption/persistence wiring).
func (g *Guardian) Prot() *Protection { return g.prot }

func (g *Guardian) inactive() bool { return g.done || g.closePlaced }

func (g *Guardian) stopReason() string {
	if g.prot.Activated() {
		return "trailing"
	}
	return "stop_loss"
}

func (g *Guardian) checkExitBar(ctx *strategy.Context, bar exchange.Kline) bool {
	if g.prot.Side == SideLong {
		if g.prot.TPHit(bar.High) {
			g.placeClose(ctx, "take_profit")
			return true
		}
		if !g.restingMode && g.prot.StopHit(bar.Low) {
			g.placeClose(ctx, g.stopReason())
			return true
		}
	} else {
		if g.prot.TPHit(bar.Low) {
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
	if g.closePlaced {
		return
	}
	// Cancel any resting stop first so it doesn't orphan on the exchange.
	if g.stopOrderID != "" {
		_ = ctx.CancelOrder(g.stopOrderID)
		g.stopOrderID = ""
	}
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.CloseLong(g.symbol, g.prot.Qty)
	} else {
		req = strategy.CloseShort(g.symbol, g.prot.Qty)
	}
	req.Reason = "guardian_" + reason
	ctx.PlaceOrder(req)
	g.closePlaced = true
	verb := "止损"
	if reason == "take_profit" {
		verb = "止盈"
	}
	g.notifyAction("closing", fmt.Sprintf("已触发%s,正在平仓(约 %s)", verb, fmtPrice(g.lastPrice)))
	g.log.Info("guardian: protective close submitted",
		zap.String("symbol", g.symbol),
		zap.String("side", g.prot.Side),
		zap.String("reason", reason),
		zap.Float64("stop", g.prot.Stop),
		zap.Float64("price", g.lastPrice))
}
