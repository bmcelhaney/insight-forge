package processing

import (
	"strings"
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
