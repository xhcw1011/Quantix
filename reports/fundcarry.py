#!/usr/bin/env python3
# Funding-rate carry backtest (stdlib). Delta-neutral cash-and-carry: long spot +
# short perp earns the perp funding when positive. Models FUNDING INCOME ONLY —
# does NOT model basis risk, perp-leg liquidation, or slippage, so the reported
# Sharpe is wildly optimistic (carry has fat negative tails this misses).
# Sticky signal (trailing avg funding) keeps churn near zero.
#
# data: reports/funding.csv  (fundingTime_ms,symbol,rate) from fapi/v1/fundingRate
# usage: fundcarry.py <slowIntervals> <thr> <onewayCostBothLegs> [symbols-csv]
import sys, math, datetime
from collections import defaultdict, deque

slow = int(sys.argv[1]) if len(sys.argv) > 1 else 90
thr = float(sys.argv[2]) if len(sys.argv) > 2 else 0.0
oneway = float(sys.argv[3]) if len(sys.argv) > 3 else 0.0005
only = set(sys.argv[4].split(",")) if len(sys.argv) > 4 else None

fund = defaultdict(dict); times = set()
for line in open("reports/funding.csv"):
    t, s, r = line.strip().split(","); t = int(t)
    fund[s][t] = float(r); times.add(t)
times = sorted(times); syms = sorted(fund)
yr = lambda t: datetime.datetime.utcfromtimestamp(t/1000).year

held = set(); byyr = defaultdict(float); rets = []; turns = 0
hist = defaultdict(lambda: deque(maxlen=slow))
for i, t in enumerate(times):
    univ = [s for s in syms if (only is None or s in only)]
    sig = {}
    for s in univ:
        if i > 0 and times[i-1] in fund[s]: hist[s].append(fund[s][times[i-1]])
        sig[s] = (sum(hist[s])/len(hist[s])) if len(hist[s]) >= slow//2 else None
    newheld = set(s for s in univ if sig[s] is not None and sig[s] > thr)
    nh = max(len(newheld), 1)
    changed = len(newheld.symmetric_difference(held)); turns += changed
    cost = changed*oneway/nh
    earn = sum(fund[s][t] for s in newheld if t in fund[s])/nh if newheld else 0
    net = earn - cost; byyr[yr(t)] += net; rets.append(net); held = newheld

span = (times[-1]-times[0])/1000/86400/365
eq = 1.0
for r in rets: eq *= (1+r)
ann = (eq**(1/span)-1)*100
mean = sum(rets)/len(rets); sd = math.sqrt(sum((x-mean)**2 for x in rets)/len(rets))
sharpe = mean/sd*math.sqrt(3*365) if sd > 0 else 0
print(f"carry slow{slow} thr{thr} cost{oneway} {'/'.join(sorted(only)) if only else 'ALL'}")
print(f"  ann {ann:.1f}%  Sharpe(funding-only, optimistic) {sharpe:.1f}  turns {turns}")
print(f"  yearly%: {{{', '.join(f'{y}:{round(v*100,1)}' for y,v in sorted(byyr.items()))}}}")
