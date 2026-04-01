# Quantix Round 6 — Final Hardening Report

**Date**: 2026-03-25
**Scope**: Enumerate all remaining P2 items, fix what's safe, produce canary deployment checklist.

---

## 1. Complete P2 List (All Remaining Items)

### FIXED THIS ROUND

| # | Issue | Files Changed | Risk Addressed |
|---|-------|--------------|----------------|
| P2-7 | HTTP status code not checked before JSON decode in OKX/Bybit — 429/500 errors misclassified as decode failures | `okx/orderbroker.go`, `okx/client.go`, `bybit/client.go` | Exchange rate limits now produce clear "HTTP 429" errors instead of "decode error", enabling proper retry classification |
| P2-8 | Zero/negative price klines silently accepted — malformed bar with price=0 could trigger catastrophic all-in orders | `okx/client.go`, `bybit/client.go`, `binance_rest.go`, `binance_ws.go` | All 3 exchanges (REST + WS) now reject bars where any OHLC price <= 0 |
| P2-2 | WS broadcast silently drops messages to slow clients | `api/ws.go` | Now logs `Warn` with user_id on each drop |
| P2-3 | WS client send buffer too small (64) | `api/ws.go` | Increased to 256 — 4x headroom for burst fill/equity updates |
| P2-5 | WS conn.Close() error silently ignored | `api/ws.go` | Now logged at Debug level |
| P2-15 | Config missing validation for critical fields | `config/config.go` | `Validate()` checks database.host, database.name, database.user, database.port at startup |

### STILL OPEN AFTER THIS ROUND

| # | Issue | Why Not Fixed | Mitigation |
|---|-------|--------------|------------|
| P2-1 | `context.Background()` used in recovery/shutdown paths instead of engine context | Changing context hierarchy mid-flight risks breaking recovery ordering; needs careful integration testing with real exchange | Recovery paths have explicit 60s timeouts; shutdown has 10s timeout. Bounded, not unbounded. |
| P2-4 | Session deactivation race (user stop + server crash simultaneously) | Requires distributed locking or 2PC; disproportionate complexity for edge case | Session auto-restart is idempotent; operator can manually stop via admin panel |
| P2-8b | Binance `UseTestnet` is a package-level global — two brokers with different modes conflict | Upstream `go-binance` library limitation; requires fork or upstream PR | Document: only one mode (testnet OR live) per process. Multi-user with mixed modes needs separate processes. |
| P2-11 | Poll goroutine may run with invalid exchangeID if placement failed | Goroutine is tied to engine context — exits when engine stops | Not a hard leak; resource waste bounded by engine lifetime |
| P2-12 | Rate limiter cleanup goroutine runs forever (no shutdown signal) | Requires plumbing a context/done channel through middleware init; low impact | Goroutine uses negligible resources; exits on process termination |
| P2-13 | No circuit breaker on repeated DB write failures | Needs careful design (when to trip, when to reset, what happens to in-flight orders) | Each write has 10s context timeout; engine shutdown waits via WaitGroup. Not unbounded. |
| P2-14 | WS writer goroutine doesn't signal reader on error | Adding cross-goroutine signaling risks deadlock in cleanup path | Ping timeout (60s) detects dead writers. Acceptable latency for WS health check. |
| P2-16 | No fill latency metric in paper engine | Observability gap, not functional. Paper is simulation. | Fills are logged. Not needed for canary (canary uses live engine). |
| P2-17 | WSS not auto-wired in Go HTTP server | TLS termination belongs at nginx/load balancer level, not in app | Deploy behind nginx with TLS. See `deploy/nginx.conf`. |
| P2-18 | No JWT refresh endpoint | Feature, not a bug. Token expiry is 24h (max 7d). | User re-logs in. Standard for stateless APIs. |
| P2-19 | Paper engine equity snapshot restore doesn't check freshness | Snapshot is tied to same strategy; replaying all fills rebuilds state | Operator can clear old snapshots before restart if concerned |
| P2-10 | Strategy params not schema-validated at API boundary | Error is caught and returned as 400 at strategy creation time | Poor UX (late error), not a safety issue |

---

## 2. Exact Files Touched This Round

### Modified
- `internal/exchange/okx/orderbroker.go` — HTTP status check in `doRequest()`
- `internal/exchange/okx/client.go` — HTTP status check in `fetchCandles()`; zero-price guard in REST kline parser and WS kline handler
- `internal/exchange/bybit/client.go` — HTTP status check in `fetchKlines()`; zero-price guard in REST kline parser and WS kline handler
- `internal/exchange/binance_rest.go` — zero-price guard in `convertBinanceKline()`
- `internal/exchange/binance_ws.go` — zero-price guard in `convertWSKline()`
- `internal/api/ws.go` — broadcast drop logging; send buffer 64→256; conn.Close error logging
- `internal/config/config.go` — `Validate()` method for critical config fields

### Created
- `docs/round6-hardening.md` — this file

---

## 3. Limited Real-Money Canary Deployment Checklist

### Prerequisites

- [ ] All Round 1–6 fixes are deployed (this branch)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes
- [ ] `go vet ./...` passes

### A. Configuration

- [ ] `config.yaml` has correct database host/port/user/name (Validate() will reject bad values at startup)
- [ ] `QUANTIX_ENCRYPTION_KEY` is set (32-byte hex, generated via `openssl rand -hex 32`) — store in secrets manager, NOT in config file
- [ ] `QUANTIX_JWT_SECRET` is set (separate from encryption key)
- [ ] `QUANTIX_LIVE_CONFIRM=true` is set (required for live OKX/Binance Futures trading)
- [ ] `QUANTIX_CORS_ORIGINS` is set to your frontend domain(s) only (no wildcard `*` in production)
- [ ] Exchange API keys have **IP whitelist** enabled on the exchange side
- [ ] Exchange API keys have **withdrawal disabled** (trade-only permissions)
- [ ] `app.env` is set to `"production"` in config.yaml
- [ ] `app.log_level` is set to `"info"` (not `"debug"` — debug is verbose)

### B. Risk Limits (Conservative Canary Values)

- [ ] `risk.max_position_pct: 0.05` (5% of equity per position — half the default)
- [ ] `risk.max_drawdown_pct: 0.10` (10% drawdown kill switch)
- [ ] `risk.max_single_loss_pct: 0.01` (1% per trade)
- [ ] Strategy `StopLossPct` is set (never run without stop-loss in canary)
- [ ] Leverage ≤ 3x for canary (even if exchange allows more)
- [ ] Start with a **single symbol** (e.g., BTCUSDT) and a **single strategy**

### C. Capital Limits

- [ ] Fund the canary exchange sub-account with **no more than 1–2% of total intended capital**
- [ ] Use exchange sub-accounts or separate API keys to isolate canary capital
- [ ] Verify available balance via `GET /api/engines` or exchange dashboard before starting

### D. Startup Sequence

1. [ ] Start PostgreSQL + TimescaleDB (verify migrations via `GET /health`)
2. [ ] Start Redis + NATS (if using monitoring/bus features)
3. [ ] Start Grafana + Prometheus (monitoring)
4. [ ] `go run ./cmd/api -config config/config.yaml` (or Docker)
5. [ ] Verify `GET /health` returns 200
6. [ ] Verify `GET /api/health` returns 200 with DB ping OK
7. [ ] Log in as admin, verify user list loads
8. [ ] Start engine via UI or API — watch first 3–5 bars for expected behavior
9. [ ] Confirm fills appear in Orders/Fills pages and in exchange dashboard

### E. Monitoring (Must-Have Before Canary)

- [ ] Grafana dashboard (`deploy/grafana/provisioning/dashboards/quantix_dashboard.json`) is loaded
- [ ] Alerts configured for:
  - Margin ratio < 20% (warn) and < 12% (critical) — already in config
  - Consecutive margin query failures ≥ 3 → critical alert (built in)
  - Protective order failure → critical Telegram/email alert (built in)
- [ ] Telegram notifications configured (`telegram.bot_token` + `telegram.chat_id`)
- [ ] Operator has Telegram app open during canary hours
- [ ] Log aggregation is running (at minimum, `journalctl -u quantix` or Docker logs)

### F. Kill Switch / Rollback

- [ ] **Immediate stop**: `POST /api/engines/{id}/stop` or Admin panel "Force Stop"
  - This cancels all pending exchange orders (built-in `CancelAllPendingOrders`)
  - Deactivates engine session (won't auto-restart)
- [ ] **Emergency**: If API is unresponsive:
  - Kill the process (`kill -TERM <pid>` — graceful shutdown cancels all orders)
  - If graceful fails: `kill -9` then manually cancel orders on exchange dashboard
- [ ] **Exchange-side kill switch**: Set exchange API key to read-only or delete it
- [ ] **Rollback**: Redeploy previous Docker image / binary. Sessions persist in DB, so new version picks up where old left off.
- [ ] Know your exchange's **manual order cancellation** flow before starting canary

### G. Canary Operating Procedures

- [ ] Run canary for minimum **72 hours** on paper mode first (verify fills, equity tracking, stop-loss firing)
- [ ] Then run canary on **real money for 1 week** with above capital/risk limits
- [ ] Monitor daily: check fills, P&L, margin ratio, alert history
- [ ] Review logs for any `ERROR` or `WARN` entries daily
- [ ] **Do not increase capital or leverage during canary week**
- [ ] If any unexpected behavior: stop engine, investigate, do not restart until root cause is understood
- [ ] After 1 clean week: consider gradual scale-up (2x capital, same risk limits)

### H. Operator Precautions

- [ ] Never run `QUANTIX_LIVE_CONFIRM=true` on a development machine
- [ ] Never use mainnet API keys in test/dev environments
- [ ] Back up database before any schema migration
- [ ] Keep exchange dashboard open in a separate browser tab during canary
- [ ] Have the exchange mobile app installed with notifications enabled
- [ ] Document the canary start time, symbol, strategy, and params in a log

---

## 4. Validation Results

```
go build ./...    — PASS (all packages compile)
go test ./...     — PASS (all test suites green)
go vet ./...      — PASS (no issues)
```

---

## 5. Final Recommendation

### Verdict: **LIMITED REAL-MONEY CANARY ELIGIBLE**

**Conditions for canary eligibility:**

1. All Round 1–6 fixes deployed (Rounds 1–5 addressed 20+ P0/P1 issues; Round 6 closes the most impactful P2s)
2. Follow the canary checklist above exactly — especially:
   - Conservative risk limits (5% position, 1% single loss, ≤3x leverage)
   - Minimal capital (1–2% of intended total)
   - Telegram alerts active
   - 72h paper-mode burn-in first
3. Remaining P2 items are **non-blocking for canary** — they affect edge cases, observability, or UX, not core trading safety

**NOT eligible for broader production** until:
- P2-1 (context hierarchy) is addressed with integration testing against real exchange APIs
- P2-13 (DB circuit breaker) is designed and tested
- P2-8b (Binance testnet global) is resolved for multi-user mixed-mode deployments
- At least 4 weeks of clean canary operation with no unexpected behavior

**What "limited canary" means:**
- Single operator, single symbol, single strategy
- Capital ≤ 2% of intended production allocation
- Active monitoring during market hours
- Immediate stop capability verified before start
