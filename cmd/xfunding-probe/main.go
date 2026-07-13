// xfunding-probe is a RESEARCH gauge for the cross-exchange funding-spread thesis:
// for each coin, how big and how persistent is Binance_funding - OKX_funding? A delta-
// neutral cross-venue carry (short the high-funding venue, long the low-funding venue,
// same coin) only works if |spread| durably exceeds 2x round-trip cost. This tool pulls
// OKX funding history (public endpoint, no auth) and joins it against Binance funding in
// the DB, then prints the spread distribution. No trading, no DB writes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

const okxHist = "https://www.okx.com/api/v5/public/funding-rate-history"

// okxSymbol maps Binance "BTCUSDT" -> OKX "BTC-USDT-SWAP".
func okxSymbol(binance string) string {
	base := strings.TrimSuffix(binance, "USDT")
	return base + "-USDT-SWAP"
}

type okxResp struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		FundingRate  string `json:"fundingRate"`
		RealizedRate string `json:"realizedRate"`
		FundingTime  string `json:"fundingTime"`
	} `json:"data"`
}

// fetchOKX pulls up to `pages` x 100 historical funding points for instId, walking
// backward in time. Returns fundingTime(ms) -> realized rate.
func fetchOKX(cli *http.Client, instId string, pages int) (map[int64]float64, error) {
	out := map[int64]float64{}
	var after string
	for p := 0; p < pages; p++ {
		url := fmt.Sprintf("%s?instId=%s&limit=100", okxHist, instId)
		if after != "" {
			url += "&after=" + after
		}
		resp, err := cli.Get(url)
		if err != nil {
			return out, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var r okxResp
		if err := json.Unmarshal(body, &r); err != nil {
			return out, err
		}
		if r.Code != "0" || len(r.Data) == 0 {
			break // 404/empty (coin not on OKX) or history exhausted
		}
		var oldest int64
		for _, d := range r.Data {
			ts, _ := strconv.ParseInt(d.FundingTime, 10, 64)
			rate := d.RealizedRate
			if rate == "" || rate == "0" {
				rate = d.FundingRate
			}
			v, _ := strconv.ParseFloat(rate, 64)
			out[ts] = v
			if oldest == 0 || ts < oldest {
				oldest = ts
			}
		}
		after = strconv.FormatInt(oldest, 10)
		time.Sleep(120 * time.Millisecond) // be gentle to the public endpoint
	}
	return out, nil
}

func main() {
	pages := flag.Int("pages", 3, "OKX history pages per coin (x100 pts, 8h each ~= 33d/page)")
	minMatch := flag.Int("min-match", 20, "skip coins with fewer than this many aligned settlements")
	flag.Parse()

	symbols := strings.Split("AAVEUSDT,ADAUSDT,ALGOUSDT,APTUSDT,ARBUSDT,ATOMUSDT,AVAXUSDT,AXSUSDT,BCHUSDT,BNBUSDT,BTCUSDT,CHZUSDT,CRVUSDT,DOGEUSDT,DOTUSDT,DYDXUSDT,EGLDUSDT,EOSUSDT,ETCUSDT,ETHUSDT,FILUSDT,GALAUSDT,GRTUSDT,HBARUSDT,ICPUSDT,IMXUSDT,INJUSDT,JUPUSDT,LDOUSDT,LINKUSDT,LTCUSDT,MANAUSDT,NEARUSDT,OPUSDT,ORDIUSDT,PYTHUSDT,RUNEUSDT,SANDUSDT,SEIUSDT,SOLUSDT,STXUSDT,SUIUSDT,THETAUSDT,TIAUSDT,TRXUSDT,UNIUSDT,VETUSDT,WLDUSDT,XLMUSDT,XRPUSDT", ",")

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

	cli := &http.Client{Timeout: 15 * time.Second}

	type coinStat struct {
		sym                 string
		n                   int
		meanAbs, meanSigned float64
		p50, p90            float64
		// persistence: corr(spread_t, spread_{t+1}) — does a wide spread stay wide?
		autocorr float64
	}
	var stats []coinStat
	var allAbs, allSigned []float64

	for _, sym := range symbols {
		bfRows, err := store.GetFunding(ctx, sym)
		if err != nil || len(bfRows) == 0 {
			continue
		}
		bin := map[int64]float64{}
		for _, r := range bfRows {
			bin[r.Time.UnixMilli()] = r.Rate
		}
		okx, err := fetchOKX(cli, okxSymbol(sym), *pages)
		if err != nil || len(okx) == 0 {
			continue
		}
		// join on funding time; OKX and Binance both settle 00/08/16 UTC
		var times []int64
		for ts := range okx {
			if _, ok := bin[ts]; ok {
				times = append(times, ts)
			}
		}
		if len(times) < *minMatch {
			continue
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		var spreads []float64
		var sumAbs, sumSigned float64
		for _, ts := range times {
			s := bin[ts] - okx[ts]
			spreads = append(spreads, s)
			sumAbs += math.Abs(s)
			sumSigned += s
		}
		n := len(spreads)
		sortedAbs := make([]float64, n)
		for i, s := range spreads {
			sortedAbs[i] = math.Abs(s)
		}
		sort.Float64s(sortedAbs)
		stats = append(stats, coinStat{
			sym:        strings.TrimSuffix(sym, "USDT"),
			n:          n,
			meanAbs:    sumAbs / float64(n),
			meanSigned: sumSigned / float64(n),
			p50:        sortedAbs[n/2],
			p90:        sortedAbs[int(float64(n)*0.9)],
			autocorr:   lag1(spreads),
		})
		allAbs = append(allAbs, sortedAbs...)
		for _, s := range spreads {
			allSigned = append(allSigned, s)
		}
		fmt.Printf("  %-6s n=%-3d  |spread| mean=%.4f%%  p90=%.4f%%  signed=%+.4f%%  autocorr=%+.2f\n",
			strings.TrimSuffix(sym, "USDT"), n, stats[len(stats)-1].meanAbs*100, stats[len(stats)-1].p90*100,
			stats[len(stats)-1].meanSigned*100, stats[len(stats)-1].autocorr)
	}

	if len(allAbs) == 0 {
		fmt.Println("no overlap found — check OKX symbol mapping / DB coverage")
		return
	}
	sort.Float64s(allAbs)
	meanAbs := mean(allAbs)
	// annualized: a captured spread is collected every 8h -> 3x/day x 365
	annMeanAbs := meanAbs * 3 * 365
	fmt.Printf("\n=== 汇总: %d coins, %d aligned settlements ===\n", len(stats), len(allAbs))
	fmt.Printf("  per-settlement |spread|:  mean %.4f%%   median %.4f%%   p90 %.4f%%\n",
		meanAbs*100, allAbs[len(allAbs)/2]*100, allAbs[int(float64(len(allAbs))*0.9)]*100)
	fmt.Printf("  annualized (if fully captured, 3x/day): mean %.1f%%/yr\n", annMeanAbs*100)
	// cost hurdle: cross-venue = 2 legs in + 2 legs out. At 5bp/side maker that's ~20bp
	// round trip = 0.20%. You must clear that on ENTRY (one-time), then collect spread each 8h.
	fmt.Printf("  cost context: 2-venue round trip ~0.20%% (4 maker legs @5bp). Spread is collected each 8h;\n")
	fmt.Printf("               break-even hold = roundtrip/|spread| = %.1f settlements (%.1f days) at mean spread.\n",
		0.0020/meanAbs, 0.0020/meanAbs/3)
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// lag1 is the lag-1 autocorrelation — persistence of the spread.
func lag1(xs []float64) float64 {
	if len(xs) < 3 {
		return 0
	}
	m := mean(xs)
	var num, den float64
	for i := 0; i < len(xs); i++ {
		den += (xs[i] - m) * (xs[i] - m)
	}
	for i := 0; i+1 < len(xs); i++ {
		num += (xs[i] - m) * (xs[i+1] - m)
	}
	if den == 0 {
		return 0
	}
	return num / den
}
