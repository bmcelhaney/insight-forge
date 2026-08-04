package processing

import (
	"testing"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestBuildDataCaptureDocument_AtomicPriceHits(t *testing.T) {
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
				// UI-style range on the tile — must NOT appear as a range in export.
				Price:            "$12.50 – $18.00 (4 offers)",
				PriceShop:        "$12.50 – $18.00 (4 offers)",
				PriceShopIsRange: true,
				LinkAmazon:       "https://www.amazon.com/s?k=MOP-123",
				LinkShop:         "https://www.google.com/search?tbm=shop&q=MOP-123",
				MarketOffers: []models.MarketOffer{
					{UnitPrice: 12.50, Quantity: 1, Currency: "USD", Channel: "shop", Merchant: "Home Depot", Source: "SERPAPI"},
					{UnitPrice: 14.99, Quantity: 1, Currency: "USD", Channel: "amazon", Merchant: "Amazon", Source: "SERPAPI"},
					{UnitPrice: 18.00, Quantity: 1, Currency: "USD", Channel: "shop", Merchant: "Walmart", Source: "SERPAPI"},
				},
			},
			{
				SKU:          "GSA-9",
				Manufacturer: "Acme",
				Source:       "GSA_ADVANTAGE",
				Price:        "$15.00",
				PriceSource:  "GSA_ADVANTAGE",
			},
		},
		AbilityOneChannelPrice: &models.ChannelPrice{
			Price:  "$22.10",
			SKU:    "AO-1",
			Source: "ABILITYONE_COM",
			URL:    "https://www.abilityone.com/search?q=7920-01-448-7052",
		},
		PartsBaseHistoricalPricing: &models.PartsBasePriceSummary{
			SignalCount:   3,
			SupplierCount: 2,
			// Summary min/max must not become range hits.
			MinUnitPrice:    "$10.00",
			MaxUnitPrice:    "$20.00",
			MedianUnitPrice: "$15.00",
			Source:          "PARTSBASE",
			Sample: []models.PartsBaseHistoricalPrice{
				{UnitPrice: "$10.00", Supplier: "Vendor A", Quantity: 5, AwardDate: "2025-01-01"},
				{UnitPrice: "$20.00", Supplier: "Vendor B", Quantity: 2, AwardDate: "2025-02-01"},
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
	}

	doc := BuildDataCaptureDocument(result, snaps, DataCaptureMeta{Commit: "abc123", BuildTime: "2026-07-31T00:00:00Z"})

	if doc.SchemaVersion != "1.1" {
		t.Fatalf("schema_version: got %q want 1.1", doc.SchemaVersion)
	}
	// PartsBase is currently excluded from data-capture (includePartsBaseInDataCapture=false).
	// Expect commercial market offers + GSA single + AbilityOne channel (no PB rows).
	if doc.Counts.PriceObservations < 4 {
		t.Fatalf("expected multiple price_observation hits, got %d (by_type=%v)", doc.Counts.PriceObservations, doc.Counts.ByType)
	}
	if doc.Counts.ByType["partsbase_summary"] != 0 || doc.Counts.ByType["partsbase_transaction"] != 0 {
		t.Fatalf("PartsBase hits should be excluded from data-capture, got by_type=%v", doc.Counts.ByType)
	}
	for _, h := range doc.Hits {
		if h.HitType == "price_observation" && h.Pricing != nil && h.Pricing.Channel == "partsbase" {
			t.Fatalf("unexpected partsbase price_observation in data-capture: %+v", h)
		}
	}

	// Every price hit must be atomic: unit_price > 0, quantity >= 1, no range fields.
	for _, h := range doc.Hits {
		if h.HitType != "price_observation" {
			// Identity commercial hits must not carry range pricing
			if h.Pricing != nil && (h.HitType == "ets_mapping" || h.HitType == "gsa_listing" || h.HitType == "commercial_supplier") {
				t.Fatalf("identity hit %s should not have pricing (got %+v)", h.HitID, h.Pricing)
			}
			continue
		}
		if h.Pricing == nil {
			t.Fatalf("price_observation %s missing pricing", h.HitID)
		}
		if h.Pricing.UnitPrice <= 0 {
			t.Fatalf("price_observation %s unit_price invalid: %v", h.HitID, h.Pricing.UnitPrice)
		}
		if h.Pricing.Quantity < 1 {
			t.Fatalf("price_observation %s quantity invalid: %d", h.HitID, h.Pricing.Quantity)
		}
	}

}

func TestParseSingleUnitPrice(t *testing.T) {
	if v, ok := parseSingleUnitPrice("$15.00"); !ok || v != 15.0 {
		t.Fatalf("single: %v %v", v, ok)
	}
	if _, ok := parseSingleUnitPrice("$12.50 – $18.00 (4 offers)"); ok {
		t.Fatal("range should reject")
	}
	if _, ok := parseSingleUnitPrice("from $69.59 (search results)"); ok {
		t.Fatal("from estimate should reject")
	}
}

func TestFormatDashedNSNLocal(t *testing.T) {
	if got := formatDashedNSNLocal("7920014487052"); got != "7920-01-448-7052" {
		t.Fatalf("got %q", got)
	}
}
