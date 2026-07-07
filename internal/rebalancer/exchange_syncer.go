package rebalancer

import (
	"context"
	"math"
	"sort"

	"github.com/Quantix/quantix/internal/exchange"
)

// Syncer reports current open positions — the diff base for a rotation. Implemented by
// the exchange (real account truth) and, in tests/paper, by the PaperBook.
type Syncer interface {
	Positions(ctx context.Context) ([]Position, error)
}

// ExchangeSyncer reads live positions from a futures/swap account via GetMarginRatios
// and values them at prices from priceFn (the rebalance-date close), so current
// notional is measured on the same prices as the targets.
type ExchangeSyncer struct {
	querier exchange.MarginQuerier
	priceFn func(symbol string) float64
}

// NewExchangeSyncer wraps a MarginQuerier; priceFn supplies the valuation price per symbol.
func NewExchangeSyncer(querier exchange.MarginQuerier, priceFn func(symbol string) float64) *ExchangeSyncer {
	return &ExchangeSyncer{querier: querier, priceFn: priceFn}
}

// Positions returns the non-flat book as signed-qty positions. Hedge-mode legs carry
// their direction in PositionSide (LONG/SHORT); one-way accounts carry a signed Size.
func (s *ExchangeSyncer) Positions(ctx context.Context) ([]Position, error) {
	ratios, err := s.querier.GetMarginRatios(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Position, 0, len(ratios))
	for _, r := range ratios {
		if r.Size == 0 {
			continue
		}
		qty := r.Size
		switch r.PositionSide {
		case "LONG":
			qty = math.Abs(r.Size)
		case "SHORT":
			qty = -math.Abs(r.Size)
		}
		out = append(out, Position{Symbol: r.Symbol, SignedQty: qty, Price: s.priceFn(r.Symbol)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}
