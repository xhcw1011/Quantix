// experiment-report compares the active strategy experiment to a baseline
// window, emits a markdown report, and applies the auto-decision rule.
//
// Usage:
//   experiment-report                 # run all active experiments
//   experiment-report -id exp-xxx     # run one
//   experiment-report -dry-run        # don't write decision, don't send TG
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/notify"
)

type experiment struct {
	ID                string
	UserID            int
	EngineID          string
	Hypothesis        string
	StartedAt         time.Time
	EndedAt           sql.NullTime
	BaselineStart     sql.NullTime
	BaselineEnd       sql.NullTime
	DecisionAfterDays int
	FailMetric        string
	FailOp            string
	FailThreshold     float64
	FailRelative      bool
	Status            string
	Notes             sql.NullString
}

type kpis struct {
	WindowStart, WindowEnd time.Time
	Days                   float64
	NCloses                int
	Wins, Losses           int
	WinRate                float64
	GrossPnL               float64
	Fees                   float64
	Net                    float64
	NetPerDay              float64
	AvgWin                 float64
	AvgLoss                float64
	NOpens                 int // distinct trade_events.event_type='open' rows in window
}

func main() {
	expID := flag.String("id", "", "specific experiment id (default: all active)")
	dryRun := flag.Bool("dry-run", false, "print only, don't update DB or send TG")
	cfgPath := flag.String("config", "config/config.yaml", "config file")
	reportDir := flag.String("out", "/opt/quantix/reports", "report output directory")
	userID := flag.Int("user-id", 4, "load TG credentials for this user_id")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil { fmt.Fprintf(os.Stderr, "config: %v\n", err); os.Exit(1) }

	log, err := logger.New(cfg.App.Env, cfg.App.LogLevel, cfg.App.LogDir)
	if err != nil { fmt.Fprintf(os.Stderr, "logger: %v\n", err); os.Exit(1) }
	defer log.Sync() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil { log.Fatal("db open", zap.Error(err)) }
	defer db.Close()
	if err := db.Ping(ctx); err != nil { log.Fatal("db ping", zap.Error(err)) }

	var tgToken string
	var tgChatID int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(tg_bot_token, ''), COALESCE(tg_chat_id, 0) FROM users WHERE id=$1`,
		*userID).Scan(&tgToken, &tgChatID); err != nil {
		log.Warn("load TG credentials failed", zap.Int("user_id", *userID), zap.Error(err))
	}
	notifier := notify.New(tgToken, tgChatID, log)

	exps, err := loadExperiments(ctx, db, *expID)
	if err != nil { log.Fatal("load experiments", zap.Error(err)) }
	if len(exps) == 0 {
		log.Info("no active experiments — nothing to report")
		return
	}

	if err := os.MkdirAll(*reportDir, 0755); err != nil {
		log.Warn("mkdir reportDir", zap.Error(err))
	}

	for _, exp := range exps {
		report, decision, err := runOne(ctx, db, exp)
		if err != nil {
			log.Error("experiment failed", zap.String("id", exp.ID), zap.Error(err))
			continue
		}
		fmt.Println(report)

		fname := fmt.Sprintf("%s/%s-%s.md", *reportDir, exp.ID, time.Now().Format("20060102"))
		if err := os.WriteFile(fname, []byte(report), 0644); err != nil {
			log.Warn("write report", zap.Error(err))
		} else {
			log.Info("report written", zap.String("file", filepath.Base(fname)))
		}

		if *dryRun { continue }

		if decision != "" {
			if _, err := db.Exec(ctx,
				`UPDATE experiments SET status=$1, decided_at=NOW() WHERE id=$2 AND status='active'`,
				decision, exp.ID); err != nil {
				log.Warn("update decision", zap.Error(err))
			}
		}

		if notifier.Enabled() {
			summary, err := buildTGSummary(ctx, db, exp, decision)
			if err != nil {
				log.Warn("build TG summary", zap.Error(err))
			}
			level := "INFO"
			if decision == "lost" { level = "ERROR" }
			notifier.SystemAlert(level, summary)
		}
	}
}

func loadExperiments(ctx context.Context, db *pgxpool.Pool, id string) ([]experiment, error) {
	q := `SELECT id, user_id, engine_id, hypothesis, started_at, ended_at,
                 baseline_start, baseline_end, decision_after_days,
                 fail_metric, fail_op, fail_threshold, fail_relative,
                 status, notes
          FROM experiments WHERE status='active'`
	args := []any{}
	if id != "" {
		q += ` AND id=$1`
		args = append(args, id)
	}
	rows, err := db.Query(ctx, q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []experiment
	for rows.Next() {
		var e experiment
		if err := rows.Scan(&e.ID, &e.UserID, &e.EngineID, &e.Hypothesis, &e.StartedAt, &e.EndedAt,
			&e.BaselineStart, &e.BaselineEnd, &e.DecisionAfterDays,
			&e.FailMetric, &e.FailOp, &e.FailThreshold, &e.FailRelative,
			&e.Status, &e.Notes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func runOne(ctx context.Context, db *pgxpool.Pool, exp experiment) (string, string, error) {
	now := time.Now().UTC()
	winEnd := now
	if exp.EndedAt.Valid { winEnd = exp.EndedAt.Time }

	bStart, bEnd := autoBaseline(exp, winEnd)

	cur, err := computeKPIs(ctx, db, exp.UserID, exp.EngineID, exp.StartedAt, winEnd)
	if err != nil { return "", "", fmt.Errorf("kpi current: %w", err) }
	base, err := computeKPIs(ctx, db, exp.UserID, exp.EngineID, bStart, bEnd)
	if err != nil { return "", "", fmt.Errorf("kpi baseline: %w", err) }

	decision := evaluate(exp, cur, now)
	report := render(exp, cur, base, decision, now)
	return report, decision, nil
}

// autoBaseline returns baseline_start/end. If explicit values are set on the
// experiment, use those. Otherwise, take a same-length window immediately
// before started_at (capped at 14 days max to avoid pre-engine periods).
func autoBaseline(exp experiment, winEnd time.Time) (time.Time, time.Time) {
	if exp.BaselineStart.Valid && exp.BaselineEnd.Valid {
		return exp.BaselineStart.Time, exp.BaselineEnd.Time
	}
	winLen := winEnd.Sub(exp.StartedAt)
	if winLen > 14*24*time.Hour { winLen = 14 * 24 * time.Hour }
	return exp.StartedAt.Add(-winLen), exp.StartedAt
}

func computeKPIs(ctx context.Context, db *pgxpool.Pool, userID int, engineID string, start, end time.Time) (kpis, error) {
	k := kpis{WindowStart: start, WindowEnd: end, Days: end.Sub(start).Hours() / 24}
	if k.Days <= 0 { return k, nil }

	// Closing fills: realized_pnl != 0. engine_id ("SYMBOL-INTERVAL-STRATEGY")
	// is only unique WITHIN one user's engines, so user_id must be part of
	// every filter here — otherwise two users sharing the same engine_id would
	// have their fills/trade_events blended into one experiment's KPIs (found
	// 2026-08-06, see migration 016).
	row := db.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE realized_pnl > 0),
			COUNT(*) FILTER (WHERE realized_pnl < 0),
			COALESCE(SUM(realized_pnl), 0),
			COALESCE(AVG(realized_pnl) FILTER (WHERE realized_pnl > 0), 0),
			COALESCE(AVG(realized_pnl) FILTER (WHERE realized_pnl < 0), 0)
		FROM fills
		WHERE user_id=$1 AND strategy_id=$2 AND realized_pnl <> 0
		  AND filled_at >= $3 AND filled_at < $4`,
		userID, engineID, start, end)
	if err := row.Scan(&k.NCloses, &k.Wins, &k.Losses, &k.GrossPnL, &k.AvgWin, &k.AvgLoss); err != nil {
		return k, err
	}

	// All fills fees in window
	if err := db.QueryRow(ctx, `
		SELECT COALESCE(SUM(fee), 0) FROM fills
		WHERE user_id=$1 AND strategy_id=$2 AND filled_at >= $3 AND filled_at < $4`,
		userID, engineID, start, end).Scan(&k.Fees); err != nil {
		return k, err
	}

	// Opens in window
	if err := db.QueryRow(ctx, `
		SELECT COUNT(*) FROM trade_events
		WHERE user_id=$1 AND engine_id=$2 AND event_type='open'
		  AND created_at >= $3 AND created_at < $4`,
		userID, engineID, start, end).Scan(&k.NOpens); err != nil {
		return k, err
	}

	if k.NCloses > 0 {
		k.WinRate = float64(k.Wins) / float64(k.NCloses) * 100
	}
	k.Net = k.GrossPnL - k.Fees
	k.NetPerDay = k.Net / k.Days
	return k, nil
}

func evaluate(exp experiment, cur kpis, now time.Time) string {
	daysRunning := now.Sub(exp.StartedAt).Hours() / 24
	if daysRunning < float64(exp.DecisionAfterDays) {
		return "" // not yet — keep active
	}

	val := metricValue(cur, exp.FailMetric)
	threshold := exp.FailThreshold

	hit := false
	switch exp.FailOp {
	case "<":  hit = val < threshold
	case "<=": hit = val <= threshold
	case ">":  hit = val > threshold
	case ">=": hit = val >= threshold
	}
	if hit { return "lost" }
	return "won"
}

func metricValue(k kpis, metric string) float64 {
	switch metric {
	case "net_per_day":  return k.NetPerDay
	case "net":          return k.Net
	case "win_rate":     return k.WinRate
	case "n_opens":      return float64(k.NOpens)
	case "n_closes":     return float64(k.NCloses)
	}
	return 0
}

func render(exp experiment, cur, base kpis, decision string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Experiment Report — %s\n\n", exp.ID)
	fmt.Fprintf(&b, "**Hypothesis**: %s\n\n", exp.Hypothesis)
	fmt.Fprintf(&b, "**Status**: %s", exp.Status)
	if decision != "" { fmt.Fprintf(&b, " → **%s**", strings.ToUpper(decision)) }
	fmt.Fprintf(&b, "\n\n")

	daysRunning := now.Sub(exp.StartedAt).Hours() / 24
	fmt.Fprintf(&b, "**Days running**: %.1f / %d (decision threshold)\n\n", daysRunning, exp.DecisionAfterDays)

	fmt.Fprintf(&b, "**Decision rule**: `%s %s %.4f`",
		exp.FailMetric, exp.FailOp, exp.FailThreshold)
	if exp.FailRelative { fmt.Fprintf(&b, " (relative to baseline)") }
	fmt.Fprintf(&b, "\n\n")

	fmt.Fprintf(&b, "## KPI comparison\n\n")
	fmt.Fprintf(&b, "| metric | baseline | experiment | Δ |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|\n")
	row := func(name string, b1, b2 float64, fmtStr string) {
		d := b2 - b1
		dStr := fmt.Sprintf(fmtStr, d)
		if d > 0 { dStr = "+" + dStr }
		fmt.Fprintf(&b, "| %s | "+fmtStr+" | "+fmtStr+" | %s |\n", name, b1, b2, dStr)
	}
	row("net per day ($)",   base.NetPerDay, cur.NetPerDay, "%.2f")
	row("net total ($)",     base.Net,       cur.Net,       "%.2f")
	row("gross PnL ($)",     base.GrossPnL,  cur.GrossPnL,  "%.2f")
	row("fees ($)",          base.Fees,      cur.Fees,      "%.2f")
	row("win rate (%)",      base.WinRate,   cur.WinRate,   "%.1f")
	row("avg win ($)",       base.AvgWin,    cur.AvgWin,    "%.2f")
	row("avg loss ($)",      base.AvgLoss,   cur.AvgLoss,   "%.2f")
	row("opens",             float64(base.NOpens),  float64(cur.NOpens),  "%.0f")
	row("closes",            float64(base.NCloses), float64(cur.NCloses), "%.0f")
	row("days",              base.Days,      cur.Days,      "%.1f")

	fmt.Fprintf(&b, "\n_Baseline window:_ `%s → %s` (%.1f d)\n",
		base.WindowStart.Format("2006-01-02 15:04Z"),
		base.WindowEnd.Format("2006-01-02 15:04Z"), base.Days)
	fmt.Fprintf(&b, "_Experiment window:_ `%s → %s` (%.1f d)\n",
		cur.WindowStart.Format("2006-01-02 15:04Z"),
		cur.WindowEnd.Format("2006-01-02 15:04Z"), cur.Days)

	if exp.Notes.Valid && exp.Notes.String != "" {
		fmt.Fprintf(&b, "\n_Notes:_ %s\n", exp.Notes.String)
	}
	return b.String()
}

func buildTGSummary(ctx context.Context, db *pgxpool.Pool, exp experiment, decision string) (string, error) {
	now := time.Now().UTC()
	winEnd := now
	if exp.EndedAt.Valid { winEnd = exp.EndedAt.Time }
	bStart, bEnd := autoBaseline(exp, winEnd)
	cur, err := computeKPIs(ctx, db, exp.UserID, exp.EngineID, exp.StartedAt, winEnd)
	if err != nil { return "", err }
	base, err := computeKPIs(ctx, db, exp.UserID, exp.EngineID, bStart, bEnd)
	if err != nil { return "", err }

	daysRunning := now.Sub(exp.StartedAt).Hours() / 24
	var b strings.Builder
	fmt.Fprintf(&b, "Exp `%s`\n", exp.ID)
	if decision != "" {
		emoji := "✅"
		if decision == "lost" { emoji = "❌" }
		fmt.Fprintf(&b, "%s *DECISION: %s*\n", emoji, strings.ToUpper(decision))
	} else {
		fmt.Fprintf(&b, "Day %.1f / %d (running)\n", daysRunning, exp.DecisionAfterDays)
	}
	fmt.Fprintf(&b, "\n*KPI (cur vs baseline)*\n")
	fmt.Fprintf(&b, "• net/day: `$%+.2f` vs `$%+.2f`\n", cur.NetPerDay, base.NetPerDay)
	fmt.Fprintf(&b, "• WR: `%.1f%%` vs `%.1f%%`\n", cur.WinRate, base.WinRate)
	fmt.Fprintf(&b, "• closes: `%d` vs `%d`\n", cur.NCloses, base.NCloses)
	fmt.Fprintf(&b, "\nFull: `/opt/quantix/reports/%s-%s.md`",
		exp.ID, time.Now().Format("20060102"))
	return b.String(), nil
}
