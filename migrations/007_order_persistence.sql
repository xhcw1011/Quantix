-- Phase 15: OMS persistence columns and indexes

-- New columns needed by OMS persistence
ALTER TABLE orders ADD COLUMN IF NOT EXISTS position_side   TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS stop_price      DOUBLE PRECISION;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS reject_reason   TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS client_order_id TEXT;

-- Partial unique index: enforces idempotency for non-null client_order_id values.
-- NULL values are exempt (rows without a client_order_id are always distinct).
CREATE UNIQUE INDEX IF NOT EXISTS uq_orders_client_order_id
    ON orders(client_order_id) WHERE client_order_id IS NOT NULL;

-- Composite index for startup recovery (GetActiveOrders) and API queries.
CREATE INDEX IF NOT EXISTS idx_orders_user_strategy_status
    ON orders(user_id, strategy_id, status);
