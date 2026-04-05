#!/bin/bash
# Quantix API server startup script
# Usage: ./scripts/start-quantix.sh
# Logs written to ./logs/quantix-YYYYMMDD.log (date rotation, append mode)

set -e
cd "$(dirname "$0")/.."

# Environment variables (defaults baked in, override via env if needed)
export QUANTIX_ENCRYPTION_KEY="${QUANTIX_ENCRYPTION_KEY:-b16f993bf0b8c2695bd9773ca9b24d060ea78182d884c3f4056fd80e4e021743}"
export QUANTIX_JWT_SECRET="${QUANTIX_JWT_SECRET:-61ea018d43c6c953b4978606778107beb341e6e56b8b9e7b21df252897b0e55d}"
export QUANTIX_LIVE_CONFIRM="${QUANTIX_LIVE_CONFIRM:-true}"
export QUANTIX_API_ADDR="${QUANTIX_API_ADDR:-:9300}"

# Log directory under project root
LOG_DIR="./logs"
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/quantix-$(date +%Y%m%d).log"

echo "=== Engine start: $(date) ===" >> "$LOG_FILE"
exec ./bin/quantix-api -config config/config.yaml >> "$LOG_FILE" 2>&1
