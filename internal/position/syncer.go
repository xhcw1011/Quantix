package position

import (
	"context"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
)

// Syncer keeps Redis + strategy in sync with exchange positions.
// Exchange is the single source of truth.
type Syncer struct {
	redis *RedisStore
	store *data.Store
	log   *zap.Logger

	mu     sync.RWMutex
	long   *StrategyPosition
	short  *StrategyPosition
	equity float64

	onChange func(PositionEvent)
	userID   int
	engineID string
	symbol   string

	// PositionClosedExternally is set when SyncFromExchange detects a position was closed
	// on the exchange (SL hit, manual close, etc.). Strategy reads and clears this flag.
	PositionClosedExternally atomic.Bool

	// IgnoreUntracked: if true, don't adopt exchange positions that aren't in Redis.
	// Prevents engine from interfering with user's manual positions.
	IgnoreUntracked bool
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

// SyncFromExchange re-queries exchange positions and updates local state.
// Called when an unmatched fill is detected (manual close, external trade, etc.)
func (s *Syncer) SyncFromExchange(ctx context.Context, querier exchange.MarginQuerier) {
	s.loadFromExchange(ctx, querier)
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

	// GetMarginRatios carries size but not entry price. Fetch entry prices via the
	// position query so a recovered (untracked) position gets a real cost basis —
	// otherwise a strategy adopting it (e.g. guardian) can't compute the stop.
	// qtyBySide additionally lets the qty-mismatch correction below cross-check
	// against this independent read before trusting a lone GetMarginRatios value
	// (same rationale as the phantom-clear cross-check further down: a single
	// flaky read must not be enough to overwrite good data — 2026-08-17 finding).
	entryBySide := map[string]float64{}
	qtyBySide := map[string]float64{}
	positionsQueryOK := false
	if pq, ok := querier.(exchange.PositionQuerier); ok {
		if positions, perr := pq.GetPositions(ctx); perr == nil {
			positionsQueryOK = true
			for _, p := range positions {
				if p.Symbol != s.symbol {
					continue
				}
				side := p.PositionSide
				if side == "" || side == "BOTH" {
					if p.Amt >= 0 {
						side = "LONG"
					} else {
						side = "SHORT"
					}
				}
				entryBySide[side] = p.EntryPrice
				qtyBySide[side] = math.Abs(p.Amt)
			}
		} else {
			s.log.Warn("syncer: entry-price query failed (recovered positions may lack cost basis)", zap.Error(perr))
		}
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

		// Check if we already have this position tracked. current, when non-nil,
		// is the SAME pointer as s.long/s.short (not a copy) — every read or
		// mutation of its fields below must stay inside the lock. A prior
		// version released the lock right after this pointer read and mutated
		// current.Filled/EntryPrice/Qty completely unprotected, racing against
		// GetLong()/GetShort() (called concurrently from the exposure guard on
		// every PlaceOrder, and from syncFillToPositionSyncer) copying *current
		// under RLock elsewhere — a classic torn-read data race (2026-08-17
		// finding).
		s.mu.Lock()
		var current *StrategyPosition
		if side == "LONG" {
			current = s.long
		} else {
			current = s.short
		}

		if current == nil {
			s.mu.Unlock()
			// Exchange has position but we don't → untracked (likely from before restart)
			if s.IgnoreUntracked {
				s.log.Info("syncer: ignoring untracked position (manual trading mode)",
					zap.String("side", side), zap.Float64("qty", math.Abs(r.Size)))
				continue
			}
			pos := &StrategyPosition{
				ExchangePosition: ExchangePosition{
					Symbol: r.Symbol, Side: side,
					Qty: math.Abs(r.Size), EntryPrice: entryBySide[side], UpdatedAt: time.Now(),
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
			// Exchange confirms this position exists — ensure filled=true. All
			// mutation of current's fields happens here, still under the lock
			// taken above; everything needed for logging/Redis (I/O) is
			// captured into local values first, then the lock is released
			// before that I/O runs.
			needsUpdate := false
			if !current.Filled {
				current.Filled = true
				needsUpdate = true
			}
			// Backfill a missing cost basis (e.g. a position recovered before this
			// fix cached entry=0 in Redis) so adopting strategies get a real entry.
			if current.EntryPrice <= 0 && entryBySide[side] > 0 {
				current.EntryPrice = entryBySide[side]
				needsUpdate = true
			}
			oldQty := current.Qty
			mismatched := false
			disagreement := false
			var otherQty float64
			if math.Abs(current.Qty-math.Abs(r.Size)) > 0.0001 {
				// A single flaky GetMarginRatios read must not be enough to
				// overwrite a good qty (same rationale as the phantom-clear
				// cross-check below). If GetPositions ran and reports a
				// DIFFERENT qty for this side than GetMarginRatios, the two
				// reads disagree — treat as inconclusive and skip the
				// correction this round rather than trust either blindly.
				var sawInPositions bool
				otherQty, sawInPositions = qtyBySide[side]
				disagreement = positionsQueryOK && sawInPositions && math.Abs(otherQty-math.Abs(r.Size)) > 0.0001
				if disagreement {
					mismatched = false
				} else {
					mismatched = true
					current.Qty = math.Abs(r.Size)
					needsUpdate = true
				}
			}
			var toPersist *StrategyPosition
			if needsUpdate {
				cp := *current
				toPersist = &cp
			}
			s.mu.Unlock()

			if disagreement {
				s.log.Warn("syncer: GetMarginRatios and GetPositions disagree on qty — treating as inconclusive, not correcting",
					zap.String("side", side),
					zap.Float64("local", oldQty),
					zap.Float64("margin_ratios_qty", math.Abs(r.Size)),
					zap.Float64("positions_qty", otherQty))
			} else if mismatched {
				s.log.Warn("syncer: qty mismatch with exchange",
					zap.String("side", side),
					zap.Float64("local", oldQty),
					zap.Float64("exchange", math.Abs(r.Size)))
			}
			if toPersist != nil {
				s.writeToRedis(ctx, toPersist)
			}
		}
	}

	// Check for phantom positions (we think we have it but exchange doesn't).
	// If we reach here, the GetMarginRatios call succeeded (errors returned
	// early at line 112) — but GetMarginRatios and GetPositions both parse the
	// SAME underlying exchange response, just with different per-field
	// strictness (e.g. GetMarginRatios silently drops an entry if markPrice
	// fails to parse; GetPositions doesn't). A transient glitch in ONE parse
	// must not be enough to wipe a real position: when GetPositions ran
	// successfully and disagrees (it saw the position, GetMarginRatios
	// didn't), treat this round as inconclusive and keep the existing
	// position rather than clear it — a 2026-08-13 incident wiped a real,
	// exchange-confirmed, stop-loss-protected SHORT this way. If GetPositions
	// isn't available or also failed, fall back to trusting GetMarginRatios
	// alone, same as before.
	s.mu.Lock()
	if s.long != nil && !exchangeLong {
		if _, sawInPositions := entryBySide["LONG"]; positionsQueryOK && sawInPositions {
			s.log.Warn("syncer: GetMarginRatios reports LONG absent but GetPositions still saw it — treating as inconclusive, not clearing")
		} else {
			s.log.Warn("syncer: phantom LONG — exchange has no position, clearing")
			s.long = nil
			s.PositionClosedExternally.Store(true)
			if s.redis != nil {
				s.redis.DeletePosition(ctx, s.symbol, "LONG")
			}
		}
	}
	if s.short != nil && !exchangeShort {
		if _, sawInPositions := entryBySide["SHORT"]; positionsQueryOK && sawInPositions {
			s.log.Warn("syncer: GetMarginRatios reports SHORT absent but GetPositions still saw it — treating as inconclusive, not clearing")
		} else {
			s.log.Warn("syncer: phantom SHORT — exchange has no position, clearing")
			s.short = nil
			s.PositionClosedExternally.Store(true)
			if s.redis != nil {
				s.redis.DeletePosition(ctx, s.symbol, "SHORT")
			}
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
