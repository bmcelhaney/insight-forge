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
	ViabilityScore        float64        `json:"viability_score"` // 0-100
	RiskScore             float64        `json:"risk_score"`      // 0-100
	Summary               string         `json:"summary"`
	Flags                 []RiskFlag     `json:"flags"`
	SupplierData          SupplierView   `json:"supplier_data"`
	RelatedNSNs           []RelatedNSN   `json:"related_nsns"`
	DemandSignals         DemandSignals  `json:"demand_signals"`
	BasedOnSnapshotIDs    []string       `json:"based_on_snapshot_ids"`
	GeneratedAt           time.Time      `json:"generated_at"`
	GeneratedBy           string         `json:"generated_by"`
	UserApproved          bool           `json:"user_approved"`
	ApprovedValue         *float64       `json:"approved_value,omitempty"`
}

// RiskFlag represents a geopolitical, regulatory, concentration, or other concern.
type RiskFlag struct {
	Type        string  `json:"type"`        // "geopolitical", "sanctions", "concentration", "regulatory", "technical"
	Severity    string  `json:"severity"`    // "low", "medium", "high", "critical"
	Description string  `json:"description"`
	SourceCodes []string `json:"source_codes"`
}

// SupplierView aggregates supplier concentration and ecosystem.
type SupplierView struct {
	TotalSuppliers      int                `json:"total_suppliers"`
	TopSuppliers        []SupplierSummary  `json:"top_suppliers"`
	ConcentrationRisk   string             `json:"concentration_risk"` // low/medium/high
	PrimaryCountries    []string           `json:"primary_countries"`
}

// SupplierSummary for top vendors.
type SupplierSummary struct {
	Name         string  `json:"name"`
	CAGE         string  `json:"cage"`
	AwardCount   int     `json:"award_count"`
	TotalValue   float64 `json:"total_value"`
	Country      string  `json:"country"`
}

// RelatedNSN for the network graph / alternatives.
type RelatedNSN struct {
	NSN         string  `json:"nsn"`
	Description string  `json:"description"`
	Relation    string  `json:"relation"` // "supersedes", "alternative", "common_supplier", "same_program"
	Confidence  float64 `json:"confidence"`
}

// DemandSignals from FPDS and historical awards.
type DemandSignals struct {
	TotalAwards       int      `json:"total_awards"`
	TotalValueUSD     float64  `json:"total_value_usd"`
	TopAgencies       []string `json:"top_agencies"`
	RecentTrend       string   `json:"recent_trend"` // "increasing", "stable", "declining"
	ProgramAssociations []string `json:"program_associations"`
}
