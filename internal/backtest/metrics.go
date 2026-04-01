package backtest

import (
	"math"
	"time"
)

// Report contains all performance metrics and raw trade data.
type Report struct {
	// Metadata
	StrategyName string
	Symbol       string
	Interval     string
	StartTime    time.Time
	EndTime      time.Time
	TotalBars    int

	// Capital
	InitialCapital float64
	FinalEquity    float64
	TotalReturn    float64 // percent
	AnnualReturn   float64 // percent, annualised

	// Risk-adjusted
	SharpeRatio float64
	CalmarRatio float64

	// Drawdown
	MaxDrawdown    float64 // percent (positive number)
	MaxDrawdownAbs float64 // absolute dollar amount

	// Trade statistics
	TotalTrades      int
	WinningTrades    int
	LosingTrades     int
	WinRate          float64 // percent
	AvgWinPct        float64
	AvgLossPct       float64
	ProfitFactor     float64
	AvgTradeDuration time.Duration

	// Raw data (for charting / CSV export)
	Trades      []Trade
	EquityCurve []EquityPoint
}

// CalcMetrics derives all performance metrics from the portfolio state.
func CalcMetrics(
	stratName, symbol, interval string,
	portfolio *Portfolio,
	startTime, endTime time.Time,
	totalBars int,
) Report {
	r := Report{
		StrategyName:   stratName,
		Symbol:         symbol,
		Interval:       interval,
		StartTime:      startTime,
		EndTime:        endTime,
		TotalBars:      totalBars,
		InitialCapital: portfolio.InitialCapital(),
		Trades:         portfolio.Trades,
		EquityCurve:    portfolio.EquityCurve,
	}

	if len(portfolio.EquityCurve) == 0 {
		return r
	}

	r.FinalEquity = portfolio.EquityCurve[len(portfolio.EquityCurve)-1].Equity
	if r.InitialCapital > 0 {
		r.TotalReturn = (r.FinalEquity/r.InitialCapital - 1) * 100
	}

	// Annualised return
	days := endTime.Sub(startTime).Hours() / 24
	if days > 0 && r.InitialCapital > 0 {
		years := days / 365.0
		r.AnnualReturn = (math.Pow(r.FinalEquity/r.InitialCapital, 1/years) - 1) * 100
	}

	// Max drawdown
	r.MaxDrawdown, r.MaxDrawdownAbs = calcMaxDrawdown(portfolio.EquityCurve)

	// Sharpe ratio (daily returns, annualised, risk-free ≈ 0)
	r.SharpeRatio = calcSharpe(portfolio.EquityCurve)

	// Calmar ratio
	if r.MaxDrawdown > 0 {
		r.CalmarRatio = r.AnnualReturn / r.MaxDrawdown
	}

	// Trade statistics
	r.TotalTrades = len(portfolio.Trades)
	var sumWinPct, sumLossPct float64
	var sumWin, sumLoss float64
	var totalDuration time.Duration

	for _, t := range portfolio.Trades {
		if t.NetPnL >= 0 {
			r.WinningTrades++
			sumWinPct += t.PnLPct
			sumWin += t.NetPnL
		} else {
			r.LosingTrades++
			sumLossPct += math.Abs(t.PnLPct)
			sumLoss += math.Abs(t.NetPnL)
		}
		totalDuration += t.ExitTime.Sub(t.EntryTime)
	}

	if r.TotalTrades > 0 {
		r.WinRate = float64(r.WinningTrades) / float64(r.TotalTrades) * 100
		r.AvgTradeDuration = totalDuration / time.Duration(r.TotalTrades)
	}
	if r.WinningTrades > 0 {
		r.AvgWinPct = sumWinPct / float64(r.WinningTrades)
	}
	if r.LosingTrades > 0 {
		r.AvgLossPct = sumLossPct / float64(r.LosingTrades)
	}
	if sumLoss > 0 {
		r.ProfitFactor = sumWin / sumLoss
	}

	return r
}

// calcMaxDrawdown returns (max drawdown %, max drawdown abs) from equity curve.
func calcMaxDrawdown(curve []EquityPoint) (pct, abs float64) {
	if len(curve) == 0 {
		return 0, 0
	}
	peak := curve[0].Equity
	for _, p := range curve {
		if p.Equity > peak {
			peak = p.Equity
		}
		dd := peak - p.Equity
		if dd > abs {
			abs = dd
			if peak > 0 {
				pct = dd / peak * 100
			}
		}
	}
	return pct, abs
}

// calcSharpe computes the annualised Sharpe ratio from the equity curve.
// Uses daily returns; risk-free rate assumed to be 0.
func calcSharpe(curve []EquityPoint) float64 {
	if len(curve) < 2 {
		return 0
	}

	// Determine bar duration to annualise correctly
	barDur := curve[1].Time.Sub(curve[0].Time)
	if barDur <= 0 {
		barDur = time.Hour // fallback
	}
	barsPerYear := float64(365*24*time.Hour) / float64(barDur)

	returns := make([]float64, 0, len(curve)-1)
	for i := 1; i < len(curve); i++ {
		prev := curve[i-1].Equity
		curr := curve[i].Equity
		if prev > 0 {
			returns = append(returns, (curr-prev)/prev)
		}
	}

	if len(returns) == 0 {
		return 0
	}

	mean := mean(returns)
	std := stdDev(returns)
	if std == 0 {
		return 0
	}

	return (mean / std) * math.Sqrt(barsPerYear)
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func stdDev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var variance float64
	for _, x := range xs {
		d := x - m
		variance += d * d
	}
	return math.Sqrt(variance / float64(len(xs)-1))
}
