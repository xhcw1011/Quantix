package aistrat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
)

// ─── GPT ─────────────────────────────────────────────────────────────────────

type gptSignal struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	EntryPrice float64 `json:"entry_price"`
	Reasoning  string  `json:"reasoning"`
	// Dual signals for hedge mode
	Long  *subSignal `json:"long,omitempty"`
	Short *subSignal `json:"short,omitempty"`
}

type subSignal struct {
	Confidence float64 `json:"confidence"`
	EntryPrice float64 `json:"entry_price"`
	Reasoning  string  `json:"reasoning"`
}

const systemPrompt = `You are a crypto futures trader. Multi-timeframe analysis: 5m (entry) + 15m (trend).

RESPONSE (strict JSON):
{"long":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."},"short":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"..."}}

MULTI-TIMEFRAME RULES:
1. CHECK indicators_15m FIRST for STRUCTURE (EMA20 vs EMA50) — this is the dominant trend:
   - 15m EMA20 > EMA50: BULLISH STRUCTURE → favor long, short needs strong evidence
   - 15m EMA20 < EMA50: BEARISH STRUCTURE → favor short, long needs strong evidence
2. THEN check return_8bar for MOMENTUM:
   - 15m return_8bar > +1%: strong upward momentum
   - 15m return_8bar < -1%: strong downward momentum
   - Between ±1%: weak/mixed momentum
3. CRITICAL — distinguish BOUNCE from REVERSAL:
   - If 15m EMA structure is BEARISH but return_8bar is temporarily positive:
     this is an OVERSOLD BOUNCE, NOT a trend reversal. Keep long confidence < 0.50.
   - If 15m EMA structure is BULLISH but return_8bar is temporarily negative:
     this is an OVERBOUGHT PULLBACK, NOT a trend reversal. Keep short confidence < 0.50.
   - True reversal requires BOTH structure change (EMA crossover) AND momentum alignment.
4. USE 5m indicators for precise timing:
   - long entry_price: nearest SUPPORT (swing_low_10, bb_lower, ema20), below current price
   - short entry_price: nearest RESISTANCE (swing_high_10, bb_upper), above current price
   - entry_price within 0.5% of current price

CONFIDENCE GUIDE:
- Strong trend (structure + momentum aligned): 0.85-0.95
- Range (EMA20 ≈ EMA50): 0.65-0.85 for both sides
- Bounce against structure (counter-trend): < 0.50
- Weak/conflicting signals: 0.30-0.60

Be decisive. When 15m STRUCTURE and MOMENTUM both align, give HIGH confidence (0.85+).
Never chase a bounce as if it were a reversal.`

type mktCtx struct {
	Symbol       string             `json:"symbol"`
	Price        float64            `json:"price"`
	Indicators   map[string]float64 `json:"indicators"`
	Indicators15 map[string]float64 `json:"indicators_15m,omitempty"`
	RecentBars   []barData          `json:"recent_bars"`
	Position     string             `json:"position"`
}
type barData struct {
	T string `json:"t"`; O, H, L, C, V float64
}

func (s *AIStrategy) buildContext(ctx *strategy.Context, bar exchange.Kline) mktCtx {
	closes := s.getCloses()
	rsi := indicator.Last(indicator.RSI(closes, s.cfg.RSIPeriod))
	macd := indicator.MACD(closes, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	bb := indicator.BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	ema20 := indicator.Last(indicator.EMA(closes, s.cfg.EMAFast))
	ema50 := indicator.Last(indicator.EMA(closes, s.cfg.EMASlow))
	atr := s.calcATR()
	bbU, bbL := indicator.Last(bb.Upper), indicator.Last(bb.Lower)
	bbPos := 0.5; if bbU-bbL > 0 { bbPos = (bar.Close - bbL) / (bbU - bbL) }
	vols := make([]float64, len(s.primaryBars())); for i, b := range s.primaryBars() { vols[i] = b.Volume }
	volMA := indicator.Last(indicator.SMA(vols, s.cfg.VolMAPeriod)); vr := 1.0; if volMA > 0 { vr = bar.Volume / volMA }

	ind := map[string]float64{
		"rsi": r2(rsi), "macd_hist": r2(indicator.Last(macd.Histogram)),
		"ema20": r2(ema20), "ema50": r2(ema50),
		"bb_upper": r2(bbU), "bb_lower": r2(bbL), "bb_pos": r3(bbPos),
		"atr": r2(atr), "vol_ratio": r3(vr),
		"swing_high_10": r2(s.findSwingHigh(10)), "swing_low_10": r2(s.findSwingLow(10)),
		"return_60bar": func() float64 {
			c := s.getCloses()
			if len(c) < 60 { return 0 }
			return r3((c[len(c)-1] - c[len(c)-60]) / c[len(c)-60] * 100)
		}(),
		"return_10bar": func() float64 {
			c := s.getCloses()
			if len(c) < 10 { return 0 }
			return r3((c[len(c)-1] - c[len(c)-10]) / c[len(c)-10] * 100)
		}(),
	}

	n := 10; if len(s.primaryBars()) < n { n = len(s.primaryBars()) }
	bars := make([]barData, n); st := len(s.primaryBars()) - n
	for i := 0; i < n; i++ {
		b := s.primaryBars()[st+i]
		bars[i] = barData{T: b.OpenTime.Format("15:04"), O: r2(b.Open), H: r2(b.High), L: r2(b.Low), C: r2(b.Close), V: r2(b.Volume)}
	}

	// ── 15m trend indicators ──
	var ind15 map[string]float64
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 20 {
		closes15 := make([]float64, len(bars15))
		for i, b := range bars15 { closes15[i] = b.Close }
		rsi15 := indicator.Last(indicator.RSI(closes15, s.cfg.RSIPeriod))
		ema20_15 := indicator.Last(indicator.EMA(closes15, s.cfg.EMAFast))
		ema50_15 := 0.0
		if len(closes15) >= 50 { ema50_15 = indicator.Last(indicator.EMA(closes15, s.cfg.EMASlow)) }
		macd15 := indicator.MACD(closes15, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
		ret8 := 0.0
		if len(closes15) >= 8 { ret8 = (closes15[len(closes15)-1] - closes15[len(closes15)-8]) / closes15[len(closes15)-8] * 100 }
		trend := "range"
		if ret8 > 1.0 { trend = "uptrend" } else if ret8 < -1.0 { trend = "downtrend" }
		_ = trend
		ind15 = map[string]float64{
			"rsi":       r2(rsi15),
			"ema20":     r2(ema20_15),
			"ema50":     r2(ema50_15),
			"macd_hist": r2(indicator.Last(macd15.Histogram)),
			"return_8bar": r3(ret8),
		}
	}

	posStr := "FLAT"
	parts := []string{}
	if s.longPos != nil && s.longPos.filled { parts = append(parts, fmt.Sprintf("LONG@%.2f", s.longPos.entryPrice)) }
	if s.shortPos != nil && s.shortPos.filled { parts = append(parts, fmt.Sprintf("SHORT@%.2f", s.shortPos.entryPrice)) }
	if len(parts) > 0 { posStr = fmt.Sprintf("%v", parts) }

	return mktCtx{Symbol: s.cfg.Symbol, Price: r2(bar.Close), Indicators: ind, Indicators15: ind15, RecentBars: bars, Position: posStr}
}

func (s *AIStrategy) callGPT(mc mktCtx) (gptSignal, error) {
	ctxJSON, _ := json.Marshal(mc)
	body, _ := json.Marshal(map[string]any{
		"model": s.cfg.Model, "temperature": s.cfg.GPTTemperature, "max_completion_tokens": s.cfg.GPTMaxTokens,
		"messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": string(ctxJSON)}},
	})
	callCtx, cancel := context.WithTimeout(context.Background(), s.cfg.GPTTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil { return gptSignal{}, err }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	resp, err := s.client.Do(req); if err != nil { return gptSignal{}, err }
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 { return gptSignal{}, fmt.Errorf("GPT %d: %s", resp.StatusCode, string(rb)) }
	var gr struct{ Choices []struct{ Message struct{ Content string `json:"content"` } `json:"message"` } `json:"choices"` }
	json.Unmarshal(rb, &gr)
	if len(gr.Choices) == 0 { return gptSignal{}, fmt.Errorf("no choices") }

	content := strings.TrimSpace(gr.Choices[0].Message.Content)
	if content == "" { return gptSignal{}, fmt.Errorf("empty GPT response") }
	// Strip markdown code fence if present
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		filtered := []string{}
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") { continue }
			filtered = append(filtered, l)
		}
		content = strings.Join(filtered, "\n")
	}

	var sig gptSignal
	if err := json.Unmarshal([]byte(content), &sig); err != nil {
		return gptSignal{}, fmt.Errorf("parse %q: %w", content, err)
	}
	return sig, nil
}

// cacheSignal stores GPT signal in Redis for backtesting replay.
// Key: quantix:signals:{symbol}:{interval} → JSON list
func (s *AIStrategy) cacheSignal(bar exchange.Kline, sig gptSignal) {
	if s.rdb == nil { return }
	entry := map[string]any{
		"time":      bar.CloseTime.Unix(),
		"bar":       s.barCount,
		"price":     r2(bar.Close),
		"atr":       r2(s.calcATR()),
		"interval":  s.cfg.PrimaryInterval,
		"mtf_score": s.lastMTFScore,
	}
	if sig.Long != nil {
		entry["long_conf"] = sig.Long.Confidence
		entry["long_entry"] = sig.Long.EntryPrice
		entry["long_reason"] = sig.Long.Reasoning
	}
	if sig.Short != nil {
		entry["short_conf"] = sig.Short.Confidence
		entry["short_entry"] = sig.Short.EntryPrice
		entry["short_reason"] = sig.Short.Reasoning
	}
	// Backward compat
	if sig.Action != "" {
		entry["action"] = sig.Action
		entry["confidence"] = sig.Confidence
		entry["entry_price"] = sig.EntryPrice
	}
	data, err := json.Marshal(entry)
	if err != nil { return }
	key := fmt.Sprintf("quantix:signals:%s:%s", s.cfg.Symbol, s.cfg.PrimaryInterval)
	if err := s.rdb.RPush(context.Background(), key, string(data)).Err(); err != nil {
		s.log.Warn("AI: signal cache failed", zap.Error(err))
	}
}
