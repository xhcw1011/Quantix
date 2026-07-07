package rebalancer

import "math"

// Level is one order-book price level.
type Level struct {
	Price float64
	Qty   float64
}

// CrossCostBp estimates the execution cost, in basis points relative to mid, of
// crossing `notional` dollars into `levels` (the side you trade into: asks for a buy,
// bids for a sell), sorted best-first. It walks the book accumulating fills; if the
// book is too thin, the remainder fills at the worst available price (conservative).
// The result is the |avg-fill − mid| / mid slippage (half-spread + market impact) — the
// per-side cost the ≤20bp paper-forward budget is measured against.
func CrossCostBp(levels []Level, mid, notional float64) float64 {
	if mid <= 0 || notional <= 0 || len(levels) == 0 {
		return 0
	}
	remaining := notional
	var cost, qty float64
	for _, l := range levels {
		if l.Price <= 0 || l.Qty <= 0 {
			continue
		}
		avail := l.Qty * l.Price
		take := math.Min(remaining, avail)
		q := take / l.Price
		cost += q * l.Price
		qty += q
		remaining -= take
		if remaining <= 0 {
			break
		}
	}
	if remaining > 0 { // book exhausted → fill the rest at the worst level price
		worst := levels[len(levels)-1].Price
		if worst > 0 {
			q := remaining / worst
			cost += q * worst
			qty += q
		}
	}
	if qty <= 0 {
		return 0
	}
	avg := cost / qty
	return math.Abs(avg-mid) / mid * 1e4
}
