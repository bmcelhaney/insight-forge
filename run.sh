#!/usr/bin/env bash
#
# Insight Forge — Dedicated Sprite Runner (nib-insightforge)
#
# On your new dedicated sprite:
#   cd /home/sprite/insight-forge
#   chmod +x run.sh
#   ./run.sh 8080
#
# Or with explicit nohup for background:
#   nohup ./run.sh 8080 > insightforge.log 2>&1 &
#
# The app now serves a complete beautiful UI at the root (or under BASE_PATH if set).
# No more Stitchify proxy hell. Own domain / own port / own DuckDB.

set -e

PORT=${1:-8080}
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================="
echo "  INSIGHT FORGE — DEDICATED SPRITE"
echo "=========================================="
echo "Directory : $(pwd)"
echo "Port      : $PORT"
echo "DuckDB    : ./data/insight-forge.duckdb"
echo ""
echo "After it starts:"
echo "  curl http://localhost:$PORT/health"
echo "  Then open the public URL for your sprite on port $PORT"
echo ""
echo "Test NSNs: 1234567890123   or   1234567890001"
echo "Press Ctrl+C to stop."
echo "=========================================="

mkdir -p data

# Make sure the weird sprite Go location is in PATH
export PATH="/.sprite/bin:$PATH"

BASE_PATH=${BASE_PATH:-""}

IF_PORT=$PORT \
IF_DUCKDB_PATH=./data/insight-forge.duckdb \
IF_BASE_PATH=$BASE_PATH \
IF_ENV=production \
go run ./cmd/server
