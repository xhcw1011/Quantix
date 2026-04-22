package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	xfactory "github.com/Quantix/quantix/internal/exchange/factory"
)

type credentialReq struct {
	Exchange   string `json:"exchange"`
	Label      string `json:"label"`
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase"`
	Testnet    bool   `json:"testnet"`
	Demo       bool   `json:"demo"`
	MarketType string `json:"market_type"`
}

type credentialResp struct {
	ID         int    `json:"id"`
	Exchange   string `json:"exchange"`
	Label      string `json:"label"`
	APIKeyMask string `json:"api_key_mask"` // first 6 chars + "..."
	Testnet    bool   `json:"testnet"`
	Demo       bool   `json:"demo"`
	MarketType string `json:"market_type"`
	IsActive   bool   `json:"is_active"`
	CreatedAt  string `json:"created_at"`
}

func maskKey(key string) string {
	if len(key) <= 6 {
		return "***"
	}
	return key[:6] + "..."
}

// handleListCredentials returns all exchange credentials for the current user.
//
//	@Summary		List credentials
//	@Description	Returns all saved exchange API credentials (keys are masked)
//	@Tags			credentials
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{array}		credentialResp
//	@Failure		500	{object}	errorResp
//	@Router			/api/credentials [get]
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	creds, err := s.store.GetCredentials(r.Context(), userID)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	out := make([]credentialResp, 0, len(creds))
	for _, c := range creds {
		plain, decErr := s.enc.Decrypt(c.APIKey)
		if decErr != nil {
			s.log.Warn("failed to decrypt API key for credential list",
				zap.Int("credential_id", c.ID), zap.Error(decErr))
		}
		out = append(out, credentialResp{
			ID:         c.ID,
			Exchange:   c.Exchange,
			Label:      c.Label,
			APIKeyMask: maskKey(plain),
			Testnet:    c.Testnet,
			Demo:       c.Demo,
			MarketType: c.MarketType,
			IsActive:   c.IsActive,
			CreatedAt:  c.CreatedAt.Format(time.RFC3339),
		})
	}
	jsonOK(w, out)
}

// handleCreateCredential saves a new exchange API credential.
//
//	@Summary		Create credential
//	@Description	Save a new exchange API key and secret (encrypted at rest)
//	@Tags			credentials
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		credentialReq	true	"Credential details"
//	@Success		201		{object}	map[string]interface{}
//	@Failure		400		{object}	errorResp
//	@Failure		500		{object}	errorResp
//	@Router			/api/credentials [post]
func (s *Server) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	var req credentialReq
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validateExchange(req.Exchange); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateLabel(req.Label); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAPIKey(req.APIKey); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateAPIKey(req.APISecret); err != nil {
		jsonError(w, "api_secret: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.MarketType == "" {
		req.MarketType = "spot"
	}

	encKey, err := s.enc.Encrypt(req.APIKey)
	if err != nil {
		jsonError(w, "encryption error", http.StatusInternalServerError)
		return
	}
	encSecret, err := s.enc.Encrypt(req.APISecret)
	if err != nil {
		jsonError(w, "encryption error", http.StatusInternalServerError)
		return
	}
	encPassphrase := ""
	if req.Passphrase != "" {
		encPassphrase, err = s.enc.Encrypt(req.Passphrase)
		if err != nil {
			jsonError(w, "encryption error", http.StatusInternalServerError)
			return
		}
	}

	cred := &data.Credential{
		UserID:     userID,
		Exchange:   req.Exchange,
		Label:      req.Label,
		APIKey:     encKey,
		APISecret:  encSecret,
		Passphrase: encPassphrase,
		Testnet:    req.Testnet,
		Demo:       req.Demo,
		MarketType: req.MarketType,
	}
	id, err := s.store.CreateCredential(r.Context(), cred)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"id": id, "message": "credential created"})
}

// handleUpdateCredential updates an existing credential.
//
//	@Summary		Update credential
//	@Description	Update an existing exchange API credential by ID
//	@Tags			credentials
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		int				true	"Credential ID"
//	@Param			body	body		credentialReq	true	"Updated credential fields"
//	@Success		200		{object}	msgResp
//	@Failure		400		{object}	errorResp
//	@Failure		404		{object}	errorResp
//	@Router			/api/credentials/{id} [put]
func (s *Server) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	credID := parseIntParam(r.PathValue("id"))
	if credID == 0 {
		jsonError(w, "invalid credential id", http.StatusBadRequest)
		return
	}

	var req credentialReq
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	existing, err := s.store.GetCredentialByID(r.Context(), credID, userID)
	if errors.Is(err, data.ErrNotFound) {
		jsonError(w, "credential not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	// Keep existing encrypted values if new ones are not provided
	if req.APIKey != "" {
		encKey, err := s.enc.Encrypt(req.APIKey)
		if err != nil {
			jsonError(w, "encryption error", http.StatusInternalServerError)
			return
		}
		existing.APIKey = encKey
	}
	if req.APISecret != "" {
		encSecret, err := s.enc.Encrypt(req.APISecret)
		if err != nil {
			jsonError(w, "encryption error", http.StatusInternalServerError)
			return
		}
		existing.APISecret = encSecret
	}
	if req.Passphrase != "" {
		encPass, err := s.enc.Encrypt(req.Passphrase)
		if err != nil {
			jsonError(w, "encryption error", http.StatusInternalServerError)
			return
		}
		existing.Passphrase = encPass
	}
	if req.Label != "" {
		existing.Label = req.Label
	}
	if req.MarketType != "" {
		existing.MarketType = req.MarketType
	}
	existing.Testnet = req.Testnet
	existing.Demo = req.Demo

	if err := s.store.UpdateCredential(r.Context(), existing); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"message": "credential updated"})
}

// handleDeleteCredential removes a credential by ID.
//
//	@Summary		Delete credential
//	@Description	Permanently delete an exchange API credential
//	@Tags			credentials
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		int	true	"Credential ID"
//	@Success		200	{object}	msgResp
//	@Failure		404	{object}	errorResp
//	@Router			/api/credentials/{id} [delete]
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	credID := parseIntParam(r.PathValue("id"))
	if credID == 0 {
		jsonError(w, "invalid credential id", http.StatusBadRequest)
		return
	}

	if err := s.store.DeleteCredential(r.Context(), credID, userID); errors.Is(err, data.ErrNotFound) {
		jsonError(w, "credential not found", http.StatusNotFound)
		return
	} else if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"message": "credential deleted"})
}

// handleTestCredential verifies exchange API connectivity.
//
//	@Summary		Test credential
//	@Description	Test exchange API connectivity and return USDT balance
//	@Tags			credentials
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		int	true	"Credential ID"
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	errorResp
//	@Router			/api/credentials/{id}/test [post]
func (s *Server) handleTestCredential(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	credID := parseIntParam(r.PathValue("id"))
	if credID == 0 {
		jsonError(w, "invalid credential id", http.StatusBadRequest)
		return
	}

	cred, err := s.store.GetCredentialByID(r.Context(), credID, userID)
	if errors.Is(err, data.ErrNotFound) {
		jsonError(w, "credential not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	// Decrypt keys
	apiKey, err := s.enc.Decrypt(cred.APIKey)
	if err != nil {
		jsonError(w, "decryption error", http.StatusInternalServerError)
		return
	}
	apiSecret, err := s.enc.Decrypt(cred.APISecret)
	if err != nil {
		jsonError(w, "decryption error", http.StatusInternalServerError)
		return
	}
	passphrase := ""
	if cred.Passphrase != "" {
		passphrase, err = s.enc.Decrypt(cred.Passphrase)
		if err != nil {
			jsonError(w, "credential decryption error", http.StatusInternalServerError)
			return
		}
	}

	// Build exchange config for connectivity test
	excCfg := config.ExchangeConfig{Active: cred.Exchange}
	switch cred.Exchange {
	case "binance":
		excCfg.Binance = config.BinanceConfig{
			APIKey: apiKey, APISecret: apiSecret,
			Testnet: cred.Testnet, MarketType: cred.MarketType,
		}
	case "okx":
		excCfg.OKX = config.OKXConfig{
			APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase,
			Demo: cred.Demo, MarketType: cred.MarketType,
		}
	case "bybit":
		excCfg.Bybit = config.BybitConfig{APIKey: apiKey, APISecret: apiSecret}
	default:
		jsonError(w, "unsupported exchange: "+cred.Exchange, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	client, err := xfactory.NewOrderClient(excCfg, s.log)
	if err != nil {
		jsonOK(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Get balance as connectivity test
	balance, err := client.GetBalance(ctx, "USDT")
	if err != nil {
		jsonOK(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonOK(w, map[string]any{"ok": true, "usdt_balance": balance})
}
