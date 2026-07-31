package processing

import (
	"testing"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestBuildDataCaptureDocument_HitInventory(t *testing.T) {
	result := models.InsightResult{
		EntityID:    "7920014487052",
		ItemName:    "MOPHEAD,WET",
		UnitOfIssue: "EA",
		GeneratedAt: time.Now(),
		GeneratedBy: "test",
		CommercialReferences: []models.CommercialReference{
			{
				SKU:          "MOP-123",
				UPC:          "012345678905",
				Manufacturer: "Acme",
				Description:  "Wet mop head",
				Source:       "ABILITYONE_ETS",
				Price:        "$12.50 – $18.00",
				PriceShop:    "$12.50 – $18.00",
				PriceShopIsRange: true,
				LinkAmazon:   "https://www.amazon.com/s?k=MOP-123",
				LinkShop:     "https://www.google.com/search?tbm=shop&q=MOP-123",
			},
			{
				SKU:          "GSA-9",
				Manufacturer: "Acme",
				Source:       "GSA_ADVANTAGE",
				Price:        "$15.00",
			},
		},
		AbilityOneChannelPrice: &models.ChannelPrice{
			Price:  "$22.10",
			SKU:    "AO-1",
			Source: "ABILITYONE_COM",
			URL:    "https://www.abilityone.com/search?q=7920-01-448-7052",
		},
		PartsBaseHistoricalPricing: &models.PartsBasePriceSummary{
			SignalCount:     3,
			SupplierCount:   2,
			MinUnitPrice:    "$10.00",
			MaxUnitPrice:    "$20.00",
			MedianUnitPrice: "$15.00",
			Source:          "PARTSBASE",
			Sample: []models.PartsBaseHistoricalPrice{
				{UnitPrice: "$10.00", Supplier: "Vendor A", Quantity: 5, AwardDate: "2025-01-01"},
			},
		},
		RelatedNSNs: []models.RelatedNSN{
			{NSN: "7920014487053", Description: "Related mop", Relation: "direct_equivalent", Confidence: 0.8},
		},
		SupplierData: models.SupplierView{
			TopSuppliers: []models.SupplierSummary{
				{Name: "NIB Workshop", CAGE: "1ABC2", AwardCount: 4, TotalValue: 1000, Country: "US"},
			},
		},
		TopCommercialSuppliers: []models.CommercialSupplier{
			{Name: "Acme", Count: 2, SKUs: []string{"MOP-123", "GSA-9"}, ExamplePrice: "$12.50 – $18.00", PricedCount: 2},
		},
		SourcingAttractiveness: 90,
		SupplyRisk:             20,
	}

	snaps := []models.DataSnapshot{
		{
			ID:         "snap-web-1",
			SourceCode: "WEB_SEARCH_INTEL",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"data_source":  "live_web_search",
				"result_count": 1,
				"results": []map[string]any{
					{"title": "Mop procurement page", "url": "https://example.com/mop", "domain": "example.com", "snippet": "Federal mop"},
				},
			},
			QualityScore: 0.8,
		},
		{
			ID:         "snap-ets-1",
			SourceCode: "ABILITYONE_ETS",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"data_source":        "ets_xlsx",
				"matched_rows_count": 2,
			},
			QualityScore: 0.95,
		},
	}

	doc := BuildDataCaptureDocument(result, snaps, DataCaptureMeta{Commit: "abc123", BuildTime: "2026-07-31T00:00:00Z"})

	if doc.Schema != models.DataCaptureSchemaID {
		t.Fatalf("schema: got %q", doc.Schema)
	}
	if doc.SchemaVersion != models.DataCaptureSchemaVersion {
		t.Fatalf("schema_version: got %q", doc.SchemaVersion)
	}
	if doc.Query.NSN != "7920014487052" {
		t.Fatalf("nsn: got %q", doc.Query.NSN)
	}
	if doc.Query.NSNDashed != "7920-01-448-7052" {
		t.Fatalf("dashed nsn: got %q", doc.Query.NSNDashed)
	}
	if doc.Generator.Commit != "abc123" {
		t.Fatalf("commit: got %q", doc.Generator.Commit)
	}
	if doc.Counts.TotalHits < 8 {
		t.Fatalf("expected many hits, got %d: types=%v", doc.Counts.TotalHits, doc.Counts.ByType)
	}
	if doc.Counts.ByType["ets_mapping"] != 1 {
		t.Fatalf("expected 1 ets_mapping, got %d", doc.Counts.ByType["ets_mapping"])
	}
	if doc.Counts.ByType["gsa_listing"] != 1 {
		t.Fatalf("expected 1 gsa_listing, got %d", doc.Counts.ByType["gsa_listing"])
	}
	if doc.Counts.ByType["channel_price"] != 1 {
		t.Fatalf("expected channel_price hit")
	}
	if doc.Counts.ByType["partsbase_transaction"] != 1 {
		t.Fatalf("expected partsbase_transaction hit")
	}
	if doc.Counts.ByType["web_result"] != 1 {
		t.Fatalf("expected web_result hit")
	}
	if doc.Counts.UniqueSKUs < 2 {
		t.Fatalf("unique skus: got %d", doc.Counts.UniqueSKUs)
	}
	if len(doc.Sources) != 2 {
		t.Fatalf("sources: got %d", len(doc.Sources))
	}
	// Must not embed long narrative fields as top-level export content
	// (document is hit inventory only).
	if doc.Purpose == "" {
		t.Fatal("purpose required")
	}
}

func TestFormatDashedNSNLocal(t *testing.T) {
	if got := formatDashedNSNLocal("7920014487052"); got != "7920-01-448-7052" {
		t.Fatalf("got %q", got)
	}
	if got := formatDashedNSNLocal("123"); got != "123" {
		t.Fatalf("short: got %q", got)
	}
}
