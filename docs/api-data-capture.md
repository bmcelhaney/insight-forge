# Insight Forge API — JSON payload guide (for developers)

**Stable machine contract:** `insight-forge.data-capture.v1` (version **1.3**)  
**Primary integration endpoint:** `POST /api/analyze`  
**UI / pricing-tool envelope:** `POST /api/insight`

**1.2:** each hit has at most **one** primary evidence URL (`links.url` + `links.url_kind`).  
**1.3:** `analysis_id` + optional `proof.screenshot` (Tigris object for visual pricing proof of `links.url`).

### Screenshot-related payload fields (1.3 + recent)

| Field | Where | Notes |
|---|---|---|
| `analysis_id` | document root | Correlates the run + Tigris object keys |
| `hits[].proof.screenshot` | on eligible hits only | Present when `capture_screenshots: true` |
| `hits[].proof.screenshot.status` | | `pending` → `ready` \| `failed` |
| `hits[].proof.screenshot.kind` | | `page_screenshot` (full page) or `product_image` (bot-wall fallback) |
| `hits[].proof.screenshot.url` | | **Presigned Tigris GET URL** when `status=ready` (~1h TTL) |
| `hits[].proof.screenshot.object_key` | | Durable Tigris key (re-presign later) |
| `hits[].proof.screenshot.bucket` | | Tigris bucket name |
| `hits[].attributes.product_image` | optional | Upstream SerpAPI thumbnail URL (source material, not Tigris) |

**Async flow:** initial `POST /api/analyze` returns screenshots as `status: "pending"` (no Tigris URL yet). Poll `GET /api/proofs/{analysis_id}` until `status: "complete"`. The poll response includes **`data_capture`** — the full document with `hits[].proof.screenshot.url` filled in for every ready image.

There is no app-level API key. Auth (if any) is at the gateway (e.g. Sprites public vs sprite URL).

---

## 1. Which endpoint to use

| Endpoint | Response body | Use when |
|---|---|---|
| **`POST /api/analyze`** | **Data-capture document only** | Downstream apps, ETL, Windmill, catalog matching, pricing loaders |
| **`GET /api/export/data/{nsn}`** | Same document as analyze (download) | File export |
| **`POST /api/insight`** | `{ nsn, result, data_capture, serp_immersive }` | UI, analyst narrative, fair-market pricing tool |
| **`GET /api/export/json/{nsn}`** | Full `InsightResult` only | Pricing-tool narrative export |

**Rule of thumb:** integrate on **`data_capture`** (or `/api/analyze`). Treat `result` as optional human/analyst context.

---

## 2. Request

```http
POST /api/analyze
Content-Type: application/json
```

```json
{
  "nsn": "8020015964253",
  "serp_immersive": true
}
```

| Field | Required | Description |
|---|---|---|
| `nsn` | **Yes** | 9-digit NIIN or 13-digit NSN |
| `serp_immersive` | No | `true` = Google Shopping + Immersive multi-store (default). `false` = shopping-search only (less SerpAPI quota) |
| `capture_screenshots` | No | `true` = **async** visual evidence of eligible `links.url` → Tigris. Hits return with `proof.screenshot.status: "pending"`. Poll `GET /api/proofs/{analysis_id}` — response includes full `data_capture` with Tigris `url` on each ready hit. Requires Tigris (+ screenshot backend, default thum.io). |

Same body works for `POST /api/insight`. Query params: `?serp_immersive=false`, `?capture_screenshots=true`.

---

## 3. High-level payload map

### A) Machine payload — Data Capture Document (`POST /api/analyze`)

```
DataCaptureDocument
├── schema / schema_version     Contract ID
├── purpose / exported_at       Why + when
├── generator                   Build identity (commit, etc.)
├── query                       NSN / NIIN / FSC asked
├── item                        Catalog identity for that NSN
├── hits[]                      ★ Atomic findings (main payload)
├── counts                      Rollups for validation
├── sources[]                   Extractor provenance (optional)
└── scores                      Light analysis scores (optional; not hits)
```

### B) Insight envelope (`POST /api/insight`)

```
{
  nsn                 Echo of request
  serp_immersive      Whether immersive was used this run
  data_capture        Same document as /api/analyze
  result              InsightResult (narrative + commercial tiles + suppliers)
}
```

---

## 4. Data Capture Document — element descriptions

### Top-level

| JSON field | Type | Description |
|---|---|---|
| `schema` | string | Always `insight-forge.data-capture.v1` |
| `schema_version` | string | Currently `1.3` (single evidence URL + optional screenshot proof) |
| `purpose` | string | Short statement of intent (machine inventory for downstream apps) |
| `exported_at` | RFC3339 time | When this document was built |
| `analysis_id` | string | UUID for this run; keys Tigris objects under `…/{nsn}/{analysis_id}/hits/` |
| `generator` | object | Who produced it |
| `query` | object | What was searched |
| `item` | object | Primary federal item identity |
| `hits` | array | **Core inventory** — one row per discrete finding |
| `counts` | object | Totals / uniqueness / priced hit counts |
| `sources` | array | Optional provenance of extractors used |
| `scores` | object | Optional scoring context (not a substitute for hits) |

### `generator`

| Field | Description |
|---|---|
| `name` | e.g. `insight-forge` |
| `commit` | Git short/hash of the running build |
| `build_time` | Build timestamp |
| `generated_by` | Optional producer tag |

### `query`

| Field | Description |
|---|---|
| `nsn` | Normalized 13-digit NSN when known |
| `nsn_dashed` | e.g. `8020-01-596-4253` |
| `niin` | Last 9 digits |
| `fsc` | First 4 digits (Federal Supply Class) |
| `entity_id` | Internal query key (usually same as NSN/NIIN) |

### `item`

| Field | Description |
|---|---|
| `name` | Item name (e.g. from WebFLIS / AbilityOne) |
| `unit_of_issue` | Federal UOI when known |
| `technical_characteristics` | Spec / tech char string when known |

### `hits[]` — the main unit of work

Each hit is **one discrete finding**: a commercial mapping, a price observation, a supplier signal, etc.

| Field | Description |
|---|---|
| `hit_id` | Stable-ish ID for this hit within the document |
| `hit_type` | Kind of finding (see types below) |
| `source` | Origin system code (e.g. `ABILITYONE_ETS`, `SERPAPI`, `UPCITEMDB`, `PARTSBASE`, `GSA_ADVANTAGE`) |
| `identifiers` | Keys for matching (NSN, SKU, UPC, mfr, …) |
| `description` | Human-readable product / mapping text |
| `pricing` | **Atomic** price only (no min–max ranges) — omitted if unpriced |
| `links` | Optional verification / product URLs |
| `context` | Short note (e.g. “JWOD listing”, market offer note) |
| `date_added` | Mapping date when known (e.g. ETS) |
| `attributes` | Free-form bag for source-specific extras |

#### Common `hit_type` values (illustrative)

| `hit_type` | Meaning |
|---|---|
| Commercial / ETS mapping | SKU–UPC–manufacturer cross-ref for the NSN |
| Market / catalog price | Atomic commercial price observation |
| Federal / channel price | AbilityOne.com or similar channel list price |
| PartsBase / procurement | Historical federal transaction unit price |
| Supplier / award-related | Supplier concentration signals (when emitted as hits) |

*(Exact type strings are source-driven; always branch on `hit_type` + `source`, not position in the array.)*

### `hits[].identifiers`

| Field | Description |
|---|---|
| `nsn` / `niin` / `fsc` | Federal keys |
| `sku` | Manufacturer / commercial part number |
| `upc` / `gtin` | Barcode identifiers |
| `manufacturer` / `brand` | Commercial brand / mfr |
| `cage` | CAGE when known |
| `contract` | Contract number when known |
| `related_nsn` | Related / alternate NSN when applicable |

### `hits[].pricing` (atomic only)

**Important:** analysis UI may show **ranges** (e.g. “$12–$24 (5 offers)”).  
**Data-capture does not export ranges.** Each priced hit is one observation:

| Field | Description |
|---|---|
| `unit_price` | Listing price for the sell unit (USD unless noted) |
| `quantity` | How many base units that listing covers (pack size; default 1) |
| `price_per_each` | `unit_price / quantity` when pack is known |
| `unit` | UOM code: `EA`, `DZ`, `CS`, `PK`, … |
| `pack_label` | e.g. `dozen`, `case of 24` |
| `base_unit` | e.g. `sheet` for a ream |
| `currency` | Usually `USD` |
| `channel` | `amazon` \| `shop` \| `catalog` \| `federal` \| `gsa` \| `partsbase` \| … |
| `merchant` | Seller / store name when known |
| `price_source` | Provenance tag (e.g. `MARKET:HOME_DEPOT`, `SERPAPI`) |
| `as_of` | Capture date when known |

**Normalize for comparison:** prefer `price_per_each` when present; otherwise treat `unit_price` as each if `quantity == 1`.

### `hits[].links` (schema **1.2** — single primary URL)

| Field | Description |
|---|---|
| `url` | **The** most accurate/reliable evidence URL for this hit |
| `url_kind` | Classification: `merchant_pdp` \| `amazon_dp` \| `federal` \| `search` \| `web` \| `other` |

**Selection (high level):** verified merchant product pages win; Amazon `/dp/` for Amazon-channel prices; federal catalog for AbilityOne channel hits.  
**Pricing integrity:** a `price_observation` URL must match that hit’s `pricing.merchant` host (e.g. Home Depot price → `homedepot.com`). If no honest URL exists, `links` is omitted rather than attaching another retailer’s page. Tracking query params are stripped.

Deprecated multi-channel fields (`shop`, `amazon`, `upc`, `federal`, `website`, `price_url`) may appear in **older ≤1.1** documents but are **not populated** in 1.2+.

### `hits[].proof` (schema **1.3** — optional)

Present when screenshots were requested and a capture was attempted.

```json
"proof": {
  "screenshot": {
    "status": "ready",
    "kind": "page_screenshot",
    "bucket": "fair-market-pricing",
    "object_key": "insight-forge/2026/08/10/8020015964253/{analysis_id}/hits/price-obs-12.png",
    "content_type": "image/png",
    "captured_at": "2026-08-10T18:00:00Z",
    "source_url": "https://www.homedepot.com/p/…",
    "width": 1280,
    "height": 720,
    "sha256": "…",
    "url": "https://t3.storage.dev/fair-market-pricing/…?X-Amz-Signature=…"
  }
}
```

| Field | Description |
|---|---|
| `status` | `ready` \| `failed` \| `pending` \| `skipped` |
| `kind` | `page_screenshot` (full page) or `product_image` (catalog photo when merchant bot-walls page capture) |
| `object_key` | Durable Tigris/S3 key (use for re-presign / archival) |
| **`url`** | **Time-limited presigned Tigris download (~1h)** — this is the image URL to attach to the hit |
| `bucket` | Tigris bucket |
| `source_url` | Page associated with the evidence (= `links.url` at capture time) |
| `error` | Safe failure text when `status=failed` |

**Eligibility:** `price_observation` / strong commercial identity with `url_kind` in `merchant_pdp` \| `amazon_dp` \| `federal`, capped by `IF_SCREENSHOT_MAX_PER_RUN`.

**How to get Tigris URLs on each hit (recommended consumer flow):**

```http
POST /api/analyze
{ "nsn": "8020015964253", "capture_screenshots": true }
```

1. Read `analysis_id` and initial `hits[]` (screenshots are `pending`).
2. Poll until complete:

```http
GET /api/proofs/{analysis_id}
```

```json
{
  "analysis_id": "…",
  "status": "complete",
  "total": 12,
  "done": 12,
  "ready": 10,
  "failed": 2,
  "hits": {
    "price-obs-3": {
      "status": "ready",
      "kind": "product_image",
      "url": "https://t3.storage.dev/…presigned…",
      "object_key": "insight-forge/…/price-obs-3.png",
      "bucket": "fair-market-pricing"
    }
  },
  "data_capture": {
    "schema": "insight-forge.data-capture.v1",
    "schema_version": "1.3",
    "analysis_id": "…",
    "hits": [
      {
        "hit_id": "price-obs-3",
        "links": { "url": "https://www.walmart.com/…", "url_kind": "merchant_pdp" },
        "proof": {
          "screenshot": {
            "status": "ready",
            "kind": "product_image",
            "url": "https://t3.storage.dev/…presigned…",
            "object_key": "insight-forge/…/price-obs-3.png",
            "bucket": "fair-market-pricing"
          }
        }
      }
    ]
  }
}
```

**Use `data_capture` from the proofs poll** as the final machine payload — every hit that has an image includes `proof.screenshot.url` (Tigris).  
Alternatively map `proofs.hits[hit_id].url` onto your copy of the analyze response by `hit_id`.

UI polls this endpoint and shows thumbnails as each shot becomes `ready`.

### `counts`

| Field | Description |
|---|---|
| `total_hits` | `len(hits)` |
| `by_type` | Histogram by `hit_type` |
| `by_source` | Histogram by `source` |
| `unique_skus` / `unique_upcs` / `unique_manufacturers` | Identity breadth |
| `priced_hits` | Hits that include `pricing` |
| `price_observations` | Count of atomic price observations (often ≈ priced hits) |

### `sources[]` (optional)

Per-extractor provenance for the run:

| Field | Description |
|---|---|
| `source_code` | Extractor id |
| `snapshot_id` / `snapshot_at` | Capture instance |
| `quality_score` | Internal quality 0–1 or 0–100 |
| `data_source` | e.g. live vs unavailable |
| `note` / `result_count` | Ops / volume hints |

### `scores` (optional)

| Field | Description |
|---|---|
| `sourcing_attractiveness` / `supply_risk` | Preferred 0–100 scores |
| `viability_score` / `risk_score` | Legacy aliases |
| `generated_at` | When scores were computed |

**Do not** treat scores as the hit inventory; they are analyst context only.

---

## 5. Insight envelope — `result` (high level)

Returned only by **`POST /api/insight`** (and pricing export). Same analysis run as data-capture; different shape for humans/tools.

| Area | Fields (conceptually) | Description |
|---|---|---|
| **Identity** | `item_name`, `unit_of_issue`, `technical_characteristics` | Item header |
| **Scores** | `sourcing_attractiveness`, `supply_risk` (+ legacy names) | 0–100 posture |
| **Narrative** | `summary`, `full_analyst_report`, `key_insights`, `flags` | Analyst text |
| **Federal demand** | `supplier_data`, `demand_signals`, `related_nsns` | Award / continuity view |
| **Commercial tiles** | `commercial_references[]` | ETS/SKU cards with links + per-channel prices (may include **ranges** in UI fields) |
| **Commercial rollup** | `top_commercial_suppliers[]` | Manufacturer aggregates |
| **Channel prices** | `abilityone_channel_price`, `partsbase_historical_pricing` | Federal list vs historical transactions |
| **Health** | `integration_health`, `partsbase_status` | External API status for banners |

### `commercial_references[]` (UI/pricing tool)

Useful if you already consume the insight path; **prefer `data_capture.hits` for new integrations**.

| Field group | Examples | Notes |
|---|---|---|
| Identity | `sku`, `upc`, `manufacturer`, `description`, `source` | ETS / commercial mapping |
| Primary price | `price`, `price_source`, `price_url`, `price_basis` | May be range string in UI fields |
| Links | `link_shop`, `link_shop_merchant`, `link_amazon`, `link_gsa`, … | Deep link when verified |
| Channel prices | `price_amazon`, `price_shop`, `price_upc`, `price_federal` + `*_source`, `*_is_range` | Per destination |
| Atomic offers | `market_offers[]` | Same idea as data-capture pricing rows: `unit_price`, `quantity`, `merchant`, `link`, `channel`, `source` |

---

## 6. Other useful JSON endpoints

| Endpoint | Purpose |
|---|---|
| `GET /health` | Liveness + feature flags (`serpapi_immersive`, PartsBase/UPC config, commit) |
| `GET /version` | `commit`, `buildTime` |
| `GET /api/quotas` | SerpAPI monthly burn (free Account API); UPC/PartsBase status notes |

---

## 7. Integration notes for consumers

1. **Contract key:** assert `schema == "insight-forge.data-capture.v1"` and read `schema_version`.  
2. **Iterate `hits`**, filter by `source` / `hit_type` / presence of `pricing`.  
3. **Prices are atomic** — one hit = one observation; never expect min/max on data-capture.  
4. **Pack-aware comparison:** use `price_per_each` when `quantity > 1`.  
5. **Links are best-effort evidence** — verify if you publish them externally; dead links may be stripped server-side.  
6. **`/api/analyze` and UI Data Capture export are the same builder** — no second schema.  
7. **Idempotency:** same NSN can yield different hit counts over time (market APIs, quota, immersive on/off).

---

## 8. Minimal example shape (`POST /api/analyze`)

```json
{
  "schema": "insight-forge.data-capture.v1",
  "schema_version": "1.3",
  "purpose": "…",
  "exported_at": "2026-08-05T12:00:00Z",
  "analysis_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "generator": {
    "name": "insight-forge",
    "commit": "cc0ab98",
    "build_time": "2026-08-05T…"
  },
  "query": {
    "nsn": "8020015964253",
    "nsn_dashed": "8020-01-596-4253",
    "niin": "015964253",
    "fsc": "8020"
  },
  "item": {
    "name": "…",
    "unit_of_issue": "EA"
  },
  "hits": [
    {
      "hit_id": "…",
      "hit_type": "…",
      "source": "SERPAPI",
      "identifiers": {
        "sku": "R091",
        "upc": "071497149299",
        "manufacturer": "WOOSTER",
        "nsn": "8020015964253"
      },
      "description": "Wooster Sherlock …",
      "pricing": {
        "unit_price": 40.35,
        "quantity": 1,
        "price_per_each": 40.35,
        "currency": "USD",
        "channel": "shop",
        "merchant": "Home Depot",
        "price_source": "SERPAPI"
      },
      "links": {
        "url": "https://www.homedepot.com/p/…",
        "url_kind": "merchant_pdp"
      }
    }
  ],
  "counts": {
    "total_hits": 42,
    "priced_hits": 28,
    "by_source": { "ABILITYONE_ETS": 10, "SERPAPI": 18 }
  }
}
```

---

## 9. One-sentence summary for stakeholders

**Insight Forge’s machine API returns a versioned, hit-oriented inventory of every structured NSN/SKU/UPC/price/link finding for one analysis run (`data-capture.v1`); optional insight endpoints wrap the same inventory with narrative scores and commercial tiles for analysts and pricing tools.**

---

## Related code

| Path | Role |
|---|---|
| [`internal/models/data_capture.go`](../internal/models/data_capture.go) | Schema structs / JSON tags |
| [`internal/processing/data_capture.go`](../internal/processing/data_capture.go) | Document builder |
| [`cmd/server/main.go`](../cmd/server/main.go) | HTTP routes (`/api/analyze`, `/api/insight`, exports) |
