package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

const (
	tableAnalyses = "nsn_analyses"
	tableHits     = "nsn_hits"
)

// IngestResult is how many rows this analyze wrote to ClickHouse.
type IngestResult struct {
	Enabled    bool   `json:"enabled"`
	Written    bool   `json:"written"`
	Analyses   int    `json:"analyses"`
	Hits       int    `json:"hits"`
	PricedHits int    `json:"priced_hits"`
	Error      string `json:"error,omitempty"`
}

func (r IngestResult) ToModel() *models.DataCaptureClickHouse {
	cp := r
	return &models.DataCaptureClickHouse{
		Enabled:    cp.Enabled,
		Written:    cp.Written,
		Analyses:   cp.Analyses,
		Hits:       cp.Hits,
		PricedHits: cp.PricedHits,
		Error:      cp.Error,
	}
}

// IngestAnalysis writes one data-capture document into nsn_analyses + nsn_hits.
func (c *Client) IngestAnalysis(ctx context.Context, doc models.DataCaptureDocument) IngestResult {
	out := IngestResult{}
	if c == nil || !c.Configured() {
		return out
	}
	out.Enabled = true
	if strings.TrimSpace(doc.AnalysisID) == "" {
		out.Error = "missing analysis_id"
		return out
	}
	analysisJSON, err := json.Marshal(analysisRowFromDoc(doc))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if err := c.ExecJSONEachRow(ctx, tableAnalyses, append(analysisJSON, '\n')); err != nil {
		out.Error = err.Error()
		return out
	}
	out.Analyses = 1
	hitsPayload, err := encodeHitRows(doc)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(hitsPayload) > 0 {
		if err := c.ExecJSONEachRow(ctx, tableHits, hitsPayload); err != nil {
			out.Error = err.Error()
			return out
		}
	}
	out.Hits = len(doc.Hits)
	out.PricedHits = doc.Counts.PriceObservations
	if out.PricedHits == 0 {
		out.PricedHits = doc.Counts.PricedHits
	}
	out.Written = true
	return out
}

type analysisRow struct {
	AnalysisID             string  `json:"analysis_id"`
	NSN                    string  `json:"nsn"`
	NSNDashed              string  `json:"nsn_dashed"`
	NIIN                   string  `json:"niin"`
	FSC                    string  `json:"fsc"`
	ItemName               string  `json:"item_name"`
	UnitOfIssue            string  `json:"unit_of_issue"`
	TechnicalChars         string  `json:"technical_chars"`
	SourcingAttractiveness float64 `json:"sourcing_attractiveness"`
	SupplyRisk             float64 `json:"supply_risk"`
	ViabilityScore         float64 `json:"viability_score"`
	RiskScore              float64 `json:"risk_score"`
	TotalHits              uint32  `json:"total_hits"`
	PricedHits             uint32  `json:"priced_hits"`
	UniqueSKUs             uint32  `json:"unique_skus"`
	UniqueUPCs             uint32  `json:"unique_upcs"`
	ExportedAt             string  `json:"exported_at"`
	RawDocument            string  `json:"raw_document"`
	GeneratorCommit        string  `json:"generator_commit"`
	SchemaVersion          string  `json:"schema_version"`
	EntityID               string  `json:"entity_id"`
	Purpose                string  `json:"purpose"`
	SchemaName             string  `json:"schema_name"`
	ScoresGeneratedAt      string  `json:"scores_generated_at"`
	UniqueManufacturers    uint32  `json:"unique_manufacturers"`
	PriceObservations      uint32  `json:"price_observations"`
	CountsByType           string  `json:"counts_by_type"`
	CountsBySource         string  `json:"counts_by_source"`
	SourcesJSON            string  `json:"sources_json"`
	GeneratorName          string  `json:"generator_name"`
	GeneratorBuildTime     string  `json:"generator_build_time"`
	GeneratorGeneratedBy   string  `json:"generator_generated_by"`
	ObjectKey              string  `json:"object_key"`
	APIAnalysisID          string  `json:"api_analysis_id"`
}

type hitRow struct {
	HitID           string  `json:"hit_id"`
	AnalysisID      string  `json:"analysis_id"`
	NSN             string  `json:"nsn"`
	HitType         string  `json:"hit_type"`
	Source          string  `json:"source"`
	SKU             string  `json:"sku"`
	UPC             string  `json:"upc"`
	GTIN            string  `json:"gtin"`
	Manufacturer    string  `json:"manufacturer"`
	CAGE            string  `json:"cage"`
	Brand           string  `json:"brand"`
	Description     string  `json:"description"`
	Context         string  `json:"context"`
	UnitPrice       float64 `json:"unit_price"`
	Quantity        uint32  `json:"quantity"`
	Currency        string  `json:"currency"`
	Channel         string  `json:"channel"`
	Merchant        string  `json:"merchant"`
	PriceSource     string  `json:"price_source"`
	PriceAsOf       string  `json:"price_as_of"`
	URL             string  `json:"url"`
	Attributes      string  `json:"attributes"`
	DateAdded       string  `json:"date_added"`
	Contract        string  `json:"contract"`
	RelatedNSN      string  `json:"related_nsn"`
	NIIN            string  `json:"niin"`
	FSC             string  `json:"fsc"`
	ParentHitID     string  `json:"parent_hit_id"`
	ParentType      string  `json:"parent_type"`
	PricePerEach    float64 `json:"price_per_each"`
	Unit            string  `json:"unit"`
	PackLabel       string  `json:"pack_label"`
	BaseUnit        string  `json:"base_unit"`
	OfferTitle      string  `json:"offer_title"`
	AttrOfferTitle  string  `json:"attr_offer_title"`
	AttrParentHitID string  `json:"attr_parent_hit_id"`
	AttrParentType  string  `json:"attr_parent_type"`
	PricedCount     float64 `json:"priced_count"`
	ReferenceCount  float64 `json:"reference_count"`
	AwardCount      float64 `json:"award_count"`
	Country         string  `json:"country"`
	MostRecentAward string  `json:"most_recent_award"`
	SharePercent    float64 `json:"share_percent"`
	TotalValue      float64 `json:"total_value"`
	Confidence      float64 `json:"confidence"`
	Relation        string  `json:"relation"`
	Catalog         string  `json:"catalog"`
	UnitOfIssue     string  `json:"unit_of_issue"`
	ObjectKey       string  `json:"object_key"`
	URLKind         string  `json:"url_kind"`
}

func analysisRowFromDoc(doc models.DataCaptureDocument) analysisRow {
	raw, _ := json.Marshal(doc)
	byType, _ := json.Marshal(doc.Counts.ByType)
	bySource, _ := json.Marshal(doc.Counts.BySource)
	sources, _ := json.Marshal(doc.Sources)
	exported := chTime(doc.ExportedAt)
	scoresAt := exported
	sa, sr, vs, rs := 0.0, 0.0, 0.0, 0.0
	if doc.Scores != nil {
		sa = doc.Scores.SourcingAttractiveness
		sr = doc.Scores.SupplyRisk
		vs = doc.Scores.ViabilityScore
		rs = doc.Scores.RiskScore
		if !doc.Scores.GeneratedAt.IsZero() {
			scoresAt = chTime(doc.Scores.GeneratedAt)
		}
	}
	if byType == nil {
		byType = []byte("{}")
	}
	if bySource == nil {
		bySource = []byte("{}")
	}
	if sources == nil {
		sources = []byte("[]")
	}
	return analysisRow{
		AnalysisID:             doc.AnalysisID,
		NSN:                    doc.Query.NSN,
		NSNDashed:              doc.Query.NSNDashed,
		NIIN:                   doc.Query.NIIN,
		FSC:                    doc.Query.FSC,
		ItemName:               doc.Item.Name,
		UnitOfIssue:            doc.Item.UnitOfIssue,
		TechnicalChars:         doc.Item.TechnicalCharacteristics,
		SourcingAttractiveness: sa,
		SupplyRisk:             sr,
		ViabilityScore:         vs,
		RiskScore:              rs,
		TotalHits:              uint32(doc.Counts.TotalHits),
		PricedHits:             uint32(doc.Counts.PricedHits),
		UniqueSKUs:             uint32(doc.Counts.UniqueSKUs),
		UniqueUPCs:             uint32(doc.Counts.UniqueUPCs),
		ExportedAt:             exported,
		RawDocument:            string(raw),
		GeneratorCommit:        doc.Generator.Commit,
		SchemaVersion:          doc.SchemaVersion,
		EntityID:               doc.Query.EntityID,
		Purpose:                doc.Purpose,
		SchemaName:             doc.Schema,
		ScoresGeneratedAt:      scoresAt,
		UniqueManufacturers:    uint32(doc.Counts.UniqueManufacturers),
		PriceObservations:      uint32(doc.Counts.PriceObservations),
		CountsByType:           string(byType),
		CountsBySource:         string(bySource),
		SourcesJSON:            string(sources),
		GeneratorName:          doc.Generator.Name,
		GeneratorBuildTime:     doc.Generator.BuildTime,
		GeneratorGeneratedBy:   doc.Generator.GeneratedBy,
		ObjectKey:              "",
		APIAnalysisID:          doc.AnalysisID,
	}
}

func encodeHitRows(doc models.DataCaptureDocument) ([]byte, error) {
	var buf bytes.Buffer
	for _, h := range doc.Hits {
		row := hitRowFromDoc(doc, h)
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func hitRowFromDoc(doc models.DataCaptureDocument, h models.DataCaptureHit) hitRow {
	row := hitRow{
		HitID:        h.HitID,
		AnalysisID:   doc.AnalysisID,
		NSN:          first(h.Identifiers.NSN, doc.Query.NSN),
		HitType:      h.HitType,
		Source:       h.Source,
		SKU:          h.Identifiers.SKU,
		UPC:          h.Identifiers.UPC,
		GTIN:         h.Identifiers.GTIN,
		Manufacturer: h.Identifiers.Manufacturer,
		CAGE:         h.Identifiers.CAGE,
		Brand:        h.Identifiers.Brand,
		Description:  h.Description,
		Context:      h.Context,
		DateAdded:    h.DateAdded,
		Contract:     h.Identifiers.Contract,
		RelatedNSN:   h.Identifiers.RelatedNSN,
		NIIN:         first(h.Identifiers.NIIN, doc.Query.NIIN),
		FSC:          first(h.Identifiers.FSC, doc.Query.FSC),
	}
	if h.Pricing != nil {
		row.UnitPrice = h.Pricing.UnitPrice
		if h.Pricing.Quantity > 0 {
			row.Quantity = uint32(h.Pricing.Quantity)
		}
		row.Currency = h.Pricing.Currency
		row.Channel = h.Pricing.Channel
		row.Merchant = h.Pricing.Merchant
		row.PriceSource = h.Pricing.PriceSource
		row.PriceAsOf = h.Pricing.AsOf
		row.PricePerEach = h.Pricing.PricePerEach
		row.Unit = h.Pricing.Unit
		row.PackLabel = h.Pricing.PackLabel
		row.BaseUnit = h.Pricing.BaseUnit
	}
	if h.Links != nil {
		row.URL = h.Links.URL
		row.URLKind = h.Links.URLKind
	}
	if h.Proof != nil && h.Proof.Screenshot != nil {
		row.ObjectKey = h.Proof.Screenshot.ObjectKey
	}
	if h.Attributes != nil {
		if b, err := json.Marshal(h.Attributes); err == nil {
			row.Attributes = string(b)
		}
		row.ParentHitID = attrString(h.Attributes, "parent_hit_id")
		row.ParentType = attrString(h.Attributes, "parent_type")
		row.AttrParentHitID = row.ParentHitID
		row.AttrParentType = row.ParentType
		row.OfferTitle = attrString(h.Attributes, "offer_title")
		row.AttrOfferTitle = row.OfferTitle
		row.Catalog = attrString(h.Attributes, "catalog")
		row.PricedCount = attrFloat(h.Attributes, "priced_count")
		row.ReferenceCount = attrFloat(h.Attributes, "reference_count")
		row.AwardCount = attrFloat(h.Attributes, "award_count")
		row.Country = attrString(h.Attributes, "country")
		row.MostRecentAward = attrString(h.Attributes, "most_recent_award")
		row.SharePercent = attrFloat(h.Attributes, "share_percent")
		row.TotalValue = attrFloat(h.Attributes, "total_value")
		row.Confidence = attrFloat(h.Attributes, "confidence")
		row.Relation = attrString(h.Attributes, "relation")
		row.UnitOfIssue = attrString(h.Attributes, "unit_of_issue")
	}
	return row
}

func chTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format("2006-01-02 15:04:05.000")
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func attrString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func attrFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}
