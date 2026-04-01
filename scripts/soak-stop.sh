#!/bin/bash
# Quantix 浸泡测试 - 停止脚本
set -e
cd "$(dirname "$0")/.."

echo "停止 Quantix..."

# 找到最新的日志目录
LATEST_LOG=$(ls -dt logs/soak-* 2>/dev/null | head -1)

if [ -f "$LATEST_LOG/api.pid" ]; then
  PID=$(cat "$LATEST_LOG/api.pid")
  if kill -0 "$PID" 2>/dev/null; then
    kill "$PID"
    echo "API Server (PID $PID) 已发送 SIGTERM（优雅关闭）"
    # 等待最多15秒
    for i in $(seq 1 15); do
      if ! kill -0 "$PID" 2>/dev/null; then
        echo "进程已退出"
        break
      fi
      sleep 1
    done
    if kill -0 "$PID" 2>/dev/null; then
      kill -9 "$PID"
      echo "强制杀死进程"
    fi
  else
    echo "PID $PID 已不存在"
  fi
fi

# 兜底清理
lsof -ti:9300 | xargs kill -9 2>/dev/null || true

echo "Quantix 已停止"
echo ""
if [ -n "$LATEST_LOG" ]; then
  echo "日志目录: $LATEST_LOG"
  echo "查看日志: less $LATEST_LOG/api-server.log"
fi
