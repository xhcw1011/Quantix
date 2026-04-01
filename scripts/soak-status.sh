#!/bin/bash
# Quantix 浸泡测试 - 状态检查脚本
cd "$(dirname "$0")/.."

API=http://localhost:9300/api
LATEST_LOG=$(ls -dt logs/soak-* 2>/dev/null | head -1)

echo "========================================="
echo "  Quantix 浸泡测试状态"
echo "  $(date)"
echo "========================================="

# 进程状态
if [ -f "$LATEST_LOG/api.pid" ]; then
  PID=$(cat "$LATEST_LOG/api.pid")
  if kill -0 "$PID" 2>/dev/null; then
    UPTIME=$(ps -o etime= -p "$PID" 2>/dev/null | xargs)
    echo "进程: 运行中 (PID $PID, uptime $UPTIME)"
  else
    echo "进程: 已停止"
  fi
fi

# 健康检查
echo ""
echo "=== 健康检查 ==="
curl -sf $API/health 2>/dev/null && echo "" || echo "API 不可达"

# 登录获取状态
TOKEN=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"stresstest","password":"StressTest123!"}' 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)

if [ -n "$TOKEN" ]; then
  echo ""
  echo "=== 引擎状态 ==="
  curl -s $API/engines -H "Authorization: Bearer $TOKEN" | python3 -m json.tool 2>/dev/null

  echo ""
  echo "=== 持仓 ==="
  curl -s $API/positions -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for p in d.get('positions',[]):
    print(f'  equity=\${p[\"equity\"]:.2f}  cash=\${p[\"cash\"]:.2f}  last_price={p[\"last_price\"]:.2f}')
    for pos in p.get('positions',[]):
        print(f'    {pos[\"symbol\"]} qty={pos[\"qty\"]:.6f} entry={pos[\"avg_entry_price\"]:.2f} upnl=\${pos[\"unrealized_pnl\"]:.2f}')
    if not p.get('positions'):
        print('    (无持仓)')
" 2>/dev/null

  echo ""
  echo "=== 成交统计 ==="
  curl -s "$API/fills?limit=100" -H "Authorization: Bearer $TOKEN" | python3 -c "
import sys,json
d=json.load(sys.stdin)
fills = d.get('fills',[])
buys = [f for f in fills if f['Side']=='BUY']
sells = [f for f in fills if f['Side']=='SELL']
total_pnl = sum(f['RealizedPnL'] for f in fills)
total_fee = sum(f['Fee'] for f in fills)
print(f'  总成交: {len(fills)} 笔 (买入 {len(buys)}, 卖出 {len(sells)})')
print(f'  总盈亏: \${total_pnl:.4f}')
print(f'  总手续费: \${total_fee:.4f}')
if sells:
    wins = len([f for f in sells if f['RealizedPnL'] > 0])
    print(f'  胜率: {wins}/{len(sells)} = {wins/len(sells)*100:.1f}%')
" 2>/dev/null
fi

# 日志统计
if [ -n "$LATEST_LOG" ]; then
  echo ""
  echo "=== 日志统计 ==="
  LOG="$LATEST_LOG/api-server.log"
  if [ -f "$LOG" ]; then
    echo "  日志大小: $(du -h "$LOG" | cut -f1)"
    echo "  ERROR数: $(grep -c 'ERROR' "$LOG" 2>/dev/null || echo 0)"
    echo "  WARN数: $(grep -c 'WARN' "$LOG" 2>/dev/null || echo 0)"
    echo "  熔断次数: $(grep -c 'CIRCUIT BREAKER' "$LOG" 2>/dev/null || echo 0)"
    echo "  WS重连: $(grep -c 'reconnecting' "$LOG" 2>/dev/null || echo 0)"
    echo "  成交日志: $(grep -c 'paper fill' "$LOG" 2>/dev/null || echo 0)"
    echo ""
    echo "=== 最新日志 (最后5行) ==="
    tail -5 "$LOG"
  fi
fi
echo ""
