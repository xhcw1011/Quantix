package composite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

func TestRecoverStateFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Pre-populate Redis with prior state (simulates surviving restart).
	st := compositeState{PosQty: -0.7, UpdatedAt: time.Now()}
	b, _ := json.Marshal(st)
	rdb.Set(context.Background(), "quantix:composite:test-engine:state", b, 0)

	a := &fakeAlpha{out: alpha.Signal{Direction: alpha.DirShort, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["redis_client"] = rdb
	ctx.Extra["engine_id"] = "test-engine"

	// One bar triggers setup → recovery.
	s.OnBar(ctx, makeBars(1, 2300)[0])

	if s.posQty != -0.7 {
		t.Fatalf("posQty=%f want -0.7 (recovery failed)", s.posQty)
	}
}

func TestRecoverStateNoOpOnEmptyRedis(t *testing.T) {
	// Fresh start: no prior key in Redis. Recovery should silently leave posQty=0.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	a := &fakeAlpha{out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["redis_client"] = rdb
	ctx.Extra["engine_id"] = "test-engine"

	s.OnBar(ctx, makeBars(1, 2300)[0])

	if s.posQty != 0 {
		t.Fatalf("posQty=%f want 0 (recovery should no-op on empty)", s.posQty)
	}
}

func TestRecoverStateNoOpWithoutRedis(t *testing.T) {
	// Backtest: no rdb in Extra. Setup runs, recovery skipped silently.
	a := &fakeAlpha{out: alpha.Signal{Direction: alpha.DirLong, Strength: 0.9}}
	s := New([]Alpha{a}, Config{Symbol: "ETHUSDT"})

	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	// No rdb, no engine_id

	s.OnBar(ctx, makeBars(1, 2300)[0])

	if s.posQty != 0 {
		t.Fatalf("posQty leaked: %f", s.posQty)
	}
}

func TestRecoverStateRoundTrip(t *testing.T) {
	// End-to-end: persist via OnFill, then recover via setup. Verifies the
	// JSON shape is stable and stateKey is consistent across persist/recover.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// First instance: persist
	s1 := New([]Alpha{&fakeAlpha{}}, Config{Symbol: "ETHUSDT"})
	s1.engineID = "round-trip"
	s1.rdb = rdb
	s1.OnFill(nil, strategy.Fill{Symbol: "ETHUSDT", Side: strategy.SideSell, Qty: 0.3})
	if s1.posQty != -0.3 {
		t.Fatalf("s1.posQty=%f want -0.3", s1.posQty)
	}

	// Second instance: recover via setupFromContext path
	s2 := New([]Alpha{&fakeAlpha{out: alpha.Signal{Direction: alpha.DirShort, Strength: 0.9}}}, Config{Symbol: "ETHUSDT"})
	ctx := strategy.NewContext(&fakePortfolio{cash: 10000}, &fakeBroker{}, zap.NewNop())
	ctx.Extra["redis_client"] = rdb
	ctx.Extra["engine_id"] = "round-trip"
	s2.OnBar(ctx, makeBars(1, 2300)[0])

	if s2.posQty != -0.3 {
		t.Fatalf("s2.posQty=%f want -0.3 (round-trip failed)", s2.posQty)
	}
}
