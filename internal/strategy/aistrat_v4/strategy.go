package aistrat_v4

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
)

// Strategy implements strategy.Strategy.
type Strategy struct {
	cfg Config
	log *zap.Logger
}

// New creates a Strategy with the given config.
func New(cfg Config, log *zap.Logger) *Strategy {
	return &Strategy{cfg: cfg, log: log}
}

// Name returns a human-readable identifier (used in logs and metrics).
func (s *Strategy) Name() string {
	return fmt.Sprintf("AI_v4(z>=%.1f,lb=%d)", s.cfg.EntryZScore, s.cfg.Lookback)
}

// OnBar handles a closed bar. Stub for Task 1; filled in Task 7.
func (s *Strategy) OnBar(_ *strategy.Context, _ exchange.Kline) {}

// OnFill handles fill events. Stub for Task 1; filled in Task 8.
func (s *Strategy) OnFill(_ *strategy.Context, _ strategy.Fill) {}
