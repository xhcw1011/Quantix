package aistrat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/position"
)

// ─── Technical Helpers ───────────────────────────────────────────────────────

func (s *AIStrategy) primaryBars() []exchange.Kline {
	return s.barsByInterval[s.cfg.PrimaryInterval]
}

func (s *AIStrategy) barsForInterval(iv string) []exchange.Kline {
	return s.barsByInterval[iv]
}

// resolveInterval returns iv, or "15m" if iv is empty — the long-standing
// default that predates RegimeInterval/HTFInterval/EntryFilterInterval.
// Centralizes the zero-value fallback so every call site (and any Config{}
// literal built outside DefaultConfig()) behaves consistently.
func resolveInterval(iv string) string {
	if iv == "" {
		return "15m"
	}
	return iv
}

// shouldWarnMissingInterval reports whether a context-interval consumer
// (regimeBars/htfBars/entryFilterBars) should warn: a non-default interval
// was configured but no bars are loaded for it. Skips "15m" itself — the
// default's own emptiness (e.g. very early warmup) isn't a misconfiguration.
func shouldWarnMissingInterval(iv string, barsLoaded int) bool {
	return iv != "15m" && barsLoaded == 0
}

// regimeBars returns the bars detectRegime() classifies against, per
// s.cfg.RegimeInterval (default "15m"). Warns if a non-default interval was
// configured but its data isn't loaded — the silent-fallback trap that made
// the HTF-alignment filter test a no-op.
func (s *AIStrategy) regimeBars() []exchange.Kline {
	iv := resolveInterval(s.cfg.RegimeInterval)
	bars := s.barsForInterval(iv)
	if shouldWarnMissingInterval(iv, len(bars)) {
		s.log.Warn("AI: RegimeInterval configured but no data loaded — falling back",
			zap.String("configured", iv))
	}
	return bars
}

// htfBars returns the bars hourlyTrendDir()/detectHourlyMode() read, per
// s.cfg.HTFInterval (default "15m"). See regimeBars for the warn behavior.
func (s *AIStrategy) htfBars() []exchange.Kline {
	iv := resolveInterval(s.cfg.HTFInterval)
	bars := s.barsForInterval(iv)
	if shouldWarnMissingInterval(iv, len(bars)) {
		s.log.Warn("AI: HTFInterval configured but no data loaded — falling back",
			zap.String("configured", iv))
	}
	return bars
}

// entryFilterBars returns the bars the TrendExhaustPct distance check and
// MTF confidence score read, per s.cfg.EntryFilterInterval (default "15m").
// See regimeBars for the warn behavior.
func (s *AIStrategy) entryFilterBars() []exchange.Kline {
	iv := resolveInterval(s.cfg.EntryFilterInterval)
	bars := s.barsForInterval(iv)
	if shouldWarnMissingInterval(iv, len(bars)) {
		s.log.Warn("AI: EntryFilterInterval configured but no data loaded — falling back",
			zap.String("configured", iv))
	}
	return bars
}

func (s *AIStrategy) getCloses() []float64 {
	bars := s.primaryBars()
	c := make([]float64, len(bars))
	for i, b := range bars {
		c[i] = b.Close
	}
	return c
}

func (s *AIStrategy) calcATR() float64 {
	return s.calcATRN(s.cfg.ATRPeriod)
}

// calcATRN computes ATR over an arbitrary period (bars), same formula as
// calcATR. Used for comparing a short-window ATR against the config-default
// (longer) window, e.g. for the volatility-elevated entry filter.
func (s *AIStrategy) calcATRN(n int) float64 {
	if len(s.primaryBars()) < n+1 {
		n = len(s.primaryBars()) - 1
		if n < 5 {
			return 0
		}
	}
	recent := s.primaryBars()[len(s.primaryBars())-n-1:]
	var sum float64
	for i := 1; i < len(recent); i++ {
		sum += math.Max(recent[i].High-recent[i].Low,
			math.Max(math.Abs(recent[i].High-recent[i-1].Close), math.Abs(recent[i].Low-recent[i-1].Close)))
	}
	return sum / float64(n)
}

// netMove returns the signed price change from the first to the last bar in
// the slice, or 0 if there are fewer than 2 bars.
func netMove(bars []exchange.Kline) float64 {
	if len(bars) < 2 {
		return 0
	}
	return bars[len(bars)-1].Close - bars[0].Close
}

// momentumDecayed reports whether recent net price momentum has weakened
// relative to the preceding window of the same length — a proxy for "the
// move that triggered this trend confirmation is already losing steam."
// lookback <= 0 disables the filter (never blocks). Requires 2×lookback bars
// of history; returns false (don't block) when there isn't enough.
func momentumDecayed(bars []exchange.Kline, lookback int, decayRatio float64) bool {
	if lookback <= 0 || len(bars) < lookback*2 {
		return false
	}
	n := len(bars)
	recentMove := math.Abs(netMove(bars[n-lookback:]))
	priorMove := math.Abs(netMove(bars[n-2*lookback : n-lookback]))
	if priorMove <= 0 {
		return false
	}
	return recentMove/priorMove < decayRatio
}

// volatilityElevated reports whether a short-window ATR is elevated relative
// to a longer-baseline ATR — a proxy for "this regime read is riding the
// aftershock of a recent volatility spike" rather than a steadily-developing
// trend. multiple <= 0 disables the filter (never blocks).
func volatilityElevated(shortATR, longATR, multiple float64) bool {
	if multiple <= 0 || longATR <= 0 {
		return false
	}
	return shortATR > longATR*multiple
}

// trendEntryBlocked reports whether a trend-mode entry should be skipped by
// any of the signal-quality filters (regime age, momentum decay, volatility
// spike, price extension). Each filter is independently config-gated (off by
// default) and evaluated against the current bar's state.
func (s *AIStrategy) trendEntryBlocked(side string, price, atr float64) bool {
	if s.cfg.DisableTrend {
		return true
	}
	if trendAgeBlocked(s.regimeAge, s.cfg.TrendMaxRegimeAge) {
		return true
	}
	if momentumDecayed(s.primaryBars(), s.cfg.TrendMomentumLookback, s.cfg.TrendMomentumDecayRatio) {
		return true
	}
	if volatilityElevated(s.calcATRN(s.cfg.TrendVolShortATRPeriod), s.calcATR(), s.cfg.TrendVolATRMultiple) {
		return true
	}
	swingLow := s.findSwingLow(s.cfg.TrendExtensionSwingBars)
	swingHigh := s.findSwingHigh(s.cfg.TrendExtensionSwingBars)
	if priceExtended(side, price, swingLow, swingHigh, atr, s.cfg.TrendMaxExtensionATR) {
		return true
	}
	if s.cfg.TrendRequireHTFAlign && htfMisaligned(side, s.hourlyTrendDir()) {
		return true
	}
	bars := s.primaryBars()
	if s.cfg.TrendMinVolumeMultiple > 0 && len(bars) > 0 {
		currentVol := bars[len(bars)-1].Volume
		avg := avgVolume(bars, s.cfg.TrendVolumeLookback)
		if volumeInsufficient(currentVol, avg, s.cfg.TrendMinVolumeMultiple) {
			return true
		}
	}
	return false
}

// htfMisaligned reports whether the higher-timeframe trend direction
// (hourlyTrendDir — actually a ~4h EMA-slope read off 15m bars, see
// hourlyTrendDir) actively opposes the entry side. htfDir==0 (no confirmed
// htf trend) does NOT block — only a CONFIRMED opposing htf trend does; the
// absence of confirmation isn't the same as active disagreement (same
// philosophy as trendCutTriggered's hourlyDir gate).
func htfMisaligned(side string, htfDir int) bool {
	switch side {
	case "LONG":
		return htfDir < 0
	case "SHORT":
		return htfDir > 0
	}
	return false
}

// avgVolume returns the mean Volume over the n bars preceding the most
// recent bar (excludes the last bar itself, which is what gets evaluated
// against this baseline). Returns 0 if there isn't enough history.
func avgVolume(bars []exchange.Kline, n int) float64 {
	if n <= 0 || len(bars) < n+1 {
		return 0
	}
	window := bars[len(bars)-1-n : len(bars)-1]
	total := 0.0
	for _, b := range window {
		total += b.Volume
	}
	return total / float64(n)
}

// volumeInsufficient reports whether current volume is too low relative to
// its recent baseline to confirm this regime read as a real move — a proxy
// for "price moved on low participation," a common precursor to fake
// breakouts / reversion. multiple <= 0 or a zero/unavailable baseline
// disables the filter (never blocks).
func volumeInsufficient(currentVol, avgVol, multiple float64) bool {
	if multiple <= 0 || avgVol <= 0 {
		return false
	}
	return currentVol < avgVol*multiple
}

// priceExtended reports whether price has already moved too far from the
// recent swing extreme in the entry direction — a proxy for "chasing a move
// already in progress" rather than catching its start. maxATRDist <= 0
// disables the filter (never blocks).
func priceExtended(side string, price, swingLow, swingHigh, atr, maxATRDist float64) bool {
	if maxATRDist <= 0 || atr <= 0 {
		return false
	}
	if side == "LONG" {
		return price-swingLow > atr*maxATRDist
	}
	return swingHigh-price > atr*maxATRDist
}

func (s *AIStrategy) findSwingLow(n int) float64 {
	if len(s.primaryBars()) < n {
		n = len(s.primaryBars())
	}
	low := math.MaxFloat64
	for i := len(s.primaryBars()) - n; i < len(s.primaryBars()); i++ {
		if s.primaryBars()[i].Low < low {
			low = s.primaryBars()[i].Low
		}
	}
	return low
}
func (s *AIStrategy) findSwingHigh(n int) float64 {
	high := 0.0
	start := len(s.primaryBars()) - n
	if start < 0 {
		start = 0
	}
	for i := start; i < len(s.primaryBars()); i++ {
		if s.primaryBars()[i].High > high {
			high = s.primaryBars()[i].High
		}
	}
	return high
}

// ─── 1h Trend Direction ─────────────────────────────────────────────────────

// hourlyTrendDir returns the trend direction off the HourlyTrendEMA-period EMA on
// 15m bars: +1 bullish, -1 bearish, 0 neutral. Default 16×15m = 4h — the old 20h
// EMA (period 80) lagged badly: it kept reading "bullish" off a prior high while
// price had already turned down, forcing the strategy to buy dips into a fall
// (the -121 long). A ~4h reference tracks the current trend; the slope threshold
// keeps it neutral in a flat range.
func (s *AIStrategy) hourlyTrendDir() int {
	bars15 := s.htfBars()
	emaN := s.cfg.HourlyTrendEMA
	if emaN < 2 || len(bars15) < emaN+2 {
		return 0
	}

	closes := make([]float64, len(bars15))
	for i, b := range bars15 {
		closes[i] = b.Close
	}

	ema := indicator.EMA(closes, emaN)
	if len(ema) < 2 {
		return 0
	}

	// Check last 2 EMA values: both must agree on direction AND the EMA must slope
	// with MEANINGFUL momentum. A bare slopeAt>0 also fires in a flat range where
	// the slow ~20h EMA barely drifts (it lags a prior trend) — that wrongly read
	// "trend" and blocked all counter-trend reversion fades in a flat market.
	// Require |slope| ≥ ema×HourlyTrendMinSlope so a flat/lagging EMA reads neutral
	// (0) and both sides fade in a genuine range; only a real trend suppresses.
	n := len(ema)
	allBull, allBear := true, true
	for i := 0; i < 2; i++ {
		idx := n - 1 - i
		priceAt := closes[len(closes)-1-i]
		emaAt := ema[idx]
		slopeAt := ema[idx] - ema[idx-1]
		thr := emaAt * s.cfg.HourlyTrendMinSlope

		if !(priceAt > emaAt && slopeAt > thr) {
			allBull = false
		}
		if !(priceAt < emaAt && slopeAt < -thr) {
			allBear = false
		}
	}

	if allBull {
		return 1
	}
	if allBear {
		return -1
	}
	return 0
}

// stickyHourlyDir applies hysteresis to the raw 1h trend direction. Once raw
// confirms ±1, the sticky value holds that sign for up to stickyBars subsequent
// neutral (0) readings before decaying to 0; an opposite confirmed reading flips
// immediately. This stops the entry filter (signal.go) from re-allowing
// counter-trend entries on every bounce inside a stair-step trend — the gap that
// let ~half the longs slip through during the 06-23~25 ETH decline. stickyBars<=0
// disables it (raw passes through). Returns (newSticky, newCooldown).
func stickyHourlyDir(raw, prevSticky, cooldown, stickyBars int) (int, int) {
	if stickyBars <= 0 {
		return raw, 0 // hysteresis off: passthrough
	}
	if raw != 0 {
		return raw, stickyBars // confirmed direction: hold + reset cooldown
	}
	if cooldown > 0 {
		return prevSticky, cooldown - 1 // neutral flicker: hold prior dir, decay
	}
	return 0, 0 // cooldown exhausted: decay to neutral
}

// detectHourlyMode determines the 1h management mode for an open position.
// TREND_STRONG: 1h EMA aligned with position + slope confirms → let profits run.
// EXIT_MODE: price crossed 1h EMA against position → prepare to exit.
// TREND_WEAK: everything else → normal management.
// Results are cached and recomputed only when new 15m bars arrive.
func (s *AIStrategy) detectHourlyMode(side string) hourlyMode {
	bars15 := s.htfBars()
	nBars := len(bars15)

	// Return cached value if 15m bars haven't changed
	if nBars == s.hourlyModeBars {
		if side == "LONG" {
			return s.cachedHourlyLong
		}
		return s.cachedHourlyShort
	}

	// Recompute
	if nBars < 82 {
		s.hourlyModeBars = nBars
		s.cachedHourlyLong = hourlyTrendWeak
		s.cachedHourlyShort = hourlyTrendWeak
		if side == "LONG" {
			return s.cachedHourlyLong
		}
		return s.cachedHourlyShort
	}

	closes := make([]float64, nBars)
	for i, b := range bars15 {
		closes[i] = b.Close
	}

	ema80 := indicator.EMA(closes, 80)
	if len(ema80) < 2 {
		s.hourlyModeBars = nBars
		s.cachedHourlyLong = hourlyTrendWeak
		s.cachedHourlyShort = hourlyTrendWeak
		if side == "LONG" {
			return s.cachedHourlyLong
		}
		return s.cachedHourlyShort
	}

	price := closes[len(closes)-1]
	emaNow := ema80[len(ema80)-1]
	slope := ema80[len(ema80)-1] - ema80[len(ema80)-2]

	// Compute for both sides at once
	s.cachedHourlyLong = hourlyTrendWeak
	if price > emaNow && slope > 0 {
		s.cachedHourlyLong = hourlyTrendStrong
	}
	if price < emaNow {
		s.cachedHourlyLong = hourlyExitMode
	}

	s.cachedHourlyShort = hourlyTrendWeak
	if price < emaNow && slope < 0 {
		s.cachedHourlyShort = hourlyTrendStrong
	}
	if price > emaNow {
		s.cachedHourlyShort = hourlyExitMode
	}

	s.hourlyModeBars = nBars
	// NOTE: lastHourlyDir is owned by the per-primary-bar update in generateSignal
	// (with hysteresis). Do NOT set it here — detectHourlyMode also runs on 1m bars
	// and would clobber the sticky value with a raw reading between primary bars.

	if side == "LONG" {
		return s.cachedHourlyLong
	}
	return s.cachedHourlyShort
}

// ─── Regime Detection ────────────────────────────────────────────────────────

// detectRegime identifies the current market structure and sets s.lastTrendDir.
// lastTrendDir: +1 = bullish (price rising), -1 = bearish (price falling), 0 = neutral.
// Only affects new entries — existing positions are managed by their entryRegime.
func (s *AIStrategy) detectRegime() Regime {
	bars := s.primaryBars()
	atr := s.calcATR()
	if atr <= 0 || len(bars) < s.cfg.RegimeN+1 {
		s.lastTrendDir = 0
		return RegimeRange // not enough data, don't trade
	}

	lastBar := bars[len(bars)-1]
	prevBar := bars[len(bars)-2]
	price := lastBar.Close

	// ── Compute overall trend direction ──
	// Prefer RegimeInterval bars (default 15m, 8 bars = 2h) for stable regime
	// detection. Falls back to primary bars if that interval's data isn't loaded.
	var recentBars []exchange.Kline
	bars15 := s.regimeBars()
	regimeN := 8 // 8 × 15m = 2 hours
	if len(bars15) >= regimeN+1 {
		recentBars = bars15[len(bars15)-regimeN:]
	} else {
		recentBars = bars[len(bars)-s.cfg.RegimeN:]
	}
	priceChange := price - recentBars[0].Close
	trendDir := 0
	if priceChange > atr*0.5 {
		trendDir = 1
	} // bullish
	if priceChange < -atr*0.5 {
		trendDir = -1
	} // bearish
	s.lastTrendDir = trendDir

	// ── Efficiency ratio = |net move| / sum(|bar moves|) ──
	// Low efficiency = choppy market (big moves cancel out). Computed unconditionally
	// (and before the Expansion early-return below) so s.lastEfficiency is always
	// fresh for this bar's Meta snapshot, regardless of which regime actually gets
	// returned — an Expansion or early-Range read must not leave a stale value from
	// whatever earlier bar last reached this line.
	totalMoves := 0.0
	for i := 1; i < len(recentBars); i++ {
		totalMoves += math.Abs(recentBars[i].Close - recentBars[i-1].Close)
	}
	efficiency := 0.0
	if totalMoves > 0 {
		efficiency = math.Abs(priceChange) / totalMoves
	}
	s.lastEfficiency = efficiency

	// ── 1. Expansion check (breakout bar + confirmation + trend alignment) ──
	barRange := lastBar.High - lastBar.Low
	body := math.Abs(lastBar.Close - lastBar.Open)
	dirOK := (lastBar.Close > prevBar.Close && lastBar.Close > lastBar.Open) ||
		(lastBar.Close < prevBar.Close && lastBar.Close < lastBar.Open)
	prevBody := math.Abs(prevBar.Close - prevBar.Open)
	prevSameDir := (lastBar.Close-prevBar.Close)*(prevBar.Close-prevBar.Open) > 0
	confirmOK := prevBody > atr*0.5 || prevSameDir
	// Expansion bar must align with overall trend direction.
	// A big bullish bar in a bearish trend = bounce, not breakout.
	barDir := 0
	if lastBar.Close > lastBar.Open {
		barDir = 1
	} else {
		barDir = -1
	}
	trendAligned := trendDir == 0 || barDir == trendDir
	if barRange > atr*s.cfg.ExpansionATRK && body > atr*s.cfg.ExpansionBodyK && dirOK && confirmOK && trendAligned {
		return RegimeExpansion
	}

	// ── 2. Force RANGE when efficiency is low ──
	// Exception: if volume is flagging an incoming move (VolGateWindow > 0 and
	// the composite score clears VolGateRegimeThresh), don't force Range just
	// because price action looks calm — fall through to the trend-strength
	// classification below instead. Cross-asset validated (2026-08-04): grid
	// positions that later needed an emergency cut had a higher volGateScore
	// at entry than ones that reverted normally. VolGateWindow <= 0 disables
	// this (score is always the 0.5 neutral sentinel, never clears a
	// meaningful threshold — original behavior unchanged).
	if efficiency < s.cfg.TrendEfficiencyMin {
		volBars := s.primaryBars()
		if s.cfg.VolGateInterval != "" {
			volBars = s.barsForInterval(s.cfg.VolGateInterval)
			if len(volBars) == 0 {
				s.log.Warn("AI: VolGateInterval configured but no data loaded — falling back to neutral score",
					zap.String("configured", s.cfg.VolGateInterval))
			}
		}
		score := volGateScore(volBars, s.cfg.VolGateWindow, s.cfg.VolGateRatioBars)
		if s.cfg.VolGateWindow <= 0 || score < s.cfg.VolGateRegimeThresh {
			return RegimeRange
		}
	}

	// ── 3. Trend strength = |close_now - close_N| / ATR ──
	trendStrength := math.Abs(priceChange) / atr

	// ── 4. Direction score ──
	dirScore := calcDirectionScore(recentBars)

	// ── 5. Classify ──
	if trendStrength > s.cfg.StrongTrendThreshold && atr/price > s.cfg.StrongTrendMinVol {
		return RegimeStrongTrend
	}
	if trendStrength > s.cfg.SlowTrendThreshold && dirScore > s.cfg.SlowTrendDirScore {
		return RegimeSlowTrend
	}

	return RegimeRange
}

// regimeAgeNext returns the next regimeAge value: incremented when this bar's
// detected regime matches the previous one, reset to 0 on a regime change.
func regimeAgeNext(prevAge int, sameRegime bool) int {
	if sameRegime {
		return prevAge + 1
	}
	return 0
}

// trendAgeBlocked reports whether a trend-mode entry should be blocked because
// the current regime has held for longer than maxAge bars (TrendMaxRegimeAge).
// maxAge <= 0 disables the filter (never blocks).
func trendAgeBlocked(regimeAge, maxAge int) bool {
	return maxAge > 0 && regimeAge > maxAge
}

// pctile returns the fraction of sample values <= val, in [0,1]. Empty → 0.5.
// Mirrors internal/strategy/grid/volgate.go's pctile.
func pctile(val float64, sample []float64) float64 {
	if len(sample) == 0 {
		return 0.5
	}
	c := 0
	for _, x := range sample {
		if x <= val {
			c++
		}
	}
	return float64(c) / float64(len(sample))
}

// volGateScore computes the composite volume-percentile score (0-1) for the
// most recent bar in bars — a continuous "how unusual is this bar's volume"
// read, ported from internal/strategy/grid/volgate.go's vol_hi/vol_up
// composite (score = 0.5*vol_hi + 0.5*vol_up). Reimplemented here as a pure,
// stateless function rather than shared, since detectRegime() consumes it as
// a continuous classification input each bar, not grid.go's stateful
// hysteresis/cooldown on/off switch. Cross-asset validated (2026-08-04): grid
// positions that later hit trend_cut/catastrophic_stop had a meaningfully
// higher entry-time score than grid_tp winners, on both ETHUSDT and BTCUSDT.
// Returns 0.5 (neutral) when there isn't enough history. window <= 0 also
// returns 0.5 (disabled sentinel — callers gate on window > 0 separately).
func volGateScore(bars []exchange.Kline, window, ratioBars int) float64 {
	n := len(bars)
	if window <= 0 || ratioBars <= 0 || n < window || n <= ratioBars {
		return 0.5
	}
	vols := make([]float64, n)
	for i, b := range bars {
		vols[i] = b.Volume
	}
	idx := n - 1
	win := vols[idx-window+1 : idx+1]
	volHi := pctile(vols[idx], win)

	ratios := make([]float64, 0, window)
	for i := idx - window + 1; i <= idx; i++ {
		if i < ratioBars {
			continue
		}
		var s float64
		for j := i - ratioBars; j < i; j++ {
			s += vols[j]
		}
		avg := s / float64(ratioBars)
		if avg > 0 {
			ratios = append(ratios, vols[i]/avg)
		}
	}
	volUp := 0.5
	if len(ratios) > 0 {
		volUp = pctile(ratios[len(ratios)-1], ratios) // last = current bar's ratio
	}
	return 0.5*volHi + 0.5*volUp
}

// gridAgeSizeScale returns the position-size multiplier for a new grid base
// open, as a continuous function of how long the current regime has already
// held (regimeAge) — unlike trendAgeBlocked's binary cutoff, this never
// fully zeroes out a signal, only tapers it. A freshly-confirmed regime
// (age 1) gets full size; each additional bar of age reduces size by
// decayRate, floored at floor so sizing never goes degenerate/zero.
// decayRate <= 0 disables scaling (always returns 1.0).
func gridAgeSizeScale(regimeAge int, decayRate, floor float64) float64 {
	if decayRate <= 0 {
		return 1.0
	}
	scale := 1.0 - float64(regimeAge-1)*decayRate
	if scale < floor {
		return floor
	}
	if scale > 1.0 {
		return 1.0
	}
	return scale
}

// calcDirectionScore measures how consistently bars move in the overall direction.
// Returns 0.0-1.0; higher = more consistent trend.
func calcDirectionScore(bars []exchange.Kline) float64 {
	if len(bars) < 2 {
		return 0
	}
	overallDir := bars[len(bars)-1].Close - bars[0].Close
	if overallDir == 0 {
		return 0
	}
	sameDir := 0
	for i := 1; i < len(bars); i++ {
		delta := bars[i].Close - bars[i-1].Close
		if delta*overallDir > 0 {
			sameDir++
		}
	}
	return float64(sameDir) / float64(len(bars)-1)
}

// adoptInitQty preserves persisted InitQty when sane, else derives a conservative
// estimate from total Qty. Prevents the compounding bug where Redis/DB had
// InitQty=0 (unset on first sync) and the old code set initQty = current total
// (which already includes any grid layers) → next layer placement uses a 50%
// of an inflated base → position grows each restart cycle.
//
// Conservative estimate: assume all GridMaxLayers were filled at GridQtyRatio.
//
//	maxFactor = 1 + GridQtyRatio × GridMaxLayers
//	initQty   = totalQty / maxFactor
//
// If estimate is too low, the cap `totalQty > initQty*2` in manageGrid simply
// blocks new layers — safe-fail, never over-grows.
func (s *AIStrategy) adoptInitQty(persisted, totalQty float64) float64 {
	// Sane persisted value: > 0 and not absurdly larger than current qty.
	if persisted > 0 && persisted <= totalQty+1e-9 {
		return persisted
	}
	maxFactor := 1.0 + s.cfg.GridQtyRatio*float64(s.cfg.GridMaxLayers)
	if maxFactor < 1 {
		maxFactor = 1
	}
	return totalQty / maxFactor
}

// adoptBarsHeld derives an initial barsHeld value from the position's persisted
// created_at, so the staleness check sees true age after a restart. Falls back
// to max(persisted, 10) when DB lookup fails (preserves prior fallback).
func (s *AIStrategy) adoptBarsHeld(side string, persisted int) int {
	openedAt := s.adoptOpenedAt(side)
	if openedAt.IsZero() {
		return max(persisted, 10)
	}
	intervalSec := 300 // 5m default
	if s.cfg.PrimaryInterval == "1m" {
		intervalSec = 60
	}
	if s.cfg.PrimaryInterval == "15m" {
		intervalSec = 900
	}
	if s.cfg.PrimaryInterval == "1h" {
		intervalSec = 3600
	}
	elapsed := int(time.Since(openedAt).Seconds() / float64(intervalSec))
	if elapsed < max(persisted, 10) {
		return max(persisted, 10)
	}
	return elapsed
}

// adoptOpenedAt returns the persisted created_at from strategy_positions for a
// given side, falling back to now() when unavailable. Used to preserve the true
// position age across engine restarts (which otherwise reset filledAt).
func (s *AIStrategy) adoptOpenedAt(side string) time.Time {
	if s.store == nil || s.userID == 0 || s.engineID == "" {
		return time.Now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t, err := s.store.GetStrategyPositionOpenedAt(ctx, s.userID, s.engineID, side)
	if err != nil || t.IsZero() {
		s.log.Debug("AI: opened_at lookup failed, using now()", zap.String("side", side), zap.Error(err))
		return time.Now()
	}
	s.log.Info("AI: adopted opened_at from DB",
		zap.String("side", side), zap.Time("opened_at", t),
		zap.Duration("age", time.Since(t)))
	return t
}

// recoverFromSyncer loads positions from PositionSyncer (Redis/exchange).
func (s *AIStrategy) recoverFromSyncer(currentPrice float64) {
	if s.syncer == nil {
		return
	}

	atr := s.calcATR()

	// Recover LONG — only if bot opened it (has strategy state in Redis: R > 0 or Mode set).
	// Manual positions (opened via exchange UI) have no strategy state → skip them.
	if lp := s.syncer.GetLong(); lp != nil && lp.Qty > 0 {
		// Skip dust positions (< $50 notional). These are residue from manual
		// edits / cleanup attempts, not real strategy state. Adopting them
		// would lock the LONG slot and prevent new entries.
		if lp.EntryPrice > 0 && lp.Qty*lp.EntryPrice < 50 {
			s.log.Info("AI: skipping LONG recovery — dust position below $50 notional",
				zap.Float64("entry", lp.EntryPrice), zap.Float64("qty", lp.Qty),
				zap.Float64("notional", lp.Qty*lp.EntryPrice))
			goto recoverShort
		}
		if lp.R == 0 && lp.Mode == "" {
			s.log.Info("AI: skipping LONG recovery — manual position (no strategy state)",
				zap.Float64("entry", lp.EntryPrice), zap.Float64("qty", lp.Qty))
			goto recoverShort
		}
		entry := lp.EntryPrice
		if entry == 0 {
			entry = currentPrice
		}

		sl := lp.StopLoss
		if sl == 0 {
			slDist := atr * s.cfg.ATRK
			minDist := entry * s.cfg.MinSLDistPct
			if slDist < minDist {
				slDist = minDist
			}
			sl = entry - slDist
		}
		tp := lp.TakeProfit
		if tp == 0 {
			tp = entry + entry*s.cfg.RangeTPPct
		}

		s.longPos = &posState{
			side: "LONG", mode: posMode(0), entryPrice: entry, entryATR: lp.EntryATR,
			gptTPPrice: lp.GptTPPrice,
			initQty:    lp.InitQty, remainQty: lp.Qty,
			R: lp.R, stopLoss: sl, takeProfit: tp,
			trailing: lp.Trailing, peakPrice: lp.PeakPrice,
			tp1RHit: lp.TP1Hit, barsHeld: s.adoptBarsHeld("LONG", lp.BarsHeld),
			filled: true, firstFillSeen: true, filledAt: s.adoptOpenedAt("LONG"),
		}
		s.longPos.initQty = s.adoptInitQty(lp.InitQty, lp.Qty)
		if s.longPos.R == 0 {
			s.longPos.R = math.Abs(entry - sl)
		}
		if s.longPos.entryATR == 0 {
			s.longPos.entryATR = atr
		} // fallback to current ATR
		// R = |entry - SL|, no cap. Consistent with "only ATR determines risk, never limits profit".
		if s.longPos.peakPrice == 0 {
			s.longPos.peakPrice = currentPrice
		}
		if s.longPos.trailing == 0 {
			s.longPos.trailing = sl
		}
		if lp.Mode == "trend" {
			s.longPos.mode = modeTrend
		}
		if lp.Mode == "range" {
			s.longPos.mode = modeRange
		}
		if lp.EntryRegime != "" {
			s.longPos.entryRegime = Regime(lp.EntryRegime)
		} else {
			s.longPos.entryRegime = s.detectRegime()
		}

		// Recover grid layers
		for _, gr := range lp.GridOrders {
			s.longPos.gridOrders = append(s.longPos.gridOrders, &gridOrder{
				entryPrice: gr.EntryPrice, qty: gr.Qty, tp: gr.TP,
				filled: gr.Filled, orderID: gr.OrderID,
			})
		}

		s.loadStagedTPsFromRedis(s.longPos)
		s.log.Info("AI: recovered LONG from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", lp.Qty),
			zap.Float64("stop", sl), zap.Float64("R", s.longPos.R),
			zap.String("regime", string(s.longPos.entryRegime)),
			zap.Int("staged_tps", len(s.longPos.stagedTPs)),
			zap.Int("grid_layers", len(s.longPos.gridOrders)))
	}

recoverShort:
	// Recover SHORT — only if bot opened it.
	if sp := s.syncer.GetShort(); sp != nil && sp.Qty > 0 {
		if sp.EntryPrice > 0 && sp.Qty*sp.EntryPrice < 50 {
			s.log.Info("AI: skipping SHORT recovery — dust position below $50 notional",
				zap.Float64("entry", sp.EntryPrice), zap.Float64("qty", sp.Qty),
				zap.Float64("notional", sp.Qty*sp.EntryPrice))
			return
		}
		if sp.R == 0 && sp.Mode == "" {
			s.log.Info("AI: skipping SHORT recovery — manual position (no strategy state)",
				zap.Float64("entry", sp.EntryPrice), zap.Float64("qty", sp.Qty))
			return
		}
		entry := sp.EntryPrice
		if entry == 0 {
			entry = currentPrice
		}

		sl := sp.StopLoss
		if sl == 0 {
			slDist := atr * s.cfg.ATRK
			minDist := entry * s.cfg.MinSLDistPct
			if slDist < minDist {
				slDist = minDist
			}
			sl = entry + slDist
		}
		tp := sp.TakeProfit
		if tp == 0 {
			tp = entry - entry*s.cfg.RangeTPPct
		}

		s.shortPos = &posState{
			side: "SHORT", mode: posMode(0), entryPrice: entry, entryATR: sp.EntryATR,
			gptTPPrice: sp.GptTPPrice,
			initQty:    sp.InitQty, remainQty: sp.Qty,
			R: sp.R, stopLoss: sl, takeProfit: tp,
			trailing: sp.Trailing, peakPrice: sp.PeakPrice,
			tp1RHit: sp.TP1Hit, barsHeld: s.adoptBarsHeld("SHORT", sp.BarsHeld),
			filled: true, firstFillSeen: true, filledAt: s.adoptOpenedAt("SHORT"),
		}
		s.shortPos.initQty = s.adoptInitQty(sp.InitQty, sp.Qty)
		if s.shortPos.R == 0 {
			s.shortPos.R = math.Abs(entry - sl)
		}
		if s.shortPos.entryATR == 0 {
			s.shortPos.entryATR = atr
		} // fallback to current ATR
		if s.shortPos.peakPrice == 0 {
			s.shortPos.peakPrice = currentPrice
		}
		if s.shortPos.trailing == 0 {
			s.shortPos.trailing = sl
		}
		if sp.Mode == "trend" {
			s.shortPos.mode = modeTrend
		}
		if sp.Mode == "range" {
			s.shortPos.mode = modeRange
		}
		if sp.EntryRegime != "" {
			s.shortPos.entryRegime = Regime(sp.EntryRegime)
		} else {
			s.shortPos.entryRegime = s.detectRegime()
		}

		for _, gr := range sp.GridOrders {
			s.shortPos.gridOrders = append(s.shortPos.gridOrders, &gridOrder{
				entryPrice: gr.EntryPrice, qty: gr.Qty, tp: gr.TP,
				filled: gr.Filled, orderID: gr.OrderID,
			})
		}

		s.loadStagedTPsFromRedis(s.shortPos)
		s.log.Info("AI: recovered SHORT from syncer",
			zap.Float64("entry", entry), zap.Float64("qty", sp.Qty),
			zap.Float64("stop", sl), zap.Float64("R", s.shortPos.R),
			zap.String("regime", string(s.shortPos.entryRegime)),
			zap.Int("staged_tps", len(s.shortPos.stagedTPs)),
			zap.Int("grid_layers", len(s.shortPos.gridOrders)))
	}
}

// syncToRedis writes the current posState to the Syncer for persistence.
func (s *AIStrategy) syncToRedis(pos *posState) {
	if s.syncer == nil || pos == nil {
		return
	}
	modeStr := "range"
	if pos.mode == modeTrend {
		modeStr = "trend"
	}

	// Persist grid layers for recovery
	var gridRecords []position.GridOrderRecord
	for _, g := range pos.gridOrders {
		gridRecords = append(gridRecords, position.GridOrderRecord{
			EntryPrice: g.entryPrice, Qty: g.qty, TP: g.tp,
			Filled: g.filled, OrderID: g.orderID,
		})
	}

	sp := &position.StrategyPosition{
		ExchangePosition: position.ExchangePosition{
			Symbol: s.cfg.Symbol, Side: pos.side,
			Qty: pos.remainQty, EntryPrice: pos.entryPrice,
		},
		Mode: modeStr, StopLoss: pos.stopLoss, TakeProfit: pos.takeProfit,
		Trailing: pos.trailing, PeakPrice: pos.peakPrice,
		R: pos.R, EntryATR: pos.entryATR, GptTPPrice: pos.gptTPPrice, InitQty: pos.initQty,
		TP1Hit: pos.tp1RHit, BarsHeld: pos.barsHeld,
		OrderID: pos.orderID, Filled: pos.filled,
		EntryRegime: string(pos.entryRegime),
		GridOrders:  gridRecords,
	}
	s.syncer.UpdatePosition(context.Background(), sp)
}

// syncRemove clears a position from Syncer.
func (s *AIStrategy) syncRemove(side string) {
	if s.syncer == nil {
		return
	}
	s.syncer.RemovePosition(context.Background(), side)
	s.deleteStagedTPsFromRedis(side)
}

// stagedTPRedisKey returns the Redis key for staged TP records.
func (s *AIStrategy) stagedTPRedisKey(side string) string {
	return fmt.Sprintf("quantix:staged_tp:%s:%s:%s", s.engineID, s.cfg.Symbol, side)
}

// saveStagedTPsToRedis persists TP records for tracking and restart recovery.
func (s *AIStrategy) saveStagedTPsToRedis(pos *posState) {
	if s.rdb == nil || pos == nil || len(pos.stagedTPs) == 0 {
		return
	}
	data, err := json.Marshal(pos.stagedTPs)
	if err != nil {
		return
	}
	s.rdb.Set(context.Background(), s.stagedTPRedisKey(pos.side), string(data), 0)
}

// loadStagedTPsFromRedis loads TP records on recovery.
// Records are loaded for reference, but stagedTPPlaced is left FALSE so that
// the next OnBar re-verifies protective orders on the exchange via recovery.
// Exchange TP/SL orders are preserved across restarts (not cancelled on shutdown).
func (s *AIStrategy) loadStagedTPsFromRedis(pos *posState) {
	if s.rdb == nil || pos == nil {
		return
	}
	val, err := s.rdb.Get(context.Background(), s.stagedTPRedisKey(pos.side)).Result()
	if err != nil || val == "" {
		return
	}
	var records []stagedTPRecord
	if err := json.Unmarshal([]byte(val), &records); err != nil {
		return
	}
	pos.stagedTPs = records
	// stagedTPPlaced stays false — recovery flow will re-verify exchange orders.
}

// deleteStagedTPsFromRedis removes TP records when position is closed.
func (s *AIStrategy) deleteStagedTPsFromRedis(side string) {
	if s.rdb == nil {
		return
	}
	s.rdb.Del(context.Background(), s.stagedTPRedisKey(side))
}

// ─── Technical BUY Signal (replaces GPT for LONG direction) ─────────────────
// techBuySignal dispatches based on regime:
// STRONG_TREND/EXPANSION/SLOW_TREND → breakout (directional markets need momentum signals)
// RANGE → reversion (sideways markets need mean-reversion signals)
func (s *AIStrategy) techBuySignal() (conf float64, entry float64) {
	if s.lastRegime == RegimeRange {
		return s.reversionBuySignal()
	}
	return s.breakoutBuySignal()
}

func (s *AIStrategy) techSellSignal() (conf float64, entry float64) {
	if s.lastRegime == RegimeRange {
		return s.reversionSellSignal()
	}
	return s.breakoutSellSignal()
}

// ─── Trend Breakout Signals (STRONG_TREND / EXPANSION) ──────────────────────
// Low-lag: price breaks N-bar high/low + momentum confirmation.
// No EMA crossover, no 15m confirmation — react to structure, not averages.

func (s *AIStrategy) breakoutBuySignal() (conf float64, entry float64) {
	bars := s.primaryBars()
	if len(bars) < 20 {
		return 0, 0
	}

	price := bars[len(bars)-1].Close
	lookback := 10 // breakout window

	// Find highest high of last N bars (excluding current)
	highestHigh := 0.0
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if i < 0 {
			continue
		}
		if bars[i].High > highestHigh {
			highestHigh = bars[i].High
		}
	}

	// Price must break above recent high
	if price <= highestHigh {
		return 0, 0
	}

	curBar := bars[len(bars)-1]

	// RSI: must not be extremely overbought (> 80 = exhaustion)
	closes := s.getCloses()
	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi > 80 {
		return 0, 0
	}

	conf = 0.75
	// Breakout strength
	breakoutPct := (price - highestHigh) / highestHigh
	if breakoutPct > 0.001 {
		conf += 0.05
	}
	if breakoutPct > 0.003 {
		conf += 0.05
	}
	// Bullish candle is a bonus, not a requirement
	if curBar.Close > curBar.Open {
		conf += 0.05
	}

	// Volume confirmation
	if len(bars) > 20 {
		avgVol := 0.0
		for i := len(bars) - 21; i < len(bars)-1; i++ {
			avgVol += bars[i].Volume
		}
		avgVol /= 20
		if curBar.Volume > avgVol*1.2 {
			conf += 0.05
		}
	}

	if conf > 0.95 {
		conf = 0.95
	}

	// Breakout = urgency: enter at market price, don't wait for pullback.
	entry = math.Round(price*100) / 100

	return conf, entry
}

func (s *AIStrategy) breakoutSellSignal() (conf float64, entry float64) {
	bars := s.primaryBars()
	if len(bars) < 20 {
		return 0, 0
	}

	price := bars[len(bars)-1].Close
	lookback := 10

	// Find lowest low of last N bars (excluding current)
	lowestLow := math.MaxFloat64
	for i := len(bars) - lookback - 1; i < len(bars)-1; i++ {
		if i < 0 {
			continue
		}
		if bars[i].Low < lowestLow {
			lowestLow = bars[i].Low
		}
	}

	// Price must break below recent low
	if price >= lowestLow {
		return 0, 0
	}

	curBar := bars[len(bars)-1]

	// RSI: must not be extremely oversold (< 20 = exhaustion)
	closes := s.getCloses()
	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 20 {
		return 0, 0
	}

	conf = 0.75
	breakoutPct := (lowestLow - price) / lowestLow
	if breakoutPct > 0.001 {
		conf += 0.05
	}
	if breakoutPct > 0.003 {
		conf += 0.05
	}
	// Bearish candle is a bonus, not a requirement
	if curBar.Close < curBar.Open {
		conf += 0.05
	}

	if len(bars) > 20 {
		avgVol := 0.0
		for i := len(bars) - 21; i < len(bars)-1; i++ {
			avgVol += bars[i].Volume
		}
		avgVol /= 20
		if curBar.Volume > avgVol*1.2 {
			conf += 0.05
		}
	}

	if conf > 0.95 {
		conf = 0.95
	}

	// Breakout = urgency: enter at market price.
	entry = math.Round(price*100) / 100

	return conf, entry
}

// ─── Mean Reversion Signals (RANGE / SLOW_TREND) ────────────────────────────
// Fade extremes: RSI oversold/overbought + Bollinger Band touch.
// Counter-trend by design — profit from range oscillation.

func (s *AIStrategy) reversionBuySignal() (conf float64, entry float64) {
	closes := s.getCloses()
	if len(closes) < 30 {
		return 0, 0
	}

	price := closes[len(closes)-1]

	// BB is the SOLE hard condition: price within 0.5% of lower band
	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	if len(bb.Lower) == 0 {
		return 0, 0
	}
	bbLower := bb.Lower[len(bb.Lower)-1]
	bbUpper := bb.Upper[len(bb.Upper)-1]
	bbMiddle := bb.Middle[len(bb.Middle)-1]

	if price > bbLower*1.005 {
		return 0, 0
	}
	if price >= bbMiddle {
		return 0, 0
	} // lower-half only — kills the long-bias tie vs the sell signal in a BB squeeze

	// Base confidence: 0.76 ensures entry when price touches BB band (RangeEntryConf=0.75).
	// Bonuses from RSI and BB penetration push conf higher for stronger signals.
	conf = 0.76
	if price < bbLower {
		conf += 0.05
	} // actually below band

	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 40 {
		conf += 0.03
	}
	if rsi < 35 {
		conf += 0.03
	}
	if rsi < 30 {
		conf += 0.03
	}

	if conf > 0.95 {
		conf = 0.95
	}

	atr := s.calcATR()
	entryBuf := atr * 0.2
	entry = math.Round((price-entryBuf)*100) / 100

	s.lastBBMiddle = bbMiddle
	s.lastBBLower = bbLower
	s.lastBBUpper = bbUpper

	return conf, entry
}

func (s *AIStrategy) reversionSellSignal() (conf float64, entry float64) {
	closes := s.getCloses()
	if len(closes) < 30 {
		return 0, 0
	}

	price := closes[len(closes)-1]

	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	if len(bb.Upper) == 0 {
		return 0, 0
	}
	bbLower := bb.Lower[len(bb.Lower)-1]
	bbUpper := bb.Upper[len(bb.Upper)-1]
	bbMiddle := bb.Middle[len(bb.Middle)-1]

	if price < bbUpper*0.995 {
		return 0, 0
	}
	if price <= bbMiddle {
		return 0, 0
	} // upper-half only — kills the long-bias tie vs the buy signal in a BB squeeze

	conf = 0.76
	if price > bbUpper {
		conf += 0.05
	}

	rsiVals := indicator.RSI(closes, s.cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi > 60 {
		conf += 0.03
	}
	if rsi > 65 {
		conf += 0.03
	}
	if rsi > 70 {
		conf += 0.03
	}

	if conf > 0.95 {
		conf = 0.95
	}

	atr := s.calcATR()
	entryBuf := atr * 0.2
	entry = math.Round((price+entryBuf)*100) / 100

	s.lastBBMiddle = bbMiddle
	s.lastBBLower = bbLower
	s.lastBBUpper = bbUpper

	return conf, entry
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// logEvent writes a trade event to DB for persistent analysis.
func (s *AIStrategy) logEvent(eventType, side, reason string, price, entryPrice, qty, confidence, pnl float64, details string) {
	if s.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.store.InsertTradeEvent(ctx, data.TradeEvent{
			UserID: s.userID, EngineID: s.engineID, Symbol: s.cfg.Symbol,
			EventType: eventType, Side: side, Price: price, EntryPrice: entryPrice,
			Qty: qty, Confidence: confidence, MTFScore: s.lastMTFScore,
			PnL: pnl, Reason: reason, Details: details,
		})
	}()
}
