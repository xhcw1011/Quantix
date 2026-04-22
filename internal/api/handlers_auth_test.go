package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Registration validation (fails before touching store)
// ---------------------------------------------------------------------------

func TestHandleRegister_InvalidBody(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := postJSON(t, ts.URL+"/api/auth/register", `{invalid json`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandleRegister_ShortUsername(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"ab","email":"a@b.com","password":"longpassword"}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["error"], "username")
}

func TestHandleRegister_InvalidEmail(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"validuser","email":"not-email","password":"longpassword"}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["error"], "email")
}

func TestHandleRegister_ShortPassword(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"validuser","email":"user@example.com","password":"short"}`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["error"], "password")
}

// ---------------------------------------------------------------------------
// Login validation (fails before touching store)
// ---------------------------------------------------------------------------

func TestHandleLogin_InvalidBody(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := postJSON(t, ts.URL+"/api/auth/login", `{bad json`)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Protected endpoint access without/invalid token
// ---------------------------------------------------------------------------

func TestProtectedEndpoint_NoToken(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp, err := http.Get(ts.URL + "/api/strategies")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestProtectedEndpoint_InvalidToken(t *testing.T) {
	_, ts := newTestServerNoStore(t)
	resp := getWithAuth(t, ts.URL+"/api/strategies", "not.a.valid.token")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
