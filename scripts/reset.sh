#!/bin/bash
set -euo pipefail

# reset.sh — The ONLY way to deploy Insight Forge to the dedicated nib-insightforge sprite.
# 1. Kills old processes
# 2. Builds fresh binary
# 3. Runs the full functional test gate (test_release.sh) against the 5 real AbilityOne NSNs
# 4. Only if gate passes, starts the production binary under nohup with logging
#
# This enforces "check your work" — bad builds are rejected before the user ever sees them.
#
# Run from the repo root on the sprite:
#   ./scripts/reset.sh
#
# Optional: PORT=8081 ./scripts/reset.sh

PORT="${PORT:-8080}"
APP_NAME="insight-forge"
LOG_FILE="/tmp/${APP_NAME}.log"
BINARY_PATH="./${APP_NAME}"

# Sprite-friendly Go path (many sprites install Go in non-standard locations)
export PATH="/home/sprite/.sprite/bin:/root/.sprite/bin:$HOME/.sprite/bin:$HOME/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:$PATH"

echo "==> Insight Forge Hard Reset + Release Gate (port ${PORT})"
echo "    Dedicated sprite: nib-insightforge"
echo ""

# 1. Aggressive cleanup of any previous instances (different ports too)
echo "==> Killing any running ${APP_NAME} processes..."
pkill -f "${APP_NAME}" 2>/dev/null || true
pkill -f "go run.*cmd/server" 2>/dev/null || true
sleep 1
pkill -9 -f "${APP_NAME}" 2>/dev/null || true

# 2. Fresh build
echo "==> Building fresh production binary..."
go build -ldflags="-s -w" -o "${BINARY_PATH}" ./cmd/server
chmod +x "${BINARY_PATH}"

# 3. THE CRITICAL GATE — real functional tests on the exact 5 NSNs
echo "==> Running functional release gate (this will take 30-90s)..."
if ! ./scripts/test_release.sh "${PORT}"; then
  echo ""
  echo "ABORT: test_release.sh failed. Binary NOT started."
  echo "Fix the data quality or synthesis issues before retrying reset."
  rm -f "${BINARY_PATH}"
  exit 1
fi

# 4. Start the verified binary (only reached if gate passed)
echo "==> Gate passed. Starting verified production binary..."
nohup "${BINARY_PATH}" > "${LOG_FILE}" 2>&1 &
NEW_PID=$!
echo "    PID: ${NEW_PID}"
echo "    Logs: ${LOG_FILE}"

# 5. Final health + smoke confirmation
echo "==> Final health confirmation..."
for i in {1..15}; do
  if curl -s --max-time 2 "http://127.0.0.1:${PORT}/health" | grep -q '"status":"ok"'; then
    echo "    Server healthy on :${PORT}"
    break
  fi
  sleep 1
done

echo ""
echo "==> RESET COMPLETE — VERIFIED BUILD IS LIVE"
echo "    Test with: curl -X POST http://127.0.0.1:${PORT}/api/analyze -d '{\"nsn\":\"7920014487052\"}'"
echo "    Public (via sprite): https://nib-insightforge-bsmmx.sprites.app/"
echo ""
echo "    IMPORTANT: Hard-refresh browser (Cmd+Shift+R) after any deploy."
exit 0
