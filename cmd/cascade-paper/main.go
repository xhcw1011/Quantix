// cascade-paper is a self-contained PAPER-FORWARD executor for the crash-fade strategy
// (buy sigma-shock cascades that already recovered intrabar, hold, exit). It fetches Binance
// 4h klines (public, no auth), computes the shock+wick signal for the latest closed bar,
// advances the persisted paper state via cascade.Step, and logs. No real orders. Run -once
// from a 4h systemd timer, or -loop in-process. State is a JSON file so it survives restarts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/Quantix/quantix/internal/cascade"
)

const klineURL = "https://fapi.binance.com/fapi/v1/klines"

var universe = []string{
	"AAVEUSDT", "ADAUSDT", "ALGOUSDT", "APTUSDT", "ARBUSDT", "ATOMUSDT", "AVAXUSDT", "AXSUSDT",
	"BCHUSDT", "BNBUSDT", "BTCUSDT", "CHZUSDT", "CRVUSDT", "DOGEUSDT", "DOTUSDT", "DYDXUSDT",
	"EGLDUSDT", "EOSUSDT", "ETCUSDT", "ETHUSDT", "FILUSDT", "GALAUSDT", "GRTUSDT", "HBARUSDT",
	"ICPUSDT", "IMXUSDT", "INJUSDT", "JUPUSDT", "LDOUSDT", "LINKUSDT", "LTCUSDT", "MANAUSDT",
	"NEARUSDT", "OPUSDT", "ORDIUSDT", "PYTHUSDT", "RUNEUSDT", "SANDUSDT", "SEIUSDT", "SOLUSDT",
	"STXUSDT", "SUIUSDT", "THETAUSDT", "TIAUSDT", "TRXUSDT", "UNIUSDT", "VETUSDT", "WLDUSDT",
	"XLMUSDT", "XRPUSDT",
}

const barMs = 4 * 3600 * 1000

type kline struct {
	openTime         int64
	high, low, close float64
	closeTime        int64
}

func fetchKlines(cli *http.Client, sym string, limit int) ([]kline, error) {
	url := fmt.Sprintf("%s?symbol=%s&interval=4h&limit=%d", klineURL, sym, limit)
	resp, err := cli.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var raw [][]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]kline, 0, len(raw))
	f := func(v interface{}) float64 { x, _ := strconv.ParseFloat(fmt.Sprint(v), 64); return x }
	for _, r := range raw {
		if len(r) < 7 {
			continue
		}
		out = append(out, kline{
			openTime: int64(f(r[0])), high: f(r[2]), low: f(r[3]), close: f(r[4]),
			closeTime: int64(f(r[6])),
		})
	}
	return out, nil
}

// signalTick computes the shock+wick tick for a symbol's latest CLOSED 4h bar. nowMs is the
// current wall-clock in ms; a bar is closed when closeTime < nowMs.
func signalTick(kl []kline, volWin int, nowMs int64) (cascade.Tick, int64, bool) {
	var closed []kline
	for _, k := range kl {
		if k.closeTime < nowMs {
			closed = append(closed, k)
		}
	}
	if len(closed) < volWin+2 {
		return cascade.Tick{}, 0, false
	}
	rets := make([]float64, len(closed))
	for i := 1; i < len(closed); i++ {
		if closed[i-1].close > 0 {
			rets[i] = closed[i].close/closed[i-1].close - 1
		}
	}
	last := len(closed) - 1
	var m, sd float64
	for j := last - volWin; j < last; j++ {
		m += rets[j]
	}
	m /= float64(volWin)
	for j := last - volWin; j < last; j++ {
		sd += (rets[j] - m) * (rets[j] - m)
	}
	sd = math.Sqrt(sd / float64(volWin))
	shock := 0.0
	if sd > 0 {
		shock = rets[last] / sd
	}
	b := closed[last]
	wick := 1.0
	if rng := b.high - b.low; rng > 0 {
		wick = (b.close - b.low) / rng
	}
	return cascade.Tick{Symbol: "", Close: b.close, Shock: shock, Wick: wick}, b.openTime, true
}

func loadState(path string) cascade.PaperState {
	st := cascade.PaperState{Equity: 1.0}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
		if st.Equity == 0 {
			st.Equity = 1.0
		}
	}
	return st
}

func saveState(path string, st cascade.PaperState) {
	data, _ := json.MarshalIndent(st, "", "  ")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		os.Rename(tmp, path)
	}
}

func tick(cli *http.Client, cfg cascade.Config, volWin int, statePath string) {
	nowMs := time.Now().UnixMilli()
	var ticks []cascade.Tick
	var barTs int64
	for _, sym := range universe {
		kl, err := fetchKlines(cli, sym, volWin+4)
		if err != nil {
			continue
		}
		tk, ots, ok := signalTick(kl, volWin, nowMs)
		if !ok {
			continue
		}
		tk.Symbol = sym
		ticks = append(ticks, tk)
		if ots > barTs {
			barTs = ots
		}
		time.Sleep(60 * time.Millisecond)
	}
	if len(ticks) == 0 {
		fmt.Println("no ticks (data fetch failed?)")
		return
	}
	st := loadState(statePath)
	st, ev := cascade.Step(st, ticks, barTs, barMs, cfg)
	saveState(statePath, st)
	report(st, ev, ticks, barTs)
}

func report(st cascade.PaperState, ev cascade.StepEvent, ticks []cascade.Tick, barTs int64) {
	bar := time.UnixMilli(barTs).UTC().Format("2006-01-02 15:04")
	// worst shock this bar, for context
	sort.Slice(ticks, func(i, j int) bool { return ticks[i].Shock < ticks[j].Shock })
	fmt.Printf("[%s UTC] equity=%.4f  open=%d  trades=%d\n", bar, st.Equity, len(st.Positions), len(st.Trades))
	if len(ticks) > 0 {
		t := ticks[0]
		fmt.Printf("  worst shock this bar: %s %.2fσ (wick %.2f)\n", t.Symbol, t.Shock, t.Wick)
	}
	for _, s := range ev.Opened {
		fmt.Printf("  OPEN  %s\n", s)
	}
	for _, c := range ev.Closed {
		fmt.Printf("  CLOSE %s  ret %+.2f%%\n", c.Symbol, c.Ret*100)
	}
	for _, p := range st.Positions {
		held := (barTs - p.EntryTs) / barMs
		fmt.Printf("  hold  %s entry@%.6g last@%.6g (%d bars, unreal %+.2f%%)\n",
			p.Symbol, p.EntryPx, p.LastPx, held, (p.LastPx/p.EntryPx-1)*100)
	}
	if n := len(st.Trades); n > 0 {
		var wins int
		var sum float64
		for _, t := range st.Trades {
			sum += t.Ret
			if t.Ret > 0 {
				wins++
			}
		}
		fmt.Printf("  ── forward stats: %d trades, win %.0f%%, mean %+.2f%%/trade, equity %+.1f%%\n",
			n, float64(wins)/float64(n)*100, sum/float64(n)*100, (st.Equity-1)*100)
	}
}

func main() {
	k := flag.Float64("k", 3.0, "shock threshold in sigma")
	hold := flag.Int("hold", 18, "holding period in 4h bars (18 = 72h)")
	wick := flag.Float64("wick", 0.4, "entry confirmation: min intrabar recovery")
	frac := flag.Float64("frac", 0.10, "capital fraction per position")
	maxc := flag.Int("max-concurrent", 8, "cap on simultaneous positions")
	cost := flag.Float64("cost", 0.0030, "round-trip cost (cascade slippage)")
	stop := flag.Float64("stop", 0, "per-position stop-loss (0 = off)")
	volWin := flag.Int("vol-win", 30, "rolling std window in bars")
	statePath := flag.String("state", "./cascade_paper_state.json", "state JSON path")
	loop := flag.Bool("loop", false, "run continuously on the 4h grid (else one tick and exit)")
	summary := flag.Bool("summary", false, "print current state and exit (no tick)")
	flag.Parse()

	cfg := cascade.Config{
		K: *k, HoldBars: *hold, FracPerTrade: *frac, MaxConcurrent: *maxc,
		CostRT: *cost, StopLoss: *stop, WickMin: *wick,
	}
	cli := &http.Client{Timeout: 20 * time.Second}

	if *summary {
		report(loadState(*statePath), cascade.StepEvent{}, nil, loadState(*statePath).LastTs)
		return
	}
	if !*loop {
		tick(cli, cfg, *volWin, *statePath)
		return
	}
	fmt.Printf("cascade-paper LOOP: crash-fade k=%.1fσ hold=%d(%dh) wick=%.2f frac=%.0f%% maxConc=%d — PAPER only\n",
		*k, *hold, *hold*4, *wick, *frac*100, *maxc)
	for {
		// next 4h boundary + 2min for the bar to finalize on the exchange
		now := time.Now().UTC()
		next := now.Truncate(4 * time.Hour).Add(4*time.Hour + 2*time.Minute)
		time.Sleep(time.Until(next))
		tick(cli, cfg, *volWin, *statePath)
	}
}
