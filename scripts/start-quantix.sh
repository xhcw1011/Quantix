#!/bin/bash
# Quantix — build, start, health check
# Engine auto-starts from DB session (auto-restart mechanism).
# If no session exists, manually start via API.

set -e
cd "$(dirname "$0")/.."

# ─── Environment ─────────────────────────────────────────────────────────────
export QUANTIX_ENCRYPTION_KEY="${QUANTIX_ENCRYPTION_KEY:-b16f993bf0b8c2695bd9773ca9b24d060ea78182d884c3f4056fd80e4e021743}"
export QUANTIX_JWT_SECRET="${QUANTIX_JWT_SECRET:-61ea018d43c6c953b4978606778107beb341e6e56b8b9e7b21df252897b0e55d}"
export QUANTIX_LIVE_CONFIRM="${QUANTIX_LIVE_CONFIRM:-true}"
export QUANTIX_API_ADDR="${QUANTIX_API_ADDR:-:9300}"
API="http://localhost${QUANTIX_API_ADDR}"

# ─── Stop old process ────────────────────────────────────────────────────────
if pgrep -f "quantix-api" > /dev/null 2>&1; then
  echo "Stopping old quantix-api..."
  pkill -f "quantix-api" || true
  sleep 2
fi

# ─── Build ───────────────────────────────────────────────────────────────────
echo "Building..."
mkdir -p ./bin
go build -o ./bin/quantix-api ./cmd/api

# ─── Start ───────────────────────────────────────────────────────────────────
LOG_DIR="./logs"
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/quantix-$(date +%Y%m%d).log"

echo "=== Engine start: $(date) ===" >> "$LOG_FILE"
# stdout goes to /dev/null — logger writes directly to log file (no duplicates)
nohup ./bin/quantix-api -config config/config.yaml > /dev/null 2>&1 &
PID=$!
echo "quantix-api started (pid: $PID)"

# ─── Health check ────────────────────────────────────────────────────────────
echo "Waiting for health check..."
for i in $(seq 1 10); do
  sleep 1
  if curl -sf "$API/api/health" > /dev/null 2>&1; then
    echo "Health: OK"
    break
  fi
  if [ $i -eq 10 ]; then
    echo "ERROR: health check failed after 10s"
    exit 1
  fi
done

# ─── Wait for auto-restart ───────────────────────────────────────────────────
# Auto-restart runs 2s after API start. Wait 5s then check.
# Do NOT call engine/start manually — it creates a second klineCh that conflicts.
sleep 5
TOKEN=$(printf '{"username":"stresstest","password":"StressTest123!"}' | \
  curl -s -X POST "$API/api/auth/login" -H 'Content-Type: application/json' -d @- 2>/dev/null | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('token',''))" 2>/dev/null || true)

if [ -n "$TOKEN" ]; then
  STATUS=$(curl -s -H "Authorization: Bearer $TOKEN" "$API/api/engine/status" 2>/dev/null || true)
  if echo "$STATUS" | grep -q '"running"'; then
    echo "Engine running (auto-restart from session)"
  else
    echo "No active session — starting engine..."
    RESULT=$(printf '{
      "credential_id": 2,
      "strategy_id": "ai",
      "symbol": "ETHUSDT",
      "interval": "5m",
      "mode": "live",
      "confirm_live": true,
      "params": {"Symbol":"ETHUSDT","EnableShort":true,"Intervals":["1m","5m","15m"]},
      "risk": {"max_position_pct":0.6,"max_drawdown_pct":0.10,"max_single_loss_pct":0.04}
    }' | curl -s --max-time 60 -X POST "$API/api/engine/start" \
      -H "Authorization: Bearer $TOKEN" \
      -H 'Content-Type: application/json' -d @- 2>/dev/null || true)
    echo "Engine: $RESULT"
  fi
else
  echo "Note: login failed — engine will auto-restart from session if available"
fi

echo ""
echo "Done. Logs: tail -f $LOG_FILE"
