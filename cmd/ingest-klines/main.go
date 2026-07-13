// Command ingest-klines refreshes recent 1d klines for the rebalancer universe from
// Binance Futures into the klines table, so the live signal reflects today (the DB
// otherwise lags). Idempotent upsert; rate-limit friendly. Public endpoint, no auth.
//
//	go run ./cmd/ingest-klines            # last 60d for the 50-coin universe
//	go run ./cmd/ingest-klines -days 400  # deeper backfill
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/rebalancer"
)

const fapi = "https://fapi.binance.com"

func get(url string) ([]byte, error) {
	var last error
	for i := 0; i < 5; i++ {
		resp, err := http.Get(url)
		if err != nil {
			last = err
			time.Sleep(time.Duration(500*(i+1)) * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return b, nil
		}
		last = fmt.Errorf("http %d", resp.StatusCode)
		time.Sleep(time.Duration(800*(i+1)) * time.Millisecond)
	}
	return nil, last
}

// fetchKlines pulls up to `bars` klines at `interval`, paginating backward (limit 1500/req).
func fetchKlines(sym, interval string, bars int) ([]exchange.Kline, error) {
	var out []exchange.Kline
	endTime := int64(0)
	for len(out) < bars {
		want := bars - len(out)
		if want > 1500 {
			want = 1500
		}
		url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d", fapi, sym, interval, want)
		if endTime > 0 {
			url += fmt.Sprintf("&endTime=%d", endTime)
		}
		b, err := get(url)
		if err != nil {
			return out, err
		}
		var raw [][]interface{}
		if err := json.Unmarshal(b, &raw); err != nil {
			return out, err
		}
		if len(raw) == 0 {
			break
		}
		batch := make([]exchange.Kline, 0, len(raw))
		for _, k := range raw {
			f := func(i int) float64 { v, _ := strconv.ParseFloat(k[i].(string), 64); return v }
			nt, _ := k[8].(float64)
			batch = append(batch, exchange.Kline{
				Symbol: sym, Interval: interval,
				OpenTime:  time.UnixMilli(int64(k[0].(float64))).UTC(),
				CloseTime: time.UnixMilli(int64(k[6].(float64))).UTC(),
				Open:      f(1), High: f(2), Low: f(3), Close: f(4),
				Volume: f(5), QuoteVolume: f(7), NumTrades: int64(nt), IsClosed: true,
			})
		}
		out = append(batch, out...) // prepend (older batch)
		endTime = int64(raw[0][0].(float64)) - 1
		if len(raw) < want {
			break
		}
	}
	return out, nil
}

func main() {
	days := flag.Int("days", 60, "how many recent bars to fetch/upsert per symbol")
	interval := flag.String("interval", "1d", "kline interval (1d, 4h, 1h, ...)")
	flag.Parse()

	log, _ := zap.NewProduction()
	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	var okN, rows int
	for i, sym := range rebalancer.DefaultUniverse {
		kl, err := fetchKlines(sym, *interval, *days)
		if err != nil {
			fmt.Printf("  [%2d/%d] %-10s fetch err: %v\n", i+1, len(rebalancer.DefaultUniverse), sym, err)
			continue
		}
		if err := store.BulkUpsertKlines(ctx, kl); err != nil {
			fmt.Printf("  [%2d/%d] %-10s upsert err: %v\n", i+1, len(rebalancer.DefaultUniverse), sym, err)
			continue
		}
		okN++
		rows += len(kl)
		last := "—"
		if len(kl) > 0 {
			last = kl[len(kl)-1].OpenTime.Format("2006-01-02")
		}
		fmt.Printf("  [%2d/%d] %-10s +%d bars (latest %s)\n", i+1, len(rebalancer.DefaultUniverse), sym, len(kl), last)
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("\ningest-klines done: %d/%d symbols, %d bars upserted\n", okN, len(rebalancer.DefaultUniverse), rows)
}
