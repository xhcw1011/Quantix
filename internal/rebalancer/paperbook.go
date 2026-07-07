package rebalancer

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Quantix/quantix/internal/strategy"
)

// PaperBook is a multi-symbol paper broker for the cross-sectional rebalancer. Unlike
// the single-symbol paper.Broker (one lastPrice), it holds a per-symbol price + signed
// position and fills market orders instantly at the set price, accruing per-side fees.
// It implements strategy.Broker so it can sit behind the ORG gateway. Paper-forward
// uses it to measure realized turnover cost against the ≤20bp budget.
type PaperBook struct {
	mu      sync.Mutex
	prices  map[string]float64
	pos     map[string]float64 // signed qty
	feeRate float64
	cost    float64
	seq     int
}

// NewPaperBook creates a paper book charging feeRate (fraction) per side on notional.
func NewPaperBook(feeRate float64) *PaperBook {
	return &PaperBook{prices: map[string]float64{}, pos: map[string]float64{}, feeRate: feeRate}
}

// SetPrice sets the current fill price for a symbol.
func (b *PaperBook) SetPrice(symbol string, price float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prices[symbol] = price
}

// PlaceOrder fills a market order instantly at the symbol's set price, updates the
// signed position, and accrues the fee. Returns an internal order id.
func (b *PaperBook) PlaceOrder(req strategy.OrderRequest) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	px := b.prices[req.Symbol]
	if px <= 0 || req.Qty <= 0 {
		return ""
	}
	signed := req.Qty
	if req.Side == strategy.SideSell {
		signed = -req.Qty
	}
	b.pos[req.Symbol] += signed
	b.cost += req.Qty * px * b.feeRate
	b.seq++
	return fmt.Sprintf("paper-%d", b.seq)
}

// CancelOrder is a no-op — paper market orders fill instantly.
func (b *PaperBook) CancelOrder(string) error { return nil }

// Positions returns the current non-flat book (signed qty + last price), sorted.
func (b *PaperBook) Positions() []Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	syms := make([]string, 0, len(b.pos))
	for s, q := range b.pos {
		if q != 0 {
			syms = append(syms, s)
		}
	}
	sort.Strings(syms)
	out := make([]Position, 0, len(syms))
	for _, s := range syms {
		out = append(out, Position{Symbol: s, SignedQty: b.pos[s], Price: b.prices[s]})
	}
	return out
}

// RealizedCost is the total fees accrued so far (paper-forward's cost meter).
func (b *PaperBook) RealizedCost() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cost
}
