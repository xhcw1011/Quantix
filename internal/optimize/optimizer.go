// Package optimize provides Walk-Forward Optimization (WFO) for trading strategies.
// It performs an in-sample / out-of-sample split across rolling windows and finds
// Pareto-optimal parameter combinations (maximize Sharpe, minimize MaxDrawdown).
package optimize

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/backtest"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

// Config defines the WFO run parameters.
type Config struct {
	Symbol        string
	Interval      string
	InSampleBars  int                   // bars in the IS (training) window
	OutSampleBars int                   // bars in the OOS (validation) window
	StepBars      int                   // bars to advance the window each step
	ParamGrid     map[string][]float64  // parameter name → candidate values
	StrategyName  string
	Capital       float64
	Fee           float64
	Slippage      float64
	Workers       int // goroutine concurrency; 0 = GOMAXPROCS
}

// ParamSet is one combination of parameter values.
type ParamSet map[string]float64

// RunResult holds a single IS backtest result for one parameter combination.
type RunResult struct {
	Params    ParamSet
	ISReport  backtest.Report
	OOSReport backtest.Report // populated only for Pareto-front members
}

// WFOWindow describes one IS/OOS split.
type WFOWindow struct {
	Index                      int
	ISStart, ISEnd, OOSStart, OOSEnd time.Time
}

// WFOResult collects all results for one WFO window.
type WFOResult struct {
	Window      WFOWindow
	AllResults  []RunResult // all IS results
	ParetoFront []RunResult // non-dominated IS results with OOS validation
}

// Optimizer performs Walk-Forward Optimization.
type Optimizer struct {
	cfg   Config
	store *data.Store
	log   *zap.Logger
}

// New creates an Optimizer.
func New(cfg Config, store *data.Store, log *zap.Logger) *Optimizer {
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.GOMAXPROCS(0)
	}
	return &Optimizer{cfg: cfg, store: store, log: log}
}

// Run executes the WFO and returns one WFOResult per window.
func (o *Optimizer) Run(ctx context.Context) ([]WFOResult, error) {
	// Load all klines for the full time range in one shot
	all, err := o.store.GetLatestKlines(ctx, o.cfg.Symbol, o.cfg.Interval, 100_000)
	if err != nil {
		return nil, fmt.Errorf("load klines: %w", err)
	}
	// GetLatestKlines returns DESC; reverse
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	windows := o.buildWindows(all)
	if len(windows) == 0 {
		return nil, fmt.Errorf("no WFO windows: need %d bars, have %d",
			o.cfg.InSampleBars+o.cfg.OutSampleBars, len(all))
	}

	paramSets := cartesian(o.cfg.ParamGrid)
	o.log.Info("WFO starting",
		zap.String("symbol", o.cfg.Symbol),
		zap.String("interval", o.cfg.Interval),
		zap.Int("windows", len(windows)),
		zap.Int("param_combinations", len(paramSets)),
		zap.Int("workers", o.cfg.Workers),
	)

	wfoResults := make([]WFOResult, 0, len(windows))
	for _, win := range windows {
		isKlines := klinesInRange(all, win.ISStart, win.ISEnd)
		oosKlines := klinesInRange(all, win.OOSStart, win.OOSEnd)

		// Run all IS combinations in parallel
		allResults := o.runWindow(ctx, win, isKlines, paramSets)

		// Find Pareto front on IS results
		paretoIS := NonDominated(allResults)

		// Validate Pareto front on OOS
		for i := range paretoIS {
			rep, err := o.runBacktest(ctx, paretoIS[i].Params, oosKlines)
			if err != nil {
				o.log.Warn("OOS backtest failed", zap.Error(err))
				continue
			}
			paretoIS[i].OOSReport = rep
		}

		wfoResults = append(wfoResults, WFOResult{
			Window:      win,
			AllResults:  allResults,
			ParetoFront: paretoIS,
		})

		o.log.Info("WFO window complete",
			zap.Int("window", win.Index),
			zap.Int("is_results", len(allResults)),
			zap.Int("pareto_size", len(paretoIS)),
		)
	}

	return wfoResults, nil
}

// buildWindows slices the klines into IS+OOS windows.
func (o *Optimizer) buildWindows(klines []exchange.Kline) []WFOWindow {
	var windows []WFOWindow
	n := len(klines)
	step := o.cfg.StepBars
	if step <= 0 {
		step = o.cfg.OutSampleBars
	}
	idx := 0
	for i := 0; ; i++ {
		isStart := idx
		isEnd := isStart + o.cfg.InSampleBars
		oosEnd := isEnd + o.cfg.OutSampleBars

		if oosEnd > n {
			break
		}

		windows = append(windows, WFOWindow{
			Index:    i,
			ISStart:  klines[isStart].OpenTime,
			ISEnd:    klines[isEnd-1].CloseTime,
			OOSStart: klines[isEnd].OpenTime,
			OOSEnd:   klines[oosEnd-1].CloseTime,
		})
		idx += step
	}
	return windows
}

// runWindow runs all parameter combinations against IS klines using a worker pool.
func (o *Optimizer) runWindow(ctx context.Context, win WFOWindow, isKlines []exchange.Kline, paramSets []ParamSet) []RunResult {
	type job struct {
		params ParamSet
	}
	jobCh := make(chan job, len(paramSets))
	for _, ps := range paramSets {
		jobCh <- job{params: ps}
	}
	close(jobCh)

	var mu sync.Mutex
	var results []RunResult
	sem := make(chan struct{}, o.cfg.Workers)

	var wg sync.WaitGroup
	for j := range jobCh {
		wg.Add(1)
		sem <- struct{}{}
		go func(params ParamSet) {
			defer wg.Done()
			defer func() { <-sem }()

			rep, err := o.runBacktest(ctx, params, isKlines)
			if err != nil {
				o.log.Debug("IS backtest failed",
					zap.Int("window", win.Index),
					zap.Error(err))
				return
			}
			mu.Lock()
			results = append(results, RunResult{Params: params, ISReport: rep})
			mu.Unlock()
		}(j.params)
	}
	wg.Wait()
	return results
}

// runBacktest runs a single backtest with the given params and klines.
func (o *Optimizer) runBacktest(ctx context.Context, params ParamSet, klines []exchange.Kline) (backtest.Report, error) {
	if len(klines) < 2 {
		return backtest.Report{}, fmt.Errorf("insufficient klines (%d)", len(klines))
	}

	// Convert ParamSet to map[string]any for registry
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	p["Symbol"] = o.cfg.Symbol

	strat, err := registry.Create(o.cfg.StrategyName, p, o.log)
	if err != nil {
		return backtest.Report{}, fmt.Errorf("create strategy: %w", err)
	}

	eng := backtest.New(backtest.Config{
		Symbol:         o.cfg.Symbol,
		Interval:       o.cfg.Interval,
		InitialCapital: o.cfg.Capital,
		FeeRate:        o.cfg.Fee,
		Slippage:       o.cfg.Slippage,
		Klines:         klines,
	}, nil, strat, o.log)

	return eng.Run(ctx)
}

// klinesInRange returns the subset of klines in [start, end].
func klinesInRange(klines []exchange.Kline, start, end time.Time) []exchange.Kline {
	var out []exchange.Kline
	for _, k := range klines {
		if (k.OpenTime.Equal(start) || k.OpenTime.After(start)) &&
			(k.OpenTime.Equal(end) || k.OpenTime.Before(end)) {
			out = append(out, k)
		}
	}
	return out
}

// cartesian generates the cartesian product of all parameter values.
func cartesian(grid map[string][]float64) []ParamSet {
	// Collect keys in deterministic order
	keys := make([]string, 0, len(grid))
	for k := range grid {
		keys = append(keys, k)
	}

	result := []ParamSet{{}}
	for _, key := range keys {
		vals := grid[key]
		var expanded []ParamSet
		for _, existing := range result {
			for _, v := range vals {
				ps := make(ParamSet, len(existing)+1)
				for k, val := range existing {
					ps[k] = val
				}
				ps[key] = v
				expanded = append(expanded, ps)
			}
		}
		result = expanded
	}
	return result
}
