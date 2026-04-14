#!/bin/bash
# Quantix — stop all running instances

set -e

if pgrep -f "quantix-api" > /dev/null 2>&1; then
  echo "Stopping quantix-api..."
  pkill -f "quantix-api" || true
  sleep 2
  # Force kill if still alive
  if pgrep -f "quantix-api" > /dev/null 2>&1; then
    echo "Force killing..."
    pkill -9 -f "quantix-api" || true
    sleep 1
  fi
  echo "Stopped."
else
  echo "quantix-api is not running."
fi
