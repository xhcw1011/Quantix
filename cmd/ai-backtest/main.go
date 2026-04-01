// AI strategy backtester with GPT signal caching.
//
// First run (generates signals + saves cache):
//   ./bin/ai-backtest -symbol ETHUSDT -interval 15m -bars 500
//
// Tune parameters using cached signals (instant, free):
//   ./bin/ai-backtest -cache signals_cache.json -atrk 2.5 -trailing 5.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
)

type CachedSignal struct {
	Bar        int     `json:"bar"`
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	EntryPrice float64 `json:"entry_price"`
	Reasoning  string  `json:"reasoning"`
	Price      float64 `json:"price"`
	ATR        float64 `json:"atr"`
}

type Trade struct {
	Side, Reason            string
	Entry, Exit, Qty, PnL, R, PnLR float64
	EntryBar, ExitBar, Bars int
}

func main() {
	cfgPath := flag.String("config", "config/config.futures.yaml", "config")
	symbol := flag.String("symbol", "ETHUSDT", "symbol")
	interval := flag.String("interval", "15m", "interval")
	nBars := flag.Int("bars", 500, "bars")
	capital := flag.Float64("capital", 100, "capital")
	atrk := flag.Float64("atrk", 2.0, "stop ATR multiplier")
	trailK := flag.Float64("trailing", 4.0, "trailing ATR multiplier")
	riskPct := flag.Float64("risk", 0.01, "risk per trade")
	minConf := flag.Float64("confidence", 0.65, "min confidence")
	callEvery := flag.Int("call-every", 5, "GPT every N bars")
	cache := flag.String("cache", "", "load cached signals")
	saveTo := flag.String("save", "signals_cache.json", "save signals to")
	flag.Parse()

	log, _ := zap.NewProduction()
	cfg, _ := config.Load(*cfgPath)

	ctx := context.Background()
	store, _ := data.New(ctx, cfg.Database.DSN(), log)
	defer store.Close()

	klines, err := store.GetLatestKlines(ctx, *symbol, *interval, *nBars)
	if err != nil {
		fmt.Fprintf(os.Stderr, "klines: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d bars (%s → %s)\n", len(klines),
		klines[0].OpenTime.Format("01-02 15:04"),
		klines[len(klines)-1].OpenTime.Format("01-02 15:04"))

	// Signals
	var signals []CachedSignal
	if *cache != "" {
		raw, _ := os.ReadFile(*cache)
		json.Unmarshal(raw, &signals)
		fmt.Printf("Loaded %d cached signals\n", len(signals))
	} else {
		signals = genSignals(cfg, klines, *symbol, *callEvery)
		raw, _ := json.MarshalIndent(signals, "", "  ")
		os.WriteFile(*saveTo, raw, 0644)
		fmt.Printf("Generated %d signals → %s\n", len(signals), *saveTo)
	}

	// Backtest
	trades, equity, maxDD, fees := backtest(klines, signals, *capital, *atrk, *trailK, *riskPct, *minConf)

	// Results
	fmt.Printf("\n═══ %s %s | ATRK=%.1f Trail=%.1f Risk=%.0f%% Conf=%.2f ═══\n",
		*symbol, *interval, *atrk, *trailK, *riskPct*100, *minConf)

	wins, losses, totalPnL := 0, 0, 0.0
	var avgWinR, avgLossR float64
	for _, t := range trades {
		totalPnL += t.PnL
		if t.PnL > 0 { wins++; avgWinR += t.PnLR } else { losses++; avgLossR += t.PnLR }
	}
	if wins > 0 { avgWinR /= float64(wins) }
	if losses > 0 { avgLossR /= float64(losses) }

	fmt.Printf("Trades: %d (W:%d L:%d WR:%.0f%%)\n", len(trades), wins, losses,
		func() float64 { if len(trades)==0{return 0}; return float64(wins)/float64(len(trades))*100 }())
	fmt.Printf("PnL: $%.4f | Fees: $%.4f\n", totalPnL, fees)
	fmt.Printf("Equity: $%.2f → $%.2f (%+.2f%%)\n", *capital, equity, (equity / *capital-1)*100)
	fmt.Printf("MaxDD: %.2f%% | Avg Win: %.1fR | Avg Loss: %.1fR\n", maxDD*100, avgWinR, avgLossR)

	if len(trades) > 0 {
		fmt.Println("\nTrades:")
		for i, t := range trades {
			fmt.Printf("  %d. %s %.3f @%.2f→%.2f $%+.4f (%.1fR) [%s] %dbars\n",
				i+1, t.Side, t.Qty, t.Entry, t.Exit, t.PnL, t.PnLR, t.Reason, t.Bars)
		}
	}
}

// ─── Backtest ────────────────────────────────────────────────────────────────

func backtest(klines []exchange.Kline, signals []CachedSignal, capital, atrk, trailK, riskPct, minConf float64) ([]Trade, float64, float64, float64) {
	eq := capital
	peak := eq
	maxDD := 0.0
	fee := 0.001
	totalFees := 0.0
	var trades []Trade

	sigMap := map[int]CachedSignal{}
	for _, s := range signals { sigMap[s.Bar] = s }

	type pos struct {
		side string; entry, qty, remain, stop, trail, peakP, R float64
		tp1 bool; eBar, held int
	}
	var p *pos
	var barBuf []exchange.Kline

	for i, k := range klines {
		barBuf = append(barBuf, k)
		if len(barBuf) > 120 { barBuf = barBuf[len(barBuf)-120:] }
		price := k.Close
		atr := calcATR(barBuf)

		if p != nil {
			p.held++
			if p.side == "LONG" && price > p.peakP { p.peakP = price }
			if p.side == "SHORT" && price < p.peakP { p.peakP = price }

			pnlR := 0.0
			if p.R > 0 {
				if p.side == "LONG" { pnlR = (price - p.entry) / p.R }
				if p.side == "SHORT" { pnlR = (p.entry - price) / p.R }
			}

			closeTrade := func(reason string) {
				pnl := 0.0
				if p.side == "LONG" { pnl = (price - p.entry) * p.remain }
				if p.side == "SHORT" { pnl = (p.entry - price) * p.remain }
				f := price * p.remain * fee; totalFees += f; pnl -= f
				eq += pnl
				trades = append(trades, Trade{Side: p.side, Entry: p.entry, Exit: price, Qty: p.remain, PnL: pnl, R: p.R, PnLR: pnlR, Reason: reason, EntryBar: p.eBar, ExitBar: i, Bars: i - p.eBar})
				p = nil
			}

			// Stop
			if (p.side == "LONG" && price <= p.stop) || (p.side == "SHORT" && price >= p.stop) {
				closeTrade("stop_loss"); goto dd
			}
			if p.held < 5 { goto dd }

			// TP +4R: close 25%, move stop to +2R
			if !p.tp1 && pnlR >= 4.0 {
				cq := p.qty * 0.25; f := price * cq * fee; totalFees += f
				pnl := 0.0
				if p.side == "LONG" { pnl = (price - p.entry) * cq - f }
				if p.side == "SHORT" { pnl = (p.entry - price) * cq - f }
				eq += pnl; p.remain -= cq; p.tp1 = true
				if p.side == "LONG" { p.stop = p.entry + p.R*2 }
				if p.side == "SHORT" { p.stop = p.entry - p.R*2 }
			}
			// Trailing (ATR with 1.2% minimum distance)
			trailDist := atr * trailK
			minTrail := p.peakP * 0.012
			if trailDist < minTrail { trailDist = minTrail }
			if p.side == "LONG" {
				nt := p.peakP - trailDist; if nt > p.trail { p.trail = nt }
				if price <= p.trail && p.trail > p.stop { closeTrade("trailing"); goto dd }
			} else {
				nt := p.peakP + trailDist; if nt < p.trail { p.trail = nt }
				if price >= p.trail && p.trail > 0 && p.trail < p.stop { closeTrade("trailing"); goto dd }
			}
			// GPT reversal
			if sig, ok := sigMap[i]; ok && p.held >= 5 {
				if p.side == "LONG" && sig.Action == "SELL" && sig.Confidence >= 0.75 { closeTrade("reversal") }
				if p != nil && p.side == "SHORT" && sig.Action == "BUY" && sig.Confidence >= 0.75 { closeTrade("reversal") }
			}
		}

		// Entry
		if p == nil {
			if sig, ok := sigMap[i]; ok && sig.Confidence >= minConf && (sig.Action == "BUY" || sig.Action == "SELL") {
				entry := sig.EntryPrice
				if entry <= 0 { entry = price }
				maxDev := price * 0.005
				if sig.Action == "BUY" && (entry > price || (price-entry) > maxDev) { entry = price }
				if sig.Action == "SELL" && (entry < price || (entry-price) > maxDev) { entry = price }

				minDist := entry * 0.008; atrDist := atr * atrk
				if atrDist < minDist { atrDist = minDist }

				stop := 0.0
				if sig.Action == "BUY" { stop = entry - atrDist; if stop >= entry { goto dd } }
				if sig.Action == "SELL" { stop = entry + atrDist; if stop <= entry { goto dd } }

				R := math.Abs(entry - stop); if R <= 0 { goto dd }
				qty := math.Floor(eq*riskPct/R*1000) / 1000; if qty <= 0 { goto dd }
				f := entry * qty * fee; totalFees += f; eq -= f

				side := "LONG"; if sig.Action == "SELL" { side = "SHORT" }
				p = &pos{side: side, entry: entry, qty: qty, remain: qty, stop: stop, trail: stop, peakP: entry, R: R, eBar: i}
			}
		}

	dd:
		if eq > peak { peak = eq }
		d := (peak - eq) / peak; if d > maxDD { maxDD = d }
	}

	// Close open
	if p != nil {
		price := klines[len(klines)-1].Close
		pnl := 0.0
		if p.side == "LONG" { pnl = (price - p.entry) * p.remain }
		if p.side == "SHORT" { pnl = (p.entry - price) * p.remain }
		f := price * p.remain * fee; totalFees += f; pnl -= f; eq += pnl
		trades = append(trades, Trade{Side: p.side, Entry: p.entry, Exit: price, Qty: p.remain, PnL: pnl, Reason: "end", EntryBar: p.eBar, ExitBar: len(klines) - 1, Bars: len(klines) - 1 - p.eBar})
	}
	return trades, eq, maxDD, totalFees
}

// ─── GPT Signal Generation ───────────────────────────────────────────────────

func genSignals(cfg *config.Config, klines []exchange.Kline, symbol string, every int) []CachedSignal {
	var signals []CachedSignal
	var barBuf []exchange.Kline
	client := &http.Client{Timeout: 15 * time.Second}

	for i, k := range klines {
		barBuf = append(barBuf, k)
		if len(barBuf) > 120 { barBuf = barBuf[len(barBuf)-120:] }
		if len(barBuf) < 60 || i%every != 0 { continue }

		closes := make([]float64, len(barBuf))
		for j, b := range barBuf { closes[j] = b.Close }
		atr := calcATR(barBuf)

		sig := callGPT(client, cfg.OpenAI.APIKey, cfg.OpenAI.Model, symbol, k, barBuf, closes, atr)
		sig.Bar = i; sig.Price = k.Close; sig.ATR = atr
		signals = append(signals, sig)

		if sig.Action != "HOLD" {
			r := sig.Reasoning; if len(r) > 60 { r = r[:60] }
			fmt.Printf("  [%d/%d] %s (%.2f) entry=%.2f @%.2f | %s\n",
				i, len(klines), sig.Action, sig.Confidence, sig.EntryPrice, sig.Price, r)
		}
	}
	return signals
}

const sysPrompt = `You are a professional crypto futures trader. Analyze market data.
RESPONSE (strict JSON, no markdown):
{"action":"BUY|SELL|HOLD","confidence":0.0-1.0,"entry_price":0.00,"reasoning":"one sentence"}
BUY=long, SELL=short, HOLD=skip. entry_price: for BUY below current (support), for SELL above (resistance), within 0.5%. HOLD=0.
Be decisive. If setup is there, trade it.`

func callGPT(client *http.Client, apiKey, model, symbol string, bar exchange.Kline, barBuf []exchange.Kline, closes []float64, atr float64) CachedSignal {
	rsi := indicator.Last(indicator.RSI(closes, 14))
	macd := indicator.MACD(closes, 12, 26, 9)
	bb := indicator.BollingerBands(closes, 20, 2.0)
	ema20 := indicator.Last(indicator.EMA(closes, 20))
	ema50 := indicator.Last(indicator.EMA(closes, 50))
	bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
	bbPos := 0.5; if bbU-bbL > 0 { bbPos = (bar.Close - bbL) / (bbU - bbL) }

	vols := make([]float64, len(barBuf)); for i, b := range barBuf { vols[i] = b.Volume }
	volMA := indicator.Last(indicator.SMA(vols, 20)); vr := 1.0; if volMA > 0 { vr = bar.Volume / volMA }

	swH, swL := 0.0, math.MaxFloat64; n := 10; if len(barBuf) < n { n = len(barBuf) }
	for j := len(barBuf) - n; j < len(barBuf); j++ {
		if barBuf[j].High > swH { swH = barBuf[j].High }; if barBuf[j].Low < swL { swL = barBuf[j].Low }
	}

	mkt, _ := json.Marshal(map[string]any{
		"symbol": symbol, "price": r2(bar.Close),
		"indicators": map[string]float64{
			"rsi": r2(rsi), "macd_hist": r2(indicator.Last(macd.Histogram)),
			"ema20": r2(ema20), "ema50": r2(ema50),
			"bb_upper": r2(bbU), "bb_lower": r2(bbL), "bb_pos": r3(bbPos),
			"atr": r2(atr), "atr_pct": r3(atr / bar.Close * 100), "vol_ratio": r3(vr),
			"swing_high_10": r2(swH), "swing_low_10": r2(swL),
		},
		"recent_bars": func() []map[string]any {
			rn := 10; if len(barBuf) < rn { rn = len(barBuf) }
			out := make([]map[string]any, rn); st := len(barBuf) - rn
			for j := 0; j < rn; j++ { b := barBuf[st+j]; out[j] = map[string]any{"t": b.OpenTime.Format("15:04"), "o": r2(b.Open), "h": r2(b.High), "l": r2(b.Low), "c": r2(b.Close), "v": r2(b.Volume)} }
			return out
		}(),
		"position": "FLAT",
	})

	body, _ := json.Marshal(map[string]any{
		"model": model, "temperature": 0.3, "max_completion_tokens": 120,
		"messages": []map[string]string{{"role": "system", "content": sysPrompt}, {"role": "user", "content": string(mkt)}},
	})

	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil { return CachedSignal{Action: "HOLD"} }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 { return CachedSignal{Action: "HOLD"} }

	var gr struct{ Choices []struct{ Message struct{ Content string `json:"content"` } `json:"message"` } `json:"choices"` }
	json.Unmarshal(rb, &gr)
	if len(gr.Choices) == 0 { return CachedSignal{Action: "HOLD"} }

	var sig CachedSignal
	json.Unmarshal([]byte(gr.Choices[0].Message.Content), &sig)
	return sig
}

func calcATR(bars []exchange.Kline) float64 {
	n := 14; if len(bars) < n+1 { return 0 }
	recent := bars[len(bars)-n-1:]
	var sum float64
	for i := 1; i < len(recent); i++ {
		sum += math.Max(recent[i].High-recent[i].Low, math.Max(math.Abs(recent[i].High-recent[i-1].Close), math.Abs(recent[i].Low-recent[i-1].Close)))
	}
	return sum / float64(n)
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }
