// Command xsfunding-backtest feeds real Binance klines + funding into the pure
// internal/xsfunding core and reports performance, to validate PARITY with
// scripts/xsmom_funding.py (target: funding factor ~35%/yr, Sharpe ~1.5) BEFORE any
// live-runner wiring. Public endpoints only (no auth).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/Quantix/quantix/internal/xsfunding"
)

const cacheDir = "/tmp/xsf_cache"

type coinData struct {
	Px, Qv, Fund map[string]float64
}

func loadCache(sym string) (*coinData, bool) {
	b, err := os.ReadFile(filepath.Join(cacheDir, sym+".json"))
	if err != nil {
		return nil, false
	}
	var d coinData
	if json.Unmarshal(b, &d) != nil || len(d.Px) == 0 {
		return nil, false
	}
	return &d, true
}

func saveCache(sym string, d *coinData) {
	b, _ := json.Marshal(d)
	_ = os.WriteFile(filepath.Join(cacheDir, sym+".json"), b, 0644)
}

const fapi = "https://fapi.binance.com"
const fundStart = "2024-09-01"

var universe = []string{
	"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT", "ADAUSDT",
	"AVAXUSDT", "LINKUSDT", "DOTUSDT", "TRXUSDT", "LTCUSDT", "BCHUSDT", "ATOMUSDT",
	"UNIUSDT", "ETCUSDT", "XLMUSDT", "NEARUSDT", "APTUSDT", "FILUSDT", "INJUSDT",
	"OPUSDT", "ARBUSDT", "SUIUSDT", "TIAUSDT", "SEIUSDT", "RUNEUSDT", "AAVEUSDT",
	"LDOUSDT", "WLDUSDT", "ALGOUSDT", "VETUSDT", "ICPUSDT", "HBARUSDT", "SANDUSDT",
	"MANAUSDT", "AXSUSDT", "GALAUSDT", "CHZUSDT", "EOSUSDT", "EGLDUSDT", "THETAUSDT",
	"GRTUSDT", "IMXUSDT", "STXUSDT", "ORDIUSDT", "PYTHUSDT", "JUPUSDT", "DYDXUSDT", "CRVUSDT",
}

const (
	L, W, K, REB, LAG = 14, 14, 5, 3, 1
	cost              = 0.0010
	capital           = 10000.0
)

func daykey(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }

func toMs(d string) int64 {
	t, _ := time.Parse("2006-01-02", d)
	return t.UnixMilli()
}

func get(url string) ([]byte, error) {
	var last error
	for i := 0; i < 4; i++ {
		resp, err := http.Get(url)
		if err != nil {
			last = err
			time.Sleep(time.Duration(300*(i+1)) * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return b, nil
		}
		last = fmt.Errorf("http %d", resp.StatusCode)
		time.Sleep(time.Duration(400*(i+1)) * time.Millisecond)
	}
	return nil, last
}

func fetchKlines(sym string) (map[string]float64, map[string]float64, error) {
	b, err := get(fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=1d&limit=1500", fapi, sym))
	if err != nil {
		return nil, nil, err
	}
	var raw [][]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, nil, err
	}
	px, qv := map[string]float64{}, map[string]float64{}
	for _, k := range raw {
		d := daykey(int64(k[0].(float64)))
		c, _ := strconv.ParseFloat(k[4].(string), 64)
		v, _ := strconv.ParseFloat(k[7].(string), 64)
		px[d], qv[d] = c, v
	}
	return px, qv, nil
}

func fetchFunding(sym string) (map[string]float64, error) {
	fd := map[string]float64{}
	cur := toMs(fundStart)
	for p := 0; p < 8; p++ {
		b, err := get(fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&startTime=%d&limit=1000", fapi, sym, cur))
		if err != nil {
			return nil, err
		}
		var raw []struct {
			FundingRate string `json:"fundingRate"`
			FundingTime int64  `json:"fundingTime"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, x := range raw {
			r, _ := strconv.ParseFloat(x.FundingRate, 64)
			fd[daykey(x.FundingTime)] += r
		}
		if len(raw) < 1000 {
			break
		}
		cur = raw[len(raw)-1].FundingTime + 1
	}
	return fd, nil
}

func main() {
	px := map[string]map[string]float64{}
	qv := map[string]map[string]float64{}
	fund := map[string]map[string]float64{}
	os.MkdirAll(cacheDir, 0755)
	loaded := 0
	for _, s := range universe {
		if d, ok := loadCache(s); ok { // 缓存命中,不重复拉(跨 run 累积攒齐 universe)
			px[s], qv[s], fund[s] = d.Px, d.Qv, d.Fund
			loaded++
			continue
		}
		p, v, err := fetchKlines(s)
		if err != nil {
			fmt.Printf("  skip %s klines: %v\n", s, err)
			continue
		}
		f, err := fetchFunding(s)
		if err != nil {
			fmt.Printf("  skip %s funding: %v\n", s, err)
			continue
		}
		saveCache(s, &coinData{Px: p, Qv: v, Fund: f})
		px[s], qv[s], fund[s] = p, v, f
		loaded++
		time.Sleep(200 * time.Millisecond) // 温柔点,防限流
	}

	dateSet := map[string]bool{}
	for s := range px {
		for d := range px[s] {
			dateSet[d] = true
		}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		if d >= fundStart { // funding 只从 fundStart 有,之前的价格没有 funding 信号,截掉
			dates = append(dates, d)
		}
	}
	sort.Strings(dates)
	idx := map[string]int{}
	for i, d := range dates {
		idx[d] = i
	}

	firstIdx := map[string]int{} // for DaysListed
	for s := range px {
		mn := len(dates)
		for d := range px[s] {
			if idx[d] < mn {
				mn = idx[d]
			}
		}
		firstIdx[s] = mn
	}

	trailFund := func(s string, i, n int) float64 {
		var t float64
		for j := i - n + 1; j <= i; j++ {
			if j >= 0 {
				t += fund[s][dates[j]]
			}
		}
		return t
	}
	futFund := func(s string, i, n int) float64 {
		var t float64
		for j := i + 1; j <= i+n && j < len(dates); j++ {
			t += fund[s][dates[j]]
		}
		return t
	}
	trailVol := func(s string, i int) float64 {
		var t float64
		var c int
		for j := i - 29; j <= i; j++ {
			if j >= 0 {
				if v, ok := qv[s][dates[j]]; ok {
					t += v
					c++
				}
			}
		}
		if c == 0 {
			return 0
		}
		return t / float64(c)
	}

	var periods []xsfunding.Period
	var stepDates []string
	for i := L + LAG; i+REB < len(dates); i += REB {
		si := i - LAG
		var coins []xsfunding.CoinState
		fwdRet := map[string]float64{}
		fwdFund := map[string]float64{}
		for s := range px {
			pSi, okSi := px[s][dates[si]]
			pSl, okSl := px[s][dates[si-L]]
			if !okSi || !okSl || pSl <= 0 {
				continue
			}
			if _, hasF := fund[s][dates[max(0, si-W)]]; !hasF { // 无 funding 覆盖 → 不进因子
				continue
			}
			coins = append(coins, xsfunding.CoinState{
				Symbol:       s,
				TrailFunding: trailFund(s, si, W),
				Price:        pSi,
				TrailVolume:  trailVol(s, si),
				DaysListed:   si - firstIdx[s],
			})
			if pNow, ok := px[s][dates[i]]; ok && pNow > 0 {
				if pFwd, ok2 := px[s][dates[i+REB]]; ok2 {
					fwdRet[s] = pFwd/pNow - 1
				}
			}
			fwdFund[s] = futFund(s, i, REB)
		}
		periods = append(periods, xsfunding.Period{Coins: coins, FwdRet: fwdRet, FwdFunding: fwdFund})
		stepDates = append(stepDates, dates[i])
	}

	if len(stepDates) < 2 {
		fmt.Printf("\n数据不足(可能被限流):loaded=%d 币, dates=%d, periods=%d。稍等重试。\n",
			loaded, len(dates), len(stepDates))
		return
	}
	cfg := xsfunding.Config{K: K, GrossFrac: 1.0, MinDaysListed: L, MinVolume: 1.0, FeeRate: cost}
	eq, steps := xsfunding.RunBacktest(periods, capital, cfg, 1.0)

	cum := eq - 1
	yrs := (toMs(stepDates[len(stepDates)-1]) - toMs(stepDates[0])) / 1000 / 86400
	years := float64(yrs) / 365
	ann := math.Pow(eq, 1/years) - 1
	mean, _ := meanStd(steps)
	_, sd := meanStd(steps)
	sharpe := 0.0
	if sd > 0 {
		sharpe = mean / sd * math.Sqrt(365.0/REB)
	}

	fmt.Printf("\n# Go parity: 截面 funding 因子  %d/%d 币  %s→%s  L%d W%d K%d REB%d 费%.0fbp\n",
		loaded, len(universe), stepDates[0], stepDates[len(stepDates)-1], L, W, K, REB, cost*1e4)
	fmt.Printf("  累计 %+.1f%%   年化 %+.1f%%   Sharpe %.2f\n", cum*100, ann*100, sharpe)
	regimes := [][3]string{{"牛", "2024-11-01", "2025-02-15"}, {"25中", "2025-02-15", "2025-10-01"}, {"26跌", "2025-10-01", "2026-12-31"}}
	fmt.Printf("  regime: ")
	eqSeries := runningEquity(steps)
	for _, r := range regimes {
		fmt.Printf("%s %+.0f%%  ", r[0], windowRet(eqSeries, stepDates, r[1], r[2])*100)
	}
	fmt.Printf("\n  (Python 目标: ~35%%/年, Sharpe ~1.5, 牛+21/中+14/跌+23)\n")
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	m := s / float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return m, math.Sqrt(v / float64(len(xs)))
}

func runningEquity(steps []float64) []float64 {
	eq := make([]float64, len(steps))
	e := 1.0
	for i, s := range steps {
		e *= 1 + s
		eq[i] = e
	}
	return eq
}

func windowRet(eq []float64, dates []string, a, b string) float64 {
	var first, last = -1, -1
	for i, d := range dates {
		if d >= a && d < b {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || last <= first {
		return 0
	}
	return eq[last]/eq[first] - 1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
