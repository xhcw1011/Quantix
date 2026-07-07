# Cross-Sectional Funding Strategy — Implementation Design

**Status: DESIGN (not implemented) · 2026-07-07**

Build the validated cross-sectional funding factor
(`docs/research/funding-factor-validated-2026-07-07.md`) as a live strategy: a
market-neutral, broad mid-cap perp long-short that ranks by funding and rebalances
every few days. It is the **Alpha Layer**, sitting on the survival stack (ORG/Pool).

## 1. The architectural challenge (must confront first)

Every existing strategy (macross, grid, aistrat) is **single-symbol, per-bar,
event-driven**: one engine = one symbol, `OnBar(bar)` reacts. This factor is
**multi-symbol, cross-sectional, rebalance-to-target**: it needs ALL coins' funding
to rank, and trades ~10–20 symbols at once on a schedule. The single-symbol engine
does not fit.

**Decision: a new "portfolio rebalancer" execution model, NOT a `strategy.Strategy`.**
It reuses the per-order infrastructure (OMS, broker, ORG, exchange clients — all
symbol-agnostic at the order level) but replaces the per-bar engine loop with a
scheduled rebalance-to-target loop.

## 2. Architecture

```
scheduler (every REB days, fixed UTC time)
   │
   ▼
Universe Manager ──► point-in-time liquid mid-cap set (listed ≥ L days, vol ≥ floor)
   │
   ▼
Data (funding + price) ──► trailing funding per coin  (Binance fapi)
   │
   ▼
Ranker (pure) ──► z-score funding, pick K lowest (long) / K highest (short)
   │
   ▼
Target Portfolio ──► dollar-neutral, equal-weight, gross = capital × grossFrac
   │
   ▼
Rebalancer ──► diff(current, target) → orders ──► [ORG] ──► broker/OMS ──► exchange
   │                                     (Layer-1 order safety, single gate)
   ▼
Pool (this strategy = one real pool: market-neutral, its own DD/exposure)
```

Reuses: `internal/exchange` clients, `internal/oms`, `internal/live.Broker` (or a
thin rebalance broker), `internal/orgateway`, `internal/pool`, `internal/position`
(syncer for current positions = exchange truth).

## 3. Components

- **Universe Manager** — the PIT liquid universe (the backtest's rule: listed ≥ L
  days, trailing 30d volume ≥ floor). Config: base list + floors. Must match the
  validated backtest's universe logic (edge lives in mid-cap breadth, 30–50+ coins).
- **Data ingest** — trailing funding per coin (Binance `/fapi/v1/fundingRate`) +
  marks. Store funding to DB (new `funding_rates` table) so the live signal and the
  backtest read the same source. Prices via existing kline/ticker.
- **Ranker (pure, TDD)** — `rank(fundingByCoin) → (longs[], shorts[])`: z-score
  trailing-W funding, K lowest long / K highest short. Pure function, unit-tested
  against the backtest's selection on the same inputs (backtest = oracle).
- **Target builder** — dollar-neutral equal weight; per-coin target notional =
  ±(capital × grossFrac) / (2K). Rounds to exchange step/tick.
- **Rebalancer/Executor** — reads current positions (position syncer = exchange
  truth), computes per-symbol deltas to target, emits orders through ORG→broker.
  Execution: prefer **limit (maker) with a timeout → market fallback** to control
  mid-cap cost (cost stress showed edge dies by ~50bp; realistic budget ≤ ~20bp).
- **Risk** — orders pass ORG (Layer-1: max-notional-per-order, gross-leverage,
  order-rate). The whole strategy is one **Pool** (market-neutral → its DD/exposure
  tracked; pool halt = stop opening/rotating, keep closing).

## 4. Execution model & cadence

- Rebalance every `REB` days (default 3) at a fixed UTC time (post-funding).
- Each rebalance: recompute target, diff vs current, place delta orders; do NOT
  fully churn if a coin stays in the same book (only trade the delta — matches the
  backtest's turnover cost model).
- Market-neutral: keep |Σlong notional − Σshort notional| ≈ 0 after rounding.

## 5. Risk integration (reuses the survival stack)

- **ORG** — every rebalance order flows Strategy→ORG→broker→exchange (shadow first,
  then enforce). ORG's per-order limits protect against fat-finger / runaway.
- **Pool** — this strategy is its own pool (e.g. "Neutral"); PoolManager tracks its
  attributed equity / DD / directional exposure. Market-neutral → gross exposure
  matters more than net; pool caps gross.
- **Kill conditions** — pool DD breach → halt rotation (hold/close only); a max
  gross leverage; a per-coin notional cap (tail-risk on illiquid mid-caps).

## 6. Testing

- **Ranker**: pure unit tests; assert it reproduces `xsmom_funding.py`'s selection
  on identical inputs (the validated backtest is the behavioral oracle).
- **Target/rebalancer**: given current + target, assert correct delta orders,
  dollar-neutrality, step/tick rounding, no full-churn when a coin persists.
- **Paper-forward**: run the whole thing on the paper broker over live data for
  weeks; compare realized behavior/cost to the backtest's assumptions (esp. mid-cap
  slippage — the biggest unknown).

## 7. Phased rollout (no real money until each gate passes)

1. **Backtest parity** — Go ranker reproduces the Python backtest selections.
2. **Paper-forward** (paper broker, live data, weeks) — validate real execution cost
   vs the ≤20bp budget; the edge dies by ~50bp, so cost is the make-or-break gate.
3. **Shadow** — compute + log target rotations live (no orders), confirm signal +
   universe behave; ORG/Pool observe.
4. **Gradual live** — small capital, market-neutral, ORG enforce + Pool caps on;
   scale only if realized net (esp. cost) matches the backtest.

## 8. Deferred / open decisions

- **Funding data storage** — new `funding_rates` table + a backfill/ingest job
  (reuse `cmd/backfill` pattern). Needed so live + backtest share one source.
- **Capital sizing** — market-neutral is capital-efficient; how much gross per $
  capital (leverage) balances return vs mid-cap tail risk. Start conservative.
- **Which exchange(s)** — Binance validated; multi-exchange later.
- **Weighting** — start equal-weight (validated); funding-magnitude or vol weighting
  later (risk of overfit).
- **The mean-reversion component may decay** — monitor live; the carry floor (~13%/yr
  pre-cost) is the durable base to fall back on.

## 9. Success criteria

Go ranker matches the backtest; paper-forward realized cost ≤ ~20bp so net stays
clearly positive; market-neutral (gross-balanced) with pool-capped tail risk; every
order through the single ORG gate. Then, and only then, small live capital.
