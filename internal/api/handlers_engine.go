package api

import (
	"net/http"

	"github.com/Quantix/quantix/internal/strategy/registry"
)

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
