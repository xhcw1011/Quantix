// Command ingest-funding backfills perp funding history from Binance Futures into
// the funding_rates table — the data layer for the cross-sectional funding strategy.
// Resumable (skips what's already stored), rate-limit friendly (backoff + inter-symbol
// delay). Public endpoint, no auth.
//
// Usage:
//
//	go run ./cmd/ingest-funding                       # default 50-coin universe from 2024-09-01
//	go run ./cmd/ingest-funding -symbols BTCUSDT,ETHUSDT -start 2024-01-01
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
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
)

const fapi = "https://fapi.binance.com"

// defaultUniverse mirrors cmd/xsfunding-backtest: mid-cap breadth is required for the
// factor (top-10-only collapses it), so keep the full liquid set.
var defaultUniverse = []string{
	"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT", "ADAUSDT",
	"AVAXUSDT", "LINKUSDT", "DOTUSDT", "TRXUSDT", "LTCUSDT", "BCHUSDT", "ATOMUSDT",
	"UNIUSDT", "ETCUSDT", "XLMUSDT", "NEARUSDT", "APTUSDT", "FILUSDT", "INJUSDT",
	"OPUSDT", "ARBUSDT", "SUIUSDT", "TIAUSDT", "SEIUSDT", "RUNEUSDT", "AAVEUSDT",
	"LDOUSDT", "WLDUSDT", "ALGOUSDT", "VETUSDT", "ICPUSDT", "HBARUSDT", "SANDUSDT",
	"MANAUSDT", "AXSUSDT", "GALAUSDT", "CHZUSDT", "EOSUSDT", "EGLDUSDT", "THETAUSDT",
	"GRTUSDT", "IMXUSDT", "STXUSDT", "ORDIUSDT", "PYTHUSDT", "JUPUSDT", "DYDXUSDT", "CRVUSDT",
}

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
		// 403/418/429 = IP throttle; back off harder.
		time.Sleep(time.Duration(800*(i+1)) * time.Millisecond)
	}
	return nil, last
}

// fetchFunding pulls all funding rows for sym at/after startMs (paginated, 1000/page).
func fetchFunding(sym string, startMs int64) ([]data.FundingRow, error) {
	var out []data.FundingRow
	cur := startMs
	for page := 0; page < 12; page++ {
		b, err := get(fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&startTime=%d&limit=1000", fapi, sym, cur))
		if err != nil {
			return out, err
		}
		var raw []struct {
			FundingRate string `json:"fundingRate"`
			FundingTime int64  `json:"fundingTime"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return out, err
		}
		if len(raw) == 0 {
			break
		}
		for _, x := range raw {
			r, _ := strconv.ParseFloat(x.FundingRate, 64)
			out = append(out, data.FundingRow{
				Time:   time.UnixMilli(x.FundingTime).UTC(),
				Symbol: sym,
				Rate:   r,
			})
		}
		if len(raw) < 1000 {
			break
		}
		cur = raw[len(raw)-1].FundingTime + 1
	}
	return out, nil
}

func main() {
	symbolsFlag := flag.String("symbols", "", "comma-separated symbols (default: 50-coin universe)")
	startFlag := flag.String("start", "2024-09-01", "earliest funding date (YYYY-MM-DD) when a symbol has no stored data")
	flag.Parse()

	universe := defaultUniverse
	if *symbolsFlag != "" {
		universe = strings.Split(*symbolsFlag, ",")
	}
	startT, err := time.Parse("2006-01-02", *startFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad -start: %v\n", err)
		os.Exit(1)
	}

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
	for i, sym := range universe {
		// Resume: start just after the latest stored funding time for this symbol.
		from := startT
		if last, err := store.MaxFundingTime(ctx, sym); err == nil && !last.IsZero() {
			from = last.Add(time.Millisecond)
		}
		fr, err := fetchFunding(sym, from.UnixMilli())
		if err != nil {
			fmt.Printf("  [%2d/%d] %-10s fetch err: %v (拉到 %d 行,先存)\n", i+1, len(universe), sym, err, len(fr))
		}
		if len(fr) > 0 {
			if err := store.BulkUpsertFunding(ctx, fr); err != nil {
				fmt.Printf("  [%2d/%d] %-10s upsert err: %v\n", i+1, len(universe), sym, err)
				continue
			}
			rows += len(fr)
		}
		okN++
		fmt.Printf("  [%2d/%d] %-10s +%d rows (from %s)\n", i+1, len(universe), sym, len(fr), from.Format("2006-01-02"))
		time.Sleep(300 * time.Millisecond) // 温柔点,防 IP 限流
	}
	fmt.Printf("\ningest-funding done: %d/%d symbols, %d new rows\n", okN, len(universe), rows)
}
