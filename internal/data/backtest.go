package data

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// BacktestJob represents a backtest run stored in the database.
type BacktestJob struct {
	ID             string
	UserID         int
	StrategyID     string
	Symbol         string
	Interval       string
	Params         map[string]any
	StartDate      *time.Time
	EndDate        *time.Time
	InitialCapital float64
	FeeRate        float64
	Slippage       float64
	Status         string // running | completed | failed
	Result         json.RawMessage
	ErrorMsg       string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

// BacktestJobInput holds the fields needed to create a new job.
type BacktestJobInput struct {
	UserID         int
	StrategyID     string
	Symbol         string
	Interval       string
	Params         map[string]any
	StartDate      *time.Time
	EndDate        *time.Time
	InitialCapital float64
	FeeRate        float64
	Slippage       float64
}

// CreateBacktestJob inserts a new job with status "running" and returns its UUID.
func (s *Store) CreateBacktestJob(ctx context.Context, inp BacktestJobInput) (string, error) {
	paramsJSON, err := json.Marshal(inp.Params)
	if err != nil {
		paramsJSON = []byte("{}")
	}
	const q = `
		INSERT INTO backtest_results
		  (user_id, strategy_id, symbol, interval, params,
		   start_date, end_date, initial_capital, fee_rate, slippage)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id`
	var id string
	err = s.pool.QueryRow(ctx, q,
		inp.UserID, inp.StrategyID, inp.Symbol, inp.Interval, paramsJSON,
		inp.StartDate, inp.EndDate, inp.InitialCapital, inp.FeeRate, inp.Slippage,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create backtest job: %w", err)
	}
	return id, nil
}

// UpdateBacktestResult updates a job's status, result JSON, and error message.
func (s *Store) UpdateBacktestResult(ctx context.Context, id, status string, result json.RawMessage, errMsg string) error {
	const q = `
		UPDATE backtest_results
		SET status=$2, result=$3, error_msg=$4, completed_at=NOW()
		WHERE id=$1`
	_, err := s.pool.Exec(ctx, q, id, status, result, errMsg)
	if err != nil {
		return fmt.Errorf("update backtest result: %w", err)
	}
	return nil
}

// GetBacktestJob fetches a single job by ID, verifying it belongs to userID.
func (s *Store) GetBacktestJob(ctx context.Context, id string, userID int) (*BacktestJob, error) {
	const q = `
		SELECT id, user_id, strategy_id, symbol, interval, params,
		       start_date, end_date, initial_capital, fee_rate, slippage,
		       status, result, error_msg, created_at, completed_at
		FROM backtest_results
		WHERE id=$1 AND user_id=$2`

	row := s.pool.QueryRow(ctx, q, id, userID)
	return scanBacktestJob(row)
}

// ListBacktestJobs returns paginated backtest jobs for a user, newest first.
func (s *Store) ListBacktestJobs(ctx context.Context, userID, limit, offset int) ([]BacktestJob, error) {
	const q = `
		SELECT id, user_id, strategy_id, symbol, interval, params,
		       start_date, end_date, initial_capital, fee_rate, slippage,
		       status, result, error_msg, created_at, completed_at
		FROM backtest_results
		WHERE user_id=$1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list backtest jobs: %w", err)
	}
	defer rows.Close()

	var jobs []BacktestJob
	for rows.Next() {
		job, err := scanBacktestJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

// DeleteBacktestJob removes a job by ID, verifying ownership.
func (s *Store) DeleteBacktestJob(ctx context.Context, id string, userID int) error {
	const q = `DELETE FROM backtest_results WHERE id=$1 AND user_id=$2`
	tag, err := s.pool.Exec(ctx, q, id, userID)
	if err != nil {
		return fmt.Errorf("delete backtest job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner is implemented by both pgx.Row and pgx.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanBacktestJob(row scanner) (*BacktestJob, error) {
	var j BacktestJob
	var paramsRaw []byte
	err := row.Scan(
		&j.ID, &j.UserID, &j.StrategyID, &j.Symbol, &j.Interval,
		&paramsRaw, &j.StartDate, &j.EndDate,
		&j.InitialCapital, &j.FeeRate, &j.Slippage,
		&j.Status, &j.Result, &j.ErrorMsg,
		&j.CreatedAt, &j.CompletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan backtest job: %w", err)
	}
	if paramsRaw != nil {
		_ = json.Unmarshal(paramsRaw, &j.Params)
	}
	return &j, nil
}
