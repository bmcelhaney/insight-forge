# Deploying Insight Forge on nib-sprite under /insightforge

This document explains the current recommended way to expose Insight Forge at a subpath on the main sprite domain.

## Architecture Context

The `nib-sprite` Fly app currently uses **Stitchify’s Next.js** (on internal port 3000) as the public gateway. Other services inside the same machine are reached either:

- Via small proxy functions inside Stitchify (e.g. Rust API)
- By direct calls from the frontend to `localhost:xxxx` (e.g. PriorityForge on 4000)

## Recommended Approach: Next.js Rewrites (Simplest)

Add the following to Stitchify’s `next.config.ts`:

```ts
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
}
```

Then run Insight Forge with:

```bash
BASE_PATH=/insightforge IF_PORT=8091 ./run.sh
```

Restart Stitchify’s Next.js process.

This is the cleanest method and matches how many multi-service setups on a single Fly machine work.

## More Robust Alternative: Catch-all Proxy Route

If rewrites cause issues with Server-Sent Events (Datastar) or certain headers, you can instead create a catch-all proxy inside Stitchify.

Create this file in Stitchify:

`src/app/api/insightforge/[...path]/route.ts`

```ts
import { NextRequest } from "next/server";

const INSIGHT_FORGE_URL = process.env.INSIGHT_FORGE_URL || "http://localhost:8091";

export async function GET(req: NextRequest) {
  return proxyToInsightForge(req);
}

export async function POST(req: NextRequest) {
  return proxyToInsightForge(req);
}

export async function PUT(req: NextRequest) {
  return proxyToInsightForge(req);
}

export async function DELETE(req: NextRequest) {
  return proxyToInsightForge(req);
}

async function proxyToInsightForge(req: NextRequest) {
  const path = req.nextUrl.pathname.replace("/api/insightforge", "");
  const url = new URL(path + req.nextUrl.search, INSIGHT_FORGE_URL);

  const headers = new Headers(req.headers);
  headers.set("host", new URL(INSIGHT_FORGE_URL).host);

  const response = await fetch(url, {
    method: req.method,
    headers,
    body: req.method !== "GET" && req.method !== "HEAD" ? await req.arrayBuffer() : undefined,
    duplex: "half",
  } as any);

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}
```

Then in `next.config.ts` you can add a rewrite:

```ts
{
  source: "/insightforge/:path*",
  destination: "/api/insightforge/:path*",
}
```

This gives you more control (you can add auth, logging, etc. later).

## Environment Variables

When running Insight Forge on the sprite:

```bash
BASE_PATH=/insightforge
IF_PORT=8091
IF_DUCKDB_PATH=/home/sprite/insight-forge/data/insight-forge.duckdb
```

## Future Direction

Once the prototype is validated, the plan is to move Insight Forge to its own Fly app (or Azure container) instead of living under the shared sprite.
