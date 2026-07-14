// breakout-probe tests the mirror of aistrat's biggest loss bucket.
//
// aistrat fades band extremes and its worst bucket is range/stop_loss: fades that
// break OUT of the range and run to a full stop. The hypothesis here is the mirror
// image — go WITH the break instead of against it (Donchian channel breakout) —
// and the only honest way to judge it is the same anti-overfit battery we hold
// every other candidate to: parameter neighbourhood, cost sensitivity, OOS split,
// cross-symbol.
//
// P&L is measured in R-multiples (each trade risks one stop-distance) so position
// sizing is vol-normalised by construction and results are comparable across
// symbols and parameters without a separate vol-scaling knob.
//
//	go run ./cmd/breakout-probe -symbol ETHUSDT -interval 5m -n 20 -stopk 2 -exitn 10 \
//	  -fee 0.0005 -from 2026-01-21 -to 2026-05-10 -split 2026-03-30
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"go.uber.org/zap"
)

type trade struct {
	entryTime time.Time
	side      int // +1 long, -1 short
	r         float64
}

// atr computes Wilder-ish ATR at each bar index using a simple rolling mean of
// true range over win bars. atr[i] uses bars [i-win, i-1] (no lookahead).
func atrSeries(kl []exchange.Kline, win int) []float64 {
	tr := make([]float64, len(kl))
	for i := range kl {
		if i == 0 {
			tr[i] = kl[i].High - kl[i].Low
			continue
		}
		h, l, pc := kl[i].High, kl[i].Low, kl[i-1].Close
		tr[i] = math.Max(h-l, math.Max(math.Abs(h-pc), math.Abs(l-pc)))
	}
	out := make([]float64, len(kl))
	for i := range kl {
		if i < win {
			continue
		}
		var s float64
		for j := i - win; j < i; j++ {
			s += tr[j]
		}
		out[i] = s / float64(win)
	}
	return out
}

func runBacktest(kl []exchange.Kline, n, exitN, atrWin int, stopK, feeFrac, slipFrac float64, timeCap int) []trade {
	atr := atrSeries(kl, atrWin)
	var trades []trade

	pos := 0 // 0 flat, +1 long, -1 short
	var entryPx, stopPx, stopDist float64
	var entryIdx int

	warm := n
	if atrWin > warm {
		warm = atrWin
	}

	for i := warm; i < len(kl); i++ {
		c := kl[i].Close

		// prior-N-bar channel (exclude current bar to avoid lookahead)
		hiN, loN := kl[i-1].High, kl[i-1].Low
		for j := i - n; j < i; j++ {
			if kl[j].High > hiN {
				hiN = kl[j].High
			}
			if kl[j].Low < loN {
				loN = kl[j].Low
			}
		}
		// exit channel (shorter)
		hiE, loE := kl[i-1].High, kl[i-1].Low
		for j := i - exitN; j < i; j++ {
			if kl[j].High > hiE {
				hiE = kl[j].High
			}
			if kl[j].Low < loE {
				loE = kl[j].Low
			}
		}

		if pos != 0 {
			// ratchet trailing stop in favour
			if pos == 1 {
				ns := c - stopK*atr[i]
				if ns > stopPx {
					stopPx = ns
				}
			} else {
				ns := c + stopK*atr[i]
				if ns < stopPx {
					stopPx = ns
				}
			}

			exit := false
			var exitPx float64
			// stop hit (use intrabar extreme)
			if pos == 1 && kl[i].Low <= stopPx {
				exit, exitPx = true, stopPx
			} else if pos == -1 && kl[i].High >= stopPx {
				exit, exitPx = true, stopPx
			}
			// opposite exit-channel break on close
			if !exit && pos == 1 && c < loE {
				exit, exitPx = true, c
			} else if !exit && pos == -1 && c > hiE {
				exit, exitPx = true, c
			}
			// time cap
			if !exit && i-entryIdx >= timeCap {
				exit, exitPx = true, c
			}

			if exit {
				gross := float64(pos) * (exitPx - entryPx)
				cost := (feeFrac + slipFrac) * (entryPx + exitPx) // both legs
				r := (gross - cost) / stopDist
				trades = append(trades, trade{entryTime: kl[entryIdx].OpenTime, side: pos, r: r})
				pos = 0
			}
			continue
		}

		// flat: look for breakout entry on close
		if atr[i] <= 0 {
			continue
		}
		if c > hiN {
			pos, entryPx, entryIdx = 1, c, i
			stopDist = stopK * atr[i]
			stopPx = c - stopDist
		} else if c < loN {
			pos, entryPx, entryIdx = -1, c, i
			stopDist = stopK * atr[i]
			stopPx = c + stopDist
		}
	}
	return trades
}

type stats struct {
	n            int
	totalR       float64
	expectancy   float64
	winRate      float64
	pf           float64
}

func summarize(ts []trade) stats {
	if len(ts) == 0 {
		return stats{}
	}
	var tot, gw, gl float64
	wins := 0
	for _, t := range ts {
		tot += t.r
		if t.r >= 0 {
			gw += t.r
			wins++
		} else {
			gl += -t.r
		}
	}
	pf := math.Inf(1)
	if gl > 0 {
		pf = gw / gl
	}
	return stats{
		n:          len(ts),
		totalR:     tot,
		expectancy: tot / float64(len(ts)),
		winRate:    float64(wins) / float64(len(ts)) * 100,
		pf:         pf,
	}
}

func (s stats) line() string {
	return fmt.Sprintf("n=%-5d totalR=%+8.1f  exp=%+.3fR  win=%4.1f%%  PF=%.2f",
		s.n, s.totalR, s.expectancy, s.winRate, s.pf)
}

func main() {
	symbol := flag.String("symbol", "ETHUSDT", "symbol")
	interval := flag.String("interval", "5m", "kline interval")
	n := flag.Int("n", 20, "Donchian entry lookback (bars)")
	exitN := flag.Int("exitn", 10, "opposite exit channel lookback (bars)")
	atrWin := flag.Int("atr", 14, "ATR window")
	stopK := flag.Float64("stopk", 2.0, "trailing stop = stopK * ATR")
	timeCap := flag.Int("timecap", 300, "max holding bars")
	fee := flag.Float64("fee", 0.0005, "per-leg fee fraction (taker ~5bp)")
	slip := flag.Float64("slip", 0.0, "per-leg slippage fraction")
	from := flag.String("from", "2026-01-21", "start date")
	to := flag.String("to", "2026-05-10", "end date")
	split := flag.String("split", "", "OOS split date YYYY-MM-DD (optional)")
	flag.Parse()

	ctx := context.Background()
	log, _ := zap.NewProduction()
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	start, _ := time.Parse("2006-01-02", *from)
	end, _ := time.Parse("2006-01-02", *to)
	kl, err := store.GetKlinesBetween(ctx, *symbol, *interval, start, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "klines: %v\n", err)
		os.Exit(1)
	}
	if len(kl) < *n+*atrWin+10 {
		fmt.Fprintf(os.Stderr, "not enough bars: %d\n", len(kl))
		os.Exit(1)
	}

	ts := runBacktest(kl, *n, *exitN, *atrWin, *stopK, *fee, *slip, *timeCap)
	fmt.Printf("%s %s  bars=%d  N=%d exitN=%d ATR=%d stopK=%.1f fee=%.4f slip=%.4f\n",
		*symbol, *interval, len(kl), *n, *exitN, *atrWin, *stopK, *fee, *slip)
	fmt.Printf("  ALL   %s\n", summarize(ts).line())

	if *split != "" {
		var ins, oos []trade
		for _, t := range ts {
			if t.entryTime.Format("2006-01-02") < *split {
				ins = append(ins, t)
			} else {
				oos = append(oos, t)
			}
		}
		fmt.Printf("  IS    %s\n", summarize(ins).line())
		fmt.Printf("  OOS   %s\n", summarize(oos).line())
	}
}
