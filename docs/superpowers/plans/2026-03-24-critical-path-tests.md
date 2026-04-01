# Critical Path Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add unit tests for the untested critical paths: API auth/middleware, input validation, crypto, paper broker order flow, and live broker order routing.

**Architecture:** Each test file uses inline mock structs (project convention — no gomock/testify.mock). Tests use `testify/assert` + `testify/require`. API handler tests use `httptest.NewServer` with the real `Server.Handler()` mux. Broker tests use mock `OrderClient`. No real DB or exchange connections.

**Tech Stack:** Go 1.24, `testing`, `net/http/httptest`, `github.com/stretchr/testify/{assert,require}`, `go.uber.org/zap`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|---------------|
| Create | `internal/api/auth_test.go` | JWT generation/validation, password hashing, token extraction |
| Create | `internal/api/validate_test.go` | Input validation functions (username, email, password, symbol, interval, exchange) |
| Create | `internal/api/crypto_test.go` | AES-256-GCM encrypt/decrypt round-trip, bad key, tampered ciphertext |
| Create | `internal/api/middleware_test.go` | Auth middleware (valid/invalid/expired/missing token, inactive user), rate limiter, CORS, realIP extraction |
| Create | `internal/api/handlers_auth_test.go` | Register, login, change password handlers via httptest |
| Create | `internal/api/handlers_engine_test.go` | Engine start/stop/list/status handlers via httptest |
| Create | `internal/paper/broker_test.go` | Paper broker: market order, limit trigger, stop trigger, TP/SL, short, cash accounting |
| Create | `internal/live/broker_test.go` | Live broker: market order routing, limit async, stop async, retry on transient error, balance sync, cancel-all |

---

### Task 1: API Auth — JWT & Password Tests

**Files:**
- Create: `internal/api/auth_test.go`

- [ ] **Step 1: Write JWT and password tests**

```go
package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidateToken(t *testing.T) {
	secret := "test-secret-at-least-32-chars-long"
	token, err := GenerateToken(42, "alice", secret, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := ValidateToken(token, secret)
	require.NoError(t, err)
	assert.Equal(t, 42, claims.UserID)
	assert.Equal(t, "alice", claims.Username)
}

func TestValidateToken_WrongSecret(t *testing.T) {
	token, _ := GenerateToken(1, "bob", "secret-a-32-chars-minimum-xxxxx", time.Hour)
	_, err := ValidateToken(token, "secret-b-32-chars-minimum-xxxxx")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestValidateToken_Expired(t *testing.T) {
	token, _ := GenerateToken(1, "bob", "test-secret-at-least-32-chars-long", -time.Hour)
	_, err := ValidateToken(token, "test-secret-at-least-32-chars-long")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestValidateToken_InvalidFormat(t *testing.T) {
	_, err := ValidateToken("not-a-jwt", "secret")
	assert.Error(t, err)
}

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("MyP@ssw0rd!")
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	assert.NoError(t, CheckPassword("MyP@ssw0rd!", hash))
	assert.Error(t, CheckPassword("wrong-password", hash))
}

func TestExtractBearerToken(t *testing.T) {
	assert.Equal(t, "abc123", extractBearerToken("Bearer abc123"))
	assert.Equal(t, "", extractBearerToken("Basic abc123"))
	assert.Equal(t, "", extractBearerToken(""))
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/api/ -run TestGenerate -v && go test ./internal/api/ -run TestHash -v && go test ./internal/api/ -run TestExtract -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/api/auth_test.go
git commit -m "test: add JWT generation/validation and password hashing tests"
```

---

### Task 2: Input Validation Tests

**Files:**
- Create: `internal/api/validate_test.go`

- [ ] **Step 1: Write validation tests**

```go
package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateUsername(t *testing.T) {
	assert.NoError(t, validateUsername("alice"))
	assert.NoError(t, validateUsername("user_123"))
	assert.Error(t, validateUsername("ab"))                      // too short
	assert.Error(t, validateUsername(strings.Repeat("a", 33)))   // too long
	assert.Error(t, validateUsername("user name"))               // space
	assert.Error(t, validateUsername("user@name"))               // special char
}

func TestValidateEmail(t *testing.T) {
	assert.NoError(t, validateEmail("user@example.com"))
	assert.Error(t, validateEmail("not-an-email"))
	assert.Error(t, validateEmail("@example.com"))
	assert.Error(t, validateEmail("user@"))
	assert.Error(t, validateEmail("user@nodot"))
}

func TestValidatePassword(t *testing.T) {
	assert.NoError(t, validatePassword("12345678"))
	assert.Error(t, validatePassword("short"))                    // too short
	assert.Error(t, validatePassword(strings.Repeat("a", 129)))   // too long
}

func TestValidateSymbol(t *testing.T) {
	assert.NoError(t, validateSymbol("BTCUSDT"))
	assert.Error(t, validateSymbol(""))
	assert.Error(t, validateSymbol("BTC-USDT"))   // hyphen not allowed
	assert.Error(t, validateSymbol(strings.Repeat("A", 21)))
}

func TestValidateInterval(t *testing.T) {
	assert.NoError(t, validateInterval("1h"))
	assert.NoError(t, validateInterval("1d"))
	assert.Error(t, validateInterval("2h"))
	assert.Error(t, validateInterval(""))
}

func TestValidateExchange(t *testing.T) {
	assert.NoError(t, validateExchange("binance"))
	assert.NoError(t, validateExchange("okx"))
	assert.NoError(t, validateExchange("bybit"))
	assert.Error(t, validateExchange("kraken"))
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/api/ -run TestValidate -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/api/validate_test.go
git commit -m "test: add input validation tests for username, email, password, symbol, interval, exchange"
```

---

### Task 3: Crypto (AES-256-GCM) Tests

**Files:**
- Create: `internal/api/crypto_test.go`

- [ ] **Step 1: Write crypto tests**

```go
package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptorRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := NewEncryptor(key)
	require.NoError(t, err)

	plaintext := "my-super-secret-api-key"
	ciphertext, err := enc.Encrypt(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := enc.Decrypt(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptorDifferentCiphertexts(t *testing.T) {
	key := make([]byte, 32)
	enc, _ := NewEncryptor(key)

	c1, _ := enc.Encrypt("same")
	c2, _ := enc.Encrypt("same")
	assert.NotEqual(t, c1, c2, "different nonces should produce different ciphertexts")
}

func TestEncryptorBadKeyLength(t *testing.T) {
	_, err := NewEncryptor([]byte("short"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")
}

func TestEncryptorDecryptTampered(t *testing.T) {
	key := make([]byte, 32)
	enc, _ := NewEncryptor(key)
	ct, _ := enc.Encrypt("secret")

	// Tamper with ciphertext
	tampered := ct[:len(ct)-2] + "XX"
	_, err := enc.Decrypt(tampered)
	assert.Error(t, err)
}

func TestEncryptorDecryptWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key2[0] = 0xFF

	enc1, _ := NewEncryptor(key1)
	enc2, _ := NewEncryptor(key2)

	ct, _ := enc1.Encrypt("secret")
	_, err := enc2.Decrypt(ct)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/api/ -run TestEncryptor -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/api/crypto_test.go
git commit -m "test: add AES-256-GCM encrypt/decrypt round-trip and error tests"
```

---

### Task 4: Middleware Tests

**Files:**
- Create: `internal/api/middleware_test.go`

- [ ] **Step 1: Write middleware tests**

This task requires a mock store for `IsUserActive` and `GetUserRole`. We define minimal inline mocks.

```go
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/Quantix/quantix/internal/config"
)


func TestRealIP(t *testing.T) {
	tests := []struct {
		name     string
		xff      string
		xri      string
		remote   string
		expected string
	}{
		{"X-Forwarded-For single", "1.2.3.4", "", "5.6.7.8:1234", "1.2.3.4"},
		{"X-Forwarded-For multiple", "1.2.3.4, 10.0.0.1", "", "5.6.7.8:1234", "1.2.3.4"},
		{"X-Real-Ip", "", "9.8.7.6", "5.6.7.8:1234", "9.8.7.6"},
		{"RemoteAddr fallback", "", "", "5.6.7.8:1234", "5.6.7.8"},
		{"RemoteAddr no port", "", "", "5.6.7.8", "5.6.7.8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				r.Header.Set("X-Real-Ip", tt.xri)
			}
			assert.Equal(t, tt.expected, realIP(r))
		})
	}
}

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := &ipRateLimiter{
		limiters: make(map[string]*ipEntry),
		r:        rate.Limit(100),
		b:        100,
	}
	for i := 0; i < 50; i++ {
		assert.True(t, rl.get("127.0.0.1").Allow())
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := &ipRateLimiter{
		limiters: make(map[string]*ipEntry),
		r:        rate.Limit(1),
		b:        1,
	}
	assert.True(t, rl.get("127.0.0.1").Allow())
	assert.False(t, rl.get("127.0.0.1").Allow())
	// Different IP should still be allowed
	assert.True(t, rl.get("10.0.0.1").Allow())
}

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	// Set env for test
	t.Setenv("QUANTIX_CORS_ORIGINS", "http://example.com")
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Allowed origin
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, "http://example.com", w.Header().Get("Access-Control-Allow-Origin"))

	// Disallowed origin — no Allow-Origin header
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Origin", "http://evil.com")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	assert.Empty(t, w2.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddleware_Preflight(t *testing.T) {
	t.Setenv("QUANTIX_CORS_ORIGINS", "*")
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("OPTIONS", "/api/test", nil)
	req.Header.Set("Origin", "http://any.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestMaxBodyBytes(t *testing.T) {
	handler := maxBodyBytes(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 100)
		_, err := r.Body.Read(buf)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(200)
	}))

	body := "this is way longer than 10 bytes"
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
```

Note: Add `"strings"` to the import block above (alongside `"net/http"`, `"net/http/httptest"`, etc).

func TestUserIDFromCtx(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(r.Context(), ctxUserID, 42)
	r = r.WithContext(ctx)
	assert.Equal(t, 42, userIDFromCtx(r))
}

func TestUserIDFromCtx_Missing(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	assert.Equal(t, 0, userIDFromCtx(r))
}
```

Note: Add `"strings"` to the import block (it's used in TestMaxBodyBytes).

- [ ] **Step 2: Run tests**

Run: `go test ./internal/api/ -run "TestRealIP|TestRateLimiter|TestCORS|TestMaxBody|TestUserID" -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/api/middleware_test.go
git commit -m "test: add middleware tests for realIP, rate limiter, CORS, body limit, context extraction"
```

---

### Task 5: API Handler Tests (Auth Endpoints)

**Files:**
- Create: `internal/api/handlers_auth_test.go`

These tests hit the full HTTP handler chain via httptest. They require a mock `data.Store` — since the `Server` takes `*data.Store` directly and all methods are on the concrete type, we test the **health** endpoint (which needs only `Ping`) and exercise the auth handler validation paths that fail **before** hitting the store. For the full register/login flow, we create a `testStoreStub` file with the needed subset.

- [ ] **Step 1: Create test store stub**

Create `internal/api/testhelpers_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
)

// newTestServerNoStore creates a Server with nil store (for validation-only handler tests).
func newTestServerNoStore(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	log := zap.NewNop()
	key := make([]byte, 32)
	enc, err := NewEncryptor(key)
	require.NoError(t, err)
	cfg := &config.Config{}
	s := NewServer(nil, enc, "test-jwt-secret-at-least-32-chars", config.SMTPConfig{}, cfg, log)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// postJSON is a test helper to POST JSON to the test server.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	return resp
}

// getWithAuth is a test helper to GET with a Bearer token.
func getWithAuth(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
```

- [ ] **Step 2: Write auth handler validation tests**

Create `internal/api/handlers_auth_test.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleRegister_InvalidBody(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := postJSON(t, ts.URL+"/api/auth/register", "not-json")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestHandleRegister_ShortUsername(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"ab","email":"a@b.com","password":"12345678"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Contains(t, body["error"], "username")
}

func TestHandleRegister_InvalidEmail(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"alice","email":"not-email","password":"12345678"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestHandleRegister_ShortPassword(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := postJSON(t, ts.URL+"/api/auth/register", `{"username":"alice","email":"a@b.com","password":"short"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestHandleLogin_InvalidBody(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := postJSON(t, ts.URL+"/api/auth/login", "not-json")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestProtectedEndpoint_NoToken(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp, err := http.Get(ts.URL + "/api/strategies")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestProtectedEndpoint_InvalidToken(t *testing.T) {
	_, ts := newTestServerNoStore(t)

	resp := getWithAuth(t, ts.URL+"/api/strategies", "invalid-jwt-token")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/api/ -run "TestHandle|TestProtected" -v`
Expected: All PASS (validation failures return before hitting nil store)

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers_auth_test.go internal/api/testhelpers_test.go
git commit -m "test: add API auth handler validation tests and test helpers"
```

---

### Task 6: Paper Broker Tests

**Files:**
- Create: `internal/paper/broker_test.go`

- [ ] **Step 1: Write paper broker tests**

```go
package paper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/risk"
	"github.com/Quantix/quantix/internal/strategy"
)

func newTestBroker(initialCash float64) (*Broker, *oms.OMS) {
	log := zap.NewNop()
	o := oms.New(oms.ModePaper, log)
	pm := oms.NewPositionManager()
	rm := risk.New(risk.Config{
		MaxPositionPct:   1.0,
		MaxDrawdownPct:   1.0,
		MaxSingleLossPct: 1.0,
	}, initialCash, log)
	b := NewBroker(o, rm, pm, "test-strat", initialCash, 0.001, 0.0005, 1, log)
	return b, o
}

func TestPaperBroker_MarketBuyAndSell(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100.0)

	// Place market buy
	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	require.NotEmpty(t, ordID)

	// Drain fill
	event := <-o.Fills()
	assert.Equal(t, strategy.SideBuy, event.Fill.Side)
	assert.InDelta(t, 1.0, event.Fill.Qty, 0.01)
	assert.Greater(t, event.Fill.Price, 100.0) // slippage applied

	// Cash should decrease
	assert.Less(t, b.Cash(), 10000.0)

	// Place market sell
	sellID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideSell,
		Type:   strategy.OrderMarket,
	})
	require.NotEmpty(t, sellID)

	event2 := <-o.Fills()
	assert.Equal(t, strategy.SideSell, event2.Fill.Side)
}

func TestPaperBroker_LimitOrderTriggersOnBar(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100.0)

	// Place limit buy at 95
	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1.0,
		Price:  95.0,
	})
	require.NotEmpty(t, ordID)

	// Bar where low doesn't reach 95 — no fill
	b.ProcessBar(102.0, 96.0, 100.0)
	select {
	case <-o.Fills():
		t.Fatal("should not fill when low > limit price")
	default:
	}

	// Bar where low reaches 95 — should fill
	b.ProcessBar(100.0, 94.0, 97.0)
	event := <-o.Fills()
	assert.Equal(t, strategy.SideBuy, event.Fill.Side)
	assert.InDelta(t, 1.0, event.Fill.Qty, 0.01)
}

func TestPaperBroker_StopMarketTriggersOnBar(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100.0)

	// Buy first
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	<-o.Fills() // drain

	// Place stop-sell at 90
	stopID := b.PlaceOrder(strategy.OrderRequest{
		Symbol:    "BTCUSDT",
		Side:      strategy.SideSell,
		Type:      strategy.OrderStopMarket,
		Qty:       1.0,
		StopPrice: 90.0,
	})
	require.NotEmpty(t, stopID)

	// Bar where low doesn't reach 90
	b.ProcessBar(100.0, 91.0, 95.0)
	select {
	case <-o.Fills():
		t.Fatal("stop should not trigger when low > stop price")
	default:
	}

	// Bar where low reaches 90 — stop should fire
	b.ProcessBar(95.0, 89.0, 92.0)
	event := <-o.Fills()
	assert.Equal(t, strategy.SideSell, event.Fill.Side)
}

func TestPaperBroker_ShortMarginAccounting(t *testing.T) {
	b, o := newTestBroker(10000)
	b.SetLastPrice(100.0)

	// Use 10x leverage broker
	log := zap.NewNop()
	oLev := oms.New(oms.ModePaper, log)
	pmLev := oms.NewPositionManager()
	rmLev := risk.New(risk.Config{
		MaxPositionPct: 1.0, MaxDrawdownPct: 1.0, MaxSingleLossPct: 1.0,
	}, 10000, log)
	bLev := NewBroker(oLev, rmLev, pmLev, "test-lev", 10000, 0.001, 0, 10, log)
	bLev.SetLastPrice(100.0)

	_ = o // silence unused

	// Open short: margin = notional * (1/10) = qty*price*0.1
	ordID := bLev.PlaceOrder(strategy.OrderRequest{
		Symbol:       "BTCUSDT",
		Side:         strategy.SideSell,
		PositionSide: strategy.PositionSideShort,
		Type:         strategy.OrderMarket,
		Qty:          10.0,
	})
	require.NotEmpty(t, ordID)
	<-oLev.Fills()

	// Margin locked: 10 * 100 * 0.1 = 100 + fee
	assert.Less(t, bLev.Cash(), 10000.0)
	assert.Greater(t, bLev.Cash(), 9800.0) // should be around 9900 - fee
}

func TestPaperBroker_ZeroPriceRejectsOrder(t *testing.T) {
	b, _ := newTestBroker(10000)
	// Don't set last price — should be 0

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	assert.Empty(t, ordID)
}

func TestPaperBroker_CashEquitySetters(t *testing.T) {
	b, _ := newTestBroker(10000)

	b.SetCashEquity(5000, 6000)
	assert.InDelta(t, 5000, b.Cash(), 0.01)
	assert.InDelta(t, 6000, b.Equity(), 0.01)
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/paper/ -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/paper/broker_test.go
git commit -m "test: add paper broker tests — market/limit/stop orders, short margin, setters"
```

---

### Task 7: Live Broker Tests

**Files:**
- Create: `internal/live/broker_test.go`

- [ ] **Step 1: Write mock OrderClient and live broker tests**

```go
package live

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/oms"
	"github.com/Quantix/quantix/internal/strategy"
)

// mockOrderClient implements exchange.OrderClient for testing.
type mockOrderClient struct {
	mu            sync.Mutex
	marketCalls   int
	limitCalls    int
	stopCalls     int
	tpCalls       int
	cancelCalls   int
	balanceCalls  int

	marketFill exchange.OrderFill
	marketErr  error
	limitID    string
	limitErr   error
	stopID     string
	stopErr    error
	tpID       string
	tpErr      error
	cancelErr  error
	balance    float64
	balanceErr error
	leverageErr error
}

func (m *mockOrderClient) PlaceMarketOrder(_ context.Context, symbol string, side exchange.OrderSide, posSide string, qty float64, clientOrderID string) (exchange.OrderFill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketCalls++
	return m.marketFill, m.marketErr
}

func (m *mockOrderClient) PlaceLimitOrder(_ context.Context, symbol string, side exchange.OrderSide, posSide string, qty, price float64, clientOrderID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limitCalls++
	return m.limitID, m.limitErr
}

func (m *mockOrderClient) PlaceStopMarketOrder(_ context.Context, symbol string, side exchange.OrderSide, posSide string, qty, stopPrice float64, clientOrderID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalls++
	return m.stopID, m.stopErr
}

func (m *mockOrderClient) PlaceTakeProfitMarketOrder(_ context.Context, symbol string, side exchange.OrderSide, posSide string, qty, triggerPrice float64, clientOrderID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tpCalls++
	return m.tpID, m.tpErr
}

func (m *mockOrderClient) SetLeverage(_ context.Context, symbol string, leverage int) error {
	return m.leverageErr
}

func (m *mockOrderClient) CancelOrder(_ context.Context, symbol, exchangeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls++
	return m.cancelErr
}

func (m *mockOrderClient) GetBalance(_ context.Context, asset string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balanceCalls++
	return m.balance, m.balanceErr
}

// Compile-time assertion
var _ exchange.OrderClient = (*mockOrderClient)(nil)

func newTestLiveBroker(mock *mockOrderClient) (*Broker, *oms.OMS) {
	log := zap.NewNop()
	o := oms.New(oms.ModeLive, log)
	pm := oms.NewPositionManager()
	b := New(mock, o, pm, log)
	b.SetEngineCtx(context.Background())
	return b, o
}

func TestLiveBroker_MarketOrderSuccess(t *testing.T) {
	mock := &mockOrderClient{
		marketFill: exchange.OrderFill{
			ExchangeID: "exch-123",
			FilledQty:  1.0,
			AvgPrice:   50000.0,
			Fee:        5.0,
			Status:     "filled",
		},
	}
	b, o := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	require.NotEmpty(t, ordID)

	// Should produce a fill event
	select {
	case event := <-o.Fills():
		assert.Equal(t, strategy.SideBuy, event.Fill.Side)
		assert.InDelta(t, 1.0, event.Fill.Qty, 0.001)
		assert.InDelta(t, 50000.0, event.Fill.Price, 0.01)
	case <-time.After(time.Second):
		t.Fatal("expected fill event")
	}

	assert.Equal(t, 1, mock.marketCalls)
}

func TestLiveBroker_MarketOrderExchangeError(t *testing.T) {
	mock := &mockOrderClient{
		marketErr: errors.New("insufficient balance"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	assert.Empty(t, ordID, "should return empty on exchange error")
	// Not transient, so only 1 attempt
	assert.Equal(t, 1, mock.marketCalls)
}

func TestLiveBroker_MarketOrderTransientRetry(t *testing.T) {
	// "connection refused" is classified as transient — broker should retry once (total 2 attempts)
	mock := &mockOrderClient{
		marketErr: errors.New("connection refused"),
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderMarket,
		Qty:    1.0,
	})
	assert.Empty(t, ordID) // both attempts fail
	assert.Equal(t, 2, mock.marketCalls, "should retry once on transient error")
}

func TestLiveBroker_LimitOrderAsync(t *testing.T) {
	mock := &mockOrderClient{
		limitID: "limit-exch-456",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   strategy.SideBuy,
		Type:   strategy.OrderLimit,
		Qty:    1.0,
		Price:  49000.0,
	})
	require.NotEmpty(t, ordID)
	assert.Equal(t, 1, mock.limitCalls)
}

func TestLiveBroker_StopOrderAsync(t *testing.T) {
	mock := &mockOrderClient{
		stopID: "stop-exch-789",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	ordID := b.PlaceOrder(strategy.OrderRequest{
		Symbol:    "BTCUSDT",
		Side:      strategy.SideSell,
		Type:      strategy.OrderStopMarket,
		Qty:       1.0,
		StopPrice: 48000.0,
	})
	require.NotEmpty(t, ordID)
	assert.Equal(t, 1, mock.stopCalls)
}

func TestLiveBroker_SyncBalance(t *testing.T) {
	mock := &mockOrderClient{balance: 5000.0}
	b, _ := newTestLiveBroker(mock)

	err := b.SyncBalance(context.Background(), "USDT")
	require.NoError(t, err)
	assert.InDelta(t, 5000.0, b.Cash(), 0.01)
	assert.InDelta(t, 5000.0, b.Equity(), 0.01)
}

func TestLiveBroker_SyncBalanceError(t *testing.T) {
	mock := &mockOrderClient{balanceErr: errors.New("api error")}
	b, _ := newTestLiveBroker(mock)

	err := b.SyncBalance(context.Background(), "USDT")
	assert.Error(t, err)
}

func TestLiveBroker_DuplicateOrderBlocked(t *testing.T) {
	mock := &mockOrderClient{
		marketFill: exchange.OrderFill{
			ExchangeID: "exch-1", FilledQty: 1.0, AvgPrice: 50000, Status: "filled",
		},
	}
	b, o := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	// First order succeeds
	id1 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 1.0,
	})
	<-o.Fills()
	require.NotEmpty(t, id1)

	// Now place a limit order (stays OPEN)
	mock.limitID = "limit-exch-2"
	id2 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderLimit, Qty: 1.0, Price: 49000,
	})
	require.NotEmpty(t, id2)

	// Try another buy — should be blocked by the pending limit
	id3 := b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderMarket, Qty: 1.0,
	})
	assert.Equal(t, id2, id3, "should return existing pending order ID")
}

func TestLiveBroker_CancelAllPending(t *testing.T) {
	mock := &mockOrderClient{
		limitID: "limit-exch-1",
	}
	b, _ := newTestLiveBroker(mock)
	b.SetLastPrice(50000.0)
	b.cash.Store(100000.0)

	// Place a limit order (stays OPEN)
	b.PlaceOrder(strategy.OrderRequest{
		Symbol: "BTCUSDT", Side: strategy.SideBuy, Type: strategy.OrderLimit, Qty: 1.0, Price: 49000,
	})

	// Cancel all (returns no error — void method)
	b.CancelAllPendingOrders(context.Background())
	assert.Equal(t, 1, mock.cancelCalls)
}

func TestIsTransientError(t *testing.T) {
	assert.True(t, isTransientError(errors.New("connection refused")))
	assert.True(t, isTransientError(errors.New("i/o timeout")))
	assert.True(t, isTransientError(errors.New("read: connection reset by peer")))
	assert.False(t, isTransientError(errors.New("insufficient balance")))
	assert.False(t, isTransientError(nil))
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/live/ -v`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/live/broker_test.go
git commit -m "test: add live broker tests — market/limit/stop order routing, retry, balance sync, cancel-all"
```

---

### Task 8: Run full test suite and verify

- [ ] **Step 1: Run all tests**

Run: `go test ./... -count=1`
Expected: All packages PASS, no regressions

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues

- [ ] **Step 3: Final commit (if any fixups needed)**

```bash
git add -A
git commit -m "test: fix any test compilation issues from full suite run"
```
