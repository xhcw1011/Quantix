package api

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	// Register at least one strategy so the registry gate passes.
	_ "github.com/Quantix/quantix/internal/strategy/macross"
)

func newTestManager(liveEnabled bool) *EngineManager {
	cfg := &config.Config{
		Live: config.LiveConfig{Enabled: liveEnabled},
	}
	return NewEngineManager(nil, nil, config.SMTPConfig{}, nil, cfg, nil, zap.NewNop())
}

// Live mode requires the per-engine confirm_live flag — an early gate that runs
// before any credential/DB access.
func TestStart_LiveRequiresConfirmLive(t *testing.T) {
	m := newTestManager(true)
	_, err := m.Start(1, StartRequest{
		Mode: "live", ConfirmLive: false,
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h",
	})
	if err == nil || !strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("expected confirm_live error, got: %v", err)
	}
}

// With confirm_live set, Start passes the early pre-flight gates and proceeds to
// credential loading (which panics on the nil test store). The real-money master
// switch (live + a non-testnet/demo credential requires the user's
// live_trading_enabled preference) is enforced after the credential loads and is
// covered by integration, not this unit test.
func TestStart_LiveWithConfirmPassesEarlyGates(t *testing.T) {
	m := newTestManager(true)
	defer func() { _ = recover() }() // nil store panics downstream — that's fine here
	_, err := m.Start(1, StartRequest{
		Mode: "live", ConfirmLive: true,
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h", CredentialID: 999,
	})
	if err != nil && strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("should have passed the confirm_live gate, got: %s", err.Error())
	}
}

// Paper mode is never blocked by the live gates.
func TestStart_PaperBypassesLiveGate(t *testing.T) {
	m := newTestManager(false)
	defer func() { _ = recover() }()
	_, err := m.Start(1, StartRequest{
		Mode:       "paper",
		StrategyID: "macross", Symbol: "BTCUSDT", Interval: "1h", CredentialID: 999,
	})
	if err != nil && strings.Contains(err.Error(), "confirm_live") {
		t.Fatalf("paper mode should bypass live gates, got: %s", err.Error())
	}
}

// TestListAll_DeterministicOrder reproduces a 2026-08-13 finding: the running-
// engines list on the frontend visibly shuffled card order every ~10s poll
// ("乱跳"). Root cause: ListAll ranges directly over m.engines[userID], a Go
// map — Go deliberately randomizes map iteration order on every range, so
// every poll returned engines in a different order even though nothing about
// them changed. The frontend applies no sort to the running list (unlike the
// stopped list, which already sorts by started_at), so this randomness went
// straight to the DOM. Calling ListAll repeatedly over an unchanged map must
// return the same order every time.
func TestListAll_DeterministicOrder(t *testing.T) {
	m := newTestManager(false)
	now := time.Now()
	m.engines = map[int]map[string]*runningEngine{
		1: {
			"zzz-engine": {engineID: "zzz-engine", userID: 1, startedAt: now, done: make(chan struct{})},
			"aaa-engine": {engineID: "aaa-engine", userID: 1, startedAt: now.Add(-2 * time.Hour), done: make(chan struct{})},
			"mmm-engine": {engineID: "mmm-engine", userID: 1, startedAt: now.Add(-1 * time.Hour), done: make(chan struct{})},
			"bbb-engine": {engineID: "bbb-engine", userID: 1, startedAt: now.Add(-30 * time.Minute), done: make(chan struct{})},
		},
	}

	var first []string
	for i := 0; i < 30; i++ {
		out := m.ListAll(1)
		ids := make([]string, len(out))
		for j, e := range out {
			ids[j] = e.EngineID
		}
		if i == 0 {
			first = ids
			continue
		}
		if !reflect.DeepEqual(ids, first) {
			t.Fatalf("ListAll order not stable across repeated calls on an unchanged map: call 0 = %v, call %d = %v", first, i, ids)
		}
	}
}

// TestListAllGlobalEngines_DeterministicOrder is ListAll's admin-facing
// sibling — same underlying map-range-without-sort bug, doubled (ranges over
// both the per-user map and the outer userID map).
func TestListAllGlobalEngines_DeterministicOrder(t *testing.T) {
	m := newTestManager(false)
	now := time.Now()
	m.engines = map[int]map[string]*runningEngine{
		1: {
			"zzz-engine": {engineID: "zzz-engine", userID: 1, startedAt: now, done: make(chan struct{})},
			"aaa-engine": {engineID: "aaa-engine", userID: 1, startedAt: now.Add(-time.Hour), done: make(chan struct{})},
		},
		2: {
			"ccc-engine": {engineID: "ccc-engine", userID: 2, startedAt: now.Add(-90 * time.Minute), done: make(chan struct{})},
		},
	}

	var first []string
	for i := 0; i < 30; i++ {
		out := m.ListAllGlobalEngines()
		ids := make([]string, len(out))
		for j, e := range out {
			ids[j] = e.EngineID
		}
		if i == 0 {
			first = ids
			continue
		}
		if !reflect.DeepEqual(ids, first) {
			t.Fatalf("ListAllGlobalEngines order not stable across repeated calls on an unchanged map: call 0 = %v, call %d = %v", first, i, ids)
		}
	}
}

// TestGuardianAdoptOnly reproduces the 2026-08-06 finding: a guardian with no
// PlaceEntry (adopt-only — never opens a new position, only manages an
// existing one) still got hard-blocked from starting without an explicit
// leverage value, and Start() would then call SetLeverage on the user's
// behalf even though nothing about adopt-only guardian needs the account's
// leverage changed. guardianAdoptOnly() is the predicate that lets Start()
// skip both the leverage requirement and the SetLeverage call for this case.
func TestGuardianAdoptOnly(t *testing.T) {
	cases := []struct {
		name string
		req  StartRequest
		want bool
	}{
		{"not guardian at all", StartRequest{StrategyID: "macross", Params: map[string]any{"PlaceEntry": false}}, false},
		{"guardian, PlaceEntry explicitly true", StartRequest{StrategyID: "guardian", Params: map[string]any{"PlaceEntry": true}}, false},
		{"guardian, PlaceEntry explicitly false", StartRequest{StrategyID: "guardian", Params: map[string]any{"PlaceEntry": false}}, true},
		{"guardian, PlaceEntry absent — matches guardian's own boolOr(...,\"PlaceEntry\",false) default", StartRequest{StrategyID: "guardian", Params: map[string]any{}}, true},
		{"guardian, nil Params", StartRequest{StrategyID: "guardian"}, true},
		{"guardian, explicit Adopt:false without PlaceEntry is ModeExplicit, not adopt-only (2026-08-07 fix)", StartRequest{StrategyID: "guardian", Params: map[string]any{"Adopt": false}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guardianAdoptOnly(c.req); got != c.want {
				t.Errorf("guardianAdoptOnly(%+v) = %v, want %v", c.req, got, c.want)
			}
		})
	}
}

// TestMacrossPositionModeMismatch reproduces the 2026-08-10 incident: a
// macross engine started with EnableShort:false (simple/one-way order
// construction — no PositionSide sent) on an account whose exchange-side
// position mode was already Hedge Mode (left over from an earlier
// EnableShort:true run on the same credential). Binance rejected every
// entry with -4061 for two days, silently, because nothing checked this at
// Start() — the mismatch only ever surfaced order-by-order, buried in the
// 订单记录 tab with no alert. macrossPositionModeMismatch is the pure
// predicate Start() now consults before ever placing an order.
func TestMacrossPositionModeMismatch(t *testing.T) {
	cases := []struct {
		name      string
		params    map[string]any
		hedgeMode bool
		wantErr   bool
	}{
		{"EnableShort true, account hedge — match", map[string]any{"EnableShort": true}, true, false},
		{"EnableShort false, account one-way — match", map[string]any{"EnableShort": false}, false, false},
		{"EnableShort absent (defaults false), account one-way — match", map[string]any{}, false, false},
		{"EnableShort false, account hedge — the actual incident", map[string]any{"EnableShort": false}, true, true},
		{"EnableShort absent (defaults false), account hedge — the actual incident", map[string]any{}, true, true},
		{"EnableShort true, account one-way — mismatch", map[string]any{"EnableShort": true}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := macrossPositionModeMismatch(c.params, c.hedgeMode)
			if (err != nil) != c.wantErr {
				t.Errorf("macrossPositionModeMismatch(%+v, hedge=%v) = %v, wantErr %v", c.params, c.hedgeMode, err, c.wantErr)
			}
		})
	}
}

// TestStopRetiredEngine_RemovesFromRunningSet is the 2026-08-06 fix: when a
// strategy self-retires (e.g. guardian once its guarded position is gone for
// good), the engine session must stop being "running" too — otherwise it
// keeps occupying its engineID slot and gets blindly resurrected on the next
// server restart (previously only Stop()/StopAll()/ForceStop() ever removed
// an entry from m.engines).
func TestStopRetiredEngine_RemovesFromRunningSet(t *testing.T) {
	m := newTestManager(false) // store == nil: exercises the DB-less path
	m.engines[1] = map[string]*runningEngine{
		"BTCUSDT-5m-guardian": {cancel: func() {}, userID: 1, engineID: "BTCUSDT-5m-guardian"},
	}
	m.engines[13] = map[string]*runningEngine{
		"BTCUSDT-5m-guardian": {cancel: func() {}, userID: 13, engineID: "BTCUSDT-5m-guardian"},
	}

	m.stopRetiredEngine(1, "BTCUSDT-5m-guardian")

	if _, ok := m.engines[1]["BTCUSDT-5m-guardian"]; ok {
		t.Fatal("a self-retired engine must be removed from the running set")
	}
	if _, ok := m.engines[13]["BTCUSDT-5m-guardian"]; !ok {
		t.Fatal("another user's identically-named engine must be untouched")
	}
}
