#!/bin/bash
# Quantix — local deploy orchestrator
# Cross-compiles binary on Mac, exports per-tenant DB rows (option c),
# rsyncs everything to remote, and runs bootstrap-server.sh over SSH.
#
# Usage:
#   ./deploy/deploy.sh                    # full first-time deploy
#   ./deploy/deploy.sh --dry-run          # show what would happen, do nothing
#   ./deploy/deploy.sh --skip-pg-redis    # only update binary + restart (PG/Redis already installed)
#   ./deploy/deploy.sh --binary-only      # just push binary, restart systemd; no DB ops
#
# Required env (with sensible defaults):
#   SSH_HOST, SSH_USER, SSH_KEY, SSH_PORT
#   QUANTIX_ENCRYPTION_KEY, QUANTIX_JWT_SECRET   (must match local — fallback to scripts/start-quantix.sh defaults)
#   QUANTIX_DB_PASSWORD                          (defaults to 'quantix_secret' from local)

set -euo pipefail
cd "$(dirname "$0")/.."

# ─── Connection params ───────────────────────────────────────────────────────
SSH_HOST="${SSH_HOST:-54.46.102.153}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY="${SSH_KEY:-/Users/apexis-backdesk/work/pem/test.pem}"
SSH_PORT="${SSH_PORT:-22}"
REMOTE_DIR="/tmp/quantix-deploy"
INSTALL_DIR="/opt/quantix"

# ─── Secrets (must match local for encrypted creds to decrypt) ───────────────
QUANTIX_ENCRYPTION_KEY="${QUANTIX_ENCRYPTION_KEY:-b16f993bf0b8c2695bd9773ca9b24d060ea78182d884c3f4056fd80e4e021743}"
QUANTIX_JWT_SECRET="${QUANTIX_JWT_SECRET:-61ea018d43c6c953b4978606778107beb341e6e56b8b9e7b21df252897b0e55d}"
QUANTIX_DB_PASSWORD="${QUANTIX_DB_PASSWORD:-quantix_secret}"
QUANTIX_TG_BOT_TOKEN="${QUANTIX_TG_BOT_TOKEN:-8641417837:AAF_x2ZxndpqPY4y9XHFF4JBqStaOcoWzdc}"

# ─── Flags ───────────────────────────────────────────────────────────────────
DRY_RUN=false
SKIP_PG_REDIS=false
BINARY_ONLY=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    --skip-pg-redis) SKIP_PG_REDIS=true ;;
    --binary-only) BINARY_ONLY=true ;;
    *) echo "Unknown flag: $arg" ; exit 1 ;;
  esac
done

ssh_run() {
  if $DRY_RUN; then
    echo "[DRY] ssh ... \"$*\""
  else
    ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new "$SSH_USER@$SSH_HOST" "$@"
  fi
}

scp_push() {
  if $DRY_RUN; then
    echo "[DRY] scp $1 → $SSH_USER@$SSH_HOST:$2"
  else
    scp -i "$SSH_KEY" -P "$SSH_PORT" -o StrictHostKeyChecking=accept-new -r "$1" "$SSH_USER@$SSH_HOST:$2"
  fi
}

step() { printf "\n\033[1;36m── %s ──\033[0m\n" "$*"; }
ok()   { printf "\033[1;32m✓\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!\033[0m %s\n" "$*"; }
die()  { printf "\033[1;31m✗\033[0m %s\n" "$*"; exit 1; }

# ─── 1. Pre-flight checks ────────────────────────────────────────────────────
step "Pre-flight checks"
[ -f "$SSH_KEY" ] || die "SSH key not found: $SSH_KEY"
chmod 600 "$SSH_KEY" 2>/dev/null || true
command -v go >/dev/null || die "go not installed locally"
command -v rsync >/dev/null || die "rsync not installed locally (brew install rsync)"
[ "${#QUANTIX_ENCRYPTION_KEY}" -eq 64 ] || die "QUANTIX_ENCRYPTION_KEY must be 64 hex chars"
[ "${#QUANTIX_JWT_SECRET}" -ge 32 ]    || die "QUANTIX_JWT_SECRET must be ≥32 chars"
ok "Local environment OK"

# Test SSH connectivity
if ! $DRY_RUN; then
  ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 "$SSH_USER@$SSH_HOST" "uname -m" || die "SSH to $SSH_USER@$SSH_HOST failed"
  ok "SSH connectivity OK"
fi

# ─── 2. Cross-compile binary ─────────────────────────────────────────────────
step "Cross-compile linux/amd64 binary"
mkdir -p ./build
if $DRY_RUN; then
  echo "[DRY] GOOS=linux GOARCH=amd64 go build -o build/quantix-api-linux ./cmd/api"
else
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o ./build/quantix-api-linux ./cmd/api
  ok "Built: $(ls -lh ./build/quantix-api-linux | awk '{print $5}')"
fi

if $BINARY_ONLY; then
  step "Binary-only mode: push + restart"
  scp_push ./build/quantix-api-linux "$REMOTE_DIR/quantix-api"
  ssh_run "sudo install -m 755 -o $SSH_USER -g $SSH_USER $REMOTE_DIR/quantix-api $INSTALL_DIR/bin/quantix-api && sudo systemctl restart quantix-api"
  sleep 4
  ssh_run "curl -sf http://localhost:9300/api/health && echo ' ← health OK'"
  ok "Binary updated. Done."
  exit 0
fi

# ─── 3. Export local DB rows (option c) ──────────────────────────────────────
step "Export users + credentials + sessions from local DB"
if $DRY_RUN; then
  echo "[DRY] pg_dump --data-only -t users -t exchange_credentials -t engine_sessions"
else
  PGPASSWORD=quantix_secret pg_dump -h localhost -U quantix -d quantix \
    --data-only --no-owner --no-acl \
    -t users -t exchange_credentials -t engine_sessions \
    > ./build/data-export.sql
  rows=$(grep -c "^INSERT\|^COPY" ./build/data-export.sql || true)
  [ "$rows" -gt 0 ] || die "Empty dump — local DB has no users?"
  ok "Dumped $(wc -l < ./build/data-export.sql | tr -d ' ') lines, $rows COPY/INSERT statements"
fi

# ─── 4. Generate env file ────────────────────────────────────────────────────
step "Generate /etc/quantix/env"
ENV_FILE="./build/quantix.env"
cat > "$ENV_FILE" <<EOF
# Quantix runtime env — chmod 600, owned by root
QUANTIX_ENCRYPTION_KEY=$QUANTIX_ENCRYPTION_KEY
QUANTIX_JWT_SECRET=$QUANTIX_JWT_SECRET
QUANTIX_LIVE_CONFIRM=true
QUANTIX_API_ADDR=:9300
QUANTIX_API_CONFIG=$INSTALL_DIR/config/config.yaml
EOF
ok "Wrote $ENV_FILE"

# ─── 5. Generate config.yaml for server ──────────────────────────────────────
step "Generate config/config.yaml (server)"
CFG_FILE="./build/config.yaml"
cat > "$CFG_FILE" <<EOF
app:
  name: quantix
  env: production
  log_level: info
  log_dir: $INSTALL_DIR/logs

database:
  host: localhost
  port: 5432
  user: quantix
  password: $QUANTIX_DB_PASSWORD
  name: quantix
  ssl_mode: disable
  max_conns: 10

redis:
  addr: localhost:6379
  password: ""
  db: 0

trading:
  mode: live
  base_currency: USDT

risk:
  max_position_pct: 0.20
  max_drawdown_pct: 0.15
  max_single_loss_pct: 0.05

monitor:
  prometheus_port: 9301
  enabled: true
  margin_warn_threshold: 0.20

live:
  enabled: true
EOF
ok "Wrote $CFG_FILE"

# ─── 6. Generate systemd unit ────────────────────────────────────────────────
step "Generate systemd unit file"
UNIT_FILE="./build/quantix-api.service"
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Quantix Trading API
After=network.target postgresql.service redis-server.service
Requires=postgresql.service

[Service]
Type=simple
User=$SSH_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=/etc/quantix/env
ExecStart=$INSTALL_DIR/bin/quantix-api
Restart=on-failure
RestartSec=10
StandardOutput=append:$INSTALL_DIR/logs/stdout.log
StandardError=append:$INSTALL_DIR/logs/stderr.log
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
ok "Wrote $UNIT_FILE"

# ─── 7. Bundle and push to server ────────────────────────────────────────────
step "Push deployment bundle to $SSH_USER@$SSH_HOST:$REMOTE_DIR"
ssh_run "rm -rf $REMOTE_DIR && mkdir -p $REMOTE_DIR"
if ! $DRY_RUN; then
  rsync -avz --progress \
    -e "ssh -i $SSH_KEY -p $SSH_PORT -o StrictHostKeyChecking=accept-new" \
    ./build/quantix-api-linux \
    ./build/data-export.sql \
    ./build/quantix.env \
    ./build/config.yaml \
    ./build/quantix-api.service \
    ./migrations \
    ./deploy/bootstrap-server.sh \
    ./scripts/monitor.py \
    "$SSH_USER@$SSH_HOST:$REMOTE_DIR/"
  ok "Pushed"
fi

# ─── 8. Run bootstrap on remote ──────────────────────────────────────────────
step "Run bootstrap-server.sh on remote"
SKIP_FLAG=""
$SKIP_PG_REDIS && SKIP_FLAG="--skip-pg-redis"
ssh_run "chmod +x $REMOTE_DIR/bootstrap-server.sh && \
         sudo QUANTIX_DB_PASSWORD='$QUANTIX_DB_PASSWORD' \
              QUANTIX_TG_BOT_TOKEN='$QUANTIX_TG_BOT_TOKEN' \
              $REMOTE_DIR/bootstrap-server.sh $SKIP_FLAG"

# ─── 9. Final health check ───────────────────────────────────────────────────
step "Wait for service and verify"
if ! $DRY_RUN; then
  for i in $(seq 1 20); do
    sleep 2
    if ssh_run "curl -sf http://localhost:9300/api/health" 2>/dev/null | grep -q healthy; then
      ok "Service responded healthy after ${i}×2s"
      break
    fi
    [ $i -eq 20 ] && die "Service didn't become healthy. Check: ssh ... 'sudo journalctl -u quantix-api -n 100'"
  done
fi

# ─── 10. Summary ─────────────────────────────────────────────────────────────
cat <<EOF

\033[1;32m✓ Deployment finished\033[0m

Server:    $SSH_USER@$SSH_HOST
Install:   $INSTALL_DIR
Service:   sudo systemctl {status|restart|stop} quantix-api
Logs:      ssh -i $SSH_KEY $SSH_USER@$SSH_HOST 'tail -f $INSTALL_DIR/logs/quantix-\$(date +%Y%m%d).log'
Health:    ssh -i $SSH_KEY $SSH_USER@$SSH_HOST 'curl -s http://localhost:9300/api/health'

\033[1;33m! IMPORTANT: local engine is still running. Stop it BEFORE the server engine
  picks up your live binance credentials, or both will hammer the same exchange:\033[0m

  pkill -f quantix-api    # on your Mac

EOF
