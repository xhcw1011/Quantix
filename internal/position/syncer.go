package position

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

// Syncer keeps Redis + strategy in sync with exchange positions.
// Exchange is the single source of truth.
type Syncer struct {
	redis    *RedisStore
	store    *data.Store
	log      *zap.Logger

	mu       sync.RWMutex
	long     *StrategyPosition
	short    *StrategyPosition
	equity   float64

	onChange func(PositionEvent)
	userID   int
	engineID string
	symbol   string
}

// SyncerConfig holds dependencies for creating a Syncer.
type SyncerConfig struct {
	Redis    *RedisStore
	Store    *data.Store
	UserID   int
	EngineID string
	Symbol   string
	OnChange func(PositionEvent)
	Log      *zap.Logger
}

// NewSyncer creates a position syncer.
func NewSyncer(cfg SyncerConfig) *Syncer {
	return &Syncer{
		redis:    cfg.Redis,
		store:    cfg.Store,
		log:      cfg.Log,
		onChange: cfg.OnChange,
		userID:   cfg.UserID,
		engineID: cfg.EngineID,
		symbol:   cfg.Symbol,
	}
}

// ─── Startup Loading (fallback chain: Redis → DB → Exchange) ─────────────────

// Load initializes positions from the fastest available source.
func (s *Syncer) Load(ctx context.Context, querier exchange.MarginQuerier) {
	// 1. Try Redis (fastest)
	if s.redis != nil {
		s.loadFromRedis(ctx)
	}

	// 2. Always verify against exchange (source of truth)
	s.loadFromExchange(ctx, querier)
}

func (s *Syncer) loadFromRedis(ctx context.Context) {
	positions, err := s.redis.GetAllPositions(ctx)
	if err != nil {
		s.log.Warn("syncer: redis load failed", zap.Error(err))
		return
	}
	for _, p := range positions {
		p := p
		if p.Side == "LONG" {
			s.long = &p
		} else if p.Side == "SHORT" {
			s.short = &p
		}
	}
	// Load cached equity
	if eq, err := s.redis.GetEquity(ctx); err == nil && eq > 0 {
		s.equity = eq
	}
	if s.long != nil || s.short != nil {
		s.log.Info("syncer: loaded from Redis",
			zap.Bool("long", s.long != nil), zap.Bool("short", s.short != nil))
	}
}

func (s *Syncer) loadFromExchange(ctx context.Context, querier exchange.MarginQuerier) {
	if querier == nil {
		return
	}
	ratios, err := querier.GetMarginRatios(ctx)
	if err != nil {
		s.log.Warn("syncer: exchange query failed", zap.Error(err))
		return
	}

	exchangeLong := false
	exchangeShort := false

	for _, r := range ratios {
		if r.Symbol != s.symbol || r.Size == 0 {
			continue
		}
		side := r.PositionSide
		if side == "" || side == "BOTH" {
			if r.Size > 0 {
				side = "LONG"
			} else {
				side = "SHORT"
			}
		}

		if side == "LONG" {
			exchangeLong = true
		} else {
			exchangeShort = true
		}

		// Check if we already have this position tracked
		s.mu.RLock()
		var current *StrategyPosition
		if side == "LONG" {
			current = s.long
		} else {
			current = s.short
		}
		s.mu.RUnlock()

		if current == nil {
			// Exchange has position but we don't → untracked (likely from before restart)
			pos := &StrategyPosition{
				ExchangePosition: ExchangePosition{
					Symbol: r.Symbol, Side: side,
					Qty: math.Abs(r.Size), UpdatedAt: time.Now(),
				},
				Mode:   "range", // default; strategy will adjust
				Filled: true,
			}
			s.mu.Lock()
			if side == "LONG" {
				s.long = pos
			} else {
				s.short = pos
			}
			s.mu.Unlock()
			s.writeToRedis(ctx, pos)
			s.log.Info("syncer: recovered untracked position from exchange",
				zap.String("side", side), zap.Float64("qty", math.Abs(r.Size)))
		} else {
			// Exchange confirms this position exists — ensure filled=true
			needsUpdate := false
			if !current.Filled {
				current.Filled = true
				needsUpdate = true
			}
			if math.Abs(current.Qty-math.Abs(r.Size)) > 0.0001 {
				s.log.Warn("syncer: qty mismatch with exchange",
					zap.String("side", side),
					zap.Float64("local", current.Qty),
					zap.Float64("exchange", math.Abs(r.Size)))
				current.Qty = math.Abs(r.Size)
				needsUpdate = true
			}
			if needsUpdate {
				s.writeToRedis(ctx, current)
			}
		}
	}

	// Check for phantom positions (we think we have it but exchange doesn't)
	s.mu.Lock()
	if s.long != nil && !exchangeLong {
		s.log.Warn("syncer: phantom LONG — exchange has no position, clearing")
		s.long = nil
		if s.redis != nil {
			s.redis.DeletePosition(ctx, s.symbol, "LONG")
		}
	}
	if s.short != nil && !exchangeShort {
		s.log.Warn("syncer: phantom SHORT — exchange has no position, clearing")
		s.short = nil
		if s.redis != nil {
			s.redis.DeletePosition(ctx, s.symbol, "SHORT")
		}
	}
	s.mu.Unlock()
}

// ─── Position Access ─────────────────────────────────────────────────────────

// GetLong returns the current LONG position (nil if none).
func (s *Syncer) GetLong() *StrategyPosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.long == nil {
		return nil
	}
	cp := *s.long
	return &cp
}

// GetShort returns the current SHORT position (nil if none).
func (s *Syncer) GetShort() *StrategyPosition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.short == nil {
		return nil
	}
	cp := *s.short
	return &cp
}

// GetEquity returns the cached exchange equity.
func (s *Syncer) GetEquity() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.equity
}

// HasPosition returns true if any position exists for the symbol.
func (s *Syncer) HasPosition(side string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if side == "LONG" {
		return s.long != nil
	}
	return s.short != nil
}

// ─── Position Updates ────────────────────────────────────────────────────────

// UpdatePosition sets/updates a position in memory + Redis + DB.
func (s *Syncer) UpdatePosition(ctx context.Context, pos *StrategyPosition) {
	pos.UpdatedAt = time.Now()
	s.mu.Lock()
	if pos.Side == "LONG" {
		s.long = pos
	} else {
		s.short = pos
	}
	s.mu.Unlock()

	s.writeToRedis(ctx, pos)
	s.writeToDB(pos)
}

// RemovePosition clears a position.
func (s *Syncer) RemovePosition(ctx context.Context, side string) {
	s.mu.Lock()
	if side == "LONG" {
		s.long = nil
	} else {
		s.short = nil
	}
	s.mu.Unlock()

	if s.redis != nil {
		s.redis.DeletePosition(ctx, s.symbol, side)
	}
	if s.store != nil {
		go func() {
			dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.store.DeleteStrategyPosition(dbCtx, s.userID, s.engineID, side)
		}()
	}
}

// UpdateEquity caches the latest exchange equity.
func (s *Syncer) UpdateEquity(ctx context.Context, equity float64) {
	s.mu.Lock()
	s.equity = equity
	s.mu.Unlock()

	if s.redis != nil {
		s.redis.SetEquity(ctx, equity)
	}
}

// ─── Exchange Event Handlers ─────────────────────────────────────────────────

// OnExchangePositionUpdate is called by User Data Stream ACCOUNT_UPDATE.
// Detects external operations and notifies strategy.
func (s *Syncer) OnExchangePositionUpdate(ctx context.Context, symbol, side string, qty, entryPrice float64) {
	if symbol != s.symbol {
		return
	}

	s.mu.RLock()
	var current *StrategyPosition
	if side == "LONG" {
		current = s.long
	} else {
		current = s.short
	}
	s.mu.RUnlock()

	if qty == 0 && current != nil {
		// Position closed
		s.log.Info("syncer: position closed via exchange",
			zap.String("side", side))
		s.RemovePosition(ctx, side)
		if s.onChange != nil {
			s.onChange(PositionEvent{
				Type:     "external_close",
				Position: ExchangePosition{Symbol: symbol, Side: side},
			})
		}
		return
	}

	if qty != 0 && current == nil {
		// New position detected
		s.log.Info("syncer: new position from exchange",
			zap.String("side", side), zap.Float64("qty", qty), zap.Float64("entry", entryPrice))
		pos := &StrategyPosition{
			ExchangePosition: ExchangePosition{
				Symbol: symbol, Side: side, Qty: math.Abs(qty),
				EntryPrice: entryPrice, UpdatedAt: time.Now(),
			},
			Filled: true,
		}
		s.UpdatePosition(ctx, pos)
		if s.onChange != nil {
			s.onChange(PositionEvent{
				Type:     "external_open",
				Position: pos.ExchangePosition,
			})
		}
		return
	}

	if qty != 0 && current != nil {
		newQty := math.Abs(qty)
		if math.Abs(current.Qty-newQty) > 0.0001 {
			s.log.Info("syncer: position qty changed",
				zap.String("side", side),
				zap.Float64("old", current.Qty), zap.Float64("new", newQty))
			// Work on a copy, then use UpdatePosition (which takes write lock)
			updated := *current
			updated.Qty = newQty
			if entryPrice > 0 {
				updated.EntryPrice = entryPrice
			}
			updated.UpdatedAt = time.Now()
			s.UpdatePosition(ctx, &updated)
		}
	}
}

// OnEquityUpdate is called by User Data Stream ACCOUNT_UPDATE with balance info.
func (s *Syncer) OnEquityUpdate(ctx context.Context, walletBalance, crossUnPnl float64) {
	s.UpdateEquity(ctx, walletBalance+crossUnPnl)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Syncer) writeToRedis(ctx context.Context, pos *StrategyPosition) {
	if s.redis == nil {
		return
	}
	if err := s.redis.SetPosition(ctx, *pos); err != nil {
		s.log.Warn("syncer: redis write failed", zap.Error(err))
	}
}

func (s *Syncer) writeToDB(pos *StrategyPosition) {
	if s.store == nil {
		return
	}
	rec := &data.StrategyPositionRecord{
		UserID: s.userID, EngineID: s.engineID,
		Side: pos.Side, Symbol: pos.Symbol, Mode: pos.Mode,
		Qty: pos.Qty, EntryPrice: pos.EntryPrice,
		StopLoss: pos.StopLoss, TakeProfit: pos.TakeProfit,
		Trailing: pos.Trailing, PeakPrice: pos.PeakPrice,
		RValue: pos.R, InitQty: pos.InitQty,
		TP1Hit: pos.TP1Hit, BarsHeld: pos.BarsHeld,
		OrderID: pos.OrderID, Filled: pos.Filled,
	}
	go func() {
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.UpsertStrategyPosition(dbCtx, s.userID, s.engineID, rec); err != nil {
			s.log.Warn("syncer: db write failed", zap.Error(err))
		}
	}()
}

// ParsePositionAmt converts position amount string to (side, qty).
func ParsePositionAmt(amt string) (string, float64) {
	f, _ := strconv.ParseFloat(amt, 64)
	if f > 0 {
		return "LONG", f
	}
	if f < 0 {
		return "SHORT", -f
	}
	return "", 0
}
