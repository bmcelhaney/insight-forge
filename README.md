# Insight Forge

NSN intelligence web application — deep multi-source analysis, viability & risk synthesis, and structured export to fair-market pricing tools.

**Architecture**: Follows the [Go Reactive Web App Framework](https://github.com/bmcelhaney/Stitchify.poc/blob/main/FRAMEWORK.md) (Datastar + Gomponents + DuckDB + chi).

**Primary Repo**: https://github.com/bmcelhaney/insight-forge  
**Hosting (prototype)**: Running on `nib-sprite` (bill-nib org)

## Status (May 2026)

**High-fidelity prototype** — actively developed.

- Live reactive workspace with partial progress updates as sources complete
- go-echarts visualizations
- Full multi-sheet Excel evidence bundle + JSON export for pricing tool
- Designed to run safely alongside Stitchify and PriorityForge on the same nib-sprite

See `ARCHITECTURE.md` for architecture decisions and isolation details.

## Quick Start (Local)

```bash
go mod tidy
go run ./cmd/server
```

Open http://localhost:8080

## Quick Start (Dedicated Sprite — Recommended)

You have a **dedicated sprite** `nib-insightforge` (public: https://nib-insightforge-bsmmx.sprites.app/).

**Production rule**: Use ONLY `./scripts/reset.sh`. It runs a strict functional gate against the 5 real AbilityOne NSNs before ever starting the binary. Bad builds are rejected early.

### On the sprite (one clean block)

```bash
cd /home/sprite
git clone https://<YOUR_GITHUB_PAT>@github.com/bmcelhaney/insight-forge.git || (cd insight-forge && git pull origin main)
cd insight-forge
git pull origin main
./scripts/reset.sh
```

The script will:
- Kill old processes
- Build
- Run `test_release.sh` (real POSTs for 7920014487052, 7520009357136, 8105015171352, 7125011515435, 5180006507821)
- Only start the binary if all 5 produce substantial, non-generic Full Analyst Reports

Hard-refresh the browser (⌘⇧R) after every deploy. The UI is now a single self-contained file with zero Tailwind CDN (eliminates the production console warning).

See `scripts/reset.sh` and `scripts/test_release.sh` for the exact gates.

```bash
# Health check
curl http://localhost:8080/health

# The app is now serving the full beautiful UI at your sprite's public URL on port 8080
```

Test with NSNs: `1234567890123` and `1234567890001`

**Exports**: After any analysis the "Export JSON (Pricing Tool)" and "Excel Evidence Bundle (5 sheets)" buttons produce real downloadable artifacts.

## Local Quick Start

```bash
go mod tidy
go run ./cmd/server
```

Open http://localhost:8080

## Architecture

- Pure Go + chi
- DuckDB as immutable single source of truth (snapshots + results + audit)
- Parallel extractors (WebFLIS, FPDS, Sanctions, extensible)
- Real synthesis engine (viability 0-100 + risk 0-100 + flags + demand + supplier view)
- Beautiful self-contained UI (Tailwind + DaisyUI + Chart.js, no fragile reactive deps)
- One-click JSON for pricing tools + full multi-sheet Excel evidence bundle
- Designed for dedicated sprite isolation (or later Azure containerization)

# Run it (dedicated sprite - clean and simple)
./run.sh 8080
```

From your Mac, expose it:

```bash
sprite proxy 8080
```

Then open: http://localhost:8080

You can change the port with `./run.sh 3000` etc.

### Local testing (from your Mac)

```bash
cd /Users/bill/insight-forge
./run.sh 8080
```

Then in another terminal:
```bash
sprite proxy 8080
```

Open http://localhost:8080

**Note**: On a dedicated sprite you generally do **not** need `BASE_PATH` unless you specifically want it mounted under a subpath.

Recommended safe ports on the shared sprite: **8090–8110** range.

## Key Concepts

- **Entity**: NSN (or NIIN/FSC/CAGE)
- **Extractors**: One per public/government source (WebFLIS, FPDS, MCRL, sanctions lists, etc.)
- **Snapshots**: Immutable rows in DuckDB captured at a point in time
- **Synthesis**: Processing layer produces viability score, risk flags, supplier views, etc.
- **UI**: Reactive server-side workspace powered by Datastar

## License

Proprietary (NIB internal prototype)
