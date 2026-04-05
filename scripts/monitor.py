#!/usr/bin/env python3
"""
Quantix Trade Monitor — runs every 2 hours via cron.
Analyzes trading logs for anomalies and generates reports.

Setup:
  crontab -e
  0 */2 * * * cd /Users/apexis-backdesk/project/go-workspace/Quantix && python3 scripts/monitor.py >> logs/monitor.log 2>&1
"""

import json
import re
import sys
import os
from datetime import datetime, timedelta
from pathlib import Path
from collections import defaultdict

# ─── Config ───────────────────────────────────────────────────────────────────

PROJECT_DIR = Path(__file__).resolve().parent.parent
LOG_DIR = PROJECT_DIR / "logs"
REPORT_DIR = PROJECT_DIR / "logs" / "reports"
REPORT_DIR.mkdir(exist_ok=True)

# How far back to analyze (2 hours)
LOOKBACK_HOURS = 2

# Alert thresholds
MAX_CONSEC_LOSSES = 3          # alert if 3+ consecutive stop-losses
MAX_LOSS_PERCENT = 5.0         # alert if equity dropped >5% in window
MIN_SIGNALS_PER_HOUR = 0       # alert if no GPT signals at all (engine dead?)
MAX_REVERSAL_RATE = 0.5        # alert if >50% of trades are reversal closes
NO_TRADE_HOURS = 4             # alert if no trades in 4+ hours AND regime != RANGE
GPT_FAIL_THRESHOLD = 3         # alert if GPT failed 3+ times in window
COUNTER_TREND_ALERT = True     # alert if opened counter-trend position

# ─── Log Parser ───────────────────────────────────────────────────────────────

LOG_TIME_FMT = "%Y-%m-%dT%H:%M:%S"

def parse_timestamp(line: str):
    """Extract timestamp from log line."""
    m = re.match(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})", line)
    if not m:
        return None
    return datetime.strptime(m.group(1), LOG_TIME_FMT)


def parse_json_fields(line: str) -> dict:
    """Extract JSON fields from a zap-style log line."""
    m = re.search(r'\{.*\}', line)
    if not m:
        return {}
    try:
        return json.loads(m.group())
    except json.JSONDecodeError:
        return {}


def get_today_log() -> Path:
    """Get today's log file path."""
    today = datetime.now().strftime("%Y%m%d")
    return LOG_DIR / f"quantix-{today}.log"


def read_recent_lines(log_path, lookback):
    """Read log lines from the last `lookback` period."""
    if not log_path.exists():
        return []
    cutoff = datetime.now() - lookback
    lines = []
    with open(log_path, "r", encoding="utf-8", errors="replace") as f:
        for line in f:
            ts = parse_timestamp(line)
            if ts and ts >= cutoff:
                lines.append(line.rstrip())
    return lines

# ─── Event Extractors ─────────────────────────────────────────────────────────

class TradeEvent:
    def __init__(self, ts, side, reason, entry=0, price=0, qty=0, pnl=0):
        self.ts = ts
        self.side = side
        self.reason = reason
        self.entry = entry
        self.price = price
        self.qty = qty
        self.pnl = pnl

def extract_events(lines):
    """Parse log lines into structured events."""
    events = {
        "signals": [],        # GPT signal decisions
        "opens": [],          # position opens
        "closes": [],         # position closes (SL, trailing, bounce, reversal)
        "tp_fills": [],       # staged TP fills
        "regime_skips": [],   # RANGE regime skips
        "gpt_failures": [],   # GPT call failures
        "reversals": [],      # GPT reversal checks
        "status": [],         # engine status snapshots
        "errors": [],         # ERROR level logs
        "restarts": [],       # engine start/stop
        "warnings": [],       # significant warnings
    }

    for line in lines:
        ts = parse_timestamp(line)
        if not ts:
            continue
        fields = parse_json_fields(line)

        # Signals
        if "AI signal →" in line:
            action = "HOLD"
            for a in ["BUY", "SELL", "BOTH", "HOLD"]:
                if f"→ {a}" in line:
                    action = a
                    break
            events["signals"].append({
                "ts": ts, "action": action,
                "price": fields.get("price", 0),
                "regime": fields.get("regime", ""),
                "trend_dir": fields.get("trend_dir", 0),
                "raw_L": fields.get("raw_L", fields.get("L", 0)),
                "raw_S": fields.get("raw_S", fields.get("S", 0)),
                "eff_L": fields.get("eff_L", fields.get("L", 0)),
                "eff_S": fields.get("eff_S", fields.get("S", 0)),
                "accum_L": fields.get("accum_L", 0),
                "accum_S": fields.get("accum_S", 0),
            })

        # Opens
        elif "AI: OPEN TREND" in line:
            events["opens"].append({
                "ts": ts, "side": fields.get("side", ""),
                "entry": fields.get("entry", 0),
                "sl": fields.get("sl", 0),
                "R": fields.get("R", 0),
                "qty": fields.get("qty", 0),
            })

        # Closes
        elif "AI: CLOSE" in line:
            events["closes"].append({
                "ts": ts, "side": fields.get("side", ""),
                "reason": fields.get("reason", ""),
                "entry": fields.get("entry", 0),
                "qty": fields.get("qty", 0),
                "est_pnl": fields.get("est_pnl", 0),
                "market": fields.get("market", False),
            })

        # Stop-loss (tick level)
        elif "TICK STOP-LOSS" in line or "TICK TRAILING" in line:
            reason = "stop_loss" if "STOP-LOSS" in line else "trailing"
            events["closes"].append({
                "ts": ts, "side": fields.get("side", ""),
                "reason": reason,
                "price": fields.get("price", 0),
                "stop": fields.get("stop", fields.get("trail", 0)),
            })

        # Staged TP fill
        elif "staged TP fill" in line:
            events["tp_fills"].append({
                "ts": ts, "side": fields.get("side", ""),
                "fill_price": fields.get("fill_price", 0),
                "fill_qty": fields.get("fill_qty", 0),
                "remain_qty": fields.get("remain_qty", 0),
                "est_pnl": fields.get("est_pnl", 0),
            })

        # Regime skip
        elif "skip" in line and "RANGE" in line:
            events["regime_skips"].append({"ts": ts})

        # GPT failures
        elif "GPT failed" in line:
            events["gpt_failures"].append({"ts": ts, "line": line})

        # Reversal checks
        elif "reversal check" in line:
            events["reversals"].append({
                "ts": ts, "holding": fields.get("holding", ""),
                "reverse_conf": fields.get("reverse_conf", 0),
                "reasoning": fields.get("reasoning", ""),
            })

        # Engine status
        elif "Live Trading Status" in line:
            events["status"].append({
                "ts": ts,
                "equity": fields.get("equity", 0),
                "cash": fields.get("cash", 0),
                "realized_pnl": fields.get("realized_pnl", 0),
                "open_positions": fields.get("open_positions", 0),
                "risk_halted": fields.get("risk_halted", False),
            })

        # Engine restarts
        elif "engine started" in line:
            events["restarts"].append({"ts": ts, "type": "start"})
        elif "live trading stopped" in line:
            events["restarts"].append({"ts": ts, "type": "stop"})

        # Errors
        elif "\tERROR\t" in line or "CRITICAL" in line:
            events["errors"].append({"ts": ts, "line": line[:200]})

        # Counter-trend warnings
        elif "market entry" in line and "regime" in line:
            events.setdefault("market_entries", []).append({
                "ts": ts, "side": fields.get("side", ""),
                "regime": fields.get("regime", ""),
                "conf": fields.get("conf", 0),
            })

        # Daily loss halt
        elif "daily loss limit" in line:
            events["warnings"].append({"ts": ts, "type": "daily_loss_halt", "line": line[:200]})

        # Close order failed
        elif "placeCloseOrder failed" in line:
            events["errors"].append({"ts": ts, "line": line[:200]})

    return events

# ─── Anomaly Detection ────────────────────────────────────────────────────────

def detect_anomalies(events):
    """Analyze events and return list of anomalies."""
    anomalies = []

    # 1. Consecutive stop-losses
    consec_sl = 0
    for c in events["closes"]:
        if c.get("reason") == "stop_loss":
            consec_sl += 1
            if consec_sl >= MAX_CONSEC_LOSSES:
                anomalies.append({
                    "severity": "HIGH",
                    "type": "consecutive_stop_losses",
                    "detail": f"{consec_sl} consecutive stop-losses detected",
                    "ts": c["ts"],
                })
        else:
            consec_sl = 0

    # 2. Equity drawdown
    statuses = events["status"]
    if len(statuses) >= 2:
        first_eq = statuses[0].get("equity", 0)
        last_eq = statuses[-1].get("equity", 0)
        if first_eq > 0:
            dd_pct = (first_eq - last_eq) / first_eq * 100
            if dd_pct >= MAX_LOSS_PERCENT:
                anomalies.append({
                    "severity": "CRITICAL",
                    "type": "equity_drawdown",
                    "detail": f"Equity dropped {dd_pct:.1f}% ({first_eq:.2f} → {last_eq:.2f})",
                })

    # 3. No signals at all (engine might be dead)
    if len(events["signals"]) == 0 and len(events["regime_skips"]) == 0:
        anomalies.append({
            "severity": "CRITICAL",
            "type": "no_activity",
            "detail": "No signals and no regime skips — engine may be dead",
        })

    # 4. No trades for extended period while regime is not RANGE
    if len(events["opens"]) == 0:
        non_range_signals = [s for s in events["signals"] if s["regime"] and s["regime"] != "RANGE"]
        if len(non_range_signals) > 0:
            hours_no_trade = LOOKBACK_HOURS
            if hours_no_trade >= NO_TRADE_HOURS:
                anomalies.append({
                    "severity": "MEDIUM",
                    "type": "no_trades_in_trend",
                    "detail": f"No trades in {hours_no_trade}h despite non-RANGE regime detected {len(non_range_signals)} times",
                })

    # 5. GPT failures
    if len(events["gpt_failures"]) >= GPT_FAIL_THRESHOLD:
        anomalies.append({
            "severity": "HIGH",
            "type": "gpt_failures",
            "detail": f"GPT failed {len(events['gpt_failures'])} times in window",
        })

    # 6. High reversal close rate
    total_closes = len(events["closes"])
    reversal_closes = len([c for c in events["closes"] if c.get("reason") == "gpt_reversal"])
    if total_closes >= 3 and reversal_closes / total_closes > MAX_REVERSAL_RATE:
        anomalies.append({
            "severity": "MEDIUM",
            "type": "high_reversal_rate",
            "detail": f"{reversal_closes}/{total_closes} closes are GPT reversals — possible whipsaw",
        })

    # 7. Counter-trend entry (opened against detected trend direction)
    for sig in events["signals"]:
        td = sig.get("trend_dir", 0)
        action = sig["action"]
        if td == -1 and action == "BUY":
            anomalies.append({
                "severity": "HIGH",
                "type": "counter_trend_entry",
                "detail": f"BUY signal in bearish trend (trend_dir=-1) at {sig['ts']} price={sig['price']}",
                "ts": sig["ts"],
            })
        if td == 1 and action == "SELL":
            anomalies.append({
                "severity": "HIGH",
                "type": "counter_trend_entry",
                "detail": f"SELL signal in bullish trend (trend_dir=+1) at {sig['ts']} price={sig['price']}",
                "ts": sig["ts"],
            })

    # 8. Frequent restarts
    restarts = [r for r in events["restarts"] if r["type"] == "start"]
    if len(restarts) >= 3:
        anomalies.append({
            "severity": "HIGH",
            "type": "frequent_restarts",
            "detail": f"Engine restarted {len(restarts)} times in {LOOKBACK_HOURS}h window",
        })

    # 9. Error-level logs
    if len(events["errors"]) > 0:
        anomalies.append({
            "severity": "HIGH",
            "type": "error_logs",
            "detail": f"{len(events['errors'])} ERROR/CRITICAL logs detected",
            "samples": [e["line"] for e in events["errors"][:3]],
        })

    # 10. Risk halted
    halted = [s for s in events["status"] if s.get("risk_halted")]
    if halted:
        anomalies.append({
            "severity": "CRITICAL",
            "type": "risk_halted",
            "detail": "Engine is risk-halted (consecutive losses or daily loss limit)",
        })

    return anomalies

# ─── Report Generator ─────────────────────────────────────────────────────────

def generate_report(events, anomalies):
    """Generate a human-readable report."""
    now = datetime.now()
    lines = [
        f"{'='*60}",
        f"  Quantix Trade Monitor Report",
        f"  {now.strftime('%Y-%m-%d %H:%M:%S')}",
        f"  Window: last {LOOKBACK_HOURS} hours",
        f"{'='*60}",
        "",
    ]

    # Summary
    statuses = events["status"]
    if statuses:
        first = statuses[0]
        last = statuses[-1]
        lines.append(f"Equity: {last.get('equity', 0):.2f} (start: {first.get('equity', 0):.2f})")
        lines.append(f"Realized PnL: {last.get('realized_pnl', 0):.2f}")
        lines.append(f"Open Positions: {last.get('open_positions', 0)}")
        lines.append("")

    # Signals summary
    signals = events["signals"]
    if signals:
        actions = defaultdict(int)
        regimes = defaultdict(int)
        for s in signals:
            actions[s["action"]] += 1
            if s["regime"]:
                regimes[s["regime"]] += 1
        lines.append(f"Signals: {len(signals)} total")
        lines.append(f"  Actions: {dict(actions)}")
        lines.append(f"  Regimes: {dict(regimes)}")
        last_sig = signals[-1]
        lines.append(f"  Latest: {last_sig['action']} at {last_sig['ts'].strftime('%H:%M')} "
                      f"price={last_sig['price']} regime={last_sig['regime']} "
                      f"trend_dir={last_sig.get('trend_dir', '?')}")
        lines.append("")

    # Trades
    opens = events["opens"]
    closes = events["closes"]
    tp_fills = events["tp_fills"]
    lines.append(f"Trades: {len(opens)} opens, {len(closes)} closes, {len(tp_fills)} TP fills")
    for o in opens:
        lines.append(f"  OPEN {o['side']} @ {o['entry']:.2f} qty={o['qty']} SL={o['sl']:.2f} R={o['R']:.2f}")
    for c in closes:
        reason = c.get("reason", "?")
        pnl = c.get("est_pnl", 0)
        lines.append(f"  CLOSE {c.get('side', '?')} reason={reason} pnl={pnl:.2f}")
    for tp in tp_fills:
        lines.append(f"  TP FILL {tp['side']} @ {tp['fill_price']:.2f} qty={tp['fill_qty']} "
                      f"remain={tp['remain_qty']} pnl={tp['est_pnl']:.2f}")
    lines.append("")

    # Reversal checks
    revs = events["reversals"]
    if revs:
        lines.append(f"Reversal Checks: {len(revs)}")
        for r in revs[-3:]:  # last 3
            lines.append(f"  {r['ts'].strftime('%H:%M')} holding={r['holding']} "
                          f"conf={r['reverse_conf']:.2f}")
        lines.append("")

    # GPT failures
    if events["gpt_failures"]:
        lines.append(f"GPT Failures: {len(events['gpt_failures'])}")
        lines.append("")

    # Restarts
    if events["restarts"]:
        lines.append(f"Engine Events: {len(events['restarts'])}")
        for r in events["restarts"]:
            lines.append(f"  {r['ts'].strftime('%H:%M')} {r['type']}")
        lines.append("")

    # Anomalies
    lines.append(f"{'─'*60}")
    if anomalies:
        lines.append(f"ANOMALIES DETECTED: {len(anomalies)}")
        lines.append("")
        for a in sorted(anomalies, key=lambda x: {"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2}.get(x["severity"], 3)):
            lines.append(f"  [{a['severity']}] {a['type']}")
            lines.append(f"    {a['detail']}")
            if "samples" in a:
                for s in a["samples"]:
                    lines.append(f"    > {s}")
            lines.append("")
    else:
        lines.append("No anomalies detected.")
        lines.append("")

    return "\n".join(lines)

# ─── Main ─────────────────────────────────────────────────────────────────────

def main():
    log_path = get_today_log()
    if not log_path.exists():
        print(f"[{datetime.now()}] No log file found: {log_path}")
        return

    lookback = timedelta(hours=LOOKBACK_HOURS)
    lines = read_recent_lines(log_path, lookback)

    if not lines:
        print(f"[{datetime.now()}] No recent log lines in last {LOOKBACK_HOURS}h")
        return

    events = extract_events(lines)
    anomalies = detect_anomalies(events)
    report = generate_report(events, anomalies)

    # Print to stdout (captured by cron)
    print(report)

    # Save report file
    ts = datetime.now().strftime("%Y%m%d-%H%M")
    report_path = REPORT_DIR / f"monitor-{ts}.txt"
    report_path.write_text(report, encoding="utf-8")

    # Print severity summary for quick scanning
    if anomalies:
        critical = [a for a in anomalies if a["severity"] == "CRITICAL"]
        high = [a for a in anomalies if a["severity"] == "HIGH"]
        if critical:
            print(f"\n*** {len(critical)} CRITICAL anomalies — immediate attention needed ***")
        if high:
            print(f"*** {len(high)} HIGH anomalies — review recommended ***")

    return len(anomalies)


if __name__ == "__main__":
    sys.exit(main() or 0)
