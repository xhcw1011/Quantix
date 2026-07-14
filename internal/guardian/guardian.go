package guardian

import (
	"math"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"go.uber.org/zap"
)

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
		if !g.tryArm(ctx, atr) {
			return
		}
	}
	g.barsHeld++
	g.evalAlerts(bar.Close)

	// Exit check first, using the stop as it stood during the bar.
	if g.checkExitBar(ctx, bar) {
		return
	}
	// Then trail for the next bar.
	g.prot.UpdateStop(bar.Close, atr)
}

// OnTick enforces the stop/TP precisely between bars and trails on favourable ticks.
func (g *Guardian) OnTick(ctx *strategy.Context, price float64) {
	if g.inactive() || g.prot == nil {
		return
	}
	g.lastPrice = price
	g.evalAlerts(price)
	if g.prot.TPHit(price) {
		g.placeClose(ctx, "take_profit")
		return
	}
	if g.prot.StopHit(price) {
		g.placeClose(ctx, g.stopReason())
		return
	}
	g.prot.UpdateStop(price, g.atr.Value())
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

// OnFill marks the guardian done once its protective close has filled.
func (g *Guardian) OnFill(_ *strategy.Context, fill strategy.Fill) {
	if g.closePlaced && !g.done {
		g.done = true
		g.log.Info("guardian: protective exit filled",
			zap.String("symbol", g.symbol),
			zap.Float64("price", fill.Price))
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
		if g.prot.StopHit(bar.Low) {
			g.placeClose(ctx, g.stopReason())
			return true
		}
	} else {
		if g.prot.TPHit(bar.Low) {
			g.placeClose(ctx, "take_profit")
			return true
		}
		if g.prot.StopHit(bar.High) {
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
	var req strategy.OrderRequest
	if g.prot.Side == SideLong {
		req = strategy.CloseLong(g.symbol, g.prot.Qty)
	} else {
		req = strategy.CloseShort(g.symbol, g.prot.Qty)
	}
	req.Reason = "guardian_" + reason
	ctx.PlaceOrder(req)
	g.closePlaced = true
	g.log.Info("guardian: protective close submitted",
		zap.String("symbol", g.symbol),
		zap.String("side", g.prot.Side),
		zap.String("reason", reason),
		zap.Float64("stop", g.prot.Stop),
		zap.Float64("price", g.lastPrice))
}
