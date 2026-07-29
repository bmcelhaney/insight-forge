package processing

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestExtractCommercialReferencesSkipsWebFLIS(t *testing.T) {
	snaps := []models.DataSnapshot{
		{
			SourceCode: "WEBFLIS",
			RawResponse: map[string]any{
				"commercial_references": []map[string]any{
					{"sku": "FAKE-123", "upc": "012345678901", "manufacturer": "Synthetic"},
				},
			},
		},
		{
			SourceCode: "ABILITYONE_ETS",
			RawResponse: map[string]any{
				"commercial_references": []map[string]any{
					{
						"sku":                    "000103M4560",
						"upc":                    "051141456017",
						"manufacturer":           "3M",
						"commercial_description": "Slip resistant tread",
						"date_added":             "01-Apr-2017",
					},
				},
			},
		},
	}

	refs := extractCommercialReferences(snaps)
	if len(refs) != 1 {
		t.Fatalf("expected 1 real ref, got %d", len(refs))
	}
	if refs[0].SKU != "000103M4560" {
		t.Fatalf("unexpected sku %q", refs[0].SKU)
	}
	if refs[0].Source != "ABILITYONE_ETS" {
		t.Fatalf("unexpected source %q", refs[0].Source)
	}
}

func TestEnrichCommercialReferencesAddsResilientLinksAndMergesGSAPrice(t *testing.T) {
	snaps := []models.DataSnapshot{
		{
			SourceCode: "GSA_ADVANTAGE",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"search_url": "https://www.gsaadvantage.gov/example",
				"prices_found": []map[string]any{
					{"mfr_part": "000103M4560", "price": "12.50", "upc": "051141456017"},
				},
			},
		},
	}
	refs := []models.CommercialReference{
		{
			SKU:          "000103M4560",
			UPC:          "51141456017", // 11-digit → pad
			Manufacturer: "3M",
			Source:       "ABILITYONE_ETS",
			Description:  "Slip resistant tread",
		},
	}

	out := enrichCommercialReferences("7220016481769", refs, snaps)
	if len(out) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(out))
	}
	r := out[0]
	if r.UPC != "051141456017" {
		t.Fatalf("expected padded UPC, got %q", r.UPC)
	}
	if r.Price == "" || !strings.Contains(r.Price, "12.50") {
		t.Fatalf("expected merged GSA price, got %q", r.Price)
	}
	if r.PriceSource != "GSA_ADVANTAGE" {
		t.Fatalf("expected GSA price source, got %q", r.PriceSource)
	}
	if r.LinkShop == "" || !strings.Contains(r.LinkShop, "google.com/search") {
		t.Fatalf("expected shop search link, got %q", r.LinkShop)
	}
	if r.LinkUPC == "" {
		t.Fatalf("expected UPC link")
	}
	if r.LinkGSA == "" {
		t.Fatalf("expected GSA link")
	}
	if r.LinkWebsite != "https://www.3m.com" {
		t.Fatalf("expected 3M homepage, got %q", r.LinkWebsite)
	}
}

func TestLooksLikeProductSKU(t *testing.T) {
	if !looksLikeProductSKU("000103M4560") {
		t.Fatal("product-like SKU rejected")
	}
	if looksLikeProductSKU("123456789012345") {
		t.Fatal("long numeric contract should be rejected")
	}
}

func TestExtractFiltersPartsBaseContractIDs(t *testing.T) {
	snaps := []models.DataSnapshot{
		{
			SourceCode: "PARTSBASE",
			RawResponse: map[string]any{
				"commercial_references": []map[string]any{
					{"sku": "47QSEA18D000Y", "manufacturer": "Some Vendor", "price": "9.99"},
					{"sku": "TG-100", "manufacturer": "Tough Guy", "price": "4.50"},
				},
			},
		},
	}
	refs := extractCommercialReferences(snaps)
	if len(refs) != 1 {
		t.Fatalf("expected only product-like PartsBase ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].SKU != "TG-100" {
		t.Fatalf("got %q", refs[0].SKU)
	}
}

func TestProbeCommercialPricesFillsFromSearch(t *testing.T) {
	clearCommercialPriceCache()
	prev := commercialPriceSearch
	t.Cleanup(func() {
		commercialPriceSearch = prev
		clearCommercialPriceCache()
		os.Unsetenv("IF_COMMERCIAL_PRICE_PROBES")
	})

	var calls atomic.Int32
	commercialPriceSearch = func(ctx context.Context, term string) ([]map[string]any, error) {
		calls.Add(1)
		return []map[string]any{{"price": "7.25", "mfr_part": term, "price_source": "ABILITYONE_COM"}}, nil
	}
	os.Setenv("IF_COMMERCIAL_PRICE_PROBES", "5")

	refs := []models.CommercialReference{
		{SKU: "ABC-100", Manufacturer: "Acme", Source: "ABILITYONE_ETS"},
		{SKU: "ALREADY", Price: "$1.00", Source: "GSA_ADVANTAGE"},
	}
	out := probeCommercialPrices(context.Background(), refs)
	if out[0].Price == "" || !strings.Contains(out[0].Price, "7.25") {
		t.Fatalf("expected probed price, got %#v", out[0])
	}
	if out[0].PriceSource != "ABILITYONE_COM" {
		t.Fatalf("price source: %q", out[0].PriceSource)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 search call, got %d", calls.Load())
	}

	// Cache hit should avoid a second network call.
	calls.Store(0)
	out2 := probeCommercialPrices(context.Background(), []models.CommercialReference{
		{SKU: "ABC-100", Manufacturer: "Acme", Source: "ABILITYONE_ETS"},
	})
	if out2[0].Price == "" {
		t.Fatal("expected cached price")
	}
	if calls.Load() != 0 {
		t.Fatalf("expected cache hit (0 calls), got %d", calls.Load())
	}
}

func TestProbeCommercialPricesDisabled(t *testing.T) {
	clearCommercialPriceCache()
	prev := commercialPriceSearch
	t.Cleanup(func() {
		commercialPriceSearch = prev
		os.Unsetenv("IF_COMMERCIAL_PRICE_PROBES")
	})
	var calls atomic.Int32
	commercialPriceSearch = func(ctx context.Context, term string) ([]map[string]any, error) {
		calls.Add(1)
		return []map[string]any{{"price": "1.00"}}, nil
	}
	os.Setenv("IF_COMMERCIAL_PRICE_PROBES", "0")
	out := probeCommercialPrices(context.Background(), []models.CommercialReference{
		{SKU: "ABC-100", Source: "ABILITYONE_ETS"},
	})
	if out[0].Price != "" {
		t.Fatalf("expected no probe when disabled, got %q", out[0].Price)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 calls, got %d", calls.Load())
	}
}

func TestNSNChannelPriceAppliedToETSRefs(t *testing.T) {
	snaps := []models.DataSnapshot{{
		SourceCode: "ABILITYONE_COMMERCE",
		Value:      13.01,
		RawResponse: map[string]any{
			"best_price": 13.01,
			"best_sku":   "7520-00-935-7136",
			"best_name":  "U.S. Government Pen",
			"search_url": "https://www.abilityone.com/search?q=7520-00-935-7136",
			"price_as_of": "2026-07-29",
		},
	}}
	refs := []models.CommercialReference{
		{SKU: "BICCSM11BK", Manufacturer: "BIC", Source: "ABILITYONE_ETS"},
	}
	out := enrichCommercialReferences("7520009357136", refs, snaps)
	if len(out) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(out))
	}
	if out[0].Price == "" || !strings.Contains(out[0].Price, "13.01") {
		t.Fatalf("expected NSN channel price applied, got %q", out[0].Price)
	}
	if out[0].PriceSource != "ABILITYONE_COM" {
		t.Fatalf("price source %q", out[0].PriceSource)
	}
}
