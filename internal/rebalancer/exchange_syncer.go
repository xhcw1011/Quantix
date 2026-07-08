package rebalancer

import (
	"context"
	"sort"

	"github.com/Quantix/quantix/internal/exchange"
)

// Syncer reports current open positions — the diff base for a rotation. Implemented by
// the exchange (real account truth) and, in tests/paper, by the PaperBook.
type Syncer interface {
	Positions(ctx context.Context) ([]Position, error)
}

// ExchangeSyncer reads live positions from a futures/swap account via GetPositions
// (SIGNED amounts, so short/long survives in one-way mode) and values them at prices
// from priceFn (the rebalance-date close), so current notional is measured on the same
// prices as the targets.
type ExchangeSyncer struct {
	querier exchange.PositionQuerier
	priceFn func(symbol string) float64
}

// NewExchangeSyncer wraps a PositionQuerier; priceFn supplies the valuation price per symbol.
func NewExchangeSyncer(querier exchange.PositionQuerier, priceFn func(symbol string) float64) *ExchangeSyncer {
	return &ExchangeSyncer{querier: querier, priceFn: priceFn}
}

// Positions returns the non-flat book as signed-qty positions. The exchange's signed
// Amt already carries direction in both one-way and hedge accounts.
func (s *ExchangeSyncer) Positions(ctx context.Context) ([]Position, error) {
	raw, err := s.querier.GetPositions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Position, 0, len(raw))
	for _, r := range raw {
		if r.Amt == 0 {
			continue
		}
		out = append(out, Position{Symbol: r.Symbol, SignedQty: r.Amt, Price: s.priceFn(r.Symbol)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}
