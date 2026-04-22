package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// HashPassword hashes a plaintext password with bcrypt.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword returns nil if password matches the stored hash.
func CheckPassword(password, hash string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// Claims holds the JWT payload.
type Claims struct {
	UserID   int    `json:"sub"`
	Username string `json:"username"`
	Exp      int64  `json:"exp"`
}

// jwtHeader is the standard HS256 header, pre-encoded.
var jwtHeader = base64url([]byte(`{"alg":"HS256","typ":"JWT"}`))

// GenerateToken creates a signed JWT valid for the given expiry duration.
func GenerateToken(userID int, username, secret string, expiry time.Duration) (string, error) {
	payload, err := json.Marshal(Claims{
		UserID:   userID,
		Username: username,
		Exp:      time.Now().Add(expiry).Unix(),
	})
	if err != nil {
		return "", err
	}
	data := jwtHeader + "." + base64url(payload)
	sig := signHS256(data, secret)
	return data + "." + sig, nil
}

// ValidateToken parses and verifies a JWT, returning its claims.
func ValidateToken(token, secret string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}
	data := parts[0] + "." + parts[1]
	if signHS256(data, secret) != parts[2] {
		return nil, errors.New("invalid token signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}
	if time.Now().Unix() > c.Exp {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

func signHS256(data, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return base64url(mac.Sum(nil))
}

func base64url(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// extractBearerToken returns the token string from "Authorization: Bearer <token>".
func extractBearerToken(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

// parseIntParam parses a path or query string integer; returns 0 on failure.
func parseIntParam(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
