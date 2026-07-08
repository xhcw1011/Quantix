#!/usr/bin/env bash
# Local daily-ingest loop for the xsfunding rebalancer: keeps the DB's klines + funding
# fresh so the scheduled rotation reads current data. Runs once immediately, then every
# ~24h. The server equivalent is deploy/systemd/xsfunding-ingest.timer (07:40 UTC daily).
#
#   nohup ./scripts/xsfunding-ingest-loop.sh >> logs/xsfunding-ingest.log 2>&1 &
set -u
cd "$(dirname "$0")/.." || exit 1

while true; do
  echo "=== ingest run $(date -u '+%Y-%m-%d %H:%M:%SZ') ==="
  ./bin/ingest-klines -days 5 2>&1 | grep -vE '^\{|"level"|"ts"|"msg"|"caller"' | tail -2
  ./bin/ingest-funding 2>&1     | grep -vE '^\{|"level"|"ts"|"msg"|"caller"' | tail -2
  echo "=== done, sleeping 24h ==="
  sleep 86400
done
