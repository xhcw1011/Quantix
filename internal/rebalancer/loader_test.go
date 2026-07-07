package rebalancer

import (
	"context"
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"go.uber.org/zap"
)

func TestLoadSeriesFromDB(t *testing.T) {
	cfg, err := config.Load("../../config/config.yaml")
	if err != nil {
		t.Skip("no config")
	}
	ctx := context.Background()
	store, err := data.New(ctx, cfg.Database.DSN(), zap.NewNop())
	if err != nil {
		t.Skip("no db")
	}
	defer store.Close()
	start, _ := time.Parse("2006-01-02", "2024-07-01")
	end, _ := time.Parse("2006-01-02", "2026-07-08")
	series, dates := LoadSeries(ctx, store, []string{"BTCUSDT", "ETHUSDT"}, start, end)
	if len(series) == 0 || len(dates) == 0 {
		t.Skip("db not populated (run ingest-funding + backfill)")
	}
	if _, ok := series["BTCUSDT"]; !ok {
		t.Fatalf("expected BTCUSDT series")
	}
	if s := series["BTCUSDT"]; len(s.Price) == 0 || len(s.Funding) == 0 {
		t.Fatalf("BTCUSDT series incomplete: px=%d fund=%d", len(s.Price), len(s.Funding))
	}
}
