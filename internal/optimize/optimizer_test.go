package optimize

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/backtest"
	"github.com/Quantix/quantix/internal/exchange"

	_ "github.com/Quantix/quantix/internal/strategy/macross" // register macross
)

func makeKlines(n int) []exchange.Kline {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	klines := make([]exchange.Kline, n)
	price := 50000.0
	for i := 0; i < n; i++ {
		price += float64(i%7-3) * 50
		if price < 1000 {
			price = 1000
		}
		t := base.Add(time.Duration(i) * time.Hour)
		klines[i] = exchange.Kline{
			Symbol:    "BTCUSDT",
			Interval:  "1h",
			OpenTime:  t,
			CloseTime: t.Add(time.Hour - time.Second),
			Open:      price,
			High:      price * 1.005,
			Low:       price * 0.995,
			Close:     price,
			Volume:    100,
			IsClosed:  true,
		}
	}
	return klines
}

func makeBacktestReport(sharpe, maxDD float64) backtest.Report {
	return backtest.Report{
		SharpeRatio: sharpe,
		MaxDrawdown: maxDD,
	}
}

func TestCartesian(t *testing.T) {
	grid := map[string][]float64{
		"FastPeriod": {5, 10},
		"SlowPeriod": {20, 30},
	}
	result := cartesian(grid)
	if len(result) != 4 {
		t.Errorf("expected 4 combinations, got %d", len(result))
	}
	for _, ps := range result {
		if _, ok := ps["FastPeriod"]; !ok {
			t.Error("missing FastPeriod in param set")
		}
		if _, ok := ps["SlowPeriod"]; !ok {
			t.Error("missing SlowPeriod in param set")
		}
	}
}

func TestBuildWindows(t *testing.T) {
	klines := makeKlines(800)
	o := &Optimizer{
		cfg: Config{
			InSampleBars:  500,
			OutSampleBars: 100,
			StepBars:      100,
		},
		log: zap.NewNop(),
	}
	windows := o.buildWindows(klines)
	// IS=500, OOS=100, step=100, total=800:
	// win0: idx [0..499] IS, [500..599] OOS → advance idx to 100
	// win1: idx [100..599] IS, [600..699] OOS → advance to 200
	// win2: idx [200..699] IS, [700..799] OOS → advance to 300 → 300+600>800, stop
	if len(windows) != 3 {
		t.Errorf("expected 3 windows, got %d", len(windows))
	}
}

func TestNonDominated(t *testing.T) {
	// 0: sharpe=2, dd=10  — not dominated (higher sharpe than 1, worse dd)
	// 1: sharpe=1, dd=5   — not dominated (lower dd than 0, lower sharpe)
	// 2: sharpe=0.5, dd=15 — dominated by both 0 and 1
	results := []RunResult{
		{Params: ParamSet{"fast": 5}, ISReport: makeBacktestReport(2.0, 10.0)},
		{Params: ParamSet{"fast": 10}, ISReport: makeBacktestReport(1.0, 5.0)},
		{Params: ParamSet{"fast": 15}, ISReport: makeBacktestReport(0.5, 15.0)},
	}

	front := NonDominated(results)
	if len(front) != 2 {
		t.Errorf("expected Pareto front of 2, got %d", len(front))
	}
	for _, r := range front {
		if r.Params["fast"] == 15 {
			t.Error("dominated result (fast=15) should not be in Pareto front")
		}
	}
}

func TestNonDominatedSingle(t *testing.T) {
	results := []RunResult{
		{ISReport: makeBacktestReport(2.0, 10.0)},
	}
	front := NonDominated(results)
	if len(front) != 1 {
		t.Errorf("single result: expected front of 1, got %d", len(front))
	}
}

func TestNonDominatedEmpty(t *testing.T) {
	front := NonDominated(nil)
	if len(front) != 0 {
		t.Errorf("empty input: expected 0, got %d", len(front))
	}
}

func TestOptimizerRunInMemory(t *testing.T) {
	klines := makeKlines(800)
	log := zap.NewNop()
	o := &Optimizer{
		cfg: Config{
			Symbol:        "BTCUSDT",
			Interval:      "1h",
			InSampleBars:  500,
			OutSampleBars: 100,
			StepBars:      100,
			StrategyName:  "macross",
			Capital:       10000,
			Fee:           0.001,
			Slippage:      0.0005,
			Workers:       2,
			ParamGrid: map[string][]float64{
				"FastPeriod": {5, 10},
				"SlowPeriod": {20, 30},
			},
		},
		log: log,
	}

	windows := o.buildWindows(klines)
	if len(windows) == 0 {
		t.Fatal("no windows")
	}

	paramSets := cartesian(o.cfg.ParamGrid)
	win := windows[0]
	isKlines := klinesInRange(klines, win.ISStart, win.ISEnd)

	results := o.runWindow(context.Background(), win, isKlines, paramSets)
	if len(results) == 0 {
		t.Error("expected at least one IS result")
		return
	}
	t.Logf("IS results: %d", len(results))

	front := NonDominated(results)
	if len(front) == 0 {
		t.Error("expected non-empty Pareto front")
	}
	t.Logf("Pareto front size: %d", len(front))
}
