package backtest

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// pendingOrder pairs a queued order with its assigned ID.
type pendingOrder struct {
	id  string
	req strategy.OrderRequest
}

// SimBroker is a simulated broker for backtesting.
// Orders are queued when the strategy calls PlaceOrder, then matched at
// the close price of the next bar (next-bar-open approximation via close).
type SimBroker struct {
	FeeRate  float64 // commission rate, e.g. 0.001 = 0.1%
	Slippage float64 // one-way slippage fraction, e.g. 0.0005

	pending   []pendingOrder
	cancelled map[string]bool // IDs that have been cancelled
	submitted map[string]bool // all IDs ever submitted (for validation)
	portfolio *Portfolio
	log       *zap.Logger
	nextID    int
}

// NewSimBroker creates a broker wired to the given portfolio.
func NewSimBroker(feeRate, slippage float64, portfolio *Portfolio, log *zap.Logger) *SimBroker {
	return &SimBroker{
		FeeRate:   feeRate,
		Slippage:  slippage,
		portfolio: portfolio,
		log:       log,
		cancelled: make(map[string]bool),
		submitted: make(map[string]bool),
	}
}

// PlaceOrder queues an order for execution on the next bar and returns its ID.
// Implements strategy.Broker.
func (b *SimBroker) PlaceOrder(req strategy.OrderRequest) string {
	if req.Type == "" {
		req.Type = strategy.OrderMarket
	}
	b.nextID++
	id := fmt.Sprintf("sim-%d", b.nextID)
	b.submitted[id] = true
	b.pending = append(b.pending, pendingOrder{id: id, req: req})
	return id
}

// CancelOrder marks a pending order as cancelled so it is skipped during Process.
// Returns an error if the order ID was never submitted.
func (b *SimBroker) CancelOrder(id string) error {
	if !b.submitted[id] {
		return fmt.Errorf("order %s not found", id)
	}
	b.cancelled[id] = true
	return nil
}

// Process matches all pending orders against the given bar's close price.
// Returns the list of fills generated. Should be called once per bar,
// AFTER the strategy's OnBar has been invoked.
func (b *SimBroker) Process(bar exchange.Kline) []strategy.Fill {
	if len(b.pending) == 0 {
		return nil
	}

	orders := b.pending
	b.pending = nil

	var fills []strategy.Fill
	for _, po := range orders {
		if b.cancelled[po.id] {
			b.log.Debug("skipping cancelled order", zap.String("id", po.id))
			continue
		}
		fill, err := b.execute(po.req, bar)
		if err != nil {
			b.log.Warn("order rejected",
				zap.String("symbol", po.req.Symbol),
				zap.String("side", string(po.req.Side)),
				zap.Error(err))
			continue
		}
		fills = append(fills, fill)

		// Update portfolio
		if trade := b.portfolio.applyFill(fill, bar.CloseTime); trade != nil {
			b.portfolio.Trades = append(b.portfolio.Trades, *trade)
		}

		b.log.Debug("fill",
			zap.String("id", fill.ID),
			zap.String("symbol", fill.Symbol),
			zap.String("side", string(fill.Side)),
			zap.Float64("qty", fill.Qty),
			zap.Float64("price", fill.Price),
			zap.Float64("fee", fill.Fee))
	}
	return fills
}

// execute simulates order execution at the bar's close price with slippage.
func (b *SimBroker) execute(req strategy.OrderRequest, bar exchange.Kline) (strategy.Fill, error) {
	// Use close price as execution price (conservative approximation)
	execPrice := bar.Close

	// Apply slippage: buys pay more, sells receive less
	switch req.Side {
	case strategy.SideBuy:
		execPrice *= (1 + b.Slippage)
	case strategy.SideSell:
		execPrice *= (1 - b.Slippage)
	}

	qty := req.Qty

	switch req.Side {
	case strategy.SideBuy:
		if qty == 0 {
			// Use 99% of available cash (keep 1% buffer for rounding)
			available := b.portfolio.cash * 0.99
			if available <= 0 {
				return strategy.Fill{}, fmt.Errorf("insufficient cash: %.4f", b.portfolio.cash)
			}
			qty = available / execPrice
		}
		cost := qty * execPrice
		fee := cost * b.FeeRate
		if cost+fee > b.portfolio.cash {
			// Scale down to what we can afford
			qty = b.portfolio.cash / (execPrice * (1 + b.FeeRate))
			cost = qty * execPrice
			fee = cost * b.FeeRate
		}
		if qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("order qty is zero after scaling")
		}
		b.nextID++
		return strategy.Fill{
			ID:        fmt.Sprintf("sim-%d", b.nextID),
			Symbol:    req.Symbol,
			Side:      strategy.SideBuy,
			Qty:       qty,
			Price:     execPrice,
			Fee:       fee,
			Timestamp: bar.CloseTime,
		}, nil

	case strategy.SideSell:
		pos, exists := b.portfolio.positions[req.Symbol]
		if !exists || pos.Qty <= 0 {
			return strategy.Fill{}, fmt.Errorf("no position to sell for %s", req.Symbol)
		}
		if qty == 0 || qty > pos.Qty {
			qty = pos.Qty // sell entire position
		}
		proceeds := qty * execPrice
		fee := proceeds * b.FeeRate
		b.nextID++
		return strategy.Fill{
			ID:        fmt.Sprintf("sim-%d", b.nextID),
			Symbol:    req.Symbol,
			Side:      strategy.SideSell,
			Qty:       qty,
			Price:     execPrice,
			Fee:       fee,
			Timestamp: bar.CloseTime,
		}, nil
	}

	return strategy.Fill{}, fmt.Errorf("unknown side: %s", req.Side)
}
