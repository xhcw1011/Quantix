package sentiment

import (
	"testing"
	"time"

	"github.com/Quantix/quantix/internal/alpha"
)

type fakeLiqCollector struct {
	buckets []LiquidationBucket
}

func (f *fakeLiqCollector) RecentBuckets(_ string, _, _ time.Time) []LiquidationBucket {
	return f.buckets
}

func liqNow() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) }

func TestLiquidationCascade_LongsLiquidatedExpectBounce(t *testing.T) {
	// Heavy long-liquidation cluster (forced SELLs) → fade by going LONG
	buckets := []LiquidationBucket{
		{StartTime: liqNow().Add(-10 * time.Minute), LongLiqUSD: 3_000_000, ShortLiqUSD: 100_000},
		{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 2_500_000, ShortLiqUSD: 50_000},
	}
	a := NewLiquidationCascade(&fakeLiqCollector{buckets: buckets}, LiquidationCascadeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if s.Direction != alpha.DirLong {
		t.Fatalf("expected LONG (longs liquidated → bounce), got %d (reason=%s)", s.Direction, s.Reason)
	}
	if s.Strength <= 0 {
		t.Fatalf("expected Strength>0, got %f", s.Strength)
	}
}

func TestLiquidationCascade_ShortsLiquidatedExpectFade(t *testing.T) {
	buckets := []LiquidationBucket{
		{StartTime: liqNow().Add(-10 * time.Minute), LongLiqUSD: 100_000, ShortLiqUSD: 3_000_000},
		{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 50_000, ShortLiqUSD: 2_500_000},
	}
	a := NewLiquidationCascade(&fakeLiqCollector{buckets: buckets}, LiquidationCascadeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if s.Direction != alpha.DirShort {
		t.Fatalf("expected SHORT (shorts liquidated → fade up), got %d", s.Direction)
	}
}

func TestLiquidationCascade_BalancedHold(t *testing.T) {
	// Both sides similar magnitudes → no clear cascade → HOLD
	buckets := []LiquidationBucket{
		{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 1_000_000, ShortLiqUSD: 900_000},
	}
	a := NewLiquidationCascade(&fakeLiqCollector{buckets: buckets}, LiquidationCascadeConfig{DominanceRatio: 2.0})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD on balanced cascade, got %d", s.Direction)
	}
}

func TestLiquidationCascade_TooSmallHold(t *testing.T) {
	// Below MinTotalUSD threshold → HOLD (noise level)
	buckets := []LiquidationBucket{
		{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 100_000, ShortLiqUSD: 10_000},
	}
	a := NewLiquidationCascade(&fakeLiqCollector{buckets: buckets}, LiquidationCascadeConfig{MinTotalUSD: 500_000})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD below MinTotalUSD, got %d", s.Direction)
	}
}

func TestLiquidationCascade_NoBucketsHold(t *testing.T) {
	a := NewLiquidationCascade(&fakeLiqCollector{buckets: nil}, LiquidationCascadeConfig{})
	s := a.Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if s.Direction != alpha.DirHold {
		t.Fatalf("expected HOLD with no data")
	}
}

func TestLiquidationCascade_StrengthScales(t *testing.T) {
	small := []LiquidationBucket{{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 1_000_000, ShortLiqUSD: 100_000}}
	large := []LiquidationBucket{{StartTime: liqNow().Add(-5 * time.Minute), LongLiqUSD: 10_000_000, ShortLiqUSD: 100_000}}
	cfg := LiquidationCascadeConfig{SaturationUSD: 5_000_000}
	sSmall := NewLiquidationCascade(&fakeLiqCollector{buckets: small}, cfg).Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	sLarge := NewLiquidationCascade(&fakeLiqCollector{buckets: large}, cfg).Predict(alpha.Features{Symbol: "ETHUSDT", Now: liqNow()})
	if sLarge.Strength <= sSmall.Strength {
		t.Fatalf("large=%f should exceed small=%f", sLarge.Strength, sSmall.Strength)
	}
	if sLarge.Strength != 1.0 {
		t.Fatalf("10M with 5M saturation should cap at 1.0, got %f", sLarge.Strength)
	}
}

func TestLiquidationCascade_Name(t *testing.T) {
	a := NewLiquidationCascade(&fakeLiqCollector{}, LiquidationCascadeConfig{})
	if a.Name() != "liquidation_cascade" {
		t.Fatalf("Name=%q", a.Name())
	}
}
