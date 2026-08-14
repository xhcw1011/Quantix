package api

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Quantix/quantix/internal/strategy/registry"
)

// tailFile returns the last n lines of the file. Reads the whole file (fine for
// daily log sizes we see — a few MB at most). Returns ([]string, nil) even when
// the file doesn't exist (treats as empty).
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow long lines
	buf := make([]string, 0, n)
	for sc.Scan() {
		buf = append(buf, sc.Text())
		if len(buf) > n*2 { // drop oldest in chunks to avoid huge slice
			buf = buf[len(buf)-n:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	return buf, nil
}

// handleListStrategies returns all registered strategy IDs.
// Used by the frontend to populate the strategy dropdown dynamically.
//
//	@Summary		List strategies
//	@Description	Returns all registered trading strategy identifiers
//	@Tags			strategies
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		string
//	@Router			/api/strategies [get]
func (s *Server) handleListStrategies(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, registry.Names())
}

// ── New multi-engine endpoints ────────────────────────────────────────────────

// listEngines returns all running engines for the current user.
//
//	@Summary		List engines
//	@Description	Returns all running and recently stopped engines for the authenticated user
//	@Tags			engines
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		EngineInfo
//	@Router			/api/engines [get]
func (s *Server) listEngines(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	jsonOK(w, s.manager.ListAll(userID))
}

// startEngine creates and starts a new live or paper trading engine.
//
//	@Summary		Start engine
//	@Description	Start a new live or paper trading engine for the authenticated user
//	@Tags			engines
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		StartRequest	true	"Engine start parameters"
//	@Success		201		{object}	map[string]string
//	@Failure		400		{object}	errorResp
//	@Router			/api/engines [post]
func (s *Server) startEngine(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	var req StartRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.CredentialID == 0 {
		jsonError(w, "credential_id is required", http.StatusBadRequest)
		return
	}
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

	engineID, err := s.manager.Start(userID, req)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]string{"engine_id": engineID, "message": "engine started"})
}

// stopEngineByID stops a specific engine by ID.
//
//	@Summary		Stop engine
//	@Description	Gracefully stop a running engine by its ID
//	@Tags			engines
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Engine ID"
//	@Success		200	{object}	msgResp
//	@Failure		404	{object}	errorResp
//	@Router			/api/engines/{id} [delete]
func (s *Server) stopEngineByID(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	engineID := r.PathValue("id")
	if engineID == "" {
		jsonError(w, "engine id is required", http.StatusBadRequest)
		return
	}

	if err := s.manager.Stop(userID, engineID); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]string{"message": "engine stopped"})
}

// getEngineByID returns info for a specific engine.
//
//	@Summary		Get engine
//	@Description	Returns the status and info for a specific engine
//	@Tags			engines
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Engine ID"
//	@Success		200	{object}	EngineInfo
//	@Failure		404	{object}	errorResp
//	@Router			/api/engines/{id} [get]
func (s *Server) getEngineByID(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	engineID := r.PathValue("id")
	if engineID == "" {
		jsonError(w, "engine id is required", http.StatusBadRequest)
		return
	}

	info, err := s.manager.GetEngine(userID, engineID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonOK(w, info)
}

// listStrategyPresets returns the preset configurations registered for a strategy.
// Empty array (not 404) when the strategy has no presets.
//
//	@Summary		List strategy presets
//	@Description	Returns one-click param bundles for a strategy
//	@Tags			strategies
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path	string	true	"Strategy name"
//	@Success		200		{array}	registry.Preset
//	@Router			/api/strategies/{name}/presets [get]
func (s *Server) listStrategyPresets(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		jsonError(w, "strategy name is required", http.StatusBadRequest)
		return
	}
	if !registry.Exists(name) {
		jsonError(w, "unknown strategy: "+name, http.StatusNotFound)
		return
	}
	presets := registry.Presets(name)
	if presets == nil {
		presets = []registry.Preset{} // ensure JSON array, not null
	}
	jsonOK(w, presets)
}

// filterLogLines keeps only lines matching BOTH engineID (if set) and grep
// (if set) — engineID is a real filter, not the no-op it used to be (a
// 2026-08-06 bug let any authenticated user read every other user's log
// lines via this endpoint, since this log file is shared across the whole
// multi-tenant server). Trims to the last `limit` matches AFTER filtering,
// not before, so the caller gets `limit` relevant lines rather than `limit`
// lines it then has to filter down from.
func filterLogLines(lines []string, engineID, grep string, limit int) []string {
	out := make([]string, 0, limit)
	for _, l := range lines {
		if engineID != "" && !strings.Contains(l, engineID) {
			continue
		}
		if grep != "" && !strings.Contains(l, grep) {
			continue
		}
		out = append(out, l)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// recentLogs returns the last N lines of today's quantix log file, optionally
// filtered by an engine_id substring. Used by the Live Log Viewer page so the
// operator can read strategy decisions without SSH'ing the server.
//
//	@Summary		Recent log lines
//	@Description	Returns last N lines of today's process log. Optional grep filter.
//	@Tags			engines
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path	string	true	"Engine ID (used for filtering only)"
//	@Param			lines	query	int		false	"Number of trailing lines to return (default 200, max 2000)"
//	@Param			grep	query	string	false	"Optional substring filter (case-sensitive)"
//	@Success		200		{object}	map[string]any
//	@Failure		500		{object}	errorResp
//	@Router			/api/engines/{id}/recent-logs [get]
func (s *Server) recentLogs(w http.ResponseWriter, r *http.Request) {
	if s.logDir == "" {
		jsonError(w, "log directory not configured", http.StatusServiceUnavailable)
		return
	}
	engineID := r.PathValue("id")
	limit := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 2000 {
		limit = 2000
	}
	grep := r.URL.Query().Get("grep")

	path := filepath.Join(s.logDir, fmt.Sprintf("quantix-%s.log", time.Now().Format("20060102")))
	lines, err := tailFile(path, limit*4) // read more so we can filter
	if err != nil {
		jsonError(w, "read log: "+err.Error(), http.StatusInternalServerError)
		return
	}

	out := filterLogLines(lines, engineID, grep, limit)

	jsonOK(w, map[string]any{
		"engine_id": engineID,
		"file":      path,
		"count":     len(out),
		"lines":     out,
	})
}

// closeEnginePosition closes a single side of the engine's open position.
//
//	@Summary		Close one side of an engine's position
//	@Description	Places a reduce-only market order to flatten the LONG or SHORT side
//	@Description	for the symbol the engine is trading. Does not stop the engine.
//	@Tags			engines
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Engine ID"
//	@Param			side	query		string	true	"LONG or SHORT"
//	@Success		200		{object}	map[string]any
//	@Failure		400		{object}	errorResp
//	@Failure		404		{object}	errorResp
//	@Failure		500		{object}	errorResp
//	@Router			/api/engines/{id}/close-position [post]
//
// updateEngineParams applies live parameter changes to a running engine's
// strategy (currently the guardian's protective config).
func (s *Server) updateEngineParams(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	engineID := r.PathValue("id")
	if engineID == "" {
		jsonError(w, "engine id is required", http.StatusBadRequest)
		return
	}
	var body struct {
		Params map[string]any `json:"params"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Params) == 0 {
		jsonError(w, "params object is required", http.StatusBadRequest)
		return
	}
	if err := s.manager.UpdateParams(userID, engineID, body.Params); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{"status": "updated", "engine_id": engineID})
}

func (s *Server) closeEnginePosition(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	engineID := r.PathValue("id")
	side := strings.ToUpper(r.URL.Query().Get("side"))
	if engineID == "" {
		jsonError(w, "engine id is required", http.StatusBadRequest)
		return
	}
	if side != "LONG" && side != "SHORT" {
		jsonError(w, "side must be LONG or SHORT", http.StatusBadRequest)
		return
	}
	symbol, qty, price, err := s.manager.ClosePosition(r.Context(), userID, engineID, side)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonOK(w, map[string]any{
		"status":     "closed",
		"engine_id":  engineID,
		"symbol":     symbol,
		"side":       side,
		"qty":        qty,
		"fill_price": price,
	})
}

// ── Legacy endpoints (kept for backward compat) ───────────────────────────────

// POST /api/engine/start  (deprecated — use POST /api/engines)
func (s *Server) handleEngineStart(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	var req StartRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.CredentialID == 0 || req.StrategyID == "" || req.Symbol == "" || req.Interval == "" {
		jsonError(w, "credential_id, strategy_id, symbol and interval are required", http.StatusBadRequest)
		return
	}

	engineID, err := s.manager.Start(userID, req)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonOK(w, map[string]string{"message": "engine started", "status": "running", "engine_id": engineID})
}

// POST /api/engine/stop  (deprecated — stops all user engines)
func (s *Server) handleEngineStop(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	s.manager.StopAll(userID)
	jsonOK(w, map[string]string{"message": "all engines stopped", "status": "stopped"})
}

// GET /api/engine/status  (deprecated — returns first engine status)
func (s *Server) handleEngineStatus(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	jsonOK(w, s.manager.Status(userID))
}
