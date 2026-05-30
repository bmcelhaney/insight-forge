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

## Quick Start (Dedicated Sprite - Recommended)

You now have a **dedicated sprite** called `nib-insightforge` (public URL: https://nib-insightforge-bsmmx.sprites.app).

This is the cleanest and recommended way to run Insight Forge.

### On the new sprite (nib-insightforge)

Enter the sprite:

```bash
sprite console -o bill-nib -s nib-insightforge
```

Inside the sprite console:

```bash
cd /home/sprite
git clone https://<YOUR_GITHUB_PAT>@github.com/bmcelhaney/insight-forge.git
cd insight-forge
git pull origin main

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
