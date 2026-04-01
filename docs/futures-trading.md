# Futures Trading Guide

## Overview

Quantix supports contract/futures trading on OKX SWAP and Binance USDM Futures with:
- Long-only and hedge (simultaneous long/short) position modes
- Leverage configuration (1x–20x)
- Automatic stop-loss and take-profit orders
- Margin ratio monitoring with Telegram alerts

## Supported Exchanges

| Exchange | Market Type | Notes |
|----------|-------------|-------|
| OKX | `swap` | Perpetual USDT-margined swaps; Demo mode available |
| Binance | `futures` (via `binance_futures` credential) | USDM perpetual; Testnet available |

## Credential Setup

Create a credential with `market_type: "swap"` or `market_type: "futures"`:

```bash
curl -X POST http://localhost:8080/api/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "label": "OKX SWAP Demo",
    "exchange": "okx",
    "market_type": "swap",
    "demo": true,
    "api_key": "YOUR_KEY",
    "api_secret": "YOUR_SECRET",
    "passphrase": "YOUR_PASSPHRASE"
  }'
```

## Starting a Futures Engine

```bash
curl -X POST http://localhost:8080/api/engines \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "credential_id": 1,
    "strategy_id": "macross",
    "symbol": "BTCUSDT",
    "interval": "1h",
    "mode": "live",
    "leverage": 3,
    "contract_mode": "hedge",
    "params": {
      "EnableShort": true,
      "StopLossPct": 0.02,
      "TakeProfitPct": 0.04
    }
  }'
```

### Parameters

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `leverage` | int | 1 | Contract leverage (1–20). Applied via exchange API before engine start. Non-fatal if exchange rejects. |
| `contract_mode` | string | "hedge" | `"hedge"` = simultaneous long+short positions; `"one_way"` = net position only |
| `params.EnableShort` | bool | false | MACross: death cross opens SHORT position (requires hedge mode credential) |
| `params.StopLossPct` | float | 0 | Stop-loss distance from entry price (e.g. 0.02 = 2%). 0 = disabled. |
| `params.TakeProfitPct` | float | 0 | Take-profit distance from entry price (e.g. 0.04 = 4%). 0 = disabled. |

## MACross Hedge Mode Logic

```
Golden cross (fast SMA > slow SMA):
  if hasShort → CloseShort (BUY, PositionSide=SHORT)
  if !hasLong → OpenLong  (BUY, PositionSide=LONG)
               ↳ auto-place STOP_MARKET at close*(1-StopLossPct)
               ↳ auto-place TAKE_PROFIT at close*(1+TakeProfitPct)

Death cross (fast SMA < slow SMA):
  if hasLong  → CloseLong  (SELL, PositionSide=LONG)
  if !hasShort → OpenShort (SELL, PositionSide=SHORT)
                ↳ auto-place STOP_MARKET at close*(1+StopLossPct)
                ↳ auto-place TAKE_PROFIT at close*(1-TakeProfitPct)
```

`hasLong` / `hasShort` state is maintained via `OnFill` callbacks, independent of portfolio position queries.

## Order Types

| Type | Implementation |
|------|---------------|
| `MARKET` | `PlaceMarketOrder` — immediate fill at best available price |
| `LIMIT` | `PlaceLimitOrder` — resting order at specified price |
| `STOP_MARKET` | `PlaceStopMarketOrder` — triggers market order when price hits stop |
| `TAKE_PROFIT` | `PlaceTakeProfitMarketOrder` — triggers market order at profit target |

### OKX Implementation
- Market orders: `POST /api/v5/trade/order` with `ordType: "market"`
- Stop/TP orders: `POST /api/v5/trade/order-algo` with `ordType: "conditional"`
- Position mode: automatically set to `"long_short_mode"` on startup (idempotent)
- Contract size (`sz`): `floor(qty_btc / ctVal)` — ctVal lazy-loaded per instrument

### Binance Futures Implementation
- Uses `go-binance/v2/futures` SDK
- Testnet: `binance.UseTestnet = true` (safe, no real funds)
- Live: requires `QUANTIX_LIVE_CONFIRM=true` env var
- PositionSide: `futures.PositionSideTypeLong` / `Short` for hedge mode

## Margin Monitoring

The `MarginMonitor` goroutine polls every 60 seconds when the order client implements `exchange.MarginQuerier`:

| Margin Ratio | Action |
|-------------|--------|
| < 20% | WARN log + Telegram alert |
| < 12% | ERROR log + urgent Telegram alert |

### OKX
Queries `GET /api/v5/account/positions` → `mgnRatio` field (fraction, higher = safer).

### Binance Futures
Approximates margin ratio from distance-to-liquidation:
- Long: `(markPrice - liqPrice) / markPrice`
- Short: `(liqPrice - markPrice) / markPrice`

## Risk Management

The risk manager applies the following checks before any order:

| Check | Default |
|-------|---------|
| Max position size | 10% of portfolio |
| Max drawdown circuit breaker | 15% |
| Max single loss | 2% |

For leveraged positions, consider tightening these via `risk` in the StartRequest:

```json
"risk": {
  "max_position_pct": 0.05,
  "max_drawdown_pct": 0.10,
  "max_single_loss_pct": 0.01
}
```
