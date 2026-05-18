# Quantix Standalone (Single-User) — Design Document

**Status**: Draft for review
**Date**: 2026-05-18
**Author**: 2026-05-18 design session with xhcw1011
**Replaces**: nothing (new product fork; existing multi-user SaaS code preserved on `main`)
**Branch (proposed)**: `standalone/main` (forked from `ops-ui-batch-1-2026-05-18`)

## 1. Product Overview

### What we're building

A single-binary, locally-installed crypto trading bot with a browser-based UI.
Customer downloads one file, double-clicks, browser opens to localhost UI,
they add a Binance API key, pick a strategy preset, and start trading.

### Who it's for

**零编程者散户** — retail crypto traders who:

- Cannot use the command line.
- Cannot install Docker.
- Read Chinese as primary language (English secondary).
- Have a Binance account and want automated trading without coding.
- Trade their own capital, typically $1k–$50k per account.

### How it's sold

- **One-time license purchase** (买断). No subscription, no profit share.
- Customer pays once, gets a license key, software runs forever on their hardware.
- No central server requirement after activation (offline-capable).
- Updates delivered as new binaries the customer downloads voluntarily.

### Why this matters for design

Each of these has architectural consequences:

| Constraint | Implication |
|---|---|
| Customer cannot install Docker | DB + cache must be in-process (SQLite + in-memory) |
| Customer trades own capital | Catastrophic UI mistakes (accidental live mode, wrong sizing) must be HARD to make |
| One-time license, offline-OK | License validated locally via HMAC, not phoned home |
| Chinese-first audience | UI + error messages must be in Chinese by default |
| Single user per install | No JWT/multi-tenancy needed; one local password is enough |
| Customer downloads updates | Build pipeline must produce signed, notarized binaries for Mac + Win |

## 2. Architecture — Why a Fork, Not a Flag

### Options considered

**Option A (rejected): Add `single_mode: true` switch to existing multi-user code.**
Keeps one codebase. Cheaper short-term.
- Cost: every read/write still has `WHERE user_id = $1` and the data model carries baggage forever. Customers see hidden complexity in logs and error messages. Future SaaS evolution is fine but ANY mistake in the conditional logic leaks one customer's data to another (security risk).

**Option B (rejected): Strangler-fig refactor in place.**
Strip multi-user gradually in the same branch.
- Cost: every commit must keep both paths working. Tests double. Adds 30–50% to every change for the duration.

**Option C (chosen): New `standalone/main` branch, rewrite the boundaries from scratch.**
Fork from `ops-ui-batch-1-2026-05-18`. Keep `main` untouched as the SaaS-friendly version.
- Win: clean schema, clean handlers, no `if singleMode {}` branches anywhere.
- Cost: two branches to maintain if we ever ship both. Mitigation: only port security/strategy fixes back to `main` (the SaaS version is dormant; cherry-pick on demand).

### Branch policy after fork

```
main                              ← current SaaS-ready code, frozen-ish.
                                    Only critical strategy fixes get cherry-picked back.

standalone/main                   ← active product branch.
standalone/feat/{topic}           ← topic branches off standalone/main.
```

`feat/revive-1671323` and `feat/ops-ui-batch-1` get archived (still on remote
for history; no further work).

## 3. System Architecture

### High level

```
                    ┌────────────────────────────────────┐
                    │   quantix.exe / Quantix.app        │
                    │  (one binary, ~50 MB)              │
                    │                                    │
                    │  ┌──────────────────────────────┐  │
                    │  │  Go process (port 9300)      │  │
                    │  │  • HTTP API + WS hub         │  │
                    │  │  • Engine + strategies       │  │
                    │  │  • SQLite via mattn/go-sqlite3 │  │
                    │  │  • Embedded web/dist         │  │
                    │  │  • Embedded UI strings (i18n)│  │
                    │  └──────────────────────────────┘  │
                    │              │                     │
                    │              ▼                     │
                    │  ┌──────────────────────────────┐  │
                    │  │  ~/Library/Application Support│ │
                    │  │      /Quantix/ (mac)         │  │
                    │  │  %APPDATA%\Quantix\ (win)    │  │
                    │  │   ├── quantix.db (SQLite)    │  │
                    │  │   ├── license.key            │  │
                    │  │   └── logs/*.log             │  │
                    │  └──────────────────────────────┘  │
                    │              │                     │
                    │              ▼                     │
                    │       Default browser              │
                    │  http://localhost:9300/            │
                    └────────────────────────────────────┘
                                   │
                                   ▼
                          Binance Futures API
                          (customer's own credential)
```

### What stays from current architecture

- HTTP API server pattern (chi-style mux, JSON responses).
- Strategy package + registry pattern (`internal/strategy/...`).
- Live engine + OMS + position manager (`internal/live/`, `internal/oms/`).
- Exchange adapters (`internal/exchange/binance_futures/`).
- React + Vite + Zustand frontend skeleton.
- WS hub for real-time push.
- Trading bus + notify (Telegram optional).

### What changes

| Layer | Now | Standalone |
|---|---|---|
| Persistent storage | Postgres 17 + TimescaleDB | SQLite (mattn/go-sqlite3, CGO) |
| Cache / hot state | Redis | sync.Map + WAL file on disk |
| Multi-user model | `users`, `user_id` on every table, JWT auth | **Keep schema as-is**; auto-seed `user_id=1` on first launch, auto-login from cookie session. No registration UI. Server bound to 127.0.0.1 only |
| Admin role / endpoints | `/api/admin/*`, role=admin | Routes stay in binary; **UI hides admin tab and never calls them**. Visible only in Developer Mode |
| Migrations | `migrations/*.sql` via custom runner | Same, but SQLite-flavored DDL |
| Config | `config/config.yaml` | First-launch wizard writes `~/Library/Application Support/Quantix/config.json` |
| Web | Built separately, served from `web/dist/` | Built into binary via `go:embed`; served from in-memory |
| Logs | `/opt/quantix/logs/quantix-YYYYMMDD.log` (linux) | Same daily-rotating WriteSyncer, into appdata `logs/` |
| Frontend i18n | English only | i18next, default zh-CN, en-US toggle |
| Onboarding | None | Forced wizard on first launch |
| License | None | License key entered on first launch; HMAC-validated offline |
| Distribution | systemd + deploy.sh | Signed .app (Mac), signed .exe (Win); both bundle dependencies |

### What gets removed (from the user-visible surface, not from the codebase)

- The `cmd/api/` flag-based config in favor of in-process config writer.
- Frontend admin tab + admin pages — code stays but never linked.
- Frontend register flow — wizard auto-creates user_id=1; no registration UI.
- The `cmd/close-positions/` CLI — UI button replaces it for the product user;
  keep the CLI in `main` branch for our own ops.
- The `cmd/quantix/` and other dev CLIs — strip from binary for size.
- All deploy/* scripts — not relevant to single-user install.

**Note (per 2026-05-18 design feedback)**: we are NOT refactoring out multi-user
plumbing (user_id columns, JWT, admin routes). They stay in the binary because
the cost of carrying them is far less than the cost of refactoring + the loss
of future optionality. Wizard auto-seeds a single user (id=1) and the UI is
the only gate. Customers never see "user" terminology.

## 4. Data Model

### SQLite schema (proposed)

Keep table names where they map cleanly; drop `user_id` everywhere it appears.

```sql
-- Single-row config table; written by onboarding wizard.
CREATE TABLE settings (
  id              INTEGER PRIMARY KEY CHECK (id = 1), -- enforce single row
  ui_language     TEXT NOT NULL DEFAULT 'zh-CN',
  ui_password_hash TEXT NOT NULL,            -- bcrypt of local password
  paper_first_warning_seen BOOLEAN DEFAULT 0,
  license_key     TEXT,
  installed_at    TIMESTAMP NOT NULL,
  schema_version  INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE credentials (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  label        TEXT NOT NULL,
  exchange     TEXT NOT NULL,                -- "binance"
  market_type  TEXT NOT NULL,                -- "spot" | "futures"
  testnet      BOOLEAN DEFAULT 0,
  demo         BOOLEAN DEFAULT 0,
  api_key_enc  BLOB NOT NULL,                -- AES-GCM encrypted
  api_secret_enc BLOB NOT NULL,
  passphrase_enc BLOB,
  is_active    BOOLEAN DEFAULT 1,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE orders (
  id           TEXT PRIMARY KEY,             -- OMS-xxxxxx
  client_order_id TEXT UNIQUE,
  exchange_id  TEXT,
  symbol       TEXT NOT NULL,
  side         TEXT NOT NULL,
  position_side TEXT NOT NULL,
  type         TEXT NOT NULL,
  status       TEXT NOT NULL,
  strategy_id  TEXT NOT NULL,
  quantity     REAL NOT NULL,
  price        REAL,
  stop_price   REAL,
  filled_quantity REAL DEFAULT 0,
  avg_fill_price REAL,
  commission   REAL DEFAULT 0,
  role         TEXT,
  reject_reason TEXT,
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_orders_strategy ON orders(strategy_id, status);
CREATE INDEX idx_orders_exchange ON orders(exchange_id) WHERE exchange_id IS NOT NULL;

CREATE TABLE fills (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  order_id     TEXT REFERENCES orders(id),
  symbol       TEXT NOT NULL,
  side         TEXT NOT NULL,
  qty          REAL NOT NULL,
  price        REAL NOT NULL,
  fee          REAL DEFAULT 0,
  realized_pnl REAL DEFAULT 0,
  filled_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_fills_time ON fills(filled_at DESC);

CREATE TABLE equity_snapshots (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  strategy_id  TEXT NOT NULL,
  equity       REAL NOT NULL,
  cash         REAL NOT NULL,
  unrealized_pnl REAL NOT NULL DEFAULT 0,
  realized_pnl REAL NOT NULL DEFAULT 0,
  snapshotted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_equity_strategy_time ON equity_snapshots(strategy_id, snapshotted_at DESC);

CREATE TABLE engine_sessions (
  engine_id    TEXT PRIMARY KEY,
  strategy_id  TEXT NOT NULL,
  credential_id INTEGER NOT NULL REFERENCES credentials(id),
  symbol       TEXT NOT NULL,
  interval     TEXT NOT NULL,
  mode         TEXT NOT NULL,                -- "live" | "paper"
  leverage     INTEGER,
  params_json  TEXT NOT NULL,                -- merged from preset + form
  is_active    BOOLEAN NOT NULL DEFAULT 0,
  started_at   TIMESTAMP,
  stopped_at   TIMESTAMP
);

CREATE TABLE notifications (
  -- per-install Telegram bot config
  id INTEGER PRIMARY KEY CHECK (id = 1),
  telegram_bot_token_enc BLOB,
  telegram_chat_id TEXT
);
```

### Migration from current Postgres schema

No data migration needed — new install starts fresh. **Current customers (us)
do not switch to standalone**; we keep using the SaaS branch.

### Position state (was in Redis)

Replaced with a small in-process struct, persisted to `quantix.db` table:

```sql
CREATE TABLE strategy_positions (
  strategy_id  TEXT NOT NULL,
  symbol       TEXT NOT NULL,
  side         TEXT NOT NULL,                -- "LONG" or "SHORT"
  state_json   TEXT NOT NULL,                -- full aistrat posState
  updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (strategy_id, symbol, side)
);
```

Reads and writes go through a thin wrapper that batches updates every ~100ms
to avoid SQLite write contention from tickManage. Same data, no Redis.

## 5. Tech Stack Decisions

### SQLite vs DuckDB vs BoltDB

**Chosen: SQLite via mattn/go-sqlite3** (with WAL journaling enabled).

- ✓ Mature, ubiquitous, mountains of debugging knowledge online.
- ✓ Real SQL — minimal handler rewrite (queries are mostly the same, just
  `$1` → `?` and `time.Time` → ISO 8601 strings).
- ✓ WAL mode handles concurrent reads + 1 writer fine for our load.
- ✗ Requires CGO. Cross-compile from Mac to Windows is slightly more painful
  but solvable (we'll use `xgo` or `zig cc`).

DuckDB rejected: overkill (it's analytics-focused), bigger binary.
BoltDB rejected: KV-only, not SQL, would force a major data-access rewrite.

### Redis replacement

Two roles split:

1. **Position state (read often, write often)**: `strategy_positions` table
   (see above). Engine keeps an in-memory map + flushes async every 100ms.
2. **Pub/sub for WS**: already abstracted via `internal/bus`. Standalone
   doesn't need cross-process pub/sub since it's one process; just a channel.

### Frontend

Keep React + Vite + Zustand. Add:

- `i18next` + `react-i18next` for Chinese/English. JSON translation files
  live alongside React components, embedded into binary.
- A "presentation mode" config — hides Admin nav, Logs page, advanced JSON
  textarea by default. A `View → Developer Mode` toggle in Settings reveals
  them. Reasoning: zero-code retail user shouldn't see "/logs" or admin tabs.

### Embedding frontend in binary

Standard Go:

```go
//go:embed web/dist
var webDistFS embed.FS

http.Handle("/", http.FileServer(http.FS(webDistFS)))
```

Build step: `npm run build` produces `web/dist/`, `go build` picks it up.
Binary ends up ~45–55 MB (most of it is the Go runtime + sqlite + go-binance).

## 6. Distribution

### Targets

- **macOS**: signed `.app` bundle, notarized via Apple Developer ID.
  Distributed as `.dmg`.
  Both ARM64 (Apple Silicon) and AMD64 (Intel) — universal binary via `lipo`.
- **Windows**: signed `.exe` with Authenticode certificate.
  Distributed as standalone `.exe` (no installer initially); install script
  drops it in `%USERPROFILE%\Quantix\` and creates a shortcut.

### Build pipeline

A GitHub Actions workflow per release tag:

1. `go test ./...` on Linux runner.
2. `npm run build` in `web/`.
3. Cross-compile binary:
   - `darwin/arm64`, `darwin/amd64` → `lipo` into universal `quantix`.
   - `windows/amd64` → `quantix.exe`.
4. Sign macOS binary (`codesign --deep --options runtime`), package as `.app`,
   create `.dmg`, notarize, staple.
5. Sign Windows binary with code-signing cert.
6. Upload to GitHub Releases (or our own download server).

Signing certificates are required from the start — unsigned binaries trigger
SmartScreen / Gatekeeper warnings that make zero-code customers give up.

### Auto-update (post-MVP)

On startup, hit `https://quantix-app.com/api/latest` (a static JSON file on
S3 / GitHub Pages), compare version, show "New version available" badge in
UI with download link. **No silent updates** — customer always confirms.

## 7. License Model

### Goals

- Customer pays once, gets a key, never has to phone home.
- Key cannot trivially be shared by changing one byte.
- We can issue per-machine keys if abuse becomes a problem (TBD post-MVP).
- No license server downtime risk.

### Design

License key format:

```
QTX-{base32(payload)}-{base32(hmac_sha256(payload, secret)[:10])}
```

Where `payload` is a packed binary blob:

```
[ 1 byte  version = 1                   ]
[ 4 bytes issued_at unix timestamp      ]
[ 4 bytes expiry unix timestamp = max-int (no expiry, for one-time buy) ]
[ 2 bytes feature flags reserved        ]
[ N bytes customer email (UTF-8)        ]
```

`secret` is a Go-embedded constant in the binary (32 random bytes).
Anyone who decompiles can extract it; that's fine for an MVP. Move to
ECDSA-signed keys (public key in binary, private key kept by us) post-MVP
if piracy becomes measurable.

### Activation flow

First launch:
1. Wizard asks: "粘贴你的 license 邮件中收到的 key"
2. Server-side: parse format → verify HMAC → display "Activated for {email}".
3. Store in `settings.license_key` and a sidecar `~/.quantix/license.key`
   (sidecar so DB wipes don't lose activation).
4. Activation check on every binary startup. No network call.

### Trial mode (optional, defer to post-MVP)

Allow 7-day fully-functional trial via a different key prefix (`QTX-TRIAL-...`).
Built-in expiry. Hard-stop at day 7+1h grace period.

## 8. User Flows

### Install + first launch (zero-code customer)

```
1. Customer pays via Stripe/微信/支付宝 (商务流程，不在工程范围内).
2. Receives email with download link + license key.
3. Mac:  downloads Quantix.dmg → drag to Applications → first launch
        triggers macOS Gatekeeper prompt (notarized so it goes through).
   Win:  downloads Quantix.exe → SmartScreen accepts signed binary.
4. App opens default browser to http://localhost:9300/setup.
5. Wizard step 1 (UI):
     "欢迎使用 Quantix。请粘贴你购买时收到的 license key。"
     [ input box ] [ 激活 button ]
6. Wizard step 2:
     "为你的本地账户设置一个密码（只在你这台机器上使用）"
     [ password ] [ confirm ]
7. Wizard step 3:
     "添加你的 Binance API key。如何创建？" → opens help modal with screenshots.
     - User pastes API key + secret.
     - Server tests connection → shows USDT balance + green check.
8. Wizard step 4:
     "选一个策略预设。我们推荐 'Default (hedge both)'"
     [ Default ] [ Drawdown-hedge only ] [ Conservative ]
     Each preset shows a one-paragraph description in Chinese.
9. Wizard step 5:
     "我们会先用 paper 模式（模拟交易，不动真钱）跑 24 小时。
      24 小时后你看到的盈利曲线满意，再切到 live 模式。"
     [ 开始 paper 跑 ]
10. Dashboard opens, engine running in paper mode. Done.
```

Total time: ~5 minutes for an engaged customer.

### Daily use

- Customer opens browser bookmark, sees Dashboard.
- Live equity, open positions, recent fills.
- Telegram (if configured) pings phone on every fill.
- Customer rarely needs to do anything; this is "set and forget" trading.

### Switching paper → live

Multiple gates to prevent accidents:

```
Settings → Trading mode
  Current: 📋 Paper (since 2026-05-18 14:00)

  [ Switch to Live mode ]
```

Click button → modal:

```
   ⚠️  切换到 Live 模式
   
   接下来 Quantix 会用你的真实 Binance 资金交易。
   
   过去 24 小时的 paper 表现：
   - 总收益: +1.23%
   - 胜率:    72%
   - 成交:    18 次
   
   请输入 "我确认开始 live 交易" 来切换。
   [ input ]
   [ 取消 ]  [ 确认切换 ]
```

If paper hasn't run 24h yet, button is greyed out with hint:
"建议至少跑 24 小时 paper 后再切 live"

### Recovery from crash / restart

- App starts, reads engine_sessions where is_active = 1, restores them.
- Same orphan-claim logic as current (already implemented).
- Same syncer recover-from-exchange logic as current.

### Forgotten password recovery

- Stop the app.
- Customer deletes `quantix.db` from appdata folder (we document this).
- Loses local password but NOT license (sidecar file).
- Re-runs wizard from step 2; re-enters API key.

## 9. MVP Scope

### In MVP (must ship for v1.0)

| Item | Notes |
|---|---|
| SQLite migration | Including data-access layer rewrite |
| In-memory Redis replacement | Position state + bus channel |
| Auto-seed single user (user_id=1) + hide registration/admin UI | Schema/code stay; only UI changes |
| Embed web/dist | go:embed + serve from FS |
| Onboarding wizard | All 5 steps |
| Chinese i18n | Full UI + error messages |
| Strategy presets with Chinese descriptions | Already exist in English; just translate + expand |
| Paper-first enforcement | Cannot go live in first 24h |
| License HMAC validation | Single-machine, no expiry |
| Mac universal .app | Signed + notarized |
| Windows .exe | Signed |
| Auto-open browser on launch | OS-specific shell-out |
| Hide advanced features from default UI | Logs page, JSON params, admin tab → Developer Mode |
| Embedded help / FAQ | "如何创建 Binance API key" with screenshots |
| Critical error handling | No raw panics in UI; translate Binance error codes |

### Out of MVP (post-v1.0)

- Strategy backtester UI (current backtest page strip out for MVP)
- SL inline editing (advanced trader feature)
- Auto-update mechanism (manual download for v1.0)
- Trial mode (only paid keys for v1.0)
- Strategy customization beyond presets + JSON box (dynamic schema)
- Multiple credentials per install (one Binance key per install for v1.0)
- Telegram bot setup wizard (skip in v1.0, leave it as "Advanced")
- Real-time strategy decision viewer (Logs page sufficient)
- Anything OKX/Bybit (Binance-only for v1.0)

### What we keep from current code

- Live engine + OMS + strategy registry — barely touch.
- Binance Futures broker — keep as-is.
- aistrat (revive 1671323 + hedge) — the only strategy shipped in v1.0.
- WS hub — keep.
- Existing UI pages: Dashboard, Engine, Positions, Credentials (each gets
  significant UX polish but the data flow is the same).

## 10. Migration Plan

### Branch creation

```bash
git checkout main             # main is at ops-ui-batch-1-2026-05-18
git checkout -b standalone/main
git push -u origin standalone/main
```

### Milestone breakdown

Estimating against one full-time engineer. Adjust if it's evening/weekend work.

**Milestone 1 — Architecture foundation (Week 1–1.5)** *(was 1–2; faster after dropping multi-user refactor)*
- M1.1: Add `internal/store/sqlite/` package; port `internal/data/` queries
  (queries keep their `user_id = ?` clauses — value is just always 1).
- M1.2: New migrations for SQLite schema (same shape as Postgres, SQLite dialect).
- M1.3: First-launch seed: insert user_id=1 with a default username if `users`
  table is empty.
- M1.4: Auth stays. Wizard step 2 sets the password for user_id=1; subsequent
  starts auto-login via persistent session cookie (no JWT in URL/headers exposed
  to customer).
- M1.5: Replace Redis position syncer with SQLite-backed in-process syncer.
- M1.6: All existing tests pass (except those mocking external Postgres).
- M1.7: Run end-to-end on dev machine: paper engine starts, fills persist.
- **Exit criteria**: server compiles and runs without Postgres/Redis.

**Milestone 2 — UI for single-user (Week 3)**
- M2.1: Onboarding wizard (5 steps).
- M2.2: `Developer Mode` toggle; hide Logs / Admin / JSON-params from default.
- M2.3: Paper-first guardrail + live-switch confirm flow.
- M2.4: i18next integration, all visible strings have zh-CN.
- M2.5: Embedded help modal for "如何创建 API key".
- **Exit criteria**: full wizard works; default UI is Chinese; no English visible.

**Milestone 3 — License + packaging (Week 4)**
- M3.1: License HMAC generator/validator.
- M3.2: License key entry in wizard step 1.
- M3.3: `go:embed` web/dist; auto-open browser on launch.
- M3.4: Cross-compile Mac universal + Windows AMD64.
- M3.5: Code signing pipeline (need Apple Developer + Authenticode certs).
- M3.6: Build .dmg installer for Mac; .exe for Windows.
- **Exit criteria**: a fresh Mac and a fresh Windows VM can install + launch +
  complete wizard + run paper engine for 5 minutes without intervention.

**Milestone 4 — Error UX polish (Week 5)**
- M4.1: Translate all 50ish Binance error codes to Chinese.
- M4.2: No raw panics surface in UI; wrap everything in friendly modals.
- M4.3: Recovery from broken DB (corrupt SQLite, missing migrations).
- M4.4: Recovery from broken license (sidecar missing, key revoked).
- M4.5: 24h stability test on a Mac mini under sustained paper trading.
- **Exit criteria**: a non-engineer friend can install + use for 24h without
  asking us anything.

**Milestone 5 — Soft launch (Week 6)**
- Pick 3 friendly users.
- Watch them install + onboard via screen share. Fix everything that confuses.
- Iterate on copy and wizard step ordering.
- Decide whether to widen distribution.

**Total: ~6 weeks** for a usable v1.0 single-user product, assuming one
dev full-time and that the architecture stays roughly as described.

### Cuts to compress

If we need to ship in 4 weeks instead of 6:
- Skip Windows. Ship Mac-only v1.0. (Mac signing is more annoying but we
  only need one toolchain.) → save ~1 week.
- Skip license HMAC. Soft launch with no license check; trust early users.
  → save 2 days.
- Skip i18n framework, just hardcode Chinese strings in JSX. → save 3 days.
  Trade-off: any future English version is a rewrite.

## 11. Open Decisions (need user input)

### D1: Where does the local password apply?

Options:
- **A**: Required to log into the UI. Mirrors the SaaS auth flow, adapted.
- **B**: No password — UI is open to anyone on `localhost`. Customer relies on
  OS user account being their own. (Macbook unlocked = anyone can trade.)
- **C**: Password optional; default on. Power user can turn off in Settings.

Recommendation: C. Default on for safety; off for convenience.

### D2: Telegram setup — in wizard, or hidden?

Telegram requires the customer to:
- Create a bot via @BotFather (technical).
- Get their chat_id (technical).
- Paste both into Quantix.

For zero-code customer, this is the highest friction step. Options:
- **A**: In wizard, with screenshots. Sets expectation but may scare some away.
- **B**: Skip in wizard. Show a "Setup notifications" card on Dashboard that's
  dismissible. Defer until customer is comfortable.
- **C**: Pre-configure with our central bot (`@xhquantix_bot`) — customer just
  gives us their Telegram username, we send them messages. Requires us to run
  the bot server (kills "fully offline" promise).

Recommendation: B for v1.0.

### D3: Single credential or multiple?

Currently `credentials` table supports many. For zero-code retail trader, do
we limit to one per install, or allow many?

- **A**: One. Simpler UI, no multi-cred selector.
- **B**: Up to 3. Lets customer split capital between testnet/demo/live.

Recommendation: A for v1.0. Reasoning: most retail customers have one Binance
account; if they want to test, switch to paper mode. Multi-cred is power-user
feature.

### D4: Initial pricing point

Out of scope for the design doc but flagged: $XX one-time price determines
what we can budget for support and what defects are tolerable. A $99 product
gets different scrutiny than a $999 product.

### D5: Refund policy / support channel

If a customer's bot loses money, who's responsible?

- We need a clear EULA: "Software provided as-is; trading is your
  responsibility." Show on first launch, require acceptance.
- Support channel: email? Discord? WeChat? Affects time allocation.

### D6: Auto-update vs manual download for v1.0

Default to manual. Customer downloads new release from a website.
Add notification badge in UI: "v1.1 available (released 2026-06-15) →
click here for changelog + download".

## 12. Risks & Mitigations

### R1: Customer loses money, blames us

**Likelihood**: certain (someone, eventually).
**Mitigation**: hard EULA on first launch, paper-mode-default, explicit
"我确认" string typing to enable live mode. Document expected drawdowns
in strategy descriptions (e.g., "Default may go to -10R in a strong trend
before mean-reverting").

### R2: SQLite corruption under sustained writes

**Likelihood**: low if WAL mode + proper sync settings.
**Mitigation**: nightly automatic backup of `quantix.db` → `quantix.db.bak`
in appdata. Document recovery: "If app fails to start, rename .bak to .db".

### R3: macOS notarization rejected

**Likelihood**: medium first time, low once we know the process.
**Mitigation**: do dry-run notarization weeks before launch. Common gotcha:
hardened runtime + certain syscalls trigger rejection; fix before code-freeze.

### R4: Windows SmartScreen still warns despite signing

**Likelihood**: high until reputation is built (Microsoft requires N installs
before SmartScreen trusts a new cert).
**Mitigation**: budget for an EV Code Signing Certificate (~$300/year)
which bypasses SmartScreen reputation requirement.

### R5: Binance API changes break us

**Likelihood**: low-medium per year.
**Mitigation**: same as today — go-binance SDK + our broker layer absorbs
changes. Auto-update mechanism becomes more important post-MVP.

### R6: Customer's machine goes to sleep / loses internet

**Likelihood**: certain.
**Mitigation**: document "Quantix must be running with internet connection
to trade. Macs should disable sleep, Windows should disable hibernate."
Detect WS disconnection and auto-reconnect with backoff (already implemented).
Telegram alert if disconnected >5min (need to add).

### R7: We picked the wrong customer

**Likelihood**: medium.
**Mitigation**: Milestone 5 is soft launch with 3 users. If they all bounce
off the wizard or feature set, pause development and rethink before building
out distribution.

### R8: Two-branch maintenance burden

**Likelihood**: high if both branches stay active.
**Mitigation**: explicitly designate `main` as "frozen for SaaS option" and
do not actively develop on it. Only cherry-pick critical strategy fixes.

## 13. Out of scope for this doc

- Stripe / 支付宝 / WeChat Pay integration (商务流程, not engineering).
- License-key issuance workflow on our end (one-time script to generate keys
  + email them; can build later).
- Marketing site, customer onboarding emails, refund processing.
- Customer support tooling.
- iOS / Android mobile companion app.
- Strategy R&D (we ship the existing aistrat revive as v1.0; new strategies
  are post-launch).

---

## Approval needed

Before I start coding, I need decisions on:

- [ ] Greenlight to fork `standalone/main` from `ops-ui-batch-1-2026-05-18`.
- [ ] D1 (local password default): A / B / C.
- [ ] D2 (Telegram in wizard): A / B / C.
- [ ] D3 (one or multiple credentials): A / B.
- [ ] D6 (auto-update for v1.0): yes / no.
- [ ] Acceptance of the 4–6 week MVP timeline (or "compress to 4 weeks" path).
- [ ] Confirmation that Mac + Windows is the v1.0 platform set (no Linux for retail).
- [ ] Who pays for Apple Developer ID ($99/yr) and Authenticode EV cert
      (~$300/yr) — those need to be acquired before Milestone 3.

Once those are answered I can start Milestone 1 (architecture foundation).
