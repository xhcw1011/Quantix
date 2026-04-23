package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/backtest"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/logger"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

type backtestReq struct {
	StrategyID     string         `json:"strategy_id"`
	Symbol         string         `json:"symbol"`
	Interval       string         `json:"interval"`
	Params         map[string]any `json:"params,omitempty"`
	StartDate      string         `json:"start_date,omitempty"` // "2024-01-01"
	EndDate        string         `json:"end_date,omitempty"`   // "2024-12-31"
	InitialCapital float64        `json:"initial_capital"`
	FeeRate        float64        `json:"fee_rate"`
	Slippage       float64        `json:"slippage"`
}

// submitBacktest runs a backtest asynchronously and returns a job ID.
//
//	@Summary		Submit backtest
//	@Description	Start an asynchronous backtest job and return its ID
//	@Tags			backtest
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		backtestReq	true	"Backtest parameters"
//	@Success		201		{object}	map[string]string
//	@Failure		400		{object}	errorResp
//	@Failure		500		{object}	errorResp
//	@Router			/api/backtest [post]
func (s *Server) submitBacktest(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	var req backtestReq
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Basic validation
	if err := validateSymbol(req.Symbol); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateInterval(req.Interval); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.StrategyID == "" {
		jsonError(w, "strategy_id is required", http.StatusBadRequest)
		return
	}

	// Apply defaults and validate numeric bounds
	if req.InitialCapital <= 0 {
		req.InitialCapital = 10000
	}
	if req.FeeRate <= 0 {
		req.FeeRate = 0.001
	}
	if req.FeeRate > 1.0 {
		jsonError(w, "fee_rate must be <= 1.0", http.StatusBadRequest)
		return
	}
	if req.Slippage < 0 || req.Slippage > 1.0 {
		jsonError(w, "slippage must be between 0 and 1.0", http.StatusBadRequest)
		return
	}

	// Parse optional date range
	var startDate, endDate *time.Time
	if req.StartDate != "" {
		t, err := time.Parse("2006-01-02", req.StartDate)
		if err != nil {
			jsonError(w, "start_date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		startDate = &t
	}
	if req.EndDate != "" {
		t, err := time.Parse("2006-01-02", req.EndDate)
		if err != nil {
			jsonError(w, "end_date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		endDate = &t
	}

	// Validate date range
	if startDate != nil && endDate != nil && startDate.After(*endDate) {
		jsonError(w, "start_date must be before end_date", http.StatusBadRequest)
		return
	}

	// Create job record (status=running)
	inp := data.BacktestJobInput{
		UserID:         userID,
		StrategyID:     req.StrategyID,
		Symbol:         req.Symbol,
		Interval:       req.Interval,
		Params:         req.Params,
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: req.InitialCapital,
		FeeRate:        req.FeeRate,
		Slippage:       req.Slippage,
	}
	jobID, err := s.store.CreateBacktestJob(r.Context(), inp)
	if err != nil {
		s.log.Error("create backtest job", zap.Error(err))
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	// Run backtest asynchronously
	go s.runBacktest(jobID, req, startDate, endDate)

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{
		"id":     jobID,
		"status": "running",
	})
}

func (s *Server) runBacktest(jobID string, req backtestReq, startDate, endDate *time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Per-job backtest logger: isolates verbose strategy/engine logs from the
	// main app log. Falls back to the main logger when LogDir is unset.
	btLog := s.log
	if s.logDir != "" {
		path := filepath.Join(s.logDir, "backtest", "backtest-"+jobID+".log")
		lvl := s.logLevel
		if lvl == "" {
			lvl = "info"
		}
		if fl, err := logger.NewFileLogger(path, lvl); err == nil {
			btLog = fl
			defer fl.Sync()
			s.log.Info("backtest started",
				zap.String("id", jobID),
				zap.String("strategy", req.StrategyID),
				zap.String("symbol", req.Symbol),
				zap.String("log_file", path),
			)
		} else {
			s.log.Warn("backtest logger init failed, falling back to main log",
				zap.String("id", jobID), zap.Error(err))
		}
	}

	// Build strategy params
	stratParams := map[string]any{
		"Symbol":   req.Symbol,
		"Interval": req.Interval,
	}
	for k, v := range req.Params {
		stratParams[k] = v
	}

	strat, err := registry.Create(req.StrategyID, stratParams, btLog)
	if err != nil {
		s.finishBacktest(ctx, jobID, nil, err.Error())
		return
	}

	cfg := backtest.Config{
		Symbol:         req.Symbol,
		Interval:       req.Interval,
		InitialCapital: req.InitialCapital,
		FeeRate:        req.FeeRate,
		Slippage:       req.Slippage,
	}
	if startDate != nil {
		cfg.StartTime = *startDate
	}
	if endDate != nil {
		cfg.EndTime = *endDate
	}

	engine := backtest.New(cfg, s.store, strat, btLog)
	// Bypass live-data staleness guard in aistrat signal.go — historical bars
	// would otherwise be rejected and the strategy never becomes "live_ready".
	engine.SetExtra("backtest_replay", true)
	report, err := engine.Run(ctx)
	if err != nil {
		s.finishBacktest(ctx, jobID, nil, err.Error())
		return
	}

	resultJSON, err := json.Marshal(report)
	if err != nil {
		s.finishBacktest(ctx, jobID, nil, "result serialization error: "+err.Error())
		return
	}
	s.finishBacktest(ctx, jobID, resultJSON, "")
}

func (s *Server) finishBacktest(ctx context.Context, jobID string, result json.RawMessage, errMsg string) {
	status := "completed"
	if errMsg != "" {
		status = "failed"
	}
	if err := s.store.UpdateBacktestResult(ctx, jobID, status, result, errMsg); err != nil {
		s.log.Error("update backtest result", zap.String("id", jobID), zap.Error(err))
	}
}

// listBacktests returns a paginated list of the user's backtest jobs.
//
//	@Summary		List backtests
//	@Description	Returns paginated list of backtest jobs for the current user
//	@Tags			backtest
//	@Produce		json
//	@Security		BearerAuth
//	@Param			limit	query	int	false	"Max records (default 20)"
//	@Param			offset	query	int	false	"Pagination offset"
//	@Success		200		{array}		object
//	@Failure		500		{object}	errorResp
//	@Router			/api/backtest [get]
func (s *Server) listBacktests(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	jobs, err := s.store.ListBacktestJobs(r.Context(), userID, limit, offset)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if jobs == nil {
		jobs = []data.BacktestJob{}
	}
	jsonOK(w, jobs)
}

// getBacktest returns the status and result of a specific backtest job.
//
//	@Summary		Get backtest
//	@Description	Returns the status and result of a specific backtest job
//	@Tags			backtest
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Backtest job ID (UUID)"
//	@Success		200	{object}	object
//	@Failure		404	{object}	errorResp
//	@Router			/api/backtest/{id} [get]
func (s *Server) getBacktest(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "id is required", http.StatusBadRequest)
		return
	}

	job, err := s.store.GetBacktestJob(r.Context(), id, userID)
	if err != nil {
		jsonError(w, "backtest not found", http.StatusNotFound)
		return
	}
	jsonOK(w, job)
}

// deleteBacktest removes a backtest job record.
//
//	@Summary		Delete backtest
//	@Description	Delete a backtest job record by ID
//	@Tags			backtest
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Backtest job ID (UUID)"
//	@Success		200	{object}	msgResp
//	@Failure		404	{object}	errorResp
//	@Router			/api/backtest/{id} [delete]
func (s *Server) deleteBacktest(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "id is required", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteBacktestJob(r.Context(), id, userID); err != nil {
		jsonError(w, "backtest not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]string{"message": "deleted"})
}
