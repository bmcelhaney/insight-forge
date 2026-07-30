#!/bin/bash
set -euo pipefail

# reset.sh — The ONLY supported way to deploy Insight Forge on nib-insightforge.
#
# This version is deliberately paranoid because previous deployment methods
# repeatedly left old binaries running.
#
# Key behavior:
#   - Always fetches origin/main and hard-resets the tree to it first
#     (so the "Target commit" is never stuck on an old local checkout)
#   - Kills every possible old process aggressively
#   - Removes known stale binary locations
#   - Builds with the target commit embedded
#   - Runs the full functional gate
#   - Verifies that the live /version exactly matches the commit just built
#   - Only declares success if the verification passes

PORT="${PORT:-8080}"
APP_NAME="insight-forge"
LOG_FILE="/tmp/${APP_NAME}.log"
BINARY_PATH="./${APP_NAME}"

# Make sure we can find Go on the sprite
export PATH="/home/sprite/.sprite/bin:/root/.sprite/bin:$HOME/.sprite/bin:$HOME/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:$PATH"

# Always target the absolute latest on origin/main, regardless of local checkout state
echo "==> Fetching latest from origin..."
git fetch origin

TARGET_COMMIT=$(git rev-parse --short origin/main 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "==> Insight Forge HARDENED Reset + Version-Verified Gate (port ${PORT})"
echo "    Target commit: ${TARGET_COMMIT}"
echo "    Dedicated sprite: nib-insightforge"
echo ""

# Move the working tree to exactly the target commit before building
echo "==> Resetting to ${TARGET_COMMIT}..."
git reset --hard origin/main
git clean -fd

COMMIT=${TARGET_COMMIT}

# 1. Extremely aggressive cleanup of every possible old instance
echo "==> Killing every possible old insight-forge / insightforge process..."
pkill -9 -f "insight-forge" 2>/dev/null || true
pkill -9 -f "insightforge" 2>/dev/null || true
pkill -9 -f "go run.*cmd/server" 2>/dev/null || true
pkill -9 -f "/home/sprite/insightforge/bin/insightforge" 2>/dev/null || true

# Extra aggressive kill for the legacy binary name/location
killall -9 insightforge 2>/dev/null || true
pkill -9 -f "insightforge" 2>/dev/null || true

# Kill anything listening on the target port (more reliable than pkill alone)
if command -v fuser >/dev/null 2>&1; then
    fuser -k -9 ${PORT}/tcp 2>/dev/null || true
elif command -v lsof >/dev/null 2>&1; then
    lsof -ti:${PORT} | xargs kill -9 2>/dev/null || true
fi

sleep 2

# Remove known old binary locations that have caused "old code still running" problems
echo "==> Removing known stale binary locations..."
rm -f /home/sprite/insightforge/bin/insightforge 2>/dev/null || true
rm -rf /home/sprite/insightforge/bin/ 2>/dev/null || true
rm -f ./insightforge 2>/dev/null || true
rm -f ./insight-forge 2>/dev/null || true

# 2. Ensure dependencies are resolved (critical after git clean -fd or fresh clones)
echo "==> Ensuring go modules are tidy (fixes missing go.sum after clean)..."
go mod tidy || echo "Warning: go mod tidy had issues, attempting build anyway..."

# 3. Build with commit embedded so we can prove what is running
echo "==> Building with embedded commit ${COMMIT}..."
go build -ldflags="-s -w -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o "${BINARY_PATH}" ./cmd/server
chmod +x "${BINARY_PATH}"

# 3. Run the functional gate (tests real data quality on your 5 NSNs)
echo "==> Running functional release gate..."
if ! ./scripts/test_release.sh "${PORT}"; then
    echo ""
    echo "ABORT: Functional gate failed. Old binary (if any) was killed. No new binary started."
    rm -f "${BINARY_PATH}"
    exit 1
fi

# 4. Start the new binary
echo "==> Gate passed. Starting new binary..."
# Always start from the repo root so .env.partsbase / static / migrations resolve.
# (Do not `source` .env.partsbase in shell — passwords may contain & and other metacharacters;
#  the Go config loader reads the dotenv file safely.)
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
nohup "${BINARY_PATH}" > "${LOG_FILE}" 2>&1 &
NEW_PID=$!
echo "    PID: ${NEW_PID}"
echo "    Logs: ${LOG_FILE}"
echo "    CWD:  $(pwd)"

# 5. Wait for health
echo "==> Waiting for health..."
for i in {1..20}; do
    if curl -s --max-time 2 "http://127.0.0.1:${PORT}/health" | grep -q '"status":"ok"'; then
        echo "    Health OK"
        break
    fi
    sleep 1
    if [ $i -eq 20 ]; then
        echo "FAIL: Health never came up"
        kill $NEW_PID 2>/dev/null || true
        exit 1
    fi
done

# 6. CRITICAL: Verify the live version matches the commit we just built
echo "==> Verifying live version matches build commit ${COMMIT}..."
for i in {1..10}; do
    LIVE_COMMIT=$(curl -s --max-time 3 "http://127.0.0.1:${PORT}/version" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get("commit", ""))
except:
    print("")
' 2>/dev/null || echo "")

    if [ "$LIVE_COMMIT" = "$COMMIT" ]; then
        echo "    ✓ Live commit matches: ${LIVE_COMMIT}"
        break
    fi

    if [ $i -eq 10 ]; then
        echo ""
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
        echo "DEPLOYMENT FAILED VERIFICATION"
        echo "We built commit ${COMMIT} but /version is reporting '${LIVE_COMMIT}'"
        echo "An old binary is still serving traffic."
        echo "Killing the process we just started..."
        echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
        kill $NEW_PID 2>/dev/null || true
        exit 1
    fi
    sleep 2
done

echo ""
echo "==> RESET COMPLETE — VERIFIED BUILD IS LIVE"
echo "    Commit: ${COMMIT}"
echo "    Test:   curl -X POST http://127.0.0.1:${PORT}/api/analyze -d '{\"nsn\":\"7920014487052\"}'"
echo "    Public: https://nib-insightforge-bsmmx.sprites.app/"
echo ""
echo "    IMPORTANT: Hard-refresh (Cmd+Shift+R) and check the commit shown in the top-right of the UI."
exit 0
