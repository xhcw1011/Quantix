// Package composite is a multi-alpha trading strategy. Each alpha
// produces a Signal independently from a shared Features snapshot;
// the strategy picks the strongest signal and turns it into orders.
package composite

import (
	"context"
	"math"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/redis/go-redis/v9"
)

// Alpha is re-exported so users don't need to import internal/alpha when
// constructing a Composite.
type Alpha = alpha.Alpha

// Config holds runtime parameters of the composite strategy.
type Config struct {
	Symbol         string
	RiskPerTrade   float64 // fraction of equity at risk per trade (default 0.005)
	SLATR          float64 // SL distance in ATR multiples (default 1.5)
	MinSignalScore float64 // skip signals below this strength (default 0.3)
	WarmupBars     int     // bars to accumulate before first prediction (default 60)
	StepSize       float64 // qty granularity per symbol (default 0.001 ETHUSDT; TON=0.1)
	TickSize       float64 // price granularity per symbol (default 0.01 ETHUSDT; TON=0.0001)
}

func (c Config) withDefaults() Config {
	if c.RiskPerTrade == 0 {
		c.RiskPerTrade = 0.005
	}
	if c.SLATR == 0 {
		c.SLATR = 1.5
	}
	if c.MinSignalScore == 0 {
		c.MinSignalScore = 0.3
	}
	if c.WarmupBars == 0 {
		c.WarmupBars = 60
	}
	return c
}

// Strategy is a strategy.Strategy implementation that composes N alphas.
type Strategy struct {
	cfg          Config
	alphas       []Alpha
	bars         []exchange.Kline
	posQty       float64 // current position size (signed: + = long, - = short)
	firstBarSeen bool    // true after first bar; setup runs once
	liveReady    bool    // true once first non-stale (or backtest_replay) bar seen
	userID       int     // from ctx.Extra; 0 in backtest
	engineID     string  // from ctx.Extra; "" in backtest
	rdb          *redis.Client // Redis for state persistence; nil in backtest
}

// New returns a Composite strategy. Panics if alphas is empty.
func New(alphas []Alpha, cfg Config) *Strategy {
	if len(alphas) == 0 {
		panic("composite: at least one alpha required")
	}
	return &Strategy{cfg: cfg.withDefaults(), alphas: alphas}
}

func (s *Strategy) Name() string { return "composite" }

func (s *Strategy) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	s.bars = append(s.bars, bar)
	barCap := s.cfg.WarmupBars * 4
	if len(s.bars) > barCap {
		s.bars = s.bars[len(s.bars)-barCap:]
	}
	if !s.firstBarSeen {
		s.setupFromContext(ctx)
		s.firstBarSeen = true
	}

	// ── Live-mode gate: skip stale backfill bars to avoid trading on
	//    historical signals during engine warmup. Mirror aistrat pattern.
	//    cmd/backtest sets ctx.Extra["backtest_replay"]=true to bypass.
	if !s.liveReady {
		isBacktest, _ := ctx.Extra["backtest_replay"].(bool)
		if isBacktest || time.Since(bar.CloseTime) < 2*time.Minute {
			s.liveReady = true
		} else {
			return
		}
	}

	if len(s.bars) < s.cfg.WarmupBars {
		return
	}

	feat := alpha.BuildFeatures(s.cfg.Symbol, s.bars)
	if feat.ATR == 0 {
		return
	}

	// Pick strongest directional signal. Strength is contractually [0, 1]
	// (alpha.Signal docstring); we trust the contract — no abs() defense.
	best := alpha.Signal{}
	for _, a := range s.alphas {
		sig := a.Predict(feat)
		if sig.Direction == 0 {
			continue
		}
		if sig.Strength > best.Strength {
			best = sig
		}
	}
	if best.Direction == 0 || best.Strength < s.cfg.MinSignalScore {
		return
	}

	side := strategy.SideBuy
	posSide := strategy.PositionSideLong
	if best.Direction < 0 {
		side = strategy.SideSell
		posSide = strategy.PositionSideShort
	}

	// Position-aware: skip if already aligned with target direction.
	if (best.Direction > 0 && s.posQty > 0) || (best.Direction < 0 && s.posQty < 0) {
		return
	}

	qty := positionSize(ctx, feat, s.cfg)
	if qty <= 0 {
		return
	}

	slDist := feat.ATR * s.cfg.SLATR
	slPrice := feat.Close - slDist // long: SL below
	if best.Direction < 0 {
		slPrice = feat.Close + slDist // short: SL above
	}
	// Round SL to symbol tick size. Binance rejects sub-tick precision with -1111.
	tick := s.cfg.TickSize
	if tick <= 0 {
		tick = 0.01 // ETHUSDT default
	}
	slPrice = math.Round(slPrice/tick) * tick

	ctx.PlaceOrder(strategy.OrderRequest{
		Symbol:       s.cfg.Symbol,
		Side:         side,
		PositionSide: posSide,
		Type:         strategy.OrderMarket,
		Qty:          qty,
		StopLoss:     slPrice,
	})
}

func (s *Strategy) OnFill(_ *strategy.Context, fill strategy.Fill) {
	if fill.Symbol != s.cfg.Symbol {
		return
	}
	if fill.Side == strategy.SideBuy {
		s.posQty += fill.Qty
	} else {
		s.posQty -= fill.Qty
	}
	s.persistState(context.Background())
}
