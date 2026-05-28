#!/bin/bash
# Quantix — server bootstrap (runs on the target server, called by deploy.sh)
# Idempotent: safe to re-run; only does what's missing.
#
# Required env (set by deploy.sh via sudo -E):
#   QUANTIX_DB_PASSWORD, QUANTIX_TG_BOT_TOKEN

set -euo pipefail

REMOTE_DIR="/tmp/quantix-deploy"
INSTALL_DIR="/opt/quantix"
SERVICE_USER="${SUDO_USER:-ubuntu}"
SKIP_PG_REDIS=false
RESTORE_DATA=false   # only restore on first bootstrap or with explicit --restore-data

for arg in "$@"; do
  [ "$arg" = "--skip-pg-redis" ] && SKIP_PG_REDIS=true
  [ "$arg" = "--restore-data" ] && RESTORE_DATA=true
done

step() { printf "\n\033[1;36m── %s ──\033[0m\n" "$*"; }
ok()   { printf "\033[1;32m✓\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!\033[0m %s\n" "$*"; }

[ "$EUID" -eq 0 ] || { echo "Must run as root (use sudo)"; exit 1; }

# ─── 1. System packages ──────────────────────────────────────────────────────
step "Update apt cache"
DEBIAN_FRONTEND=noninteractive apt-get update -qq
ok "apt updated"

if ! $SKIP_PG_REDIS; then
  step "Install PostgreSQL 17 + TimescaleDB"
  if ! dpkg -l postgresql-17 2>/dev/null | grep -q '^ii'; then
    # PostgreSQL official apt repo (PG 17 not in default Ubuntu repos)
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl gnupg lsb-release ca-certificates
    install -d /usr/share/postgresql-common/pgdg
    curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
    sh -c "echo 'deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main' > /etc/apt/sources.list.d/pgdg.list"

    # TimescaleDB apt repo (gpg --batch + --no-tty for non-interactive ssh contexts)
    curl -fsSL https://packagecloud.io/timescale/timescaledb/gpgkey | gpg --batch --yes --no-tty --dearmor -o /usr/share/keyrings/timescaledb.gpg
    sh -c "echo 'deb [signed-by=/usr/share/keyrings/timescaledb.gpg] https://packagecloud.io/timescale/timescaledb/ubuntu/ $(lsb_release -cs) main' > /etc/apt/sources.list.d/timescaledb.list"

    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql-17 postgresql-client-17 timescaledb-2-postgresql-17

    # Tune timescaledb for hypertables
    timescaledb-tune --quiet --yes --pg-config=/usr/lib/postgresql/17/bin/pg_config || true
    systemctl restart postgresql
    ok "PostgreSQL 17 + TimescaleDB installed"
  else
    ok "PostgreSQL 17 already present (skip install)"
  fi

  step "Install Redis"
  if ! command -v redis-server >/dev/null; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq redis-server
    systemctl enable --now redis-server
    ok "Redis installed and started"
  else
    ok "Redis already present"
  fi
fi

# ─── 2. Database + user ──────────────────────────────────────────────────────
step "Create quantix DB user and database"
DBPW="${QUANTIX_DB_PASSWORD:?must be set}"

# Create role if missing
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='quantix'" | grep -q 1; then
  sudo -u postgres psql -c "CREATE USER quantix WITH ENCRYPTED PASSWORD '$DBPW';"
  ok "Created role 'quantix'"
else
  sudo -u postgres psql -c "ALTER USER quantix WITH ENCRYPTED PASSWORD '$DBPW';"
  ok "Updated password for existing role 'quantix'"
fi

# Create DB if missing
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='quantix'" | grep -q 1; then
  sudo -u postgres createdb -O quantix quantix
  ok "Created database 'quantix'"
else
  ok "Database 'quantix' already exists"
fi

# Enable TimescaleDB extension in the db
sudo -u postgres psql -d quantix -c "CREATE EXTENSION IF NOT EXISTS timescaledb;" >/dev/null
ok "TimescaleDB extension enabled in 'quantix'"

# ─── 3. Install dirs ─────────────────────────────────────────────────────────
step "Set up $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"/{bin,config,migrations,logs,scripts}
install -m 755 "$REMOTE_DIR/quantix-api-linux"  "$INSTALL_DIR/bin/quantix-api"
install -m 644 "$REMOTE_DIR/config.yaml"        "$INSTALL_DIR/config/config.yaml"
cp -f "$REMOTE_DIR"/migrations/*.sql            "$INSTALL_DIR/migrations/"
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"
ok "Installed binary, config, migrations"

# ─── 4. Secrets file ─────────────────────────────────────────────────────────
step "Install /etc/quantix/env"
mkdir -p /etc/quantix
install -m 600 -o root -g root "$REMOTE_DIR/quantix.env" /etc/quantix/env
ok "Installed /etc/quantix/env (chmod 600 root)"

# ─── 5. Apply migrations via psql (each .sql is idempotent) ──────────────────
step "Apply schema migrations"
applied=0
for f in $(ls "$INSTALL_DIR/migrations/"*.sql | sort); do
  if sudo -u postgres psql -d quantix -v ON_ERROR_STOP=1 -f "$f" >/tmp/quantix-migrate.log 2>&1; then
    applied=$((applied + 1))
  else
    warn "Migration failed: $(basename $f)"
    tail -20 /tmp/quantix-migrate.log
    exit 1
  fi
done
ok "Applied $applied migrations"

# Tables/sequences are owned by postgres after the migrations above. Reassign to
# quantix so the binary can re-run idempotent migrations on startup without
# hitting "must be owner" errors.
sudo -u postgres psql -d quantix >/dev/null <<'SQL'
DO $$ DECLARE r record;
BEGIN
  FOR r IN SELECT tablename FROM pg_tables WHERE schemaname='public' LOOP
    EXECUTE format('ALTER TABLE public.%I OWNER TO quantix', r.tablename);
  END LOOP;
  FOR r IN SELECT sequence_name FROM information_schema.sequences WHERE sequence_schema='public' LOOP
    EXECUTE format('ALTER SEQUENCE public.%I OWNER TO quantix', r.sequence_name);
  END LOOP;
END $$;
GRANT USAGE, CREATE ON SCHEMA public TO quantix;
SQL
ok "Reassigned table/sequence ownership to quantix"

# ─── 6. Restore data dump (first bootstrap only, or with --restore-data) ────
step "Restore users + credentials + sessions"
# Auto-detect first bootstrap: empty users table → safe to restore from local dump.
# Production deploys must NOT truncate-and-replace the server's live state
# (sessions written on-server include APIKey injection etc that local dump lacks).
EXISTING_USERS=$(sudo -u postgres psql -d quantix -tAc "SELECT count(*) FROM users" 2>/dev/null || echo 0)
if [ "$EXISTING_USERS" -eq 0 ]; then
  RESTORE_DATA=true
  warn "users table empty — first install, will restore from dump"
fi

if $RESTORE_DATA; then
  if [ -s "$REMOTE_DIR/data-export.sql" ]; then
    sudo -u postgres psql -d quantix <<SQL >/dev/null
TRUNCATE engine_sessions, exchange_credentials, users RESTART IDENTITY CASCADE;
SQL
    sudo -u postgres psql -d quantix < "$REMOTE_DIR/data-export.sql" >/dev/null
    uc=$(sudo -u postgres psql -d quantix -tAc "SELECT count(*) FROM users")
    cc=$(sudo -u postgres psql -d quantix -tAc "SELECT count(*) FROM exchange_credentials")
    sc=$(sudo -u postgres psql -d quantix -tAc "SELECT count(*) FROM engine_sessions")
    ok "Restored: $uc users, $cc credentials, $sc sessions"
  else
    warn "No data-export.sql found — fresh DB, no users restored"
  fi
else
  ok "Skipped restore — preserving server's live data ($EXISTING_USERS users present). Use --restore-data to override."
fi

# Also re-write the TG token if provided (in case local DB didn't have it yet)
if [ -n "${QUANTIX_TG_BOT_TOKEN:-}" ]; then
  sudo -u postgres psql -d quantix -c \
    "UPDATE users SET tg_bot_token='$QUANTIX_TG_BOT_TOKEN', tg_chat_id=2091951120 WHERE id=4;" >/dev/null 2>&1 || true
fi

# ─── 7. Install systemd unit ─────────────────────────────────────────────────
step "Install systemd unit"
install -m 644 "$REMOTE_DIR/quantix-api.service" /etc/systemd/system/quantix-api.service
systemctl daemon-reload
systemctl enable quantix-api >/dev/null
ok "Unit installed and enabled"

# ─── 8. Start service ────────────────────────────────────────────────────────
step "Start quantix-api"
systemctl restart quantix-api
sleep 3
if systemctl is-active --quiet quantix-api; then
  ok "Service active"
else
  warn "Service not active — last 20 log lines:"
  journalctl -u quantix-api -n 20 --no-pager
  exit 1
fi

# ─── 9. Health check ─────────────────────────────────────────────────────────
step "Health check"
for i in $(seq 1 15); do
  sleep 2
  if curl -sf http://localhost:9118/api/health 2>/dev/null | grep -q healthy; then
    ok "API healthy"
    break
  fi
  [ $i -eq 15 ] && { warn "API not healthy after 30s"; journalctl -u quantix-api -n 30 --no-pager; exit 1; }
done

# ─── 9b. Install nginx + frontend ────────────────────────────────────────────
step "Install nginx + web frontend"
if ! command -v nginx >/dev/null; then
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx
  systemctl enable --now nginx
  ok "nginx installed"
else
  ok "nginx already present"
fi

# Web frontend: rsync staged dist/ into /opt/quantix/web (idempotent).
if [ -d "$REMOTE_DIR/dist" ]; then
  mkdir -p "$INSTALL_DIR/web"
  rsync -a --delete "$REMOTE_DIR/dist/" "$INSTALL_DIR/web/"
  chown -R www-data:www-data "$INSTALL_DIR/web"
  ok "Frontend installed to $INSTALL_DIR/web ($(du -sh $INSTALL_DIR/web | cut -f1))"
else
  warn "web/dist not in bundle — skipping frontend install"
fi

# nginx site config + upgrade-map (drop in, validate, reload).
if [ -f "$REMOTE_DIR/nginx-quantix.conf" ]; then
  install -m 644 "$REMOTE_DIR/nginx-quantix.conf" /etc/nginx/sites-available/quantix
  ln -sf /etc/nginx/sites-available/quantix /etc/nginx/sites-enabled/quantix
  ok "Quantix site config installed"
fi
if [ -f "$REMOTE_DIR/nginx-upgrade-map.conf" ]; then
  install -m 644 "$REMOTE_DIR/nginx-upgrade-map.conf" /etc/nginx/conf.d/quantix-upgrade-map.conf
  ok "Quantix upgrade-map installed"
fi
if nginx -t >/dev/null 2>&1; then
  systemctl reload nginx
  ok "nginx reloaded — frontend live at :9119"
else
  warn "nginx config test failed:"
  nginx -t || true
fi

# ─── 10. Install monitor cron (every 2h) ─────────────────────────────────────
step "Install monitor cron"
if [ -f "$REMOTE_DIR/monitor.py" ]; then
  install -m 755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$REMOTE_DIR/monitor.py" "$INSTALL_DIR/scripts/monitor.py"
  mkdir -p "$INSTALL_DIR/logs/reports"
  chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR/scripts" "$INSTALL_DIR/logs/reports"
  CRON_LINE="0 */2 * * * cd $INSTALL_DIR && /usr/bin/python3 $INSTALL_DIR/scripts/monitor.py >> $INSTALL_DIR/logs/monitor.log 2>&1"
  ( sudo -u "$SERVICE_USER" crontab -l 2>/dev/null | grep -v "monitor.py" ; echo "$CRON_LINE" ) | sudo -u "$SERVICE_USER" crontab -
  ok "Cron installed (every 2h, runs as $SERVICE_USER)"
else
  warn "monitor.py not in bundle — skipping cron"
fi

# ─── 11. Cleanup ─────────────────────────────────────────────────────────────
rm -f /tmp/quantix-migrate.log
echo
ok "Bootstrap complete."
echo
echo "Useful commands on this server:"
echo "  sudo systemctl status quantix-api"
echo "  sudo journalctl -u quantix-api -f"
echo "  tail -f $INSTALL_DIR/logs/quantix-\$(date +%Y%m%d).log"
echo "  sudo -u postgres psql -d quantix"
echo "  ls $INSTALL_DIR/logs/reports/    # monitor 2h reports"
