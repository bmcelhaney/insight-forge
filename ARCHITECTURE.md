# Insight Forge — Architecture

**Date**: 2026-05-29  
**Baseline**: Go Reactive Web App Framework v2.2 (from Stitchify.poc/FRAMEWORK.md)  
**Hosting (Prototype Phase)**: nib-sprite (Linux on Fly.io)  
**Future**: Containerized deployment on Azure

## Guiding Principle

We follow the strict architecture, directory structure, coding standards, and patterns defined in `Stitchify.poc/FRAMEWORK.md` unless explicitly noted here.

All deviations must be recorded in this file with rationale.

## Domain Adaptation (NSN Intelligence)

| Framework Concept | Insight Forge Mapping |
|-------------------|-----------------------|
| Entity            | NSN (13-digit), also supports partial NIIN / FSC / CAGE queries |
| Data Sources      | WebFLIS / PUB LOG, FPDS, MCRL, SAM.gov, sanctions lists (OFAC, etc.), supplier registries, historical award data |
| Snapshots         | Immutable `data_snapshots` rows with `raw_response` JSON + quality metadata |
| Processed Results | `processed_results` containing viability_score (0-100), risk_score (0-100), flags, supplier concentration, related NSN graph data, demand signals |
| Export Payload    | Structured JSON for the fair-market pricing tool + full evidence bundle (Excel/PDF) |

## Directory Structure

Follows the framework exactly (see `Stitchify.poc/FRAMEWORK.md` §3.1).

Key packages for this domain:
- `internal/extraction/` — one file per source (webflis.go, fpds.go, sanctions.go, ...)
- `internal/processing/synthesis.go` — core viability + risk engine
- `internal/models/nsn.go`, `snapshot.go`, `insight.go`

## DuckDB Schema Notes

We start with the framework's starter template (`migrations/001_init.sql`) and extend it for NSN-specific fields (technical characteristics, packaging, unit of issue, etc.).

All schema changes go through numbered migrations.

## UI Approach (MVP)

- Reactive workspace using Datastar + Gomponents (no full page reloads)
- Left: NSN search + recent analyses
- Center: Source table + go-echarts visualizations (volume trends, supplier distribution, risk timeline)
- Right: Insight card (viability, risk heatmap, top flags, "Send to Pricing Tool")

## Extractor Contract

All extractors implement:

```go
type Extractor interface {
    Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error)
    SourceCode() string
}
```

## Current Status

**Strongly isolated from other apps on the sprite.**

- Directory: `/home/sprite/insight-forge` (separate from `stitchify.poc` and PriorityForge)
- Database: `./data/insight-forge.duckdb` (completely independent DuckDB file)
- Port: Configurable via `IF_PORT` (default 8080 — use a different port on the shared sprite)
- No shared processes, static files, or dependencies with Stitchify or PriorityForge

## Implementation Progress

- Full domain models + synthesis engine (viability + risk scoring)
- Parallel extractors (WebFLIS, FPDS, Sanctions) with realistic data
- Reactive Datastar + Gomponents workspace (sidebar + center + insight card)
- History / recent analyses
- JSON export for pricing tool
- Follows Stitchify Go Framework structure and patterns exactly

## Running Safely on Shared nib-sprite

To avoid any conflict with Stitchify or PriorityForge:

```bash
# Run on a non-conflicting port (example)
IF_PORT=8091 go run ./cmd/server

# Or with explicit data path
IF_PORT=8091 IF_DUCKDB_PATH=/home/sprite/insight-forge/data/insight-forge.duckdb go run ./cmd/server
```

Later this will become its own Fly app (or Azure container) as planned.

## Deviations from Framework (if any)

None yet.

## References

- Stitchify Go Framework: `Stitchify.poc/FRAMEWORK.md`
- Original Insight Forge requirements: original spec (functional flows, scoring, export)
- Deployment target (prototype): nib-sprite
