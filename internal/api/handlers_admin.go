package api

import (
	"net/http"
	"strconv"
)

// adminListUsers handles GET /api/admin/users
// Returns all registered users with their running engine count.
//
//	@Summary		Admin: List users
//	@Description	Returns all registered users (admin only)
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		map[string]interface{}
//	@Failure		403	{object}	errorResp
//	@Failure		500	{object}	errorResp
//	@Router			/api/admin/users [get]
func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.GetAllUsers(r.Context())
	if err != nil {
		jsonError(w, "failed to list users: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Count running engines per user
	allEngines := s.manager.ListAllGlobalEngines()
	engineCountByUser := make(map[int]int)
	for _, e := range allEngines {
		if e.Running {
			engineCountByUser[e.UserID]++
		}
	}

	type userInfo struct {
		ID            int    `json:"id"`
		Username      string `json:"username"`
		Email         string `json:"email"`
		Role          string `json:"role"`
		IsActive      bool   `json:"is_active"`
		CreatedAt     string `json:"created_at"`
		RunningEngines int   `json:"running_engines"`
	}

	out := make([]userInfo, len(users))
	for i, u := range users {
		out[i] = userInfo{
			ID:             u.ID,
			Username:       u.Username,
			Email:          u.Email,
			Role:           u.Role,
			IsActive:       u.IsActive,
			CreatedAt:      u.CreatedAt.Format("2006-01-02T15:04:05Z"),
			RunningEngines: engineCountByUser[u.ID],
		}
	}
	jsonOK(w, out)
}

// adminSetUserActive handles PUT /api/admin/users/{id}/activate
// Body: {"active": true|false}
//
//	@Summary		Admin: Set user active
//	@Description	Activate or deactivate a user account (admin only)
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		int		true	"User ID"
//	@Param			body	body		object	true	"Active flag"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	errorResp
//	@Failure		403		{object}	errorResp
//	@Router			/api/admin/users/{id}/activate [put]
func (s *Server) adminSetUserActive(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	userID, err := strconv.Atoi(idStr)
	if err != nil || userID <= 0 {
		jsonError(w, "invalid user id", http.StatusBadRequest)
		return
	}

	var body struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.store.SetUserActive(r.Context(), userID, body.Active); err != nil {
		jsonError(w, "update failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// If deactivating, stop all engines for that user
	if !body.Active {
		s.manager.StopAll(userID)
	}

	jsonOK(w, map[string]any{"user_id": userID, "is_active": body.Active})
}

// adminListEngines handles GET /api/admin/engines
// Returns all running engines across all users.
//
//	@Summary		Admin: List all engines
//	@Description	Returns all running engines across all users (admin only)
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		EngineInfo
//	@Failure		403	{object}	errorResp
//	@Router			/api/admin/engines [get]
func (s *Server) adminListEngines(w http.ResponseWriter, r *http.Request) {
	engines := s.manager.ListAllGlobalEngines()
	if engines == nil {
		engines = []EngineInfo{}
	}
	jsonOK(w, engines)
}

// adminForceStopEngine handles DELETE /api/admin/engines/{user_id}/{engine_id}
// Force-stops any engine regardless of owner.
//
//	@Summary		Admin: Force stop engine
//	@Description	Force-stop any engine regardless of owner (admin only)
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Param			user_id		path		int		true	"User ID"
//	@Param			engine_id	path		string	true	"Engine ID"
//	@Success		200			{object}	map[string]string
//	@Failure		400			{object}	errorResp
//	@Failure		403			{object}	errorResp
//	@Failure		404			{object}	errorResp
//	@Router			/api/admin/engines/{user_id}/{engine_id} [delete]
func (s *Server) adminForceStopEngine(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.PathValue("user_id")
	engineID := r.PathValue("engine_id")

	userID, err := strconv.Atoi(userIDStr)
	if err != nil || userID <= 0 {
		jsonError(w, "invalid user_id", http.StatusBadRequest)
		return
	}
	if engineID == "" {
		jsonError(w, "missing engine_id", http.StatusBadRequest)
		return
	}

	if err := s.manager.ForceStop(userID, engineID); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	jsonOK(w, map[string]string{"status": "stopped", "engine_id": engineID})
}
