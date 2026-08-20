package clickhouse

import (
	"strings"
	"testing"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func sampleDoc() models.DataCaptureDocument {
	return models.DataCaptureDocument{
		Schema:        models.DataCaptureSchemaID,
		SchemaVersion: models.DataCaptureSchemaVersion,
		Purpose:       "test",
		ExportedAt:    time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		AnalysisID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Generator:     models.DataCaptureGenerator{Name: "insight-forge", Commit: "abc123", BuildTime: "2026-08-20T12:00:00Z"},
		Query:         models.DataCaptureQuery{NSN: "8020015964253", NSNDashed: "8020-01-596-4253", NIIN: "015964253", FSC: "8020", EntityID: "8020015964253"},
		Item:          models.DataCaptureItem{Name: "ROLLER", UnitOfIssue: "EA"},
		Hits: []models.DataCaptureHit{
			{
				HitID:       "price-obs-1",
				HitType:     "price_observation",
				Source:      "SERPAPI",
				Identifiers: models.DataCaptureIdentifiers{NSN: "8020015964253", SKU: "R091", Manufacturer: "WOOSTER"},
				Pricing: &models.DataCapturePricing{
					UnitPrice: 40.35, Quantity: 1, PricePerEach: 40.35, Currency: "USD",
					Channel: "shop", Merchant: "Home Depot", PriceSource: "SERPAPI",
				},
				Links:      &models.DataCaptureLinks{URL: "https://www.homedepot.com/p/x", URLKind: "merchant_pdp"},
				Attributes: map[string]any{"parent_hit_id": "ets-1", "parent_type": "ets_mapping", "offer_title": "Wooster R091"},
			},
			{
				HitID:       "ets-1",
				HitType:     "ets_mapping",
				Source:      "ABILITYONE_ETS",
				Identifiers: models.DataCaptureIdentifiers{SKU: "R091", UPC: "071497149299"},
				Description: "Wooster Sherlock",
			},
		},
		Counts: models.DataCaptureCounts{TotalHits: 2, PricedHits: 1, PriceObservations: 1, UniqueSKUs: 1, ByType: map[string]int{"price_observation": 1}},
		Scores: &models.DataCaptureScores{SourcingAttractiveness: 90, SupplyRisk: 20, GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)},
	}
}

func TestAnalysisRowFromDoc(t *testing.T) {
	row := analysisRowFromDoc(sampleDoc())
	if row.AnalysisID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("analysis_id %q", row.AnalysisID)
	}
	if row.NSN != "8020015964253" || row.APIAnalysisID != row.AnalysisID {
		t.Fatalf("nsn/api id: %+v", row)
	}
	if row.ItemName != "ROLLER" || row.PriceObservations != 1 {
		t.Fatalf("item/counts: %+v", row)
	}
	if !strings.Contains(row.CountsByType, "price_observation") {
		t.Fatalf("counts_by_type %q", row.CountsByType)
	}
	if row.ExportedAt != "2026-08-20 12:00:00.000" {
		t.Fatalf("exported_at %q", row.ExportedAt)
	}
}

func TestHitRowsIncludeURLKindAndParent(t *testing.T) {
	doc := sampleDoc()
	payload, err := encodeHitRows(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(payload)
	if !strings.Contains(s, `"url_kind":"merchant_pdp"`) {
		t.Fatalf("missing url_kind: %s", s)
	}
	if !strings.Contains(s, `"merchant":"Home Depot"`) {
		t.Fatalf("missing merchant: %s", s)
	}
	if !strings.Contains(s, `"parent_hit_id":"ets-1"`) {
		t.Fatalf("missing parent: %s", s)
	}
	if strings.Count(s, "\n") != 2 {
		t.Fatalf("expected 2 NDJSON rows, got %q", s)
	}
}

func TestIngestNoopWhenUnconfigured(t *testing.T) {
	var c *Client
	if err := c.IngestAnalysis(nil, sampleDoc()); err != nil {
		t.Fatal(err)
	}
}
