-- ─────────────────────────────────────────────
-- Funding rates (perp funding history) — for the cross-sectional funding strategy.
-- One row per (symbol, funding time). Binance pays funding every 8h.
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS funding_rates (
    time         TIMESTAMPTZ NOT NULL,
    symbol       TEXT NOT NULL,
    funding_rate DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (time, symbol)
);

-- Hypertable, 30-day chunks (funding is 3/day → far sparser than klines).
SELECT create_hypertable('funding_rates', 'time', if_not_exists => TRUE, chunk_time_interval => INTERVAL '30 days');

CREATE INDEX IF NOT EXISTS idx_funding_symbol ON funding_rates (symbol, time DESC);
