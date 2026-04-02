#!/bin/bash
# Quantix 实盘重启 — 保留订单/成交历史，只重置 session
set -e
cd "$(dirname "$0")/.."

echo "停止引擎..."
lsof -ti:9300 | xargs kill 2>/dev/null || true
sleep 3
lsof -ti:9300 | xargs kill -9 2>/dev/null || true
sleep 1

# 只重置 session（让引擎重新注册），保留 orders/fills/equity
echo "重置 session（保留订单历史）..."
psql -U quantix -d quantix -c "DELETE FROM engine_sessions WHERE user_id=4;" 2>/dev/null

echo "启动..."
nohup ./scripts/start-quantix.sh > /dev/null 2>&1 &
sleep 6

echo "健康检查..."
curl -sf http://localhost:9300/api/health && echo " OK" || echo " FAIL"

echo ""
echo "注意：引擎需要通过 API 手动启动"
echo "  或等待 auto-restart 自动恢复（如果之前有 active session）"
