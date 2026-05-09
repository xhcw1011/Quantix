package composite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Quantix/quantix/internal/alpha"
	"github.com/Quantix/quantix/internal/strategy"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestSetupReadsContextExtras(t *testing.T) {
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["user_id"] = 42
	ctx.Extra["engine_id"] = "test-engine"

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}

	if s.userID != 42 {
		t.Fatalf("userID=%d want 42 (setup didn't read ctx.Extra)", s.userID)
	}
	if s.engineID != "test-engine" {
		t.Fatalf("engineID=%q want test-engine", s.engineID)
	}
}

func TestSetupRunsOnceOnly(t *testing.T) {
	// Subsequent ctx.Extra changes after first bar should NOT mutate s.userID/engineID.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["user_id"] = 42
	ctx.Extra["engine_id"] = "first"

	bars := makeBars(70, 2300)
	s.OnBar(ctx, bars[0]) // first bar — setup runs

	// Mutate Extra; subsequent OnBar should NOT re-read
	ctx.Extra["engine_id"] = "second"
	for _, b := range bars[1:] {
		s.OnBar(ctx, b)
	}

	if s.engineID != "first" {
		t.Fatalf("setup re-ran: engineID=%q want first", s.engineID)
	}
}

func TestSetupBacktestEmptyExtraOK(t *testing.T) {
	// Backtest contexts have empty Extra. Setup should not panic; userID/engineID stay zero.
	a := &fakeAlpha{name: "x", out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	// ctx.Extra is empty by default

	for _, b := range makeBars(70, 2300) {
		s.OnBar(ctx, b)
	}

	if s.userID != 0 || s.engineID != "" {
		t.Fatalf("backtest setup leaked: userID=%d engineID=%q", s.userID, s.engineID)
	}
}

func TestOnFillPersistsToRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	s := New([]Alpha{&fakeAlpha{}}, Config{Symbol: "ETHUSDT"})
	s.engineID = "test-engine"
	s.rdb = rdb

	s.OnFill(nil, strategy.Fill{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5})

	got, err := rdb.Get(context.Background(), "quantix:composite:test-engine:state").Result()
	if err != nil {
		t.Fatalf("redis get: %v", err)
	}
	var st compositeState
	if err := json.Unmarshal([]byte(got), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.PosQty != 0.5 {
		t.Fatalf("PosQty=%f want 0.5", st.PosQty)
	}
	if st.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt not set")
	}
}

func TestPersistStateNoOpWithoutRedis(t *testing.T) {
	// Backtest path: no rdb, no engineID. persistState must not panic.
	s := New([]Alpha{&fakeAlpha{}}, Config{Symbol: "ETHUSDT"})
	s.OnFill(nil, strategy.Fill{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5})
	if s.posQty != 0.5 {
		t.Fatalf("posQty update broken: got %f", s.posQty)
	}
}

func TestPersistStateNoOpWithoutEngineID(t *testing.T) {
	// rdb present but engineID still empty (pre-setup). Must skip Redis write.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	s := New([]Alpha{&fakeAlpha{}}, Config{Symbol: "ETHUSDT"})
	s.rdb = rdb
	// engineID stays ""

	s.OnFill(nil, strategy.Fill{Symbol: "ETHUSDT", Side: strategy.SideBuy, Qty: 0.5})

	keys := mr.Keys()
	if len(keys) != 0 {
		t.Fatalf("expected no Redis writes without engineID, got keys: %v", keys)
	}
}
