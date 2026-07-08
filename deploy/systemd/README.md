# xsfunding rebalancer — server deployment (systemd)

Runs the cross-sectional funding rebalancer as an **isolated** systemd service on the
server, so it auto-starts on boot, restarts on crash, and can NOT take down the main
`quantix-api` service (which runs the live aistrat engines).

Two units:
- **xsfunding-live.service** — long-lived `-schedule` loop; rotates every 3 days @ 08:00 UTC.
- **xsfunding-ingest.timer** → **xsfunding-ingest.service** — daily klines+funding refresh @ 07:40 UTC.

## One-time setup on the server (54.46.102.153)

1. **Build + push the binaries** (from dev):
   ```bash
   GOOS=linux GOARCH=amd64 go build -o bin/xsfunding-live   ./cmd/xsfunding-live
   GOOS=linux GOARCH=amd64 go build -o bin/xsfunding-shadow ./cmd/xsfunding-shadow
   GOOS=linux GOARCH=amd64 go build -o bin/ingest-klines    ./cmd/ingest-klines
   GOOS=linux GOARCH=amd64 go build -o bin/ingest-funding   ./cmd/ingest-funding
   scp bin/{xsfunding-live,xsfunding-shadow,ingest-klines,ingest-funding} ubuntu@SERVER:/opt/quantix/bin/
   ```

2. **DB: run the funding_rates migration** on the server's postgres (it's auto-run by the
   API service on boot via `store.RunMigrations`, so a `quantix-api` restart applies
   `013_funding_rates.sql`; or apply it manually).

3. **Populate the DB** (first backfill — deeper history):
   ```bash
   cd /opt/quantix && ./bin/ingest-klines -days 400 && ./bin/ingest-funding
   ```

4. **Credentials** — put the dedicated account's API key/secret in a gitignored env file:
   ```bash
   sudo tee /opt/quantix/.xsfunding.env >/dev/null <<'EOF'
   QUANTIX_TESTNET_API_KEY=...
   QUANTIX_TESTNET_SECRET=...
   # MAINNET real money only: also set the -testnet=false flag in the .service AND:
   # QUANTIX_LIVE_CONFIRM=true
   EOF
   sudo chmod 600 /opt/quantix/.xsfunding.env
   ```

5. **Install + enable the units**:
   ```bash
   sudo cp deploy/systemd/xsfunding-live.service          /etc/systemd/system/
   sudo cp deploy/systemd/xsfunding-ingest.service        /etc/systemd/system/
   sudo cp deploy/systemd/xsfunding-ingest.timer          /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now xsfunding-ingest.timer
   sudo systemctl enable --now xsfunding-live
   ```

6. **Watch it**:
   ```bash
   journalctl -u xsfunding-live -f
   systemctl list-timers xsfunding-ingest.timer
   ```

## Going live (real money)

Only after paper-forward confirms cost/edge. Then, on a **dedicated mainnet sub-account**:
edit `xsfunding-live.service` → add `-testnet=false`, set `QUANTIX_LIVE_CONFIRM=true` in the
env file, size `-capital`, and `systemctl restart xsfunding-live`. The OrderBroker refuses
mainnet without `QUANTIX_LIVE_CONFIRM=true`.
