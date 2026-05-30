#!/usr/bin/env bash
#
# Insight Forge - Runner
#
# On the dedicated sprite (nib-insightforge):
#   ./run.sh 8080
#
# On a shared sprite (not recommended anymore):
#   BASE_PATH=/insightforge ./run.sh 8091
#
# Usage:
#   ./run.sh        # defaults to port 8091
#   ./run.sh 8080   # specific port
#
# Isolation: Own directory + own DuckDB file.

set -e

# Default safe port (different from typical Stitchify/PriorityForge ports)
PORT=${1:-8091}

# Ensure we're in the right directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================="
echo "  Insight Forge - Sprite Prototype"
echo "=========================================="
echo ""
echo "Running in: $(pwd)"
echo "Using port: $PORT"
echo ""
echo "Isolation notes:"
echo "  • Own directory: /home/sprite/insight-forge"
echo "  • Own database:  ./data/insight-forge.duckdb"
echo "  • Will NOT affect Stitchify or PriorityForge"
echo ""
echo "After starting, you have two options:"
echo ""
echo "  A) For local testing from your Mac:"
echo "     sprite proxy $PORT"
echo "     Then open http://localhost:$PORT"
echo ""
echo "  B) For production-like access on the sprite domain (recommended):"
echo "     Set up rewrites in Stitchify's next.config.ts (see docs)"
echo "     Then access via https://nib-sprite.../insightforge"
echo ""
echo "Press Ctrl+C to stop."
echo ""

# Create data dir if it doesn't exist
mkdir -p data

# Run with explicit environment for clarity
# Set BASE_PATH if you want the app mounted under a subpath on the sprite
# (e.g. so it appears at https://nib-sprite.../insightforge)
BASE_PATH=${BASE_PATH:-""}

IF_PORT=$PORT \
IF_DUCKDB_PATH=./data/insight-forge.duckdb \
IF_BASE_PATH=$BASE_PATH \
IF_ENV=prototype \
go run ./cmd/server
