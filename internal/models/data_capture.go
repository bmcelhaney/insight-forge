package models

import "time"

// Data-capture export schema for downstream applications.
// Stable contract: insight-forge.data-capture.v1
//
// This is intentionally hit-oriented (NSN/SKU/UPC/ETS/commercial/etc.), not
// the narrative InsightResult used by the pricing-tool export.
//
// Pricing on export hits is atomic: unit_price + quantity only (no ranges).
// Analysis UI may still show market ranges; those are not used here.

const (
	DataCaptureSchemaID = "insight-forge.data-capture.v1"
	// 1.3: analysis_id + optional proof.screenshot (Tigris) for links.url evidence.
	// 1.2: single primary evidence URL per hit (links.url + url_kind).
	DataCaptureSchemaVersion = "1.3"
)

// DataCaptureDocument is a machine-readable inventory of every structured hit
// Insight Forge resolved for one analysis query. Designed as an input payload
// for other applications (catalog matching, pricing engines, ERP loaders, etc.).
type DataCaptureDocument struct {
	Schema        string    `json:"schema"`
	SchemaVersion string    `json:"schema_version"`
	Purpose       string    `json:"purpose"`
	ExportedAt    time.Time `json:"exported_at"`
	// AnalysisID ties all hits and Tigris screenshot objects for this run.
	AnalysisID string               `json:"analysis_id,omitempty"`
	Generator  DataCaptureGenerator `json:"generator"`
	Query      DataCaptureQuery     `json:"query"`
	Item       DataCaptureItem      `json:"item"`
	Hits       []DataCaptureHit     `json:"hits"`
	Counts     DataCaptureCounts    `json:"counts"`
	// Sources lists extractor/snapshot provenance for this run (not narrative analysis).
	Sources []DataCaptureSource `json:"sources,omitempty"`
	// Scores are optional context only; consumers should not treat them as hits.
	Scores *DataCaptureScores `json:"scores,omitempty"`
	// Timings is server-side latency for this analyze (ms). For Windmill/ops, not pricing.
	Timings *DataCaptureTimings `json:"timings,omitempty"`
	// URLCoverage summarizes how many priced hits have a merchant-matched proof URL.
	URLCoverage *DataCaptureURLCoverage `json:"url_coverage,omitempty"`
}

// DataCaptureTimings is wall-clock milliseconds for one analyze/export run.
type DataCaptureTimings struct {
	TotalMS           int64           `json:"total_ms"`
	ExtractMS         int64           `json:"extract_ms"`
	SynthesizeMS      int64           `json:"synthesize_ms"`
	CommercialProbeMS int64           `json:"commercial_probe_ms,omitempty"`
	ProductLinksMS    int64           `json:"product_links_ms,omitempty"`
	SerpMS            int64           `json:"serp_ms,omitempty"`
	ImmersiveMS       int64           `json:"immersive_ms,omitempty"`
	UPCMS             int64           `json:"upc_ms,omitempty"`
	LinkVerifyMS      int64           `json:"link_verify_ms,omitempty"`
	DataCaptureMS     int64           `json:"data_capture_ms,omitempty"`
	Extractors        []NamedDuration `json:"extractors,omitempty"`
}

// NamedDuration is one timed phase or extractor.
type NamedDuration struct {
	Name string `json:"name"`
	MS   int64  `json:"ms"`
}

// DataCaptureURLCoverage is how many price_observation hits have usable proof URLs.
type DataCaptureURLCoverage struct {
	PriceObservations int `json:"price_observations"`
	WithURL           int `json:"with_url"`
	WithStrongURL     int `json:"with_strong_url"` // merchant_pdp | amazon_dp | federal
	WithoutURL        int `json:"without_url"`
	SearchOnly        int `json:"search_only"`
}

// DataCaptureGenerator identifies the producing application/build.
type DataCaptureGenerator struct {
	Name        string `json:"name"`
	Commit      string `json:"commit,omitempty"`
	BuildTime   string `json:"build_time,omitempty"`
	GeneratedBy string `json:"generated_by,omitempty"`
}

// DataCaptureQuery is the analyst query that produced the hit set.
type DataCaptureQuery struct {
	NSN       string `json:"nsn"`
	NSNDashed string `json:"nsn_dashed,omitempty"`
	NIIN      string `json:"niin,omitempty"`
	FSC       string `json:"fsc,omitempty"`
	EntityID  string `json:"entity_id,omitempty"`
}

// DataCaptureItem is the primary item identity for the query NSN.
type DataCaptureItem struct {
	Name                     string `json:"name,omitempty"`
	UnitOfIssue              string `json:"unit_of_issue,omitempty"`
	TechnicalCharacteristics string `json:"technical_characteristics,omitempty"`
}

// DataCaptureHit is one discrete finding (mapping, listing, price observation, supplier, etc.).
type DataCaptureHit struct {
	HitID       string                 `json:"hit_id"`
	HitType     string                 `json:"hit_type"`
	Source      string                 `json:"source"`
	Identifiers DataCaptureIdentifiers `json:"identifiers"`
	Description string                 `json:"description,omitempty"`
	// Pricing is set only for atomic price hits (unit_price + quantity). Never a range.
	Pricing *DataCapturePricing `json:"pricing,omitempty"`
	Links   *DataCaptureLinks   `json:"links,omitempty"`
	// Proof holds durable evidence artifacts (e.g. Tigris screenshot of links.url).
	Proof      *DataCaptureProof `json:"proof,omitempty"`
	Context    string            `json:"context,omitempty"`
	DateAdded  string            `json:"date_added,omitempty"`
	Attributes map[string]any    `json:"attributes,omitempty"`
}

// DataCaptureProof groups non-URL evidence artifacts for a hit (schema 1.3+).
type DataCaptureProof struct {
	Screenshot *DataCaptureScreenshot `json:"screenshot,omitempty"`
}

// DataCaptureScreenshot is a visual evidence artifact stored in object storage.
type DataCaptureScreenshot struct {
	// Status: pending | ready | failed | skipped
	Status string `json:"status"`
	// Kind: page_screenshot (full page) | product_image (catalog photo when page is bot-walled)
	Kind        string    `json:"kind,omitempty"`
	Bucket      string    `json:"bucket,omitempty"`
	ObjectKey   string    `json:"object_key,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
	CapturedAt  time.Time `json:"captured_at,omitempty"`
	// SourceURL is the durable product/page URL for this hit (same intent as links.url).
	// Store this for human/merchant reference — not the Tigris object.
	SourceURL  string `json:"source_url,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// URL is intentionally not populated. Short-lived presigned links are unsuitable
	// for DB storage. Use bucket + object_key for durable Tigris reference.
	// (Field retained omitempty for older documents only.)
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// DataCaptureIdentifiers groups product/entity keys commonly used by downstream systems.
type DataCaptureIdentifiers struct {
	NSN          string `json:"nsn,omitempty"`
	NIIN         string `json:"niin,omitempty"`
	FSC          string `json:"fsc,omitempty"`
	SKU          string `json:"sku,omitempty"`
	UPC          string `json:"upc,omitempty"`
	GTIN         string `json:"gtin,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	CAGE         string `json:"cage,omitempty"`
	Brand        string `json:"brand,omitempty"`
	Contract     string `json:"contract,omitempty"`
	RelatedNSN   string `json:"related_nsn,omitempty"`
}

// DataCapturePricing is one atomic price observation for downstream systems.
// No min/max/range fields — each observation is unit_price for quantity units.
type DataCapturePricing struct {
	UnitPrice    float64 `json:"unit_price"`               // listing price for the sell unit
	Quantity     int     `json:"quantity"`                 // base units covered by unit_price (pack size)
	PricePerEach float64 `json:"price_per_each,omitempty"` // unit_price / quantity when quantity >= 1
	Unit         string  `json:"unit,omitempty"`           // EA, DZ, CS, PK, BX, CT, RM, …
	PackLabel    string  `json:"pack_label,omitempty"`     // e.g. "dozen", "case of 24"
	BaseUnit     string  `json:"base_unit,omitempty"`      // e.g. "sheet"
	Currency     string  `json:"currency,omitempty"`       // default USD when empty on export
	Channel      string  `json:"channel,omitempty"`        // amazon | shop | catalog | federal | gsa | partsbase
	Merchant     string  `json:"merchant,omitempty"`
	PriceSource  string  `json:"price_source,omitempty"` // provenance tag
	AsOf         string  `json:"as_of,omitempty"`
}

// DataCaptureLinks holds the single primary evidence URL for a hit (schema 1.2+).
// Downstream systems should use URL only; multi-channel fields below are deprecated
// and are not populated by the 1.2+ builder (kept for unmarshaling older documents).
type DataCaptureLinks struct {
	// URL is the most accurate/reliable product or evidence link for this hit.
	URL string `json:"url,omitempty"`
	// URLKind classifies URL for consumers:
	//   merchant_pdp | amazon_dp | federal | search | web | other
	URLKind string `json:"url_kind,omitempty"`

	// Deprecated (schema ≤1.1 multi-link export). Empty in 1.2+ documents.
	Shop     string `json:"shop,omitempty"`
	Amazon   string `json:"amazon,omitempty"`
	UPC      string `json:"upc,omitempty"`
	Federal  string `json:"federal,omitempty"`
	Website  string `json:"website,omitempty"`
	PriceURL string `json:"price_url,omitempty"`
}

// DataCaptureCounts summarizes the hit inventory for quick validation by consumers.
type DataCaptureCounts struct {
	TotalHits           int            `json:"total_hits"`
	ByType              map[string]int `json:"by_type"`
	BySource            map[string]int `json:"by_source"`
	UniqueSKUs          int            `json:"unique_skus"`
	UniqueUPCs          int            `json:"unique_upcs"`
	UniqueManufacturers int            `json:"unique_manufacturers"`
	PricedHits          int            `json:"priced_hits"` // hits with atomic unit_price
	PriceObservations   int            `json:"price_observations"`
}

// DataCaptureSource is provenance for one extractor snapshot used in the run.
type DataCaptureSource struct {
	SourceCode   string    `json:"source_code"`
	SnapshotID   string    `json:"snapshot_id,omitempty"`
	SnapshotAt   time.Time `json:"snapshot_at,omitempty"`
	QualityScore float64   `json:"quality_score,omitempty"`
	DataSource   string    `json:"data_source,omitempty"`
	Note         string    `json:"note,omitempty"`
	ResultCount  int       `json:"result_count,omitempty"`
	FetchMS      int64     `json:"fetch_ms,omitempty"`
}

// DataCaptureScores is lightweight analysis context (not a substitute for the pricing export).
type DataCaptureScores struct {
	SourcingAttractiveness float64   `json:"sourcing_attractiveness,omitempty"`
	SupplyRisk             float64   `json:"supply_risk,omitempty"`
	ViabilityScore         float64   `json:"viability_score,omitempty"`
	RiskScore              float64   `json:"risk_score,omitempty"`
	GeneratedAt            time.Time `json:"generated_at,omitempty"`
}
