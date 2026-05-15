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

const systemPrompt = `Crypto futures signal scorer. JSON only:
{"long":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"<1 sentence>"},"short":{"confidence":0.0-1.0,"entry_price":0.00,"reasoning":"<1 sentence>"}}

INPUTS: trend_dir (+1=up,-1=down,0=flat), regime, indicators_15m.structure (1=bull,-1=bear,0=range), 5m indicators.

SCORING RULES (in priority order):
1. trend_dir is the PRIMARY signal. Score WITH-trend high, AGAINST-trend low.
2. 15m structure CONFIRMS trend_dir. If both agree → high conf. If they disagree → moderate.
3. 5m indicators (MACD, RSI, price vs EMA) fine-tune timing within the trend.

CONFIDENCE TABLE:
  trend + structure + 5m aligned  → with-trend: 0.85-0.95
  trend + structure agree, 5m weak → with-trend: 0.70-0.85
  trend only (structure neutral)  → with-trend: 0.55-0.70
  no trend (flat/range)           → both sides: 0.40-0.60
  COUNTER-TREND (any scenario)    → HARD CAP: 0.25

NEVER score counter-trend > 0.25. When trend_dir=-1, long MUST be ≤0.25. When trend_dir=+1, short MUST be ≤0.25. A bounce against the trend is noise, not signal.

ENTRY: long=nearest support below price, short=nearest resistance above. Within 0.3% of price.`

type mktCtx struct {
	Symbol       string             `json:"symbol"`
	Price        float64            `json:"price"`
	Regime       string             `json:"regime"`
	TrendDir     int                `json:"trend_dir"` // +1=bullish, -1=bearish, 0=neutral
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
	atr := s.calcATR()

	// 5m indicators — only what the prompt actually uses
	ind := map[string]float64{
		"rsi":           r2(rsi),
		"macd_hist":     r2(indicator.Last(macd.Histogram)),
		"atr":           r2(atr),
		"swing_high_10": r2(s.findSwingHigh(10)),
		"swing_low_10":  r2(s.findSwingLow(10)),
		"ema20":         r2(indicator.Last(indicator.EMA(closes, s.cfg.EMAFast))),
	}

	// Last 5 bars (enough for pattern, saves ~50% tokens vs 10 bars)
	n := 5; if len(s.primaryBars()) < n { n = len(s.primaryBars()) }
	bars := make([]barData, n); st := len(s.primaryBars()) - n
	for i := 0; i < n; i++ {
		b := s.primaryBars()[st+i]
		bars[i] = barData{T: b.OpenTime.Format("15:04"), O: r2(b.Open), H: r2(b.High), L: r2(b.Low), C: r2(b.Close), V: r2(b.Volume)}
	}

	// 15m trend — structure + return only (GPT doesn't need raw EMA values)
	ind15 := map[string]float64{"structure": 0, "return_8bar": 0}
	bars15 := s.barsForInterval("15m")
	if len(bars15) >= 20 {
		closes15 := make([]float64, len(bars15))
		for i, b := range bars15 { closes15[i] = b.Close }
		ret8 := 0.0
		if len(closes15) >= 8 { ret8 = (closes15[len(closes15)-1] - closes15[len(closes15)-8]) / closes15[len(closes15)-8] * 100 }
		ema10_15 := indicator.Last(indicator.EMA(closes15, 10))
		ema30_15 := 0.0
		if len(closes15) >= 30 { ema30_15 = indicator.Last(indicator.EMA(closes15, 30)) }
		structure := 0.0
		if ema30_15 > 0 {
			if ema10_15 > ema30_15 { structure = 1 }
			if ema10_15 < ema30_15 { structure = -1 }
		}
		ind15 = map[string]float64{
			"structure":  structure,
			"return_8bar": r3(ret8),
		}
	}

	posStr := "FLAT"
	if s.longPos != nil && s.longPos.filled { posStr = fmt.Sprintf("LONG@%.2f", s.longPos.entryPrice) }
	if s.shortPos != nil && s.shortPos.filled { posStr = fmt.Sprintf("SHORT@%.2f", s.shortPos.entryPrice) }

	return mktCtx{Symbol: s.cfg.Symbol, Price: r2(bar.Close), Regime: string(s.lastRegime), TrendDir: s.lastTrendDir, Indicators: ind, Indicators15: ind15, RecentBars: bars, Position: posStr}
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
	if err := json.Unmarshal(rb, &gr); err != nil {
		return gptSignal{}, fmt.Errorf("GPT response parse: %w (body: %.200s)", err, string(rb))
	}
	if len(gr.Choices) == 0 { return gptSignal{}, fmt.Errorf("no choices (body: %.200s)", string(rb)) }

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
	// Keep only last 2000 signals (~2 weeks at 144/day) to prevent unbounded growth.
	s.rdb.LTrim(context.Background(), key, -2000, -1)
}

// hasCachedSignals checks if Redis has cached GPT signals for backtest replay.
func (s *AIStrategy) hasCachedSignals() bool {
	if s.rdb == nil { return false }
	key := fmt.Sprintf("quantix:signals:%s:%s", s.cfg.Symbol, s.cfg.PrimaryInterval)
	n, err := s.rdb.LLen(context.Background(), key).Result()
	return err == nil && n > 0
}

// loadReplaySignals loads all cached signals from Redis into memory for backtest replay.
func (s *AIStrategy) loadReplaySignals() {
	if s.rdb == nil { return }
	key := fmt.Sprintf("quantix:signals:%s:%s", s.cfg.Symbol, s.cfg.PrimaryInterval)
	items, err := s.rdb.LRange(context.Background(), key, 0, -1).Result()
	if err != nil {
		s.log.Warn("AI: failed to load replay signals", zap.Error(err))
		return
	}
	for _, item := range items {
		var raw map[string]any
		if err := json.Unmarshal([]byte(item), &raw); err != nil { continue }
		sig := gptSignal{}
		if lc, ok := raw["long_conf"].(float64); ok && lc > 0 {
			le, _ := raw["long_entry"].(float64)
			lr, _ := raw["long_reason"].(string)
			sig.Long = &subSignal{Confidence: lc, EntryPrice: le, Reasoning: lr}
		}
		if sc, ok := raw["short_conf"].(float64); ok && sc > 0 {
			se, _ := raw["short_entry"].(float64)
			sr, _ := raw["short_reason"].(string)
			sig.Short = &subSignal{Confidence: sc, EntryPrice: se, Reasoning: sr}
		}
		s.replaySignals = append(s.replaySignals, sig)
	}
	s.log.Info("AI: loaded replay signals", zap.Int("count", len(s.replaySignals)))
}

// nextReplaySignal returns the next cached signal for backtest replay.
func (s *AIStrategy) nextReplaySignal() (gptSignal, error) {
	if s.replayIdx >= len(s.replaySignals) {
		return gptSignal{}, fmt.Errorf("no more replay signals (%d/%d)", s.replayIdx, len(s.replaySignals))
	}
	sig := s.replaySignals[s.replayIdx]
	s.replayIdx++
	return sig, nil
}
