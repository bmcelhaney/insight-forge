# Insight Forge

NSN intelligence web application — deep multi-source analysis, viability & risk synthesis, and structured export to fair-market pricing tools.

**Current Architecture**: Pure Go backend (chi) + static single-file HTML frontend (Tailwind + vanilla JavaScript). No reactive frameworks or external databases in the current version.

**Primary Repo**: https://github.com/bmcelhaney/insight-forge  
**Live Instance**: https://nib-insightforge-bsmmx.sprites.app/

## Status (June 2026)

High-fidelity prototype actively used for NIB analyst work.

- Clean, fast UI with dynamic score coloring (green/amber/red) for both Sourcing Attractiveness and Supply Risk
- Strong Analyst Recommendation and Key Insights powered by curated AbilityOne data + live sources
- Primary API (`POST /api/analyze`) returns the data-capture hit inventory (not narrative analysis)
- Two JSON export modes:
  - Data capture: same schema as `/api/analyze` (`/api/export/data/{nsn}`, `insight-forge.data-capture.v1` / v1.1). Atomic `unit_price` + `quantity`.
  - Pricing tool: full InsightResult (`/api/export/json/{nsn}`)
- Hardened deployment with commit embedding and functional gates

## Quick Start (Dedicated Sprite)

```bash
cd ~/insight-forge
./scripts/reset.sh
```

Hard-refresh the browser after deployment.

See `DEPLOYMENT.md` for the full hardened process.

## Local Development

```bash
go mod tidy
go run ./cmd/server
```

Open http://localhost:8080

## Architecture

- Pure Go backend using `chi`
- Static single-file frontend (`static/index.html`) with Tailwind CDN + vanilla JavaScript + Chart.js
- Parallel extractors pulling from USAspending, GSA Advantage (direct scrape), WebFLIS, PartsBase market-pricing data, a curated high-fidelity AbilityOne map, and the AbilityOne ETS spreadsheet cross-reference (`docs/20260701 AbilityOne ETS File.xlsx`) for SKU/UPC/manufacturer enrichment
- Central synthesis engine that produces:
  - Dynamic Sourcing Attractiveness & Supply Risk scores (with traffic-light coloring)
  - Key Insights
  - Analyst Recommendation
  - Full narrative report
- Clean APIs for external tools:
  - `POST /api/analyze` — **primary machine API**: data-capture document only (`insight-forge.data-capture.v1` / v1.1)
  - `GET /api/export/data/{nsn}` — **identical JSON body** to `POST /api/analyze` (file download; also used by UI “Export JSON (Data Capture)”)
  - `POST /api/insight` — full InsightResult + embedded `data_capture` (web UI / pricing consumers)
  - `GET /api/export/json/{nsn}` — full InsightResult download for pricing tools

Data Capture is always built by `BuildDataCaptureDocument` on the server — there is no separate client-side schema.

See `ARCHITECTURE.md` for more details on the extractor + synthesis model.

## License

Proprietary (NIB internal prototype)
