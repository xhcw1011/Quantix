-- Trade event log for analysis (persistent across restarts)
CREATE TABLE IF NOT EXISTS trade_events (
    id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL,
    engine_id TEXT NOT NULL,
    symbol TEXT NOT NULL,
    event_type TEXT NOT NULL,  -- 'signal', 'open', 'close', 'mtf_score', 'reversal', 'grid'
    side TEXT,                 -- 'LONG', 'SHORT'
    price DOUBLE PRECISION,
    entry_price DOUBLE PRECISION,
    qty DOUBLE PRECISION,
    confidence DOUBLE PRECISION,
    mtf_score INT,
    pnl DOUBLE PRECISION,
    reason TEXT,               -- 'stop_loss', 'trailing', 'range_tp', 'gpt_reversal', etc.
    details JSONB,             -- extra data (GPT reasoning, indicators, etc.)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_trade_events_user_engine ON trade_events(user_id, engine_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_trade_events_type ON trade_events(event_type, created_at DESC);
