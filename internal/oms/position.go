package oms

import (
	"fmt"
	"sync"

	"github.com/Quantix/quantix/internal/strategy"
)

// LivePosition tracks an open position for a single symbol and position side.
// For spot / one-way futures: PositionSide = "" (net).
// For hedge-mode futures: PositionSide = "LONG" or "SHORT".
type LivePosition struct {
	Symbol        string
	PositionSide  string  // "", "LONG", "SHORT"
	Qty           float64
	AvgEntryPrice float64
	TotalFee      float64
	RealizedPnL   float64
}

// UnrealizedPnL returns the current mark-to-market profit/loss.
// SHORT positions gain when price falls.
func (p *LivePosition) UnrealizedPnL(currentPrice float64) float64 {
	if p.PositionSide == string(strategy.PositionSideShort) {
		return (p.AvgEntryPrice - currentPrice) * p.Qty
	}
	return (currentPrice - p.AvgEntryPrice) * p.Qty
}

// posKey returns the map key for a given symbol and positionSide.
func posKey(symbol, positionSide string) string {
	if positionSide == "" {
		return symbol
	}
	return fmt.Sprintf("%s:%s", symbol, positionSide)
}

// PositionManager maintains a real-time view of all open positions.
// Supports both net (spot) and hedge-mode (futures long+short) positions.
// It is safe for concurrent use.
type PositionManager struct {
	mu        sync.RWMutex
	positions map[string]*LivePosition // posKey(symbol, positionSide) → position
}

// NewPositionManager creates an empty position manager.
func NewPositionManager() *PositionManager {
	return &PositionManager{positions: make(map[string]*LivePosition)}
}

// ApplyFill updates positions based on a fill event.
// Routes to long, short, or net position depending on fill.PositionSide.
// Returns realized PnL from this fill (non-zero when reducing/closing a position).
func (pm *PositionManager) ApplyFill(fill strategy.Fill) float64 {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	posSide := string(fill.PositionSide)
	key := posKey(fill.Symbol, posSide)
	pos, exists := pm.positions[key]

	// Opening side: BUY for LONG/net, SELL for SHORT
	isOpen := (posSide != string(strategy.PositionSideShort) && fill.Side == strategy.SideBuy) ||
		(posSide == string(strategy.PositionSideShort) && fill.Side == strategy.SideSell)

	if isOpen {
		if !exists {
			pm.positions[key] = &LivePosition{
				Symbol:        fill.Symbol,
				PositionSide:  posSide,
				Qty:           fill.Qty,
				AvgEntryPrice: fill.Price,
				TotalFee:      fill.Fee,
			}
			return 0
		}
		// Average-in to existing position
		total := pos.Qty + fill.Qty
		pos.AvgEntryPrice = (pos.Qty*pos.AvgEntryPrice + fill.Qty*fill.Price) / total
		pos.Qty = total
		pos.TotalFee += fill.Fee
		return 0
	}

	// Closing side
	if !exists || pos.Qty <= 0 {
		return 0
	}
	qty := fill.Qty
	if qty > pos.Qty {
		qty = pos.Qty
	}

	var realized float64
	if posSide == string(strategy.PositionSideShort) {
		realized = (pos.AvgEntryPrice-fill.Price)*qty - fill.Fee
	} else {
		realized = (fill.Price-pos.AvgEntryPrice)*qty - fill.Fee
	}

	pos.RealizedPnL += realized
	pos.Qty -= qty
	pos.TotalFee += fill.Fee

	if pos.Qty < filledEps {
		delete(pm.positions, key)
	}
	return realized
}

// Position returns a copy of the net/long position for the given symbol.
// Use LongPosition / ShortPosition for hedge-mode futures.
// ok is false if no position exists.
func (pm *PositionManager) Position(symbol string) (LivePosition, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.positions[posKey(symbol, "")]
	if !ok {
		return LivePosition{}, false
	}
	return *p, true
}

// LongPosition returns a copy of the long leg for hedge-mode futures.
func (pm *PositionManager) LongPosition(symbol string) (LivePosition, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.positions[posKey(symbol, string(strategy.PositionSideLong))]
	if !ok {
		return LivePosition{}, false
	}
	return *p, true
}

// ShortPosition returns a copy of the short leg for hedge-mode futures.
func (pm *PositionManager) ShortPosition(symbol string) (LivePosition, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.positions[posKey(symbol, string(strategy.PositionSideShort))]
	if !ok {
		return LivePosition{}, false
	}
	return *p, true
}

// All returns copies of all open positions (all sides).
func (pm *PositionManager) All() []LivePosition {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]LivePosition, 0, len(pm.positions))
	for _, p := range pm.positions {
		result = append(result, *p)
	}
	return result
}

// TotalUnrealizedPnL computes gross unrealized PnL using provided prices.
// Correctly handles both long (profit when price rises) and
// short (profit when price falls) positions.
func (pm *PositionManager) TotalUnrealizedPnL(prices map[string]float64) float64 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var total float64
	for _, pos := range pm.positions {
		if price, ok := prices[pos.Symbol]; ok {
			total += pos.UnrealizedPnL(price)
		}
	}
	return total
}

// TotalMarketValue returns the total mark-to-market value of all open positions.
// For long positions: qty * currentPrice. For short: qty * avgEntry (margin locked).
func (pm *PositionManager) TotalMarketValue(prices map[string]float64) float64 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var total float64
	for _, pos := range pm.positions {
		price, ok := prices[pos.Symbol]
		if !ok || pos.Qty == 0 {
			continue
		}
		if pos.PositionSide == string(strategy.PositionSideShort) {
			// Short: margin is locked at entry; market value = margin + unrealized PnL
			total += pos.Qty*pos.AvgEntryPrice + pos.UnrealizedPnL(price)
		} else {
			// Long/net: market value = qty * current price
			total += pos.Qty * price
		}
	}
	return total
}
