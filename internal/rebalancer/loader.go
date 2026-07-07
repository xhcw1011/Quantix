package rebalancer

import (
	"context"
	"sort"
	"time"

	"github.com/Quantix/quantix/internal/data"
)

func daykey(t time.Time) string { return t.UTC().Format("2006-01-02") }

// LoadSeries reads 1d klines (close, quote-volume) + funding for each symbol from the
// DB into map[symbol]Series, and returns the sorted union of all dates.
func LoadSeries(ctx context.Context, store *data.Store, symbols []string, start, end time.Time) (map[string]Series, []string) {
	series := make(map[string]Series, len(symbols))
	dateSet := map[string]bool{}
	for _, s := range symbols {
		kl, err := store.GetKlinesBetween(ctx, s, "1d", start, end)
		if err != nil || len(kl) == 0 {
			continue
		}
		price := map[string]float64{}
		vol := map[string]float64{}
		first := daykey(kl[0].OpenTime)
		for _, k := range kl {
			d := daykey(k.OpenTime)
			price[d], vol[d] = k.Close, k.QuoteVolume
			dateSet[d] = true
		}
		fr, _ := store.GetFunding(ctx, s)
		fund := map[string]float64{}
		for _, r := range fr {
			fund[daykey(r.Time)] += r.Rate
		}
		series[s] = Series{Price: price, Volume: vol, Funding: fund, First: first}
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	return series, dates
}
