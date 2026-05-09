// Package composite — recovery.go contains lifecycle hooks for live engine
// integration (read dependencies from ctx.Extra, persist/restore state).
// Backtest contexts have empty Extra so all hooks degrade to no-ops.
package composite

import (
	"github.com/Quantix/quantix/internal/strategy"
)

// setupFromContext reads live-engine dependencies from ctx.Extra. Called
// once on the first bar. Backtest contexts have empty Extra — fields stay
// at zero values and nothing breaks.
func (s *Strategy) setupFromContext(ctx *strategy.Context) {
	if v, ok := ctx.Extra["user_id"].(int); ok {
		s.userID = v
	}
	if v, ok := ctx.Extra["engine_id"].(string); ok {
		s.engineID = v
	}
}
