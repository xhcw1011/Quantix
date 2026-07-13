// unlock-probe is a RESEARCH gauge for the token-unlock-drift thesis: do large cliff
// unlocks (discrete token releases to insiders/investors) create predictable negative
// price drift as the market front-runs / absorbs the sell pressure? Pulls DeFiLlama
// unlock schedules (free datasets host), sizes each cliff by % of max supply, and measures
// daily price drift in windows around large unlocks. No trading, no DB writes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

const unlockBase = "https://defillama-datasets.llama.fi/emissions/"

// universe symbol (Binance) -> DeFiLlama slug, for the coins in our 50-universe that have
// ongoing unlock schedules.
var slug = map[string]string{
	"ARBUSDT": "arbitrum", "APTUSDT": "aptos", "TIAUSDT": "celestia", "JUPUSDT": "jupiter",
	"WLDUSDT": "worldcoin", "GRTUSDT": "the-graph", "DYDXUSDT": "dydx", "PYTHUSDT": "pyth",
	"SEIUSDT": "sei", "LDOUSDT": "lido", "UNIUSDT": "uniswap", "AAVEUSDT": "aave",
	"CRVUSDT": "curve-finance", "IMXUSDT": "immutable", "SUIUSDT": "sui", "INJUSDT": "injective",
	"OPUSDT": "optimism", "STXUSDT": "stacks", "NEARUSDT": "near-protocol", "SANDUSDT": "the-sandbox",
}

type emis struct {
	Metadata struct {
		UnlockEvents []struct {
			Timestamp int64 `json:"timestamp"`
			Summary   struct {
				TotalTokensCliff float64 `json:"totalTokensCliff"`
			} `json:"summary"`
		} `json:"unlockEvents"`
	} `json:"metadata"`
	SupplyMetrics struct {
		MaxSupply float64 `json:"maxSupply"`
	} `json:"supplyMetrics"`
}

func fetchUnlocks(cli *http.Client, s string) (*emis, error) {
	resp, err := cli.Get(unlockBase + s)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var e emis
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func main() {
	minPct := flag.Float64("min-pct", 0.005, "only count cliffs >= this fraction of max supply")
	win := flag.Int("win", 5, "drift window in trading days (pre/post)")
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
	cli := &http.Client{Timeout: 30 * time.Second}
	kStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	kEnd := time.Now().UTC()

	type ev struct {
		coin           string
		date           string
		pctSupply      float64
		pre, evt, post float64
		baseline       float64 // coin's mean win-day return (control for trend)
	}
	var events []ev
	var nCoins, nCliffs int
	perCoin := map[string][]float64{} // coin -> abnormal post drifts

	syms := make([]string, 0, len(slug))
	for s := range slug {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	for _, sym := range syms {
		e, err := fetchUnlocks(cli, slug[sym])
		if err != nil || e.SupplyMetrics.MaxSupply == 0 {
			continue
		}
		kl, err := store.GetKlinesBetween(ctx, sym, "1d", kStart, kEnd)
		if err != nil || len(kl) < 30 {
			continue
		}
		nCoins++
		// per-coin trading calendar: sorted dates + close by index
		var dates []string
		close := map[string]float64{}
		for _, k := range kl {
			d := k.OpenTime.UTC().Format("2006-01-02")
			if _, ok := close[d]; !ok {
				dates = append(dates, d)
			}
			close[d] = k.Close
		}
		sort.Strings(dates)
		idx := map[string]int{}
		for i, d := range dates {
			idx[d] = i
		}
		// baseline: coin's mean win-trading-day return over the whole sample (trend control)
		var bsum float64
		var bn int
		for i := 0; i+*win < len(dates); i++ {
			if c0 := close[dates[i]]; c0 > 0 {
				bsum += close[dates[i+*win]]/c0 - 1
				bn++
			}
		}
		baseline := 0.0
		if bn > 0 {
			baseline = bsum / float64(bn)
		}
		maxS := e.SupplyMetrics.MaxSupply
		for _, u := range e.Metadata.UnlockEvents {
			amt := u.Summary.TotalTokensCliff
			if amt <= 0 {
				continue
			}
			pct := amt / maxS
			if pct < *minPct {
				continue
			}
			nCliffs++
			// find first trading day on/after the unlock timestamp
			ud := time.Unix(u.Timestamp, 0).UTC().Format("2006-01-02")
			ei := -1
			for i, d := range dates {
				if d >= ud {
					ei = i
					break
				}
			}
			if ei < *win+1 || ei+*win+1 >= len(dates) {
				continue // not enough surrounding price history
			}
			pre := close[dates[ei-1]]/close[dates[ei-1-*win]] - 1  // [t-win-1 .. t-1]
			evt := close[dates[ei+1]]/close[dates[ei-1]] - 1       // [t-1 .. t+1]
			post := close[dates[ei+1+*win]]/close[dates[ei+1]] - 1 // [t+1 .. t+1+win]
			events = append(events, ev{sym, ud, pct, pre, evt, post, baseline})
			perCoin[sym] = append(perCoin[sym], post-baseline)
		}
	}

	if len(events) == 0 {
		fmt.Println("no qualifying unlock events found — loosen -min-pct or check coverage")
		return
	}
	fmt.Printf("=== 大解锁事件 (>=%.1f%% max supply), %d coins, %d cliffs total, %d qualifying+priced ===\n",
		*minPct*100, nCoins, nCliffs, len(events))
	// aggregate: RAW vs ABNORMAL (post minus coin baseline) — the trend control is decisive
	var mPre, mEvt, mPost, mAbn float64
	var nPostNeg, nAbnNeg int
	for _, e := range events {
		mPre += e.pre
		mEvt += e.evt
		mPost += e.post
		mAbn += e.post - e.baseline
		if e.post < 0 {
			nPostNeg++
		}
		if e.post-e.baseline < 0 {
			nAbnNeg++
		}
	}
	n := float64(len(events))
	fmt.Printf("\n=== 汇总 (thesis: 解锁后应为负 ABNORMAL 漂移) ===\n")
	fmt.Printf("  RAW      mean pre%dd=%+.2f%%  evt±1=%+.2f%%  post%dd=%+.2f%% (%.0f%% neg)\n",
		*win, mPre/n*100, mEvt/n*100, *win, mPost/n*100, float64(nPostNeg)/n*100)
	fmt.Printf("  ABNORMAL post%dd (post - coin baseline) = %+.2f%%  (%.0f%% neg)  <-- 控制趋势后的真信号\n",
		*win, mAbn/n*100, float64(nAbnNeg)/n*100)
	fmt.Printf("  round-trip cost to short: ~0.10%% (2 legs @5bp).\n")
	// per-coin: is the effect broad or driven by one downtrending alt?
	fmt.Printf("\n=== 按币拆解 (abnormal post drift = 事件后%dd收益 − 该币基线) ===\n", *win)
	fmt.Printf("%-8s %5s %12s %12s\n", "coin", "n", "mean abn", "% neg")
	coins := make([]string, 0, len(perCoin))
	for c := range perCoin {
		coins = append(coins, c)
	}
	sort.Strings(coins)
	for _, c := range coins {
		xs := perCoin[c]
		var s float64
		var neg int
		for _, x := range xs {
			s += x
			if x < 0 {
				neg++
			}
		}
		fmt.Printf("%-8s %5d %11.2f%% %11.0f%%\n", c, len(xs), s/float64(len(xs))*100, float64(neg)/float64(len(xs))*100)
	}
}
