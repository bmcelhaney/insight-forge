# Insight Forge — Architecture

**Current Date**: June 2026  
**Hosting**: Dedicated sprite (`nib-insightforge`)

## Overview

Insight Forge uses a simple, maintainable Go + static frontend architecture optimized for rapid iteration and high-quality analyst output on AbilityOne NSNs.

- **Backend**: Pure Go (`chi` router)
- **Frontend**: Single self-contained `static/index.html` (Tailwind via CDN + vanilla JS + Chart.js)
- **No** reactive frameworks (Datastar/Gomponents), no DuckDB, and no Next.js in the current production version.

## Core Data Flow

1. Client sends NSN (via POST `/api/analyze` or GET `/api/export/json/{nsn}`)
2. `Extractor` registry runs multiple sources in parallel:
   - USAspending (live award aggregates)
   - GSA Advantage (direct JWOD form POST + HTML scrape)
   - PartsBase market-pricing API (live supplier and condition-code price signals)
   - WebFLIS
   - Curated high-fidelity AbilityOne map (strongest signal for many items)
   - OFAC sanctions
3. `Synthesis` engine (`internal/processing/synthesis.go`) combines snapshots into a rich `InsightResult`:
   - Dynamic Sourcing Attractiveness & Supply Risk scores with traffic-light coloring
   - Key Insights
   - Analyst Recommendation (with special handling for AbilityOne items)
   - Full narrative report
   - Supplier ecosystem, demand signals, flags, related NSNs

## Why This Architecture

- Maximum focus on **data quality and analyst usefulness** rather than frontend framework complexity.
- Extremely fast local iteration (edit HTML or Go, refresh).
- Easy to reason about and debug.
- Reliable deployment story via `./scripts/reset.sh` with commit embedding and functional gates.

## Key Components

- `internal/extraction/` — Pluggable data sources implementing the `Extractor` interface
- `internal/processing/synthesis.go` — The heart of the system (score calculation, rich report generation, AbilityOne priority logic)
- `internal/models/` — Core data structures (`InsightResult`, `SupplierView`, `DemandSignals`, etc.)
- `static/index.html` — The entire UI (deliberately kept as one file)

## AbilityOne Handling

A curated map in `internal/extraction/abilityone.go` provides high-fidelity data (producing NPA, CAGE, CID, demand character, risks, etc.) for high-volume AbilityOne items. This data takes priority in the synthesis layer for identity fields, Key Insights, and the Analyst Recommendation when present.

## Deployment

See `DEPLOYMENT.md`. The only supported method is `./scripts/reset.sh`, which enforces version verification and functional testing before starting the binary.

## Future Considerations

- Possible move to containerized deployment (Azure or Fly.io dedicated app)
- Richer Excel export (currently basic)
- Additional live data sources as they become available
