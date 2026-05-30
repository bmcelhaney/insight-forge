# Insight Forge

NSN intelligence web application — deep multi-source analysis, viability & risk synthesis, and structured export to fair-market pricing tools.

**Architecture**: Follows the [Go Reactive Web App Framework](https://github.com/bmcelhaney/Stitchify.poc/blob/main/FRAMEWORK.md) (Datastar + Gomponents + DuckDB + chi).

**Primary Repo**: https://github.com/bmcelhaney/insight-forge  
**Hosting (prototype)**: Running on `nib-sprite` (bill-nib org)

## Status

Scaffolding in progress. See `ARCHITECTURE.md` for current decisions and how this maps to the Stitchify Go Framework.

## Quick Start (Local)

```bash
go mod tidy
go run ./cmd/server
```

Open http://localhost:8080

## Quick Start (on nib-sprite)

**Important**: This app is fully isolated. It lives in its own directory and uses its own DuckDB file. It will not affect your Stitchify or PriorityForge apps.

```bash
git clone https://github.com/bmcelhaney/insight-forge.git /home/sprite/insight-forge
cd /home/sprite/insight-forge

# Run on a free port so it doesn't conflict with other apps on the sprite
IF_PORT=8091 go run ./cmd/server
```

Then expose it with something like:
```bash
sprite proxy 8091
```

Recommended ports for this prototype on the shared sprite: 8090–8100 range.

## Key Concepts

- **Entity**: NSN (or NIIN/FSC/CAGE)
- **Extractors**: One per public/government source (WebFLIS, FPDS, MCRL, sanctions lists, etc.)
- **Snapshots**: Immutable rows in DuckDB captured at a point in time
- **Synthesis**: Processing layer produces viability score, risk flags, supplier views, etc.
- **UI**: Reactive server-side workspace powered by Datastar

## License

Proprietary (NIB internal prototype)
