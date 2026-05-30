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

## Quick Start (on nib-sprite)

**Important**: This app is **fully isolated**. It lives in its own directory and uses its own DuckDB file. It will **not** affect your Stitchify or PriorityForge apps on the sprite.

### Running under `/insightforge` on the sprite domain (recommended)

This is the goal — making it available at `https://nib-sprite.../insightforge`.

1. Start the app with the correct base path:

```bash
cd /home/sprite/insight-forge
git pull origin main

BASE_PATH=/insightforge ./run.sh 8091
```

2. Configure routing in **Stitchify** (the current gateway).

   Edit `/home/sprite/stitchify.poc/next.config.ts` and add rewrites:

   ```ts
   import type { NextConfig } from "next";

   const nextConfig: NextConfig = {
     output: "standalone",
     async rewrites() {
       return [
         {
           source: "/insightforge/:path*",
           destination: "http://localhost:8091/:path*",
         },
         {
           source: "/insightforge",
           destination: "http://localhost:8091/",
         },
       ];
     },
   };

   export default nextConfig;
   ```

3. Restart the Next.js process in Stitchify so it picks up the new config.

After that, the app should be reachable at `/insightforge` on the main sprite domain.

### Local testing from your Mac

```bash
./run.sh 8091
```

Then in another terminal:
```bash
sprite proxy 8091
```

Open http://localhost:8091

Recommended safe ports on the shared sprite: **8090–8110** range.

## Key Concepts

- **Entity**: NSN (or NIIN/FSC/CAGE)
- **Extractors**: One per public/government source (WebFLIS, FPDS, MCRL, sanctions lists, etc.)
- **Snapshots**: Immutable rows in DuckDB captured at a point in time
- **Synthesis**: Processing layer produces viability score, risk flags, supplier views, etc.
- **UI**: Reactive server-side workspace powered by Datastar

## License

Proprietary (NIB internal prototype)
