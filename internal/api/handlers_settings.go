package api

import (
	"encoding/json"
	"net/http"
)

// handleGetLiveTrading reports whether the user has enabled real-money live trading
// (the master switch checked before any live engine on a real credential can start).
func (s *Server) handleGetLiveTrading(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	enabled, err := s.store.GetUserBoolPref(r.Context(), userID, "live_trading_enabled")
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"enabled": enabled})
}

// handleSetLiveTrading turns the real-money master switch on or off for the user.
func (s *Server) handleSetLiveTrading(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.store.SetUserBoolPref(r.Context(), userID, "live_trading_enabled", body.Enabled); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"enabled": body.Enabled})
}
