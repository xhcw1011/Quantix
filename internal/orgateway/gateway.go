// Package orgateway implements the Order Risk Gateway (ORG): the single risk
// entry point every strategy order must pass through before reaching the broker.
//
//	Strategy → ctx.PlaceOrder → [ORG] → realBroker → Exchange
//
// ORG is a decorator around strategy.Broker, so it is engine-agnostic and the
// broker never needs to know which strategy produced an order. V1 implements
// Layer 1 (Order Validation) only: hard ALLOW/DENY checks with a Risk Reason.
// Layers 2-4 (risk events / account risk / position sizing) come later.
//
// It runs in one of two modes:
//   - Shadow  — evaluate, log the decision, record stats, but ALWAYS forward.
//   - Enforce — additionally block (drop) orders a rule DENYs.
//
// A gateway only ever blocks orders that OPEN or INCREASE risk; orders that
// reduce/close a position always pass, and cancels always pass.
package orgateway

import (
	"sync"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// Mode controls whether the gateway enforces its decisions.
type Mode int

const (
	// Shadow evaluates and records but never blocks (for pre-enforcement rollout).
	Shadow Mode = iota
	// Enforce blocks orders a rule DENYs.
	Enforce
)

func (m Mode) String() string {
	if m == Enforce {
		return "enforce"
	}
	return "shadow"
}

// Decision is a rule's verdict on an order. Reason is "" when allowed.
type Decision struct {
	Allow  bool
	Reason string // a stable reason code, e.g. ReasonMaxPositionPct
	Detail string // human-readable detail for logs/audit
}

// allow is the canonical "no objection" decision.
var allow = Decision{Allow: true}

// OrderState is the account/market snapshot a rule needs to judge one order.
type OrderState struct {
	Equity        float64 // account equity
	PositionValue float64 // current notional of the position in the order's symbol (abs)
	GrossNotional float64 // total gross notional across all open positions (abs)
	Price         float64 // current price for the order's symbol
	Leverage      float64 // account max leverage (for the gross-leverage rule)
}

// StateProvider supplies the account/market state at decision time. Each engine
// (live/paper/backtest) implements it from its own view of positions and prices.
type StateProvider interface {
	Snapshot(req strategy.OrderRequest) OrderState
}

// Rule evaluates one order against the state and returns a Decision.
type Rule interface {
	Name() string
	Eval(req strategy.OrderRequest, st OrderState) Decision
}

// Gateway is the Order Risk Gateway. It implements strategy.Broker so it can wrap
// any engine's broker transparently.
type Gateway struct {
	inner strategy.Broker
	rules []Rule
	state StateProvider
	mode  Mode
	log   *zap.Logger

	mu    sync.Mutex
	stats map[string]int // "ALLOW" and each reason code → count
}

// New builds a gateway wrapping inner, evaluating rules in order against state.
func New(inner strategy.Broker, rules []Rule, state StateProvider, mode Mode, log *zap.Logger) *Gateway {
	if log == nil {
		log = zap.NewNop()
	}
	return &Gateway{inner: inner, rules: rules, state: state, mode: mode, log: log, stats: map[string]int{}}
}

// PlaceOrder evaluates the order, records/logs the decision, then forwards it
// unless the gateway is enforcing and a rule denied it.
func (g *Gateway) PlaceOrder(req strategy.OrderRequest) string {
	d := g.evaluate(req)
	g.record(d)
	g.logDecision(req, d)

	if g.mode == Shadow || d.Allow {
		return g.inner.PlaceOrder(req)
	}
	return "" // enforced DENY: no order reaches the broker
}

// CancelOrder always passes through — the gateway never blocks risk-reducing actions.
func (g *Gateway) CancelOrder(orderID string) error {
	return g.inner.CancelOrder(orderID)
}

// evaluate runs rules in order; the first DENY wins.
func (g *Gateway) evaluate(req strategy.OrderRequest) Decision {
	st := g.state.Snapshot(req)
	for _, r := range g.rules {
		if d := r.Eval(req, st); !d.Allow {
			return d
		}
	}
	return allow
}

func (g *Gateway) record(d Decision) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d.Allow {
		g.stats["ALLOW"]++
	} else {
		g.stats[d.Reason]++
	}
}

func (g *Gateway) logDecision(req strategy.OrderRequest, d Decision) {
	verdict := "ALLOW"
	if !d.Allow {
		verdict = "DENY"
	}
	g.log.Info("ORG decision",
		zap.String("mode", g.mode.String()),
		zap.String("verdict", verdict),
		zap.String("reason", d.Reason),
		zap.String("detail", d.Detail),
		zap.String("symbol", req.Symbol),
		zap.String("side", string(req.Side)),
		zap.String("positionSide", string(req.PositionSide)),
		zap.Float64("qty", req.Qty),
	)
}

// Stats returns a copy of the ALLOW/DENY-by-reason counters (for the shadow-mode
// weekly review: how many orders, how many denied, and why).
func (g *Gateway) Stats() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.stats))
	for k, v := range g.stats {
		out[k] = v
	}
	return out
}

// opensOrIncreases reports whether an order opens or increases risk. ORG only
// evaluates these; risk-reducing (closing) orders always pass.
//
//	BUY  Long/Net  → open/increase long   (BUY Short = closing a short → skip)
//	SELL Short     → open short           (SELL Long/Net = closing a long → skip)
func opensOrIncreases(req strategy.OrderRequest) bool {
	if req.Side == strategy.SideBuy {
		return req.PositionSide != strategy.PositionSideShort
	}
	return req.PositionSide == strategy.PositionSideShort
}

// orderCost estimates the notional this order adds. Qty==0 is the strategy
// "all-in" signal (the live broker resolves it to ~all cash), so ORG treats it
// as a full-size order (≈ equity) rather than a free pass.
func orderCost(req strategy.OrderRequest, st OrderState) float64 {
	if req.Qty == 0 {
		return st.Equity
	}
	return req.Qty * st.Price
}
