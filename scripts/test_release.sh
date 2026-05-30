#!/bin/bash
set -euo pipefail

# test_release.sh — Functional gate for Insight Forge releases
# Performs real POST /api/analyze against the 5 canonical AbilityOne NSNs.
# Fails (non-zero exit) if any NSN produces thin/generic output.
# This script is called by reset.sh before declaring a deploy good.
#
# Usage:
#   ./scripts/test_release.sh [port]
#   PORT=8080 ./scripts/test_release.sh

PORT="${1:-${PORT:-8080}}"
BASE="http://127.0.0.1:${PORT}"
TIMEOUT=45
BINARY="./insight-forge-testbin"

# Sprite-friendly Go path (many sprites install Go in non-standard locations)
export PATH="/home/sprite/.sprite/bin:/root/.sprite/bin:$HOME/.sprite/bin:$HOME/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:$PATH"

echo "==> Insight Forge Release Gate (test_release.sh)"
echo "    Target: ${BASE}"
echo "    Testing the 5 canonical AbilityOne NSNs..."

# 1. Build a temporary binary (ensures we test exactly what will ship)
echo "==> Building production binary for functional test..."
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
go build -ldflags="-s -w -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" -o "$BINARY" ./cmd/server
chmod +x "$BINARY"

# 2. Start server on the test port (isolated)
echo "==> Starting isolated test server on :${PORT}..."
"$BINARY" --port "$PORT" > /tmp/insightforge-test.log 2>&1 &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null || true; rm -f "$BINARY"' EXIT

# 3. Wait for health (patient, up to TIMEOUT seconds)
echo "==> Waiting for server health..."
for i in $(seq 1 $TIMEOUT); do
  if curl -s --max-time 2 "${BASE}/health" | grep -q '"status":"ok"'; then
    echo "    Health OK after ${i}s"
    break
  fi
  sleep 1
  if [ $i -eq $TIMEOUT ]; then
    echo "FAIL: Server did not become healthy within ${TIMEOUT}s"
    cat /tmp/insightforge-test.log || true
    exit 1
  fi
done

# 4. The exact 5 NSNs the analyst team validates against
NSNS=(
  "7920014487052"
  "7520009357136"
  "8105015171352"
  "7125011515435"
  "5180006507821"
)

FAIL=0

for NSN in "${NSNS[@]}"; do
  echo "==> Testing NSN ${NSN}..."
  RESP=$(curl -s --max-time 30 -X POST "${BASE}/api/analyze" \
    -H "Content-Type: application/json" \
    -d "{\"nsn\":\"${NSN}\"}" || echo '{"error":"curl failed"}')

  # Basic HTTP-level sanity (the curl succeeded and we got JSON)
  if ! echo "$RESP" | grep -q '"result"'; then
    echo "  FAIL: No 'result' object returned for ${NSN}"
    echo "  Response head: $(echo "$RESP" | head -c 300)"
    FAIL=1
    continue
  fi

  # Extract key fields (very tolerant jq-free parsing for CI sprite environments)
  SUMMARY=$(echo "$RESP" | python3 -c '
import sys, json
try:
  d = json.load(sys.stdin)
  r = d.get("result", d)
  print(r.get("summary") or r.get("Summary") or "")
except: print("")
' 2>/dev/null || echo "")

  REPORT=$(echo "$RESP" | python3 -c '
import sys, json
try:
  d = json.load(sys.stdin)
  r = d.get("result", d)
  print(r.get("full_analyst_report") or r.get("FullAnalystReport") or r.get("market_commentary") or "")
except: print("")
' 2>/dev/null || echo "")

  ATTRACT=$(echo "$RESP" | python3 -c '
import sys, json
try:
  d = json.load(sys.stdin)
  r = d.get("result", d)
  print(r.get("sourcing_attractiveness") or r.get("viability_score") or 0)
except: print("0")
' 2>/dev/null || echo "0")

  # Gate 1: Summary must be substantial (AbilityOne cases produce 180+ chars)
  if [ ${#SUMMARY} -lt 140 ]; then
    echo "  FAIL: Summary too short (${#SUMMARY} chars) for ${NSN}"
    echo "  Got: ${SUMMARY:0:120}..."
    FAIL=1
  fi

  # Gate 2: Full Analyst Report must exist and be long (the key anti-generic requirement)
  if [ ${#REPORT} -lt 320 ]; then
    echo "  FAIL: Full Analyst Report too thin (${#REPORT} chars) for ${NSN} — generic data detected"
    FAIL=1
  fi

  # Gate 3: Must have a numeric sourcing score
  if ! echo "$ATTRACT" | grep -qE '^[0-9]+(\.[0-9]+)?$'; then
    echo "  FAIL: No valid sourcing attractiveness score for ${NSN}"
    FAIL=1
  fi

  if [ $FAIL -eq 0 ]; then
    echo "  PASS: ${NSN} — summary ${#SUMMARY} chars, report ${#REPORT} chars, score ${ATTRACT}"
  fi
done

# 5. Final verdict
if [ $FAIL -ne 0 ]; then
  echo ""
  echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
  echo "RELEASE GATE FAILED — one or more NSNs produced insufficient data."
  echo "Do NOT ship this build. Fix synthesis or extractors first."
  echo "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
  exit 1
fi

echo ""
echo "==> All 5 AbilityOne NSNs passed functional gates."

# Final sanity: confirm the test binary is reporting the commit we built
LIVE=$(curl -s --max-time 3 "${BASE}/version" | python3 -c '
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get("commit", ""))
except:
    print("")
' 2>/dev/null || echo "")

if [ "$LIVE" != "$COMMIT" ] && [ -n "$LIVE" ]; then
    echo "WARNING: Test binary reports commit '${LIVE}' but we built '${COMMIT}'"
fi

echo "==> Release is cleared for deployment."
exit 0
