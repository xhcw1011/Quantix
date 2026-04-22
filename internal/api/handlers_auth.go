package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Quantix/quantix/internal/data"
)

type registerReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authResp struct {
	Token    string `json:"token"`
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// handleRegister creates a new user account.
//
//	@Summary		Register
//	@Description	Create a new user account
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		registerReq	true	"Registration details"
//	@Success		201		{object}	authResp
//	@Failure		400		{object}	errorResp
//	@Failure		409		{object}	errorResp
//	@Router			/api/auth/register [post]
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)

	if err := validateUsername(req.Username); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateEmail(req.Email); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	id, err := s.store.CreateUser(r.Context(), req.Username, req.Email, hash)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			jsonError(w, "username or email already exists", http.StatusConflict)
			return
		}
		s.log.Sugar().Errorf("create user: %v", err)
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	token, err := GenerateToken(id, req.Username, s.jwtSecret, s.jwtExpiry)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, authResp{Token: token, UserID: id, Username: req.Username, Role: "user"})
}

// handleLogin authenticates a user and returns a JWT token.
//
//	@Summary		Login
//	@Description	Authenticate and receive a JWT token
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		loginReq	true	"Login credentials"
//	@Success		200		{object}	authResp
//	@Failure		401		{object}	errorResp
//	@Failure		403		{object}	errorResp
//	@Router			/api/auth/login [post]
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	user, err := s.store.GetUserByUsername(r.Context(), strings.TrimSpace(req.Username))
	if errors.Is(err, data.ErrNotFound) {
		// Always run bcrypt to prevent timing-based username enumeration.
		// The dummy hash is a valid bcrypt hash that will never match any password.
		_ = CheckPassword(req.Password, "$2a$12$000000000000000000000uGvJMVnKhMYDiybRfZNqOSLOlkPausO")
		jsonError(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	if err := CheckPassword(req.Password, user.PasswordHash); err != nil {
		jsonError(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if !user.IsActive {
		jsonError(w, "account disabled", http.StatusForbidden)
		return
	}

	token, err := GenerateToken(user.ID, user.Username, s.jwtSecret, s.jwtExpiry)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, authResp{Token: token, UserID: user.ID, Username: user.Username, Role: user.Role})
}

// handleChangePassword handles PUT /api/users/me/password.
// Body: {"current_password": "...", "new_password": "..."}
//
//	@Summary		Change password
//	@Description	Update the current user's password
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		object	true	"Password change request"
//	@Success		200		{object}	msgResp
//	@Failure		400		{object}	errorResp
//	@Failure		401		{object}	errorResp
//	@Router			/api/users/me/password [put]
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	if err := CheckPassword(req.CurrentPassword, user.PasswordHash); err != nil {
		jsonError(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}

	newHash, err := HashPassword(req.NewPassword)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	if err := s.store.UpdatePassword(r.Context(), userID, newHash); err != nil {
		s.log.Sugar().Errorf("update password for user %d: %v", userID, err)
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"message": "password updated successfully"})
}

// handleGetNotifications returns the current user's Telegram notification settings.
// GET /api/users/me/notifications
//
//	@Summary		Get notification settings
//	@Description	Returns the current user's Telegram notification configuration
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	errorResp
//	@Router			/api/users/me/notifications [get]
func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	token, chatID, err := s.store.GetUserNotifications(r.Context(), userID)
	if errors.Is(err, data.ErrNotFound) {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	// Mask the token: return only whether it is set, not the full value.
	tokenSet := token != ""
	jsonOK(w, map[string]any{
		"tg_bot_token_set": tokenSet,
		"tg_chat_id":       chatID,
	})
}

// handleUpdateNotifications saves the user's Telegram bot token and chat ID.
// PUT /api/users/me/notifications
// Body: {"tg_bot_token": "...", "tg_chat_id": 123456789}
//
//	@Summary		Update notification settings
//	@Description	Save Telegram bot token and chat ID for notifications
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		object	true	"Notification settings"
//	@Success		200		{object}	msgResp
//	@Failure		400		{object}	errorResp
//	@Router			/api/users/me/notifications [put]
func (s *Server) handleUpdateNotifications(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	var req struct {
		TgBotToken string `json:"tg_bot_token"`
		TgChatID   int64  `json:"tg_chat_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateUserNotifications(r.Context(), userID, strings.TrimSpace(req.TgBotToken), req.TgChatID); err != nil {
		s.log.Sugar().Errorf("update notifications for user %d: %v", userID, err)
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"message": "notification settings updated"})
}

// handleTestNotification sends a test alert through the user's configured channels.
// POST /api/users/me/notifications/test
//
//	@Summary		Test notifications
//	@Description	Send a test message through configured notification channels
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	msgResp
//	@Failure		400	{object}	errorResp
//	@Router			/api/users/me/notifications/test [post]
func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	n := s.manager.buildNotifier(r.Context(), userID)
	if !n.Enabled() {
		jsonError(w, "no notification channels configured", http.StatusBadRequest)
		return
	}

	n.SystemAlert("INFO", "Test notification from Quantix — your alerts are working!")
	jsonOK(w, map[string]string{"message": "test notification sent"})
}

// handleMe returns the current user's profile.
//
//	@Summary		Get current user
//	@Description	Returns the authenticated user's profile information
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	errorResp
//	@Router			/api/users/me [get]
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]any{
		"id":         user.ID,
		"username":   user.Username,
		"email":      user.Email,
		"role":       user.Role,
		"created_at": user.CreatedAt,
	})
}
