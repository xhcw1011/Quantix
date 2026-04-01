package backtest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// PrintSummary writes a human-readable performance summary to w.
func PrintSummary(r Report, w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	sep := strings.Repeat("─", 52)

	fmt.Fprintln(tw, sep)
	fmt.Fprintf(tw, "  Backtest Report: %s\n", r.StrategyName)
	fmt.Fprintln(tw, sep)
	fmt.Fprintf(tw, "  Symbol\t%s\t(%s)\n", r.Symbol, r.Interval)
	fmt.Fprintf(tw, "  Period\t%s → %s\n",
		r.StartTime.Format("2006-01-02"),
		r.EndTime.Format("2006-01-02"))
	fmt.Fprintf(tw, "  Bars\t%d\n", r.TotalBars)
	fmt.Fprintln(tw, sep)

	fmt.Fprintf(tw, "  Initial Capital\t$%.2f\n", r.InitialCapital)
	fmt.Fprintf(tw, "  Final Equity\t$%.2f\n", r.FinalEquity)
	fmt.Fprintf(tw, "  Total Return\t%.2f%%\n", r.TotalReturn)
	fmt.Fprintf(tw, "  Annual Return\t%.2f%%\n", r.AnnualReturn)
	fmt.Fprintln(tw, sep)

	fmt.Fprintf(tw, "  Sharpe Ratio\t%.3f\n", r.SharpeRatio)
	fmt.Fprintf(tw, "  Calmar Ratio\t%.3f\n", r.CalmarRatio)
	fmt.Fprintf(tw, "  Max Drawdown\t%.2f%%  ($%.2f)\n", r.MaxDrawdown, r.MaxDrawdownAbs)
	fmt.Fprintln(tw, sep)

	fmt.Fprintf(tw, "  Total Trades\t%d\n", r.TotalTrades)
	fmt.Fprintf(tw, "  Win Rate\t%.1f%%  (%d W / %d L)\n", r.WinRate, r.WinningTrades, r.LosingTrades)
	fmt.Fprintf(tw, "  Avg Win\t+%.2f%%\n", r.AvgWinPct)
	fmt.Fprintf(tw, "  Avg Loss\t-%.2f%%\n", r.AvgLossPct)
	fmt.Fprintf(tw, "  Profit Factor\t%.2f\n", r.ProfitFactor)
	fmt.Fprintf(tw, "  Avg Duration\t%s\n", formatDuration(r.AvgTradeDuration))
	fmt.Fprintln(tw, sep)

	tw.Flush()
}

// WriteJSON serialises the full report (including equity curve and trades) to w.
func WriteJSON(r Report, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTradesCSV writes the trade list as a CSV file to path.
func WriteTradesCSV(r Report, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	cw.Write([]string{ //nolint:errcheck
		"symbol", "side", "entry_time", "exit_time",
		"entry_price", "exit_price", "qty",
		"gross_pnl", "fee", "net_pnl", "pnl_pct",
	})

	for _, t := range r.Trades {
		cw.Write([]string{ //nolint:errcheck
			t.Symbol,
			string(t.Side),
			t.EntryTime.Format(time.RFC3339),
			t.ExitTime.Format(time.RFC3339),
			strconv.FormatFloat(t.EntryPrice, 'f', 6, 64),
			strconv.FormatFloat(t.ExitPrice, 'f', 6, 64),
			strconv.FormatFloat(t.Qty, 'f', 8, 64),
			strconv.FormatFloat(t.GrossPnL, 'f', 4, 64),
			strconv.FormatFloat(t.Fee, 'f', 4, 64),
			strconv.FormatFloat(t.NetPnL, 'f', 4, 64),
			strconv.FormatFloat(t.PnLPct, 'f', 4, 64),
		})
	}
	cw.Flush()
	return cw.Error()
}

// WriteEquityCSV writes the equity curve as a CSV file to path.
func WriteEquityCSV(r Report, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	cw.Write([]string{"time", "equity", "cash"}) //nolint:errcheck

	for _, p := range r.EquityCurve {
		cw.Write([]string{ //nolint:errcheck
			p.Time.Format(time.RFC3339),
			strconv.FormatFloat(p.Equity, 'f', 4, 64),
			strconv.FormatFloat(p.Cash, 'f', 4, 64),
		})
	}
	cw.Flush()
	return cw.Error()
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return d.String()
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 24 {
		days := h / 24
		h = h % 24
		return fmt.Sprintf("%dd %dh %dm", days, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
