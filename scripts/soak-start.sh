#!/bin/bash
# Quantix 长时间运行测试 - 启动脚本
# 用法: ./scripts/soak-start.sh
# 停止: ./scripts/soak-stop.sh

set -e
cd "$(dirname "$0")/.."
PROJECT_DIR=$(pwd)
LOG_DIR="$PROJECT_DIR/logs/soak-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$LOG_DIR"

# 环境变量
export QUANTIX_ENCRYPTION_KEY=b16f993bf0b8c2695bd9773ca9b24d060ea78182d884c3f4056fd80e4e021743
export QUANTIX_JWT_SECRET=61ea018d43c6c953b4978606778107beb341e6e56b8b9e7b21df252897b0e55d
export QUANTIX_LIVE_CONFIRM=true
export QUANTIX_API_ADDR=:9300

# 杀掉旧进程
lsof -ti:9300 | xargs kill -9 2>/dev/null || true
sleep 2

# 编译
echo "编译..."
go build -o bin/quantix-api ./cmd/api

# 启动 API Server
echo "启动 API Server (port 9300)..."
nohup "$PROJECT_DIR/bin/quantix-api" -config config/config.yaml \
  > "$LOG_DIR/api-server.log" 2>&1 &
API_PID=$!
echo $API_PID > "$LOG_DIR/api.pid"
echo "API PID: $API_PID"

sleep 5

# 健康检查
if ! curl -sf http://localhost:9300/api/health > /dev/null; then
  echo "ERROR: API server failed to start. Check $LOG_DIR/api-server.log"
  exit 1
fi
echo "API Server: healthy"

# 登录
API=http://localhost:9300/api
TOKEN=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"stresstest","password":"StressTest123!"}' | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
echo "Token: OK"

# 启动 Paper 引擎
echo "启动 Paper 引擎 (macross, BTCUSDT, 1m)..."
RESULT=$(curl -s --max-time 60 -X POST $API/engine/start \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "credential_id":1,
    "strategy_id":"macross",
    "symbol":"BTCUSDT",
    "interval":"1m",
    "mode":"paper",
    "params":{"FastPeriod":5,"SlowPeriod":15},
    "risk":{"max_position_pct":1.0,"max_drawdown_pct":0.30,"max_single_loss_pct":1.0},
    "paper":{"initial_capital":10000,"fee_rate":0.001,"slippage":0.0005}
  }')
echo "Engine: $RESULT"

# 记录启动信息
cat > "$LOG_DIR/info.txt" << EOF
Quantix 浸泡测试
启动时间: $(date)
API 端口: 9300
Prometheus: 9301
API PID: $API_PID
日志目录: $LOG_DIR
策略: macross (FastPeriod=5, SlowPeriod=15)
交易对: BTCUSDT
K线周期: 1m
模式: Paper (Demo Mode)
初始资金: 10000 USDT

查看日志: tail -f $LOG_DIR/api-server.log
查看状态: curl -s http://localhost:9300/api/positions -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
停止服务: ./scripts/soak-stop.sh
EOF

echo ""
echo "========================================="
echo "  Quantix 浸泡测试已启动"
echo "  日志: $LOG_DIR/api-server.log"
echo "  状态: curl http://localhost:9300/api/health"
echo "  停止: ./scripts/soak-stop.sh"
echo "========================================="
