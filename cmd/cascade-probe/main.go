// cascade-probe is a RESEARCH gauge for the liquidation-cascade-reversion thesis: after a
// violent forced-selling move (a bar whose return is many sigma below recent vol), does
// price bounce (mean-revert)? Uses 4h klines (a cascade plays out over hours) across the
// universe, measures forward return after shock bars, controlled for each coin's baseline
// forward return. Tests both crash-fade (buy after down-shock) and squeeze-fade (short after
// up-shock). No trading, no DB writes.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

var universe = []string{
	"AAVEUSDT", "ADAUSDT", "ALGOUSDT", "APTUSDT", "ARBUSDT", "ATOMUSDT", "AVAXUSDT", "AXSUSDT",
	"BCHUSDT", "BNBUSDT", "BTCUSDT", "CHZUSDT", "CRVUSDT", "DOGEUSDT", "DOTUSDT", "DYDXUSDT",
	"EGLDUSDT", "EOSUSDT", "ETCUSDT", "ETHUSDT", "FILUSDT", "GALAUSDT", "GRTUSDT", "HBARUSDT",
	"ICPUSDT", "IMXUSDT", "INJUSDT", "JUPUSDT", "LDOUSDT", "LINKUSDT", "LTCUSDT", "MANAUSDT",
	"NEARUSDT", "OPUSDT", "ORDIUSDT", "PYTHUSDT", "RUNEUSDT", "SANDUSDT", "SEIUSDT", "SOLUSDT",
	"STXUSDT", "SUIUSDT", "THETAUSDT", "TIAUSDT", "TRXUSDT", "UNIUSDT", "VETUSDT", "WLDUSDT",
	"XLMUSDT", "XRPUSDT",
}

func main() {
	k := flag.Float64("k", 3.0, "shock threshold in sigma (cascade = |return| >= k * rolling std)")
	volWin := flag.Int("vol-win", 30, "rolling std window in 4h bars")
	side := flag.String("side", "down", "down (crash-fade, buy) | up (squeeze-fade, short)")
	wickMin := flag.Float64("wick-min", 0, "require intrabar recovery >= this (close-low)/(high-low) for down (0=off)")
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
	kStart := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	kEnd := time.Now().UTC()

	horizons := []int{1, 2, 3, 6} // 4h, 8h, 12h, 24h forward
	type agg struct {
		n      int
		sumFwd [4]float64 // forward return after shock (signed for the trade)
		sumAbn [4]float64 // abnormal = fwd - baseline
		sqFwd  [4]float64 // sum of squares of trade return (for std/Sharpe)
		negAbn [4]int
	}
	var A agg
	perCoin := map[string]*agg{}
	var totalBars int

	for _, sym := range universe {
		kl, err := store.GetKlinesBetween(ctx, sym, "4h", kStart, kEnd)
		if err != nil || len(kl) < 100 {
			continue
		}
		n := len(kl)
		ret := make([]float64, n)
		for i := 1; i < n; i++ {
			if kl[i-1].Close > 0 {
				ret[i] = kl[i].Close/kl[i-1].Close - 1
			}
		}
		// baseline forward returns over ALL bars (trend control), per horizon
		var baseSum [4]float64
		var baseN [4]int
		for i := 0; i < n; i++ {
			for hi, h := range horizons {
				if i+h < n && kl[i].Close > 0 {
					baseSum[hi] += kl[i+h].Close/kl[i].Close - 1
					baseN[hi]++
				}
			}
		}
		var base [4]float64
		for hi := range horizons {
			if baseN[hi] > 0 {
				base[hi] = baseSum[hi] / float64(baseN[hi])
			}
		}
		totalBars += n
		pc := &agg{}
		for i := *volWin; i < n; i++ {
			// rolling std of returns over trailing volWin bars
			var m, sd float64
			for j := i - *volWin; j < i; j++ {
				m += ret[j]
			}
			m /= float64(*volWin)
			for j := i - *volWin; j < i; j++ {
				sd += (ret[j] - m) * (ret[j] - m)
			}
			sd = math.Sqrt(sd / float64(*volWin))
			if sd == 0 {
				continue
			}
			shock := ret[i] / sd
			isShock := false
			if *side == "down" {
				isShock = shock <= -*k
				if isShock && *wickMin > 0 {
					rng := kl[i].High - kl[i].Low
					if rng > 0 && (kl[i].Close-kl[i].Low)/rng < *wickMin {
						isShock = false
					}
				}
			} else {
				isShock = shock >= *k
			}
			if !isShock || i+horizons[len(horizons)-1] >= n {
				continue
			}
			A.n++
			pc.n++
			for hi, h := range horizons {
				fwd := kl[i+h].Close/kl[i].Close - 1
				// trade return: buy after down-shock -> earn +fwd; short after up-shock -> earn -fwd
				tr := fwd
				bl := base[hi]
				if *side == "up" {
					tr = -fwd
					bl = -base[hi]
				}
				abn := tr - bl
				A.sumFwd[hi] += tr
				A.sumAbn[hi] += abn
				A.sqFwd[hi] += tr * tr
				pc.sumFwd[hi] += tr
				pc.sumAbn[hi] += abn
				if abn < 0 {
					A.negAbn[hi]++
				}
			}
		}
		if pc.n > 0 {
			perCoin[sym] = pc
		}
	}

	if A.n == 0 {
		fmt.Println("no shock bars found — lower -k")
		return
	}
	fmt.Printf("=== 清算级联反转 gauge: side=%s k=%.1fσ volWin=%d  (4h bars) ===\n", *side, *k, *volWin)
	fmt.Printf("  %d shock bars across %d bars (%.2f%% of bars), %d coins\n",
		A.n, totalBars, float64(A.n)/float64(totalBars)*100, len(perCoin))
	action := "buy-the-crash 交易收益(+fwd)"
	if *side == "up" {
		action = "short-the-squeeze 交易收益(-fwd)"
	}
	fmt.Printf("\n  %s, 分 horizon (控制基线后 abnormal 才是真信号):\n", action)
	fmt.Printf("  %-8s %11s %13s %8s %8s %14s\n", "horizon", "mean trade", "mean ABN", "%abnNeg", "perT-Sh", "net@30bp cost")
	labels := []string{"4h", "8h", "12h", "24h"}
	nf := float64(A.n)
	for hi, lb := range labels {
		mean := A.sumFwd[hi] / nf
		std := math.Sqrt(A.sqFwd[hi]/nf - mean*mean)
		sh := 0.0
		if std > 0 {
			sh = mean / std // per-trade Sharpe (raw, not annualized)
		}
		// realistic cascade execution: fading a liquidation = wide spreads/slippage ~30bp round trip
		net := A.sumFwd[hi]/nf - 0.0030
		fmt.Printf("  %-8s %10.3f%% %12.3f%% %7.0f%% %8.2f %12.3f%%\n",
			lb, mean*100, A.sumAbn[hi]/nf*100, float64(A.negAbn[hi])/nf*100, sh, net*100)
	}
	fmt.Printf("  (perT-Sh = per-trade Sharpe mean/std; crashes are fat-tailed so this matters more than mean)\n")

	// per-coin at 12h horizon (index 2): broad or a few coins?
	fmt.Printf("\n  === 按币 abnormal @12h (前/后各若干) ===\n")
	type cs struct {
		sym string
		n   int
		abn float64
	}
	var css []cs
	for s, pc := range perCoin {
		css = append(css, cs{s, pc.n, pc.sumAbn[2] / float64(pc.n)})
	}
	sort.Slice(css, func(i, j int) bool { return css[i].abn < css[j].abn })
	show := func(c cs) { fmt.Printf("    %-8s n=%-3d abn@12h=%+.2f%%\n", c.sym, c.n, c.abn*100) }
	for i := 0; i < len(css) && i < 6; i++ {
		show(css[i])
	}
	fmt.Printf("    ...\n")
	for i := len(css) - 4; i < len(css); i++ {
		if i >= 0 {
			show(css[i])
		}
	}
}
