#!/usr/bin/env bash
#
# Insight Forge - Easy runner for the nib-sprite prototype
#
# This script makes it simple to run Insight Forge on the shared sprite
# without conflicting with Stitchify or PriorityForge.
#
# Usage:
#   ./run.sh                 # runs on port 8091 (recommended safe port)
#   ./run.sh 8100            # runs on a custom port
#
# Isolation guarantees:
# - Runs from its own directory (/home/sprite/insight-forge)
# - Uses its own DuckDB file (./data/insight-forge.duckdb)
# - No shared state with other apps on the sprite

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
echo "After starting, expose it with:"
echo "  sprite proxy $PORT"
echo ""
echo "Press Ctrl+C to stop."
echo ""

# Create data dir if it doesn't exist
mkdir -p data

# Run with explicit environment for clarity
IF_PORT=$PORT \
IF_DUCKDB_PATH=./data/insight-forge.duckdb \
IF_ENV=prototype \
go run ./cmd/server
