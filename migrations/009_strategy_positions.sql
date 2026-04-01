-- Strategy position state backup (Redis is primary, this is recovery fallback)
CREATE TABLE IF NOT EXISTS strategy_positions (
    user_id     INT NOT NULL,
    engine_id   TEXT NOT NULL,
    side        TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'range',
    qty         DOUBLE PRECISION NOT NULL DEFAULT 0,
    entry_price DOUBLE PRECISION NOT NULL DEFAULT 0,
    stop_loss   DOUBLE PRECISION,
    take_profit DOUBLE PRECISION,
    "trailing"  DOUBLE PRECISION,
    peak_price  DOUBLE PRECISION,
    r_value     DOUBLE PRECISION,
    init_qty    DOUBLE PRECISION,
    tp1_hit     BOOLEAN DEFAULT FALSE,
    bars_held   INT DEFAULT 0,
    order_id    TEXT,
    filled      BOOLEAN DEFAULT TRUE,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, engine_id, side)
);
