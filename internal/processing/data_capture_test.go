package processing

import (
	"strings"
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
				// Prefer product PDP for shop evidence (schema 1.2 single URL).
				LinkShop:         "https://www.homedepot.com/p/Acme-Mop/12345",
				LinkShopMerchant: "Home Depot",
				LinkAmazon:       "https://www.amazon.com/dp/B000TESTMOP1",
				PriceURL:         "https://www.homedepot.com/p/Acme-Mop/12345",
				MarketOffers: []models.MarketOffer{
					{UnitPrice: 12.50, Quantity: 1, Currency: "USD", Channel: "shop", Merchant: "Home Depot", Source: "SERPAPI", Link: "https://www.homedepot.com/p/Acme-Mop/12345"},
					{UnitPrice: 14.99, Quantity: 1, Currency: "USD", Channel: "amazon", Merchant: "Amazon", Source: "SERPAPI", Link: "https://www.amazon.com/dp/B000TESTMOP1"},
					{UnitPrice: 18.00, Quantity: 1, Currency: "USD", Channel: "shop", Merchant: "Walmart", Source: "SERPAPI", Link: "https://www.walmart.com/ip/Acme-Mop/999"},
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

	if doc.SchemaVersion != "1.3" {
		t.Fatalf("schema_version: got %q want 1.3", doc.SchemaVersion)
	}
	if doc.AnalysisID == "" {
		t.Fatal("expected analysis_id")
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
	// Schema 1.2: at most one primary URL per hit (links.url); no multi-channel link bag.
	for _, h := range doc.Hits {
		if h.Links != nil {
			if h.Links.Shop != "" || h.Links.Amazon != "" || h.Links.UPC != "" ||
				h.Links.Federal != "" || h.Links.Website != "" || h.Links.PriceURL != "" {
				t.Fatalf("hit %s still has multi-channel link fields (1.2 is single url only): %+v", h.HitID, h.Links)
			}
			if h.Links.URL != "" && h.Links.URLKind == "" {
				t.Fatalf("hit %s has url without url_kind", h.HitID)
			}
		}
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

	// ETS parent identity hit should resolve to a single merchant PDP (not multi-link).
	var ets *models.DataCaptureHit
	for i := range doc.Hits {
		if doc.Hits[i].HitType == "ets_mapping" {
			ets = &doc.Hits[i]
			break
		}
	}
	if ets == nil || ets.Links == nil || ets.Links.URL == "" {
		t.Fatal("expected ets_mapping hit with single primary url")
	}
	if ets.Links.URLKind != "merchant_pdp" && ets.Links.URLKind != "amazon_dp" {
		t.Fatalf("ets primary url_kind=%q want merchant_pdp or amazon_dp (url=%s)", ets.Links.URLKind, ets.Links.URL)
	}

	// AbilityOne channel price → federal kind
	var ao *models.DataCaptureHit
	for i := range doc.Hits {
		if doc.Hits[i].HitType == "price_observation" && doc.Hits[i].Pricing != nil &&
			doc.Hits[i].Pricing.Channel == "federal" {
			ao = &doc.Hits[i]
			break
		}
	}
	if ao == nil || ao.Links == nil || ao.Links.URLKind != "federal" {
		t.Fatalf("expected federal price hit with url_kind=federal, got %+v", ao)
	}
}

func TestBestCommercialEvidenceLinksPrefersPDP(t *testing.T) {
	c := models.CommercialReference{
		SKU:        "R091",
		LinkShop:   "https://www.homedepot.com/p/Wooster-R091/203150945",
		LinkAmazon: "https://www.amazon.com/s?k=R091",
		LinkGSA:    "https://www.abilityone.com/search?q=8020-01-596-4253",
		PriceURL:   "https://www.homedepot.com/p/Wooster-R091/203150945",
	}
	lnk := bestCommercialEvidenceLinks(c)
	if lnk == nil || !strings.Contains(lnk.URL, "homedepot.com/p/") {
		t.Fatalf("want HD PDP, got %+v", lnk)
	}
	if lnk.URLKind != "merchant_pdp" {
		t.Fatalf("url_kind=%q", lnk.URLKind)
	}
	if lnk.Shop != "" || lnk.Amazon != "" {
		t.Fatalf("deprecated multi-link fields must be empty: %+v", lnk)
	}
}

func TestBestPriceObservationLinksUsesOfferLink(t *testing.T) {
	parent := models.CommercialReference{
		LinkShop:   "https://www.homedepot.com/p/Other/1",
		LinkAmazon: "https://www.amazon.com/dp/B000OTHER",
	}
	o := models.MarketOffer{
		UnitPrice: 18, Channel: "shop", Merchant: "Walmart",
		Link: "https://www.walmart.com/ip/Item/999",
	}
	lnk := bestPriceObservationLinks(o, parent)
	if lnk == nil || !strings.Contains(lnk.URL, "walmart.com/ip/") {
		t.Fatalf("want offer walmart link, got %+v", lnk)
	}
	if lnk.URLKind != "merchant_pdp" {
		t.Fatalf("kind=%q", lnk.URLKind)
	}
}

func TestBestPriceObservationLinksNeverMisattributesMerchant(t *testing.T) {
	// Newegg price must NOT get parent Home Depot URL (common bad evidence case).
	parent := models.CommercialReference{
		LinkShop:   "https://www.homedepot.com/p/Wooster-R091/203150945",
		PriceURL:   "https://www.homedepot.com/p/Wooster-R091/203150945",
		LinkAmazon: "https://www.amazon.com/dp/B000DZGQIM",
		MarketOffers: []models.MarketOffer{
			{Merchant: "Home Depot", Link: "https://www.homedepot.com/p/Wooster-R091/203150945", UnitPrice: 40.35},
			{Merchant: "Ace Hardware", Link: "https://www.acehardware.com/p/1098904", UnitPrice: 33.99},
		},
	}
	newegg := models.MarketOffer{
		UnitPrice: 20.39, Channel: "shop", Merchant: "Newegg.com",
		// No link / dead link stripped — must not invent HD URL.
		Link: "",
	}
	if lnk := bestPriceObservationLinks(newegg, parent); lnk != nil {
		t.Fatalf("newegg without own link must not inherit HD url: %+v", lnk)
	}
	// Ace with own link keeps Ace.
	ace := models.MarketOffer{
		UnitPrice: 33.99, Channel: "shop", Merchant: "Ace Hardware",
		Link: "https://www.acehardware.com/p/1098904?x429=true&utm_source=google",
	}
	lnk := bestPriceObservationLinks(ace, parent)
	if lnk == nil || !strings.Contains(lnk.URL, "acehardware.com") {
		t.Fatalf("ace should keep ace url: %+v", lnk)
	}
	if strings.Contains(lnk.URL, "utm_source") {
		t.Fatalf("tracking params should be stripped: %s", lnk.URL)
	}
	// Home Depot price can use parent HD link.
	hd := models.MarketOffer{UnitPrice: 40.35, Channel: "shop", Merchant: "Home Depot", Link: ""}
	lnk = bestPriceObservationLinks(hd, parent)
	if lnk == nil || !strings.Contains(lnk.URL, "homedepot.com/p/") {
		t.Fatalf("home depot may use parent HD link: %+v", lnk)
	}
}

func TestCleanEvidenceURLFixesBrokenQuery(t *testing.T) {
	// Live bug: missing ? before &intsrc= → parseable then tracking stripped.
	got := cleanEvidenceURL("https://www.homedepot.com/p/Wooster/203150945&intsrc=CATF_2950")
	if strings.Contains(got, "945&intsrc") {
		t.Fatalf("broken &query should be fixed/stripped, got %q", got)
	}
	if !strings.Contains(got, "homedepot.com/p/Wooster/203150945") {
		t.Fatalf("path should remain, got %q", got)
	}
	if strings.Contains(got, "intsrc") {
		t.Fatalf("intsrc tracking should be stripped: %q", got)
	}
}

func TestHostMatchesMerchant(t *testing.T) {
	if !hostMatchesMerchant("https://www.homedepot.com/p/x", "Home Depot") {
		t.Fatal("hd")
	}
	if hostMatchesMerchant("https://www.homedepot.com/p/x", "Newegg.com") {
		t.Fatal("hd must not match newegg")
	}
	if !hostMatchesMerchant("https://www.walmart.com/ip/x", "Walmart - Supply the Home") {
		t.Fatal("walmart marketplace seller label")
	}
	if !hostMatchesMerchant("https://www.amazon.com/dp/B000", "Amazon Marketplace Used") {
		t.Fatal("amazon")
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
