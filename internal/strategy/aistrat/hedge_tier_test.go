package aistrat

import (
	"fmt"
	"math"
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
)

// ─── Helpers ────────────────────────────────────────────────────────────────

func defaultTestCfg() *Config {
	c := DefaultConfig()
	c.GridHedgeEnabled = true
	c.HedgeTier1PnlR = -1.0
	c.HedgeTier1Ratio = 0.25
	c.HedgeTier2PnlR = -2.0
	c.HedgeTier2Ratio = 0.50
	c.HedgeTier3PnlR = -3.0
	c.HedgeTier3Ratio = 0.80
	c.HedgeTrailPeakPct = 0.50
	c.HedgeMinPeakFavor = 0.10
	c.HedgeMainRecoverPnlR = -0.30
	c.GridTrailEnabled = true
	c.GridTrailActivatePnlR = 1.0
	c.GridTrailPullbackR = 0.5
	return &c
}

// ─── Tier-open selector ─────────────────────────────────────────────────────

func TestSelectHedgeTierForOpen_OnlyUpgrades(t *testing.T) {
	cfg := defaultTestCfg()
	cases := []struct {
		name      string
		pnlR      float64
		curTier   int
		wantTier  int
		wantRatio float64
	}{
		{"t0_no_trigger", -0.5, 0, 0, 0.00},
		{"t0_to_t1", -1.0, 0, 1, 0.25},
		{"t0_to_t2", -2.0, 0, 2, 0.50},
		{"t0_to_t3_crash", -3.5, 0, 3, 0.80},
		{"t1_upgrade_to_t2", -2.1, 1, 2, 0.50},
		{"t1_stay_when_recover", 0.0, 1, 1, 0.25}, // only upgrades; recovery doesn't downgrade
		{"t2_stay_when_recover", -0.5, 2, 2, 0.50},
		{"t2_upgrade_to_t3", -3.2, 2, 3, 0.80},
		{"t3_max", -5.0, 3, 3, 0.80},
	}
	for _, tc := range cases {
		got, ratio := selectHedgeTierForOpen(tc.pnlR, tc.curTier, cfg)
		if got != tc.wantTier || math.Abs(ratio-tc.wantRatio) > 1e-9 {
			t.Errorf("%s: pnlR=%.2f cur=%d → got (%d, %.2f), want (%d, %.2f)",
				tc.name, tc.pnlR, tc.curTier, got, ratio, tc.wantTier, tc.wantRatio)
		}
	}
}

// ─── Hedge favor compute ────────────────────────────────────────────────────

func TestComputeHedgeFavor_LongMainShortHedge(t *testing.T) {
	// Main LONG, hedge SHORT opened at 2290.
	p := &posState{side: "LONG", hedgeQty: 0.1, hedgeEntry: 2290}
	cases := []struct {
		price    float64
		expected float64
	}{
		{2290, 0.0},  // at hedge entry = breakeven
		{2280, 1.0},  // price down 10 → SHORT hedge +$1.0
		{2300, -1.0}, // price up 10 → SHORT hedge -$1.0
	}
	for _, tc := range cases {
		got := computeHedgeFavor(p, tc.price)
		if math.Abs(got-tc.expected) > 1e-6 {
			t.Errorf("price=%.2f: got %.4f, want %.4f", tc.price, got, tc.expected)
		}
	}
}

func TestComputeHedgeFavor_ShortMainLongHedge(t *testing.T) {
	p := &posState{side: "SHORT", hedgeQty: 0.1, hedgeEntry: 2300}
	// price up 10 → LONG hedge +$1.0
	got := computeHedgeFavor(p, 2310)
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("got %.4f, want 1.0", got)
	}
}

// ─── Grid trail ─────────────────────────────────────────────────────────────

func TestGridTrail_TriggerOnPullback(t *testing.T) {
	cfg := defaultTestCfg()
	p := &posState{
		side: "LONG", entryPrice: 2314.0, R: 18.0,
	}
	// peak at 2334 (pnlR=+1.11), retreat to 2324 (+0.56) → pullback 0.55 > 0.5
	pnlR1 := computePnlR(p, 2334.0)
	if pnlR1 < cfg.GridTrailActivatePnlR {
		t.Fatalf("setup error: pnlR1=%.2f", pnlR1)
	}
	p.gridTrailActive = true
	p.gridTrailPeakPnlR = pnlR1

	pnlR2 := computePnlR(p, 2324.0)
	pullback := p.gridTrailPeakPnlR - pnlR2
	if pullback < cfg.GridTrailPullbackR {
		t.Errorf("pullback=%.2f, want >= %.2f", pullback, cfg.GridTrailPullbackR)
	}
}

// ─── Log-based scenario simulation ──────────────────────────────────────────

type simBar struct {
	time   string
	price  float64
	regime string
	tag    string
}

type simSummary struct {
	hedgeOpens      int
	hedgeCloses     int
	lastCloseReason string
	hedgeLockedPnl  float64 // cumulative across all close events
	trailTriggered  bool
	triggerPrice    float64
	triggerPnlR     float64
	peakPnlR        float64
	troughPnlR      float64
}

func simulate(t *testing.T, cfg *Config, p *posState, bars []simBar, label string) simSummary {
	t.Helper()
	s := simSummary{}
	t.Logf("─── %s ───", label)
	t.Logf("%-7s %-8s %-12s %6s %5s %8s %8s %-30s",
		"time", "price", "regime", "pnlR", "tier", "hedge_q", "peak_fv", "note")

	barCount := 0
	for _, b := range bars {
		barCount++
		pnlR := computePnlR(p, b.price)
		if pnlR > s.peakPnlR {
			s.peakPnlR = pnlR
		}
		if pnlR < s.troughPnlR {
			s.troughPnlR = pnlR
		}

		note := b.tag

		// ── Cooldown gate ──
		delta := barCount - p.hedgeClosedBar
		cooldownActive := p.hedgeQty <= 0 && p.hedgeClosedBar > 0 && delta >= 0 &&
			cfg.HedgeReopenCooldownBars > 0 &&
			delta < cfg.HedgeReopenCooldownBars

		// ── Tier escalation (open only, never downgrades) ──
		newTier, targetRatio := selectHedgeTierForOpen(pnlR, p.hedgeTier, cfg)
		if newTier > p.hedgeTier && !cooldownActive {
			targetQty := math.Floor(p.remainQty*targetRatio*1000) / 1000
			if targetQty > p.hedgeQty+0.0005 {
				delta := math.Floor((targetQty-p.hedgeQty)*1000) / 1000
				// Update avg entry
				if p.hedgeQty <= 0 {
					p.hedgeEntry = b.price
				} else {
					p.hedgeEntry = (p.hedgeEntry*p.hedgeQty + b.price*delta) / (p.hedgeQty + delta)
				}
				p.hedgeQty += delta
				p.hedgeActive = true
				p.hedgeTier = newTier
				p.hedgePeakFavor = computeHedgeFavor(p, b.price)
				s.hedgeOpens++
				note += fmt.Sprintf(" OPEN tier%d Δ%.3f@%.2f", newTier, delta, b.price)
			}
		}

		// ── Track peak favor ──
		if p.hedgeQty > 0 {
			f := computeHedgeFavor(p, b.price)
			if f > p.hedgePeakFavor {
				p.hedgePeakFavor = f
			}
		}

		// ── Close criteria (each bar re-evaluates; multiple cycles possible) ──
		if p.hedgeQty > 0 {
			currentFavor := computeHedgeFavor(p, b.price)
			reason := ""
			if p.hedgePeakFavor >= cfg.HedgeMinPeakFavor {
				retreat := p.hedgePeakFavor - currentFavor
				if retreat/p.hedgePeakFavor >= cfg.HedgeTrailPeakPct {
					reason = "hedge_trail"
				}
			}
			if pnlR >= cfg.HedgeMainRecoverPnlR && currentFavor >= 0 {
				reason = "main_recover"
			}
			if reason != "" {
				s.hedgeCloses++
				s.lastCloseReason = reason
				s.hedgeLockedPnl += currentFavor
				note += fmt.Sprintf(" CLOSE %s qty=%.3f lockPnL=%+.3f", reason, p.hedgeQty, currentFavor)
				p.hedgeQty = 0
				p.hedgeActive = false
				p.hedgeTier = 0
				p.hedgePeakFavor = 0
				p.hedgeClosedBar = barCount
			}
		}

		// ── Grid trail ──
		trailMark := ""
		if cfg.GridTrailEnabled && !s.trailTriggered {
			if !p.gridTrailActive && pnlR >= cfg.GridTrailActivatePnlR {
				p.gridTrailActive = true
				p.gridTrailPeakPnlR = pnlR
				trailMark = "ACT"
			}
			if p.gridTrailActive {
				if pnlR > p.gridTrailPeakPnlR {
					p.gridTrailPeakPnlR = pnlR
				}
				pullback := p.gridTrailPeakPnlR - pnlR
				if pullback >= cfg.GridTrailPullbackR {
					trailMark = "TRIGGER"
					s.trailTriggered = true
					s.triggerPrice = b.price
					s.triggerPnlR = pnlR
					if p.hedgeQty > 0 {
						s.hedgeLockedPnl += computeHedgeFavor(p, b.price)
						note += fmt.Sprintf(" hedge_final_close+%.3f", computeHedgeFavor(p, b.price))
						p.hedgeQty = 0
						s.hedgeCloses++
						s.lastCloseReason = "main_closed"
					}
				} else {
					trailMark = "track"
				}
			}
		}

		t.Logf("%-7s %8.2f %-12s %+6.2f %5d %8.3f %8.3f %-30s",
			b.time, b.price, b.regime, pnlR, p.hedgeTier, p.hedgeQty, p.hedgePeakFavor,
			fmt.Sprintf("%s%s", trailMark, func() string {
				if trailMark != "" {
					return " "
				}
				return ""
			}())+note)

		if s.trailTriggered {
			break
		}
	}
	t.Logf("summary: opens=%d closes=%d lastReason=%s cumLockedPnL=%+.3f trailTrig=%v trigPrice=%.2f trigPnlR=%+.2f peak=%+.2f trough=%+.2f",
		s.hedgeOpens, s.hedgeCloses, s.lastCloseReason, s.hedgeLockedPnl,
		s.trailTriggered, s.triggerPrice, s.triggerPnlR, s.peakPnlR, s.troughPnlR)
	return s
}

// ── SHORT 2302.64 — 2026-04-20 logs ──
var scenarioShort_2302_64 = []simBar{
	{"17:10", 2302.64, "RANGE", "OPEN"},
	{"17:15", 2305.00, "RANGE", ""},
	{"17:40", 2309.15, "RANGE", ""},
	{"18:05", 2311.53, "RANGE", ""},
	{"18:20", 2318.00, "RANGE", ""},
	{"18:35", 2315.00, "RANGE", ""},
	{"19:15", 2307.99, "RANGE", ""},
	{"19:45", 2306.93, "RANGE", ""},
	{"20:06", 2309.94, "RANGE", ""},
	{"20:30", 2314.17, "RANGE", ""},
	{"21:15", 2318.30, "STRONG_TREND", ""},
	{"22:00", 2309.46, "RANGE", ""},
	{"22:55", 2290.78, "EXPANSION", ""},
	{"23:00", 2289.55, "STRONG_TREND", ""},
	{"23:05", 2283.05, "STRONG_TREND", "deepest"},
	{"23:10", 2283.79, "STRONG_TREND", ""},
	{"23:15", 2291.68, "RANGE", ""},
	{"23:20", 2296.70, "RANGE", ""},
	{"23:25", 2307.54, "RANGE", ""},
	{"23:30", 2316.60, "RANGE", ""},
}

func TestSimulate_Short_2302_64(t *testing.T) {
	cfg := defaultTestCfg()
	p := &posState{
		side: "SHORT", entryPrice: 2302.64, R: 18.42, initQty: 0.112, remainQty: 0.112,
		mode: modeRange, filled: true,
	}
	summary := simulate(t, cfg, p, scenarioShort_2302_64, "SHORT-2302.64 (new)")
	_ = summary
}

// ── LONG 2314.53 — 2026-04-20 logs (post-restart) ──
var scenarioLong_2314_53 = []simBar{
	{"21:30", 2314.53, "RANGE", "OPEN"},
	{"21:45", 2311.42, "RANGE", ""},
	{"21:49", 2307.16, "RANGE", "layer1 fill"},
	{"22:31", 2309.58, "RANGE", ""},
	{"22:35", 2309.64, "RANGE", ""},
	{"22:40", 2305.35, "RANGE", ""},
	{"22:45", 2301.51, "RANGE", ""},
	{"22:50", 2305.47, "RANGE", ""},
	{"22:55", 2290.78, "EXPANSION", ""},
	{"23:00", 2289.55, "STRONG_TREND", ""},
	{"23:05", 2283.05, "STRONG_TREND", "deepest"},
	{"23:10", 2283.79, "STRONG_TREND", ""},
	{"23:15", 2291.68, "RANGE", ""},
	{"23:20", 2296.70, "RANGE", ""},
	{"23:25", 2307.54, "RANGE", ""},
	{"23:30", 2316.60, "RANGE", ""},
	{"23:35", 2308.16, "RANGE", ""},
}

func TestSimulate_Long_2314_53(t *testing.T) {
	cfg := defaultTestCfg()
	p := &posState{
		side: "LONG", entryPrice: 2314.53, R: 18.52, initQty: 0.111, remainQty: 0.111,
		mode: modeRange, filled: true,
	}
	summary := simulate(t, cfg, p, scenarioLong_2314_53, "LONG-2314.53 (new)")
	_ = summary
}

// ── Adversarial: price keeps dropping, hedge should escalate and profit ──
var scenarioLongCrash = []simBar{
	{"t0", 2300, "RANGE", "OPEN"},
	{"t1", 2290, "RANGE", ""},        // -1R → tier 1 (R=10)
	{"t2", 2285, "RANGE", ""},        // -1.5R
	{"t3", 2280, "EXPANSION", ""},    // -2R → tier 2
	{"t4", 2275, "STRONG_TREND", ""}, // -2.5R
	{"t5", 2270, "STRONG_TREND", ""}, // -3R → tier 3
	{"t6", 2260, "STRONG_TREND", ""}, // -4R (bottom)
	{"t7", 2265, "STRONG_TREND", ""}, // rebound
	{"t8", 2270, "STRONG_TREND", ""}, // bounce
	{"t9", 2280, "RANGE", ""},        // reversal continues
	{"t10", 2290, "RANGE", ""},       // approaching main
	{"t11", 2300, "RANGE", ""},       // back to entry
}

func TestSimulate_LongCrash_DeepTrend(t *testing.T) {
	cfg := defaultTestCfg()
	p := &posState{
		side: "LONG", entryPrice: 2300, R: 10.0, initQty: 0.1, remainQty: 0.1,
		mode: modeRange, filled: true,
	}
	summary := simulate(t, cfg, p, scenarioLongCrash, "LONG-crash-deep-trend")
	// Hedge must escalate through all three tiers on the way down and unwind on the bounce.
	if summary.hedgeOpens < 3 {
		t.Errorf("expected >=3 tier opens on a -4R crash, got %d", summary.hedgeOpens)
	}
	if summary.hedgeCloses == 0 {
		t.Errorf("expected the hedge to close on the rebound, got 0 closes")
	}
}

// ─── Regime-break exit (mechanism 3) ─────────────────────────────────────────

// regimeOpposesGrid: only a trend AGAINST the position side counts as adverse.
func TestRegimeOpposesGrid(t *testing.T) {
	cases := []struct {
		regime  Regime
		dir     int
		side    string
		adverse bool
	}{
		{RegimeStrongTrend, -1, "LONG", true},   // downtrend vs long → adverse
		{RegimeStrongTrend, +1, "LONG", false},  // uptrend aligns with long
		{RegimeExpansion, -1, "LONG", true},     // expansion counts too
		{RegimeStrongTrend, +1, "SHORT", true},  // uptrend vs short → adverse
		{RegimeStrongTrend, -1, "SHORT", false}, // downtrend aligns with short
		{RegimeRange, -1, "LONG", false},        // non-trending regime → never adverse
		{RegimeSlowTrend, -1, "LONG", false},    // slow trend not counted
	}
	for _, tc := range cases {
		s := &AIStrategy{lastRegime: tc.regime, lastTrendDir: tc.dir}
		p := &posState{side: tc.side}
		if got := s.regimeOpposesGrid(p); got != tc.adverse {
			t.Errorf("regime=%s dir=%d side=%s: got %v, want %v", tc.regime, tc.dir, tc.side, got, tc.adverse)
		}
	}
}

// Drive the REAL manageGridRegimeExit through a bar sequence with an unreachable
// pnlR gate (so it never reaches closePos), verifying the adverse-bar counter
// accumulates on adverse bars and resets on aligned/neutral ones.
func TestManageGridRegimeExit_CounterAndReset(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GridRegimeExitEnabled = true
	cfg.GridRegimeExitBars = 3
	cfg.GridRegimeExitPnlR = -50.0 // unreachable → close never fires
	s := &AIStrategy{cfg: cfg, log: zap.NewNop()}
	p := &posState{side: "LONG", entryPrice: 2300, R: 10, mode: modeRange, filled: true, remainQty: 0.1}
	pp := p

	steps := []struct {
		regime     Regime
		dir        int
		price      float64
		wantStreak int
	}{
		{RegimeRange, 0, 2298, 0},        // neutral → 0
		{RegimeStrongTrend, -1, 2296, 1}, // adverse → 1
		{RegimeStrongTrend, -1, 2294, 2}, // adverse → 2
		{RegimeStrongTrend, +1, 2295, 0}, // aligned uptrend → reset
		{RegimeStrongTrend, -1, 2293, 1}, // adverse → 1
		{RegimeStrongTrend, -1, 2291, 2}, // adverse → 2
		{RegimeStrongTrend, -1, 2289, 3}, // adverse → 3 (>= bars, but pnlR gate blocks)
	}
	for i, st := range steps {
		s.lastRegime = st.regime
		s.lastTrendDir = st.dir
		closed := s.manageGridRegimeExit(nil, exchange.Kline{Close: st.price}, p, &pp)
		if closed {
			t.Fatalf("step %d: close fired despite unreachable pnlR gate", i)
		}
		if p.adverseRegimeBars != st.wantStreak {
			t.Errorf("step %d: adverseRegimeBars=%d, want %d", i, p.adverseRegimeBars, st.wantStreak)
		}
	}
}

// The fire decision: exit only when the streak AND the pnlR gate both pass.
// Mirrors manageGridRegimeExit's trigger arithmetic (using the real helpers).
func TestRegimeExit_FireDecision(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GridRegimeExitBars = 3
	cfg.GridRegimeExitPnlR = -1.0
	p := &posState{side: "LONG", entryPrice: 2300, R: 10}
	fire := func(streak int, price float64) bool {
		pnlR := computePnlR(p, price)
		return streak >= cfg.GridRegimeExitBars && pnlR <= cfg.GridRegimeExitPnlR
	}
	if fire(2, 2288) { // pnlR=-1.2 passes gate but streak<3
		t.Error("streak<bars must not fire")
	}
	if fire(3, 2295) { // streak ok but pnlR=-0.5 > -1.0
		t.Error("pnlR above gate must not fire")
	}
	if !fire(3, 2288) { // streak>=3 and pnlR=-1.2 <= -1.0 → fire
		t.Error("streak>=bars AND pnlR<=gate must fire")
	}
}
