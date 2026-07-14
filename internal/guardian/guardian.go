package guardian

import (
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
	atr    *ATR
	log    *zap.Logger

	prevClose   float64
	lastPrice   float64
	barsHeld    int
	closePlaced bool // a protective close has been submitted (prevents dupes)
	done        bool // the protective close has filled
}

// NewGuardian builds a Guardian for an already-armed Protection.
func NewGuardian(symbol string, prot *Protection, atrWindow int, log *zap.Logger) *Guardian {
	if log == nil {
		log = zap.NewNop()
	}
	return &Guardian{symbol: symbol, prot: prot, atr: NewATR(atrWindow), log: log}
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
	g.prevClose = bar.Close
	g.lastPrice = bar.Close
	g.barsHeld++

	// Exit check first, using the stop as it stood during the bar.
	if g.checkExitBar(ctx, bar) {
		return
	}
	// Then trail for the next bar.
	g.prot.UpdateStop(bar.Close, atr)
}

// OnTick enforces the stop/TP precisely between bars and trails on favourable ticks.
func (g *Guardian) OnTick(ctx *strategy.Context, price float64) {
	if g.inactive() {
		return
	}
	g.lastPrice = price
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
