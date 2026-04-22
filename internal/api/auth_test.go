package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidateToken(t *testing.T) {
	secret := "test-secret-key-123"
	token, err := GenerateToken(42, "alice", secret, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := ValidateToken(token, secret)
	require.NoError(t, err)
	assert.Equal(t, 42, claims.UserID)
	assert.Equal(t, "alice", claims.Username)
	assert.True(t, claims.Exp > time.Now().Unix())
}

func TestValidateToken_WrongSecret(t *testing.T) {
	token, err := GenerateToken(1, "bob", "correct-secret", time.Hour)
	require.NoError(t, err)

	_, err = ValidateToken(token, "wrong-secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token signature")
}

func TestValidateToken_Expired(t *testing.T) {
	token, err := GenerateToken(1, "bob", "secret", -time.Hour)
	require.NoError(t, err)

	_, err = ValidateToken(token, "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token expired")
}

func TestValidateToken_InvalidFormat(t *testing.T) {
	_, err := ValidateToken("not-a-jwt", "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token format")

	_, err = ValidateToken("a.b", "secret")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token format")

	_, err = ValidateToken("", "secret")
	assert.Error(t, err)
}

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("mypassword123")
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	err = CheckPassword("mypassword123", hash)
	assert.NoError(t, err)

	err = CheckPassword("wrongpassword", hash)
	assert.Error(t, err)
}

func TestExtractBearerToken(t *testing.T) {
	assert.Equal(t, "abc123", extractBearerToken("Bearer abc123"))
	assert.Equal(t, "", extractBearerToken("Basic abc123"))
	assert.Equal(t, "", extractBearerToken(""))
}
