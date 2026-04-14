// AI strategy backtester — matches live signal.go + manage.go logic.
//
// Uses Redis-cached GPT signals + DB klines. No GPT API calls needed.
//
//   go run ./cmd/ai-backtest -days 14
//   go run ./cmd/ai-backtest -days 14 -atrk 3.5 -trailing 3.5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
)

// ─── Config (mirrors live DefaultConfig) ─────────────────────────────────────

type BTConfig struct {
	Capital           float64
	RiskPerTrade      float64
	ATRK              float64 // SLOW_TREND SL
	SwingSLMinATR     float64
	SwingSLMaxATR     float64
	TrailingATRK      float64
	ConfThreshold     float64 // entry confidence
	RegimeEntryConf   float64
	CounterTrendCap   float64
	TrendEffMin       float64 // efficiency ratio filter
	TPLevel           float64 // R-multiple for TP
	TPQtySplit        float64 // fraction closed at TP
	GptTPMinR         float64
	GptTPMaxR         float64
	BounceTPR         float64
	ReversalConf      float64
	AccumBaseThresh   float64
	SignalDecay       float64
	SignalAccumMax    float64
	BoostMinConf      float64
	MinHoldBars       int
	MinTrendBars      int
	EntryOffsetPct    float64
	FeePct            float64 // one-way fee
	RegimeN           int
	StrongTrendThresh float64
	SlowTrendThresh   float64
	SlowTrendDirScore float64
	ATRPeriod         int
	RSIPeriod         int
	EMAFast           int
	EMASlow           int
	MACDFast          int
	MACDSlow          int
	MACDSignal        int
}

func defaultBTConfig() BTConfig {
	return BTConfig{
		Capital: 80, RiskPerTrade: 0.03,
		ATRK: 3.5, SwingSLMinATR: 2.5, SwingSLMaxATR: 4.0, TrailingATRK: 3.5,
		ConfThreshold: 0.82, RegimeEntryConf: 0.80, CounterTrendCap: 0.25,
		TrendEffMin: 0.30,
		TPLevel: 0.7, TPQtySplit: 0.60, GptTPMinR: 0.30, GptTPMaxR: 1.0,
		BounceTPR: 0.40, ReversalConf: 0.75,
		AccumBaseThresh: 0.30, SignalDecay: 0.80, SignalAccumMax: 0.85,
		BoostMinConf: 0.70,
		MinHoldBars: 3, MinTrendBars: 5,
		EntryOffsetPct: 0.00068, FeePct: 0.0002, // maker 0.02%
		RegimeN: 20, StrongTrendThresh: 2.5, SlowTrendThresh: 1.5, SlowTrendDirScore: 0.60,
		ATRPeriod: 60, RSIPeriod: 14, EMAFast: 20, EMASlow: 50,
		MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
	}
}

// ─── GPT Signal from Redis ───────────────────────────────────────────────────

type GPTSignal struct {
	Time       int64   `json:"time"`
	Price      float64 `json:"price"`
	ATR        float64 `json:"atr"`
	LongConf   float64 `json:"long_conf"`
	ShortConf  float64 `json:"short_conf"`
	LongEntry  float64 `json:"long_entry"`
	ShortEntry float64 `json:"short_entry"`
}

// ─── Trade ───────────────────────────────────────────────────────────────────

type Trade struct {
	Side       string
	Entry, Exit float64
	Qty, Remain float64
	R, PnL, PnLR float64
	Reason     string
	EntryTime, ExitTime time.Time
	Bars       int
}

// ─── Position ────────────────────────────────────────────────────────────────

type pos struct {
	side      string
	entry     float64
	qty       float64
	remain    float64
	stop      float64
	trail     float64
	peakP     float64
	R         float64
	entryATR  float64
	gptTP     float64
	tp        float64
	tpQty     float64
	tp1Hit    bool
	eBar      int
	held      int
	entryTime time.Time
}

func main() {
	days := flag.Int("days", 14, "days of history")
	atrk := flag.Float64("atrk", 0, "override ATRK")
	trailing := flag.Float64("trailing", 0, "override TrailingATRK")
	risk := flag.Float64("risk", 0, "override RiskPerTrade")
	conf := flag.Float64("conf", 0, "override RegimeEntryConf")
	effMin := flag.Float64("eff", 0, "override TrendEfficiencyMin")
	tpLevel := flag.Float64("tp", 0, "override TPLevel")
	flag.Parse()

	cfg := defaultBTConfig()
	if *atrk > 0 { cfg.ATRK = *atrk }
	if *trailing > 0 { cfg.TrailingATRK = *trailing }
	if *risk > 0 { cfg.RiskPerTrade = *risk }
	if *conf > 0 { cfg.RegimeEntryConf = *conf }
	if *effMin > 0 { cfg.TrendEffMin = *effMin }
	if *tpLevel > 0 { cfg.TPLevel = *tpLevel }

	log, _ := zap.NewProduction()
	appCfg, _ := config.Load("config/config.yaml")
	ctx := context.Background()

	store, err := data.New(ctx, appCfg.Database.DSN(), log)
	if err != nil { fmt.Fprintf(os.Stderr, "db: %v\n", err); os.Exit(1) }
	defer store.Close()

	// Load klines
	nBars := *days * 24 * 12 // 5m bars per day = 288
	klines, err := store.GetLatestKlines(ctx, "ETHUSDT", "5m", nBars)
	if err != nil { fmt.Fprintf(os.Stderr, "klines: %v\n", err); os.Exit(1) }
	// DB returns DESC order, reverse to chronological
	for i, j := 0, len(klines)-1; i < j; i, j = i+1, j-1 {
		klines[i], klines[j] = klines[j], klines[i]
	}
	fmt.Printf("Loaded %d 5m bars: %s → %s\n", len(klines),
		klines[0].OpenTime.Format("01-02 15:04"),
		klines[len(klines)-1].OpenTime.Format("01-02 15:04"))

	// Load GPT signals from Redis
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	raw, err := rdb.LRange(ctx, "quantix:signals:ETHUSDT:5m", 0, -1).Result()
	if err != nil { fmt.Fprintf(os.Stderr, "redis: %v\n", err); os.Exit(1) }

	sigMap := map[int64]GPTSignal{}
	for _, r := range raw {
		var s GPTSignal
		json.Unmarshal([]byte(r), &s)
		// Align signal time to 5m bar open: floor to nearest 5min
		aligned := s.Time - (s.Time % 300)
		sigMap[aligned] = s
	}
	fmt.Printf("Loaded %d GPT signals from Redis (aligned to 5m bars)\n", len(sigMap))

	// Run backtest
	trades, equity, maxDD, totalFees := backtest(klines, sigMap, cfg)

	// Results
	fmt.Printf("\n═══ BACKTEST RESULTS ═══\n")
	fmt.Printf("Config: ATRK=%.1f Trail=%.1f Risk=%.0f%% Conf=%.2f EffMin=%.2f TP=%.1fR\n",
		cfg.ATRK, cfg.TrailingATRK, cfg.RiskPerTrade*100, cfg.RegimeEntryConf, cfg.TrendEffMin, cfg.TPLevel)

	wins, losses := 0, 0
	var totalPnL, avgWinR, avgLossR float64
	for _, t := range trades {
		totalPnL += t.PnL
		if t.PnL > 0 { wins++; avgWinR += t.PnLR } else { losses++; avgLossR += t.PnLR }
	}
	if wins > 0 { avgWinR /= float64(wins) }
	if losses > 0 { avgLossR /= float64(losses) }

	wr := 0.0
	if len(trades) > 0 { wr = float64(wins) / float64(len(trades)) * 100 }
	fmt.Printf("Trades: %d (W:%d L:%d WR:%.0f%%)\n", len(trades), wins, losses, wr)
	fmt.Printf("PnL: $%.2f | Fees: $%.2f\n", totalPnL, totalFees)
	fmt.Printf("Equity: $%.2f → $%.2f (%+.1f%%)\n", cfg.Capital, equity, (equity/cfg.Capital-1)*100)
	fmt.Printf("MaxDD: %.1f%% | Avg Win: %.2fR | Avg Loss: %.2fR\n", maxDD*100, avgWinR, avgLossR)

	if len(trades) > 0 {
		fmt.Println("\nTrades:")
		for i, t := range trades {
			fmt.Printf("  %d. %s %s %.3f @%.2f→%.2f $%+.2f (%.1fR) [%s] %dbars\n",
				i+1, t.EntryTime.Format("01-02 15:04"), t.Side, t.Qty,
				t.Entry, t.Exit, t.PnL, t.PnLR, t.Reason, t.Bars)
		}
	}
}

// ─── Backtest Engine ─────────────────────────────────────────────────────────

func backtest(klines []exchange.Kline, sigMap map[int64]GPTSignal, cfg BTConfig) ([]Trade, float64, float64, float64) {
	eq := cfg.Capital
	peak := eq
	maxDD := 0.0
	totalFees := 0.0
	var trades []Trade
	var p *pos

	// State
	accumLong, accumShort := 0.0, 0.0
	lastTrendDir := 0
	stopBar := -1

	closes := make([]float64, 0, len(klines))

	for i, k := range klines {
		closes = append(closes, k.Close)
		price := k.Close

		// ── Manage existing position ──
		if p != nil {
			p.held++
			if p.side == "LONG" && price > p.peakP { p.peakP = price }
			if p.side == "SHORT" && price < p.peakP { p.peakP = price }

			pnlR := 0.0
			if p.R > 0 {
				if p.side == "LONG" { pnlR = (price - p.entry) / p.R }
				if p.side == "SHORT" { pnlR = (p.entry - price) / p.R }
			}

			closeTrade := func(reason string, closePrice float64) {
				pnl := 0.0
				if p.side == "LONG" { pnl = (closePrice - p.entry) * p.remain }
				if p.side == "SHORT" { pnl = (p.entry - closePrice) * p.remain }
				f := closePrice * p.remain * cfg.FeePct
				totalFees += f
				pnl -= f
				eq += pnl
				trades = append(trades, Trade{
					Side: p.side, Entry: p.entry, Exit: closePrice,
					Qty: p.qty, Remain: p.remain, R: p.R,
					PnL: pnl, PnLR: pnlR, Reason: reason,
					EntryTime: p.entryTime, ExitTime: k.OpenTime,
					Bars: i - p.eBar,
				})
				if reason == "stop_loss" { stopBar = i }
				accumLong = 0
				accumShort = 0
				p = nil
			}

			// SL
			if (p.side == "LONG" && price <= p.stop) || (p.side == "SHORT" && price >= p.stop) {
				closeTrade("stop_loss", price)
				goto updateDD
			}

			if p.held < cfg.MinHoldBars { goto updateDD }

			// TP check (partial)
			if !p.tp1Hit && p.tp > 0 {
				if (p.side == "LONG" && price >= p.tp) || (p.side == "SHORT" && price <= p.tp) {
					// Close TPQtySplit fraction at TP
					cq := math.Floor(p.qty*cfg.TPQtySplit*1000) / 1000
					if cq > p.remain { cq = p.remain }
					pnl := 0.0
					if p.side == "LONG" { pnl = (p.tp - p.entry) * cq }
					if p.side == "SHORT" { pnl = (p.entry - p.tp) * cq }
					f := p.tp * cq * cfg.FeePct
					totalFees += f
					pnl -= f
					eq += pnl
					p.remain -= cq
					p.tp1Hit = true
					if p.remain <= 0.001 {
						trades = append(trades, Trade{
							Side: p.side, Entry: p.entry, Exit: p.tp,
							Qty: p.qty, R: p.R, PnL: pnl, PnLR: pnlR,
							Reason: "TP", EntryTime: p.entryTime, ExitTime: k.OpenTime,
							Bars: i - p.eBar,
						})
						p = nil
						goto updateDD
					}
					trades = append(trades, Trade{
						Side: p.side, Entry: p.entry, Exit: p.tp,
						Qty: cq, R: p.R, PnL: pnl, PnLR: pnlR,
						Reason: "TP_partial", EntryTime: p.entryTime, ExitTime: k.OpenTime,
						Bars: i - p.eBar,
					})
				}
			}

			if p == nil { goto updateDD }
			if p.held < cfg.MinTrendBars { goto updateDD }

			// Trailing
			{
				atr := calcATR(closes, cfg.ATRPeriod)
				trailATR := math.Max(p.entryATR, atr)
				trailDist := trailATR * cfg.TrailingATRK
				var newTrail float64
				if p.side == "LONG" {
					newTrail = p.peakP - trailDist
					if newTrail < p.stop { newTrail = p.stop }
					if newTrail > p.trail { p.trail = newTrail }
					if price <= p.trail && p.trail > p.stop {
						closeTrade("trailing", price)
						goto updateDD
					}
				} else {
					newTrail = p.peakP + trailDist
					if newTrail > p.stop { newTrail = p.stop }
					if p.trail == 0 || newTrail < p.trail { p.trail = newTrail }
					if p.trail > 0 && p.trail < p.stop && price >= p.trail {
						closeTrade("trailing", price)
						goto updateDD
					}
				}
			}

			// Bounce TP (remaining after partial TP)
			if p != nil && p.tp1Hit && p.remain > 0 && pnlR > 0 {
				bounce := cfg.BounceTPR * p.R
				if p.side == "LONG" && p.peakP-price >= bounce {
					closeTrade("bounce_tp", price)
					goto updateDD
				}
				if p.side == "SHORT" && price-p.peakP >= bounce {
					closeTrade("bounce_tp", price)
					goto updateDD
				}
			}

			// GPT reversal check
			if p != nil && p.held >= cfg.MinTrendBars {
				if sig, ok := sigMap[k.OpenTime.Unix()]; ok {
					revConf := 0.0
					if p.side == "LONG" { revConf = sig.ShortConf }
					if p.side == "SHORT" { revConf = sig.LongConf }
					if revConf >= cfg.ReversalConf {
						closeTrade("reversal", price)
						goto updateDD
					}
				}
			}
		}

		// ── Entry logic ──
		if p == nil && i > cfg.RegimeN && stopBar != i {
			// Regime detection
			regime, trendDir := detectRegime(closes, cfg)
			lastTrendDir = trendDir

			// Decay accumulation
			accumLong *= cfg.SignalDecay
			accumShort *= cfg.SignalDecay
			if accumLong < 0.01 { accumLong = 0 }
			if accumShort < 0.01 { accumShort = 0 }

			// GPT signal
			sig, hasSig := sigMap[k.OpenTime.Unix()]
			if !hasSig { goto updateDD }

			longConf := sig.LongConf
			shortConf := sig.ShortConf
			longEntry := sig.LongEntry
			shortEntry := sig.ShortEntry

			// Counter-trend cap
			if lastTrendDir < 0 && longConf > cfg.CounterTrendCap { longConf = cfg.CounterTrendCap }
			if lastTrendDir > 0 && shortConf > cfg.CounterTrendCap { shortConf = cfg.CounterTrendCap }

			// Accumulation
			if longConf > cfg.AccumBaseThresh { accumLong += longConf - cfg.AccumBaseThresh }
			if shortConf > cfg.AccumBaseThresh { accumShort += shortConf - cfg.AccumBaseThresh }
			if accumLong > cfg.SignalAccumMax { accumLong = cfg.SignalAccumMax }
			if accumShort > cfg.SignalAccumMax { accumShort = cfg.SignalAccumMax }
			if accumLong > 0 && accumShort > 0 {
				d := accumLong - accumShort
				if d > 0 { accumLong = d; accumShort = 0 } else { accumShort = -d; accumLong = 0 }
			}

			effLong := math.Max(longConf, accumLong)
			effShort := math.Max(shortConf, accumShort)

			// EMA slope filter
			if len(closes) >= 22 {
				ema20 := indicator.EMA(closes, cfg.EMAFast)
				if len(ema20) >= 2 {
					slope := ema20[len(ema20)-1] - ema20[len(ema20)-2]
					if slope < 0 { effLong = 0; accumLong = 0 }
					if slope > 0 && lastTrendDir >= 0 { effShort = 0; accumShort = 0 }
				}
			}

			// Tech buy signal
			techConf, techEntry := techBuySignal(closes, cfg)
			if techConf > effLong {
				effLong = techConf
				if techEntry > 0 { longEntry = techEntry }
			}

			// Entry threshold
			entryConfLong := cfg.ConfThreshold
			entryConfShort := cfg.ConfThreshold
			if regime == "STRONG_TREND" || regime == "EXPANSION" {
				if lastTrendDir >= 0 { entryConfLong = cfg.RegimeEntryConf }
				if lastTrendDir <= 0 { entryConfShort = cfg.RegimeEntryConf }
			} else if regime == "SLOW_TREND" {
				slow := (cfg.RegimeEntryConf + cfg.ConfThreshold) / 2
				if lastTrendDir >= 0 { entryConfLong = slow }
				if lastTrendDir <= 0 { entryConfShort = slow }
			}

			// Single direction
			if effLong >= entryConfLong && effShort >= entryConfShort {
				if effLong >= effShort { effShort = 0 } else { effLong = 0 }
			}

			atr := calcATR(closes, cfg.ATRPeriod)

			// Open LONG
			if effLong >= entryConfLong {
				entry := price - price*cfg.EntryOffsetPct
				if longEntry > 0 && longEntry < price { entry = longEntry }
				entry = math.Round(entry*100) / 100

				sl := calcSL("LONG", entry, atr, closes, cfg, regime)
				R := math.Abs(entry - sl)
				if R <= 0 { goto updateDD }
				qty := math.Floor(eq*cfg.RiskPerTrade/R*1000) / 1000
				if qty <= 0 { goto updateDD }

				// TP
				tp := calcTP("LONG", entry, R, shortEntry, cfg)
				tpQty := math.Floor(qty*cfg.TPQtySplit*1000) / 1000

				f := entry * qty * cfg.FeePct; totalFees += f; eq -= f
				p = &pos{side: "LONG", entry: entry, qty: qty, remain: qty,
					stop: sl, trail: sl, peakP: entry, R: R, entryATR: atr,
					gptTP: shortEntry, tp: tp, tpQty: tpQty,
					eBar: i, entryTime: k.OpenTime}
			}

			// Open SHORT
			if p == nil && effShort >= entryConfShort && regime != "RANGE" {
				entry := price + price*cfg.EntryOffsetPct
				if shortEntry > 0 && shortEntry > price { entry = shortEntry }
				entry = math.Round(entry*100) / 100

				sl := calcSL("SHORT", entry, atr, closes, cfg, regime)
				R := math.Abs(entry - sl)
				if R <= 0 { goto updateDD }
				qty := math.Floor(eq*cfg.RiskPerTrade/R*1000) / 1000
				if qty <= 0 { goto updateDD }

				tp := calcTP("SHORT", entry, R, longEntry, cfg)
				tpQty := math.Floor(qty*cfg.TPQtySplit*1000) / 1000

				f := entry * qty * cfg.FeePct; totalFees += f; eq -= f
				p = &pos{side: "SHORT", entry: entry, qty: qty, remain: qty,
					stop: sl, trail: sl, peakP: entry, R: R, entryATR: atr,
					gptTP: longEntry, tp: tp, tpQty: tpQty,
					eBar: i, entryTime: k.OpenTime}
			}
		}

	updateDD:
		if eq > peak { peak = eq }
		d := (peak - eq) / peak
		if d > maxDD { maxDD = d }
	}

	// Close open position at end
	if p != nil {
		price := klines[len(klines)-1].Close
		pnl := 0.0
		if p.side == "LONG" { pnl = (price - p.entry) * p.remain }
		if p.side == "SHORT" { pnl = (p.entry - price) * p.remain }
		f := price * p.remain * cfg.FeePct; totalFees += f; pnl -= f; eq += pnl
		trades = append(trades, Trade{
			Side: p.side, Entry: p.entry, Exit: price, Qty: p.remain, R: p.R,
			PnL: pnl, Reason: "end", EntryTime: p.entryTime,
			ExitTime: klines[len(klines)-1].OpenTime, Bars: len(klines) - 1 - p.eBar,
		})
	}
	return trades, eq, maxDD, totalFees
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func calcATR(closes []float64, period int) float64 {
	if len(closes) < period+1 { return 5.0 }
	sum := 0.0
	for i := len(closes) - period; i < len(closes); i++ {
		sum += math.Abs(closes[i] - closes[i-1])
	}
	return sum / float64(period)
}

func detectRegime(closes []float64, cfg BTConfig) (string, int) {
	n := cfg.RegimeN
	if len(closes) < n+1 { return "RANGE", 0 }

	window := closes[len(closes)-n-1:]
	priceChange := window[len(window)-1] - window[0]
	atr := calcATR(closes, cfg.ATRPeriod)
	if atr <= 0 { return "RANGE", 0 }

	trendDir := 0
	if priceChange > atr*0.5 { trendDir = 1 }
	if priceChange < -atr*0.5 { trendDir = -1 }

	// Efficiency ratio
	totalMoves := 0.0
	for i := 1; i < len(window); i++ {
		totalMoves += math.Abs(window[i] - window[i-1])
	}
	eff := 0.0
	if totalMoves > 0 { eff = math.Abs(priceChange) / totalMoves }
	if eff < cfg.TrendEffMin { return "RANGE", trendDir }

	trendStrength := math.Abs(priceChange) / atr

	// Direction score
	overallDir := window[len(window)-1] - window[0]
	sameDir := 0
	for i := 1; i < len(window); i++ {
		if (window[i]-window[i-1])*overallDir > 0 { sameDir++ }
	}
	dirScore := float64(sameDir) / float64(len(window)-1)

	if trendStrength > cfg.StrongTrendThresh {
		return "STRONG_TREND", trendDir
	}
	if trendStrength > cfg.SlowTrendThresh && dirScore > cfg.SlowTrendDirScore {
		return "SLOW_TREND", trendDir
	}
	return "RANGE", trendDir
}

func calcSL(side string, entry, atr float64, closes []float64, cfg BTConfig, regime string) float64 {
	if regime == "STRONG_TREND" || regime == "EXPANSION" {
		// Swing-based SL
		swLevel := findSwing(closes, 20, side == "LONG")
		buffer := atr * 0.5
		var sl float64
		if side == "LONG" {
			sl = swLevel - buffer
			if entry-sl < atr*cfg.SwingSLMinATR { sl = entry - atr*cfg.SwingSLMinATR }
			if entry-sl > atr*cfg.SwingSLMaxATR { sl = entry - atr*cfg.SwingSLMaxATR }
		} else {
			sl = swLevel + buffer
			if sl-entry < atr*cfg.SwingSLMinATR { sl = entry + atr*cfg.SwingSLMinATR }
			if sl-entry > atr*cfg.SwingSLMaxATR { sl = entry + atr*cfg.SwingSLMaxATR }
		}
		return math.Round(sl*100) / 100
	}
	// ATR-based
	dist := atr * cfg.ATRK
	minDist := entry * 0.008
	if dist < minDist { dist = minDist }
	if side == "LONG" { return math.Round((entry-dist)*100) / 100 }
	return math.Round((entry+dist)*100) / 100
}

func findSwing(closes []float64, lookback int, findLow bool) float64 {
	if len(closes) < lookback { lookback = len(closes) }
	window := closes[len(closes)-lookback:]
	if findLow {
		min := window[0]
		for _, v := range window { if v < min { min = v } }
		return min
	}
	max := window[0]
	for _, v := range window { if v > max { max = v } }
	return max
}

func calcTP(side string, entry, R, gptLevel float64, cfg BTConfig) float64 {
	dist := cfg.TPLevel * R
	if gptLevel > 0 {
		var gptDist float64
		if side == "LONG" { gptDist = gptLevel - entry } else { gptDist = entry - gptLevel }
		if gptDist >= cfg.GptTPMinR*R && gptDist <= cfg.GptTPMaxR*R {
			dist = gptDist
		}
	}
	if dist < cfg.GptTPMinR*R { dist = cfg.GptTPMinR * R }

	if side == "LONG" { return math.Round((entry+dist)*100) / 100 }
	return math.Round((entry-dist)*100) / 100
}

func techBuySignal(closes []float64, cfg BTConfig) (float64, float64) {
	if len(closes) < 30 { return 0, 0 }
	price := closes[len(closes)-1]

	ema20 := indicator.EMA(closes, cfg.EMAFast)
	if len(ema20) >= 2 && ema20[len(ema20)-1]-ema20[len(ema20)-2] < 0 {
		return 0, 0
	}

	ema9 := indicator.EMA(closes, 9)
	ema21 := indicator.EMA(closes, 21)
	if len(ema9) < 2 || len(ema21) < 2 { return 0, 0 }

	ema9Now := ema9[len(ema9)-1]
	ema21Now := ema21[len(ema21)-1]
	if ema9Now <= ema21Now { return 0, 0 }

	rsiVals := indicator.RSI(closes, cfg.RSIPeriod)
	rsi := indicator.Last(rsiVals)
	if rsi < 40 || rsi > 70 { return 0, 0 }

	macd := indicator.MACD(closes, cfg.MACDFast, cfg.MACDSlow, cfg.MACDSignal)
	if indicator.Last(macd.Histogram) <= 0 { return 0, 0 }

	conf := 0.75
	if rsi > 50 && rsi < 65 { conf += 0.05 }

	entry := math.Round((price-price*cfg.EntryOffsetPct)*100) / 100
	return conf, entry
}

func init() {
	// Suppress unused import
	_ = sort.Strings
}
