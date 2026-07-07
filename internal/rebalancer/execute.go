package rebalancer

import "github.com/Quantix/quantix/internal/strategy"

// ExecuteRotation runs one rebalance end-to-end: push the asOf prices into the book,
// read current positions as the diff base, plan the rotation, and place the delta
// trades through sink. `book` is the price/position source (a *PaperBook live, an
// exchange adapter later); `sink` is the order path — the ORG gateway in production
// (which forwards to the same book), or the book itself in tests. Returns the Plan.
func ExecuteRotation(series map[string]Series, dates []string, asOf string, cfg Config, steps map[string]float64, book *PaperBook, sink strategy.Broker) Plan {
	for s, sr := range series {
		if px, ok := sr.Price[asOf]; ok && px > 0 {
			book.SetPrice(s, px)
		}
	}
	current := PositionsToNotional(book.Positions())
	plan := PlanRotation(series, dates, asOf, current, cfg, steps)
	for _, tr := range plan.Trades {
		side := strategy.SideBuy
		if tr.Side == "SELL" {
			side = strategy.SideSell
		}
		sink.PlaceOrder(strategy.OrderRequest{
			Symbol: tr.Symbol,
			Side:   side,
			Type:   strategy.OrderMarket,
			Qty:    tr.Qty,
		})
	}
	return plan
}
