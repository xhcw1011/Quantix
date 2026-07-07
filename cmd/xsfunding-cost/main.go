// Command xsfunding-cost estimates the cross-sectional funding rebalancer's REAL
// execution cost from live Binance order-book depth, across a range of capital sizes —
// the make-or-break ≤20bp gate. Public depth endpoint only (read-only, no orders).
//
// It walks each universe coin's book for the per-position order size at each capital
// level, averages buy+sell slippage across the universe, and maps that to the implied
// net annual return using the replay-measured turnover (~58× capital/yr).
//
//	go run ./cmd/xsfunding-cost
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/Quantix/quantix/internal/rebalancer"
)

const (
	fapi     = "https://fapi.binance.com"
	cacheDir = "/tmp/xsf_depth"
	K        = 5 // positions per side → 2K=10 book slots
	// Cost→return mapping (see memory funding_factor): net@10bp ≈ 50%, each 10bp of
	// per-side cost drags ~5.8%/yr (annual turnover ~58× capital from the runner replay).
	preCostGrossPct = 55.8
	dragPerBp       = 0.58 // %/yr net drag per 1bp per-side cost
	takerFeeBp      = 5.0  // Binance USDM taker fee (~5bp); all-in cost = slippage + fee
)

type depth struct {
	Bids, Asks []rebalancer.Level
}

func get(url string) ([]byte, error) {
	var last error
	for i := 0; i < 4; i++ {
		resp, err := http.Get(url)
		if err != nil {
			last = err
			time.Sleep(time.Duration(400*(i+1)) * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return b, nil
		}
		last = fmt.Errorf("http %d", resp.StatusCode)
		time.Sleep(time.Duration(600*(i+1)) * time.Millisecond)
	}
	return nil, last
}

func parseLevels(raw [][]string) []rebalancer.Level {
	out := make([]rebalancer.Level, 0, len(raw))
	for _, r := range raw {
		p, _ := strconv.ParseFloat(r[0], 64)
		q, _ := strconv.ParseFloat(r[1], 64)
		if p > 0 && q > 0 {
			out = append(out, rebalancer.Level{Price: p, Qty: q})
		}
	}
	return out
}

func fetchDepth(sym string) (*depth, error) {
	cp := filepath.Join(cacheDir, sym+".json")
	if b, err := os.ReadFile(cp); err == nil {
		var d depth
		if json.Unmarshal(b, &d) == nil && len(d.Asks) > 0 {
			return &d, nil
		}
	}
	b, err := get(fmt.Sprintf("%s/fapi/v1/depth?symbol=%s&limit=1000", fapi, sym))
	if err != nil {
		return nil, err
	}
	var raw struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	d := &depth{Bids: parseLevels(raw.Bids), Asks: parseLevels(raw.Asks)}
	if len(d.Asks) == 0 || len(d.Bids) == 0 {
		return nil, fmt.Errorf("empty book")
	}
	if out, err := json.Marshal(d); err == nil {
		_ = os.WriteFile(cp, out, 0644)
	}
	return d, nil
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(p / 100 * float64(len(s)-1))
	return s[i]
}

func main() {
	os.MkdirAll(cacheDir, 0755)
	books := map[string]*depth{}
	loaded := 0
	for _, sym := range rebalancer.DefaultUniverse {
		d, err := fetchDepth(sym)
		if err != nil {
			fmt.Printf("  skip %s: %v\n", sym, err)
			continue
		}
		books[sym] = d
		loaded++
		time.Sleep(150 * time.Millisecond)
	}
	if loaded == 0 {
		fmt.Println("no depth loaded (rate-limited?) — retry; cache accumulates in", cacheDir)
		return
	}

	capitals := []float64{10_000, 25_000, 50_000, 100_000, 250_000, 500_000, 1_000_000}
	fmt.Printf("\n# xsfunding execution-cost curve  (%d/%d 币, real Binance depth, K%d → %d 仓)\n",
		loaded, len(rebalancer.DefaultUniverse), K, 2*K)
	fmt.Printf("  每仓下单额=资金/%d;单边全进出成本 = 滑点(买卖两侧穿越 vs mid 均值)+ %.0fbp taker fee\n\n", 2*K, takerFeeBp)
	fmt.Printf("  %8s | %8s %10s | %9s | %9s | %9s\n", "资金", "滑点bp", "p75滑点bp", "全成本bp", "年拖累", "隐含净收益")
	for _, cap := range capitals {
		posSize := cap / float64(2*K)
		var costs []float64
		for _, d := range books {
			mid := (d.Bids[0].Price + d.Asks[0].Price) / 2
			buy := rebalancer.CrossCostBp(d.Asks, mid, posSize)
			sell := rebalancer.CrossCostBp(d.Bids, mid, posSize)
			costs = append(costs, (buy+sell)/2)
		}
		slip := mean(costs)
		p75 := pct(costs, 75)
		allIn := slip + takerFeeBp
		drag := dragPerBp * allIn
		net := preCostGrossPct - drag
		fmt.Printf("  $%7.0f | %8.1f %10.1f | %9.1f | %7.1f%% | %8.1f%%\n", cap, slip, p75, allIn, drag, net)
	}
	fmt.Printf("\n  注:深度=当下单一快照(非跨时间平均);净收益是回测毛边(未证实盘)减成本,均值回归那 2/3 可能衰减。\n")
	fmt.Printf("      40%% 需净≈40 → 全成本≲%.0fbp(=滑点≲%.0fbp)。看哪档资金越线。\n",
		(preCostGrossPct-40)/dragPerBp, (preCostGrossPct-40)/dragPerBp-takerFeeBp)
}
