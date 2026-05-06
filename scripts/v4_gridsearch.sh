#!/bin/bash
# v4 parameter grid-search over backtest.
# Tries combinations of Lookback, EntryZScore, StopZScore, TimeStopBars
# and writes summary CSV.

set -e

OUT_DIR="${OUT_DIR:-/tmp/qbt/v4_grid}"
mkdir -p "$OUT_DIR"
SUMMARY="$OUT_DIR/summary.csv"
echo "lookback,entry_z,stop_z,time_stop,return_pct,sharpe,max_dd_pct,trades,win_rate,profit_factor" > "$SUMMARY"

START="${START:-2026-01-21}"
END="${END:-2026-05-06}"
CAPITAL="${CAPITAL:-5000}"
FEE="${FEE:-0.0004}"
SLIP="${SLIP:-0.0001}"

LOOKBACKS=(10 20 30 50)
ENTRY_ZS=(2.0 2.5 3.0)
STOP_ZS=(3.0 3.5 4.0)
TIME_STOPS=(6 12 24)

count=0
total=$(( ${#LOOKBACKS[@]} * ${#ENTRY_ZS[@]} * ${#STOP_ZS[@]} * ${#TIME_STOPS[@]} ))

for lb in "${LOOKBACKS[@]}"; do
  for ez in "${ENTRY_ZS[@]}"; do
    for sz in "${STOP_ZS[@]}"; do
      if (( $(echo "$sz <= $ez" | bc -l) )); then
        continue
      fi
      for ts in "${TIME_STOPS[@]}"; do
        count=$((count + 1))
        out="$OUT_DIR/lb${lb}_ez${ez}_sz${sz}_ts${ts}.json"
        echo "[$count/$total] lb=$lb ez=$ez sz=$sz ts=$ts"
        ./bin/backtest -strategy ai_v4 -symbol ETHUSDT -interval 5m \
          -start "$START" -end "$END" -capital "$CAPITAL" \
          -fee "$FEE" -slippage "$SLIP" \
          -params "{\"Symbol\":\"ETHUSDT\",\"Lookback\":$lb,\"EntryZScore\":$ez,\"StopZScore\":$sz,\"TimeStopBars\":$ts}" \
          -out-json "$out" > /dev/null 2>&1 || { echo "  FAILED, skip"; continue; }

        python3 -c "
import json
d = json.load(open('$out'))
m = d.get('metrics', d)
print(f\"$lb,$ez,$sz,$ts,{m.get('total_return_pct',0):.4f},{m.get('sharpe_ratio',0):.4f},{m.get('max_drawdown_pct',0):.4f},{m.get('total_trades',0)},{m.get('win_rate',0):.4f},{m.get('profit_factor',0):.4f}\")
" >> "$SUMMARY"
      done
    done
  done
done

echo
echo "=== TOP 10 by Sharpe (PF >= 1.0 only) ==="
python3 -c "
import csv
rows = []
with open('$SUMMARY') as f:
    r = csv.DictReader(f)
    for row in r:
        try:
            if float(row['profit_factor']) >= 1.0:
                rows.append(row)
        except: pass
rows.sort(key=lambda r: float(r['sharpe']), reverse=True)
hdr = ['lookback','entry_z','stop_z','time_stop','return_pct','sharpe','max_dd_pct','trades','profit_factor']
print(' | '.join(f'{h:>9}' for h in hdr))
for r in rows[:10]:
    print(' | '.join(f'{r[h]:>9}' for h in hdr))
print()
print(f'Total combos with PF>=1.0: {len(rows)}')
"

echo
echo "=== TOP 10 by Sharpe (ALL, even losing) — for diagnostic ==="
python3 -c "
import csv
rows = list(csv.DictReader(open('$SUMMARY')))
rows.sort(key=lambda r: float(r['sharpe']), reverse=True)
hdr = ['lookback','entry_z','stop_z','time_stop','return_pct','sharpe','max_dd_pct','trades','profit_factor']
print(' | '.join(f'{h:>9}' for h in hdr))
for r in rows[:10]:
    print(' | '.join(f'{r[h]:>9}' for h in hdr))
"
