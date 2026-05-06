# Quantix Server Deployment

Two scripts: `deploy.sh` (runs on Mac) → `bootstrap-server.sh` (runs on server, called automatically).

## First-time deployment

```bash
# 1. Verify locally — see what would happen, change nothing
./deploy/deploy.sh --dry-run

# 2. Real deploy: cross-compiles, exports DB, installs PG/Redis/TimescaleDB,
#    creates DB, restores users + credentials + sessions, starts systemd service
./deploy/deploy.sh

# 3. After it reports green, manually stop your local engine to avoid double-trading:
pkill -f quantix-api
```

## What gets installed on the server

```
/opt/quantix/
├── bin/quantix-api          # cross-compiled linux/amd64 binary (~20 MB)
├── config/config.yaml       # generated server config
├── migrations/*.sql         # 11 schema files
├── logs/                    # rotates daily as quantix-YYYYMMDD.log
│   └── reports/             # monitor.py output
└── scripts/monitor.py       # 2h cron

/etc/quantix/env             # secrets, chmod 600 root
/etc/systemd/system/quantix-api.service
```

## Subsequent deployments (just code changes)

```bash
./deploy/deploy.sh --binary-only      # cross-compile, scp, systemctl restart
```

## Config knobs

```bash
SSH_HOST=54.46.102.153 \
SSH_USER=ubuntu \
SSH_KEY=/Users/apexis-backdesk/work/pem/test.pem \
QUANTIX_DB_PASSWORD='YourStrongerPasswordHere' \
./deploy/deploy.sh
```

If you don't set these, defaults from `deploy.sh` top are used (matches your local config).

## Verifying after deploy

```bash
# Service status
ssh -i ~/work/pem/test.pem ubuntu@54.46.102.153 'sudo systemctl status quantix-api'

# Live log tail
ssh -i ~/work/pem/test.pem ubuntu@54.46.102.153 \
  'tail -f /opt/quantix/logs/quantix-$(date +%Y%m%d).log'

# Health check
ssh -i ~/work/pem/test.pem ubuntu@54.46.102.153 \
  'curl -s http://localhost:9300/api/health'
```

You should also receive a Telegram message within ~30s of service start.

## Operations

```bash
# On the server:
sudo systemctl restart quantix-api
sudo systemctl stop quantix-api          # safe; engine state persists in DB
sudo journalctl -u quantix-api -n 200    # systemd log
sudo -u postgres psql -d quantix         # poke at DB
```

## Rollback

`deploy.sh` keeps `/opt/quantix/bin/quantix-api.prev`. To roll back to the previous binary:

```bash
ssh ubuntu@54.46.102.153 'sudo mv /opt/quantix/bin/quantix-api.prev /opt/quantix/bin/quantix-api && sudo systemctl restart quantix-api'
```

(NOTE: `.prev` is only created on `--binary-only` deploys; first-time install has no prev.)

## Security notes

- `:9300` listens on all interfaces by default. Lock down via UFW:
  ```bash
  sudo ufw allow 22
  sudo ufw allow from YOUR_HOME_IP to any port 9300
  sudo ufw enable
  ```
- For HTTPS / public access, put nginx in front. Not done by the script.
- DB password is in `/etc/quantix/env` (chmod 600). Don't commit it.
- Encryption key MUST match local: encrypted exchange credentials in DB are AES-GCM with that key.

## Troubleshooting

| Symptom | Check |
|---|---|
| SSH fails | `chmod 600 ~/work/pem/test.pem`, security group allows :22 from your IP |
| `psql: command not found` after install | `sudo apt install -y postgresql-client-17` |
| Service won't start | `sudo journalctl -u quantix-api -n 50` |
| "no migration files found" | `ls /opt/quantix/migrations/` should show 11 .sql files |
| Engine doesn't auto-restart from session | Check `engine_sessions` table: `sudo -u postgres psql -d quantix -c 'select * from engine_sessions'` |
| TG silent | Check `/etc/quantix/env` keys; check user row: `sudo -u postgres psql -d quantix -c 'select id, username, length(tg_bot_token) from users'` |
