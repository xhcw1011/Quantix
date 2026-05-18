package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Quantix/quantix/internal/data"
)

// filterFromQuery parses common fill/order filter params from query string.
// Dates must be in YYYY-MM-DD format; "to" is extended to end-of-day.
func filterFromQuery(r *http.Request) data.RecordFilter {
	f := data.RecordFilter{
		Symbol:        r.URL.Query().Get("symbol"),
		StrategyID:    r.URL.Query().Get("strategy_id"),
		Mode:          r.URL.Query().Get("mode"),
		ClientOrderID: r.URL.Query().Get("client_order_id"),
	}
	if from := r.URL.Query().Get("from"); from != "" {
		f.From, _ = time.Parse("2006-01-02", from)
	}
	if to := r.URL.Query().Get("to"); to != "" {
		t, err := time.Parse("2006-01-02", to)
		if err == nil {
			f.To = t.Add(24*time.Hour - time.Nanosecond)
		}
	}
	return f
}

// handleOrders returns paginated order history for the current user.
//
//	@Summary		List orders
//	@Description	Returns paginated order history with optional filters
//	@Tags			trading
//	@Produce		json
//	@Security		BearerAuth
//	@Param			symbol			query	string	false	"Filter by symbol (e.g. BTCUSDT)"
//	@Param			strategy_id		query	string	false	"Filter by strategy ID"
//	@Param			mode			query	string	false	"Filter by mode (live|paper)"
//	@Param			from			query	string	false	"Start date YYYY-MM-DD"
//	@Param			to				query	string	false	"End date YYYY-MM-DD"
//	@Param			client_order_id	query	string	false	"Exact match on client order ID (32-char idempotency key)"
//	@Param			limit			query	int		false	"Max records (default 50)"
//	@Param			offset			query	int		false	"Pagination offset"
//	@Success		200				{object}	map[string]interface{}
//	@Failure		500				{object}	errorResp
//	@Router			/api/orders [get]
func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	limit, offset := paginationParams(r)
	f := filterFromQuery(r)

	orders, err := s.store.GetOrders(r.Context(), userID, limit, offset, f)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if orders == nil {
		orders = []data.OrderRecord{}
	}
	jsonOK(w, map[string]any{"orders": orders, "limit": limit, "offset": offset})
}

// handleFills returns paginated fill (trade execution) history.
//
//	@Summary		List fills
//	@Description	Returns paginated fill history with optional filters
//	@Tags			trading
//	@Produce		json
//	@Security		BearerAuth
//	@Param			symbol		query	string	false	"Filter by symbol"
//	@Param			strategy_id	query	string	false	"Filter by strategy ID"
//	@Param			mode		query	string	false	"Filter by mode (live|paper)"
//	@Param			from		query	string	false	"Start date YYYY-MM-DD"
//	@Param			to			query	string	false	"End date YYYY-MM-DD"
//	@Param			limit		query	int		false	"Max records (default 50)"
//	@Param			offset		query	int		false	"Pagination offset"
//	@Success		200			{object}	map[string]interface{}
//	@Failure		500			{object}	errorResp
//	@Router			/api/fills [get]
func (s *Server) handleFills(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	limit, offset := paginationParams(r)
	f := filterFromQuery(r)

	fills, err := s.store.GetFills(r.Context(), userID, limit, offset, f)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if fills == nil {
		fills = []data.Fill{}
	}
	jsonOK(w, map[string]any{"fills": fills, "limit": limit, "offset": offset})
}

// handlePositions returns live open positions across all running engines.
//
//	@Summary		Get positions
//	@Description	Returns live open positions with unrealized P&L for all running engines
//	@Tags			trading
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/api/positions [get]
func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	snapshots := s.manager.GetPositions(userID)
	if snapshots == nil {
		snapshots = []EnginePositions{}
	}
	jsonOK(w, map[string]any{"positions": snapshots})
}

// handleEquity returns equity curve snapshots.
//
//	@Summary		Get equity curve
//	@Description	Returns equity snapshot history for chart display
//	@Tags			trading
//	@Produce		json
//	@Security		BearerAuth
//	@Param			strategy_id	query	string	false	"Filter by strategy ID"
//	@Param			limit		query	int		false	"Max records (default 200, max 5000)"
//	@Param			since		query	int		false	"Unix timestamp seconds; only snapshots >= this time"
//	@Param			period		query	string	false	"Convenience filter: 1d, 7d, 30d, all (overrides since)"
//	@Success		200			{object}	map[string]interface{}
//	@Failure		500			{object}	errorResp
//	@Router			/api/equity [get]
func (s *Server) handleEquity(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	strategyID := r.URL.Query().Get("strategy_id")
	limitStr := r.URL.Query().Get("limit")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 5000 {
		limit = 200
	}

	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			since = n
		}
	}
	// period is a friendlier alias for since; "all" clears it.
	switch r.URL.Query().Get("period") {
	case "1d":
		since = time.Now().Add(-24 * time.Hour).Unix()
		if limit < 500 {
			limit = 500
		}
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour).Unix()
		if limit < 2000 {
			limit = 2000
		}
	case "30d":
		since = time.Now().Add(-30 * 24 * time.Hour).Unix()
		if limit < 5000 {
			limit = 5000
		}
	case "all":
		since = 0
		if limit < 5000 {
			limit = 5000
		}
	}

	snapshots, err := s.store.GetEquitySnapshots(r.Context(), userID, strategyID, limit, since)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"snapshots": snapshots})
}

// handleSummary returns a dashboard summary (equity, win rate, engine status).
//
//	@Summary		Get summary
//	@Description	Returns aggregated dashboard stats: equity, P&L, win rate, engine status
//	@Tags			trading
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	map[string]interface{}
//	@Router			/api/summary [get]
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	// Latest equity snapshot
	snapshots, _ := s.store.GetEquitySnapshots(r.Context(), userID, "", 1, 0)
	var equity, cash, unrealized, realized float64
	if len(snapshots) > 0 {
		latest := snapshots[len(snapshots)-1]
		equity = latest.Equity
		cash = latest.Cash
		unrealized = latest.UnrealizedPnL
		realized = latest.RealizedPnL
	}

	// Fill stats
	fills, _ := s.store.GetFills(r.Context(), userID, 1000, 0, data.RecordFilter{})
	totalFills := len(fills)
	wins := 0
	for _, f := range fills {
		if f.RealizedPnL > 0 {
			wins++
		}
	}
	winRate := 0.0
	if totalFills > 0 {
		winRate = float64(wins) / float64(totalFills) * 100
	}

	// Engine status
	status := s.manager.Status(userID)

	jsonOK(w, map[string]any{
		"equity":         equity,
		"cash":           cash,
		"unrealized_pnl": unrealized,
		"realized_pnl":   realized,
		"total_fills":    totalFills,
		"win_rate":       winRate,
		"engine_status":  map[bool]string{true: "running", false: "stopped"}[status.Running],
		"strategy_id":    status.StrategyID,
	})
}

func paginationParams(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return
}
