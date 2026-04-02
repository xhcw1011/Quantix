#!/bin/bash
# Quantix API server startup script
# Usage: ./scripts/start-quantix.sh
# Logs written to ./logs/quantix-YYYYMMDD.log (date rotation, append mode)

set -e
cd "$(dirname "$0")/.."

# Required environment variables (override defaults if needed)
export QUANTIX_ENCRYPTION_KEY="${QUANTIX_ENCRYPTION_KEY:?Set QUANTIX_ENCRYPTION_KEY}"
export QUANTIX_JWT_SECRET="${QUANTIX_JWT_SECRET:?Set QUANTIX_JWT_SECRET}"
export QUANTIX_LIVE_CONFIRM="${QUANTIX_LIVE_CONFIRM:-true}"
export QUANTIX_API_ADDR="${QUANTIX_API_ADDR:-:9300}"

# Log directory under project root
LOG_DIR="./logs"
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/quantix-$(date +%Y%m%d).log"

echo "=== Engine start: $(date) ===" >> "$LOG_FILE"
exec ./bin/quantix-api -config config/config.yaml >> "$LOG_FILE" 2>&1
