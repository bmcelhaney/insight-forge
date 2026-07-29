package models

import "time"

// NSN represents a normalized National Stock Number (13 digits preferred).
// Supports partial queries (NIIN, FSC, CAGE).
type NSN struct {
	ID          string    `json:"id"`           // Full 13-digit NSN when known
	NIIN        string    `json:"niin"`         // Last 9 digits
	FSC         string    `json:"fsc"`          // First 4 digits
	CAGE        string    `json:"cage,omitempty"`
	Description string    `json:"description"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Snapshot is an immutable capture from one source at one moment in time.
// This is the fundamental unit in the DuckDB single source of truth.
type DataSnapshot struct {
	ID            string         `json:"id"`
	EntityID      string         `json:"entity_id"` // NSN or query key
	SourceCode    string         `json:"source_code"`
	Value         float64        `json:"value,omitempty"`
	Currency      string         `json:"currency"`
	QuantityMin   int            `json:"quantity_min"`
	QuantityMax   *int           `json:"quantity_max,omitempty"`
	ReferenceID   string         `json:"reference_id,omitempty"`
	EffectiveFrom *time.Time     `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time     `json:"effective_to,omitempty"`
	SnapshotAt    time.Time      `json:"snapshot_at"`
	RawResponse   map[string]any `json:"raw_response"` // Always captured
	QualityScore  float64        `json:"quality_score"` // 0-1 or 0-100
	IsOutlier     bool           `json:"is_outlier"`
	CreatedBy     string         `json:"created_by"`
}

// InsightResult is the synthesized output for an NSN.
type InsightResult struct {
	ID                    string         `json:"id"`
	EntityID              string         `json:"entity_id"`
	ViabilityScore        float64        `json:"viability_score"` // 0-100 (legacy)
	RiskScore             float64        `json:"risk_score"`      // 0-100 (legacy)
	SourcingAttractiveness float64       `json:"sourcing_attractiveness"` // 0-100 preferred new name
	SupplyRisk            float64        `json:"supply_risk"`     // 0-100 preferred new name
	Summary               string         `json:"summary"`
	AnalystRecommendation string         `json:"analyst_recommendation,omitempty"`
	MarketCommentary      string         `json:"market_commentary,omitempty"`
	FullAnalystReport     string         `json:"full_analyst_report,omitempty"`
	PricingTrend          string         `json:"pricing_trend,omitempty"`
	Flags                 []RiskFlag     `json:"flags"`
	SupplierData          SupplierView   `json:"supplier_data"`
	TopDisrupters         []SupplierSummary `json:"top_disrupters,omitempty"`
	ConcentrationIndex    float64        `json:"concentration_index,omitempty"`
	RelatedNSNs           []RelatedNSN   `json:"related_nsns"`
	DemandSignals         DemandSignals  `json:"demand_signals"`
	Citations             []string       `json:"citations,omitempty"`
	KeyInsights           []string       `json:"key_insights,omitempty"`
	ItemName              string         `json:"item_name,omitempty"`
	UnitOfIssue           string         `json:"unit_of_issue,omitempty"`
	TechnicalCharacteristics string      `json:"technical_characteristics,omitempty"`
	BasedOnSnapshotIDs    []string       `json:"based_on_snapshot_ids"`
	GeneratedAt           time.Time      `json:"generated_at"`
	GeneratedBy           string         `json:"generated_by"`
	UserApproved          bool           `json:"user_approved"`
	ApprovedValue         *float64       `json:"approved_value,omitempty"`

	// New: Commercial cross-references (SKUs, UPCs) associated with the NSN
	CommercialReferences []CommercialReference `json:"commercial_references,omitempty"`

	// Extended analysis that incorporates signals from the related commercial SKUs/UPCs
	// and relates them back to the primary NSN (pricing deltas, substitution risk/opportunity, etc.)
	ExtendedAnalysis string `json:"extended_analysis,omitempty"`

	// Top commercial suppliers derived from SKU and UPC cross-references found for this NSN.
	TopCommercialSuppliers []CommercialSupplier `json:"top_commercial_suppliers,omitempty"`

	// AbilityOne.com catalog list price for this NSN (federal channel). Shown separately
	// from commercial/ETS row pricing — never used as a default fill on those rows.
	AbilityOneChannelPrice *ChannelPrice `json:"abilityone_channel_price,omitempty"`
}

// ChannelPrice is a standalone NSN-level catalog price (not a commercial SKU quote).
type ChannelPrice struct {
	Price  string `json:"price"`
	SKU    string `json:"sku,omitempty"`
	Name   string `json:"name,omitempty"`
	Brand  string `json:"brand,omitempty"`
	Source string `json:"source"` // e.g. ABILITYONE_COM
	AsOf   string `json:"as_of,omitempty"`
	URL    string `json:"url,omitempty"`
	Note   string `json:"note,omitempty"`
}

// RiskFlag represents a geopolitical, regulatory, concentration, or other concern.
type RiskFlag struct {
	Type        string  `json:"type"`        // "geopolitical", "sanctions", "concentration", "regulatory", "technical"
	Severity    string  `json:"severity"`    // "low", "medium", "high", "critical"
	Description string  `json:"description"`
	Implication string  `json:"implication,omitempty"` // Analytical note on what it means and suggested action
	SourceCodes []string `json:"source_codes"`
}

// SupplierView aggregates supplier concentration and ecosystem.
type SupplierView struct {
	TotalSuppliers        int                `json:"total_suppliers"`
	TopSuppliers          []SupplierSummary  `json:"top_suppliers"`
	ConcentrationRisk     string             `json:"concentration_risk"` // low/medium/high
	PrimaryCountries      []string           `json:"primary_countries"`
	AwardPeriod             string  `json:"award_period"` // e.g. "Jan 2023 – Dec 2025 (36 months)"
	TopSuppliersTotalValue  float64 `json:"top_suppliers_total_value,omitempty"`
	EcosystemNote           string  `json:"ecosystem_note,omitempty"` // Short analytical note on overall supplier health/continuity
	ContinuityAssessment    string  `json:"continuity_assessment,omitempty"` // Deeper forward-looking note on supply continuity / risk
}

// SupplierSummary for top vendors.
type SupplierSummary struct {
	Name            string  `json:"name"`
	CAGE            string  `json:"cage"`
	AwardCount      int     `json:"award_count"`
	TotalValue      float64 `json:"total_value"`
	Country         string  `json:"country"`
	SharePercent    float64 `json:"share_percent,omitempty"`   // estimated share of total awards
	MostRecentAward string  `json:"most_recent_award,omitempty"` // e.g. "2025-04"
}

// RelatedNSN for the network graph / alternatives.
type RelatedNSN struct {
	NSN         string  `json:"nsn"`
	Description string  `json:"description"`
	Relation    string  `json:"relation"` // "supersedes", "direct_equivalent"
	Confidence  float64 `json:"confidence"`
}

// CommercialReference captures manufacturer SKUs, UPCs, and other commercial identifiers
// associated with the federal NSN. These enable cross-channel analysis (federal vs commercial pricing,
// substitution opportunities, and supply risk signals from the commercial side).
type CommercialReference struct {
	SKU          string `json:"sku"`                     // Manufacturer / commercial part number
	UPC          string `json:"upc,omitempty"`           // Universal Product Code (12 digits) or GTIN-12
	GTIN         string `json:"gtin,omitempty"`          // Longer GTIN if available
	Manufacturer string `json:"manufacturer,omitempty"`
	Description  string `json:"description,omitempty"`   // Commercial or AbilityOne product description
	Source       string `json:"source"`                  // e.g. "ABILITYONE_ETS", "GSA_ADVANTAGE"
	Price        string `json:"price,omitempty"`         // Observed commercial/federal channel price
	PriceSource  string `json:"price_source,omitempty"`  // GSA_ADVANTAGE | PARTSBASE | etc.
	PriceAsOf    string `json:"price_as_of,omitempty"`   // ISO date or capture time when known
	PriceURL     string `json:"price_url,omitempty"`     // Best URL to view/verify the price
	LinkShop     string `json:"link_shop,omitempty"`     // Resilient marketplace/search URL (rarely 404s)
	LinkUPC      string `json:"link_upc,omitempty"`      // UPC-based search URL
	LinkGSA      string `json:"link_gsa,omitempty"`      // GSA Advantage search URL
	LinkWebsite  string `json:"link_website,omitempty"`  // Manufacturer homepage when known
	Context      string `json:"context,omitempty"`       // Short note from source (e.g. "JWOD listing", "substitute")
	DateAdded    string `json:"date_added,omitempty"`    // ETS mapping date when available
}

// CommercialSupplier represents an aggregated commercial supplier derived from SKU/UPC data.
type CommercialSupplier struct {
	Name        string   `json:"name"`                  // Manufacturer or commercial supplier name
	SKUs        []string `json:"skus,omitempty"`        // Associated SKUs
	UPCs        []string `json:"upcs,omitempty"`        // Associated UPCs
	Count       int      `json:"count"`                 // How many times this supplier appeared in cross-refs
	ExamplePrice string  `json:"example_price,omitempty"`
	Source      string   `json:"source,omitempty"`      // Primary source of this data
}

// DemandSignals from FPDS and historical awards.
type DemandSignals struct {
	TotalAwards         int      `json:"total_awards"`
	TotalValueUSD       float64  `json:"total_value_usd"`
	TopAgencies         []string `json:"top_agencies"`
	RecentTrend         string   `json:"recent_trend"` // "increasing", "stable", "declining"
	ProgramAssociations []string `json:"program_associations"`
	AwardPeriod         string   `json:"award_period"` // e.g. "Jan 2023 – Dec 2025 (36 months)"
	YoYChange           string   `json:"yoy_change,omitempty"`     // e.g. "+8% vs prior year"
	PeakPeriods         string   `json:"peak_periods,omitempty"`   // e.g. "Q4 2024, Q1 2025"
	DemandNote          string   `json:"demand_note,omitempty"`    // Analytical note on demand health / outlook
}
