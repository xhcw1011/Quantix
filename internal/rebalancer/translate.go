package rebalancer

import (
	"math"

	"github.com/Quantix/quantix/internal/xsfunding"
)

// Position is current exchange truth for one symbol (signed qty: + long, − short).
type Position struct {
	Symbol    string
	SignedQty float64
	Price     float64
}

// PositionsToNotional converts positions to the signed-notional map the rebalancer
// diffs against (flat positions omitted).
func PositionsToNotional(pos []Position) map[string]float64 {
	out := make(map[string]float64, len(pos))
	for _, p := range pos {
		if p.SignedQty == 0 {
			continue
		}
		out[p.Symbol] = p.SignedQty * p.Price
	}
	return out
}

// Trade is a concrete placeable order derived from a signed-notional delta.
type Trade struct {
	Symbol string
	Side   string // "BUY" | "SELL"
	Qty    float64
}

// OrdersToTrades converts signed-notional orders into side + step-rounded qty, using
// current prices and each symbol's lot step. Orders that round to zero qty are dropped.
func OrdersToTrades(orders []xsfunding.Order, prices, steps map[string]float64) []Trade {
	var out []Trade
	for _, o := range orders {
		px := prices[o.Symbol]
		if px <= 0 {
			continue
		}
		qty := math.Abs(o.Notional) / px
		if step := steps[o.Symbol]; step > 0 {
			qty = math.Floor(qty/step) * step
		}
		if qty <= 0 {
			continue
		}
		side := "BUY"
		if o.Notional < 0 {
			side = "SELL"
		}
		out = append(out, Trade{Symbol: o.Symbol, Side: side, Qty: qty})
	}
	return out
}
