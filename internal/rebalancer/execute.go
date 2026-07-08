package rebalancer

import "github.com/Quantix/quantix/internal/strategy"

// ExecuteRotationSink plans the rotation against `current` (signed notional, from a live
// position syncer) and places the delta trades through sink. Unlike ExecuteRotation it
// holds no PaperBook: trades are priced from priceFn (so a close for a symbol that left
// the eligible set still executes), and lot-step rounding is left to the sink/broker.
// Returns the Plan.
func ExecuteRotationSink(series map[string]Series, dates []string, asOf string, cfg Config, priceFn func(symbol string) float64, current map[string]float64, sink strategy.Broker) Plan {
	plan := PlanRotation(series, dates, asOf, current, cfg, nil)
	prices := map[string]float64{}
	for _, o := range plan.Orders {
		prices[o.Symbol] = priceFn(o.Symbol)
	}
	plan.Trades = OrdersToTrades(plan.Orders, prices, nil)
	for _, tr := range plan.Trades {
		side := strategy.SideBuy
		if tr.Side == "SELL" {
			side = strategy.SideSell
		}
		sink.PlaceOrder(strategy.OrderRequest{Symbol: tr.Symbol, Side: side, Type: strategy.OrderMarket, Qty: tr.Qty})
	}
	return plan
}

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
	// Re-price trades off the BOOK (which retains a last price for every held symbol),
	// so a close for a symbol that dropped out of the current universe — and thus has
	// no asOf price in the eligible set — still executes instead of being dropped.
	prices := map[string]float64{}
	for _, o := range plan.Orders {
		prices[o.Symbol] = book.Price(o.Symbol)
	}
	plan.Trades = OrdersToTrades(plan.Orders, prices, steps)
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
