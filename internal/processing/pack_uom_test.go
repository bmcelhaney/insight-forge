package processing

import (
	"strings"
	"testing"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestParsePackUOM(t *testing.T) {
	cases := []struct {
		in       string
		wantQty  int
		wantUnit string
	}{
		{"Glue Top Pads, Wide/Legal Rule, 50 White 8.5 x 11 Sheets, Dozen", 12, "DZ"},
		{"72/Carton Roaring Spring pads", 72, "CT"},
		{"Case of 24 paper towels", 24, "CS"},
		{"12-pack ballpoint pens", 12, "PK"},
		{"Pack of 6 highlighters", 6, "PK"},
		{"Box of 10 dry erase markers", 10, "BX"},
		{"Copy paper ream", 500, "RM"},
		{"pair of gloves", 2, "PR"},
		{"Set of 4 binders", 4, "SET"},
		{"24 count multipurpose cleaner", 24, "PK"},
		{"100/PK rubber bands", 100, "PK"},
		{"simple description no pack", 0, ""},
		{"UNIT OF ISSUE: DZ", 12, "DZ"},
		{"Writing Pad - Letter - White EA", 1, "EA"},
	}
	for _, tc := range cases {
		got := parsePackUOM(tc.in)
		if got.PackQuantity != tc.wantQty || got.Unit != tc.wantUnit {
			t.Errorf("%q: got qty=%d unit=%q want qty=%d unit=%q (label=%q)",
				tc.in, got.PackQuantity, got.Unit, tc.wantQty, tc.wantUnit, got.Label)
		}
	}
}

func TestEnrichMarketOfferPack(t *testing.T) {
	o := models.MarketOffer{UnitPrice: 24.00, Quantity: 1, Currency: "USD"}
	enrichMarketOfferPack(&o, "AmPad Glue Top Pads Dozen")
	if o.Quantity != 12 {
		t.Fatalf("quantity: got %d want 12", o.Quantity)
	}
	if o.Unit != "DZ" {
		t.Fatalf("unit: got %q", o.Unit)
	}
	if o.PricePerEach != 2.0 {
		t.Fatalf("price_per_each: got %v want 2.0", o.PricePerEach)
	}
	if o.PackLabel == "" {
		t.Fatal("expected pack_label")
	}
}

func TestEnrichCommercialMarketOffers(t *testing.T) {
	r := &models.CommercialReference{
		Description: "Writing pads, 50 sheets, Dozen",
		MarketOffers: []models.MarketOffer{
			{UnitPrice: 36.00, Quantity: 1, Source: "SERPAPI", Title: "Legal Pads 12 Pack", Channel: "shop"},
		},
	}
	enrichCommercialMarketOffers(r)
	o := r.MarketOffers[0]
	// Title "12 Pack" or description "Dozen" — either yields pack > 1
	if o.Quantity <= 1 {
		t.Fatalf("expected pack quantity > 1, got %d (unit=%s label=%s)", o.Quantity, o.Unit, o.PackLabel)
	}
	if o.PricePerEach <= 0 {
		t.Fatal("expected price_per_each")
	}
}

func TestNormalizeCommercialDisplayPrices_PerEach(t *testing.T) {
	r := &models.CommercialReference{
		Description: "Glue Top Pads, Dozen",
		Price:       "$36.00",
		PriceShop:   "$36.00",
		MarketOffers: []models.MarketOffer{
			{UnitPrice: 36.00, Quantity: 12, PricePerEach: 3.0, Unit: "DZ", Channel: "shop", Source: "SERPAPI"},
			{UnitPrice: 48.00, Quantity: 12, PricePerEach: 4.0, Unit: "DZ", Channel: "shop", Source: "SERPAPI"},
			{UnitPrice: 34.84, Quantity: 12, PricePerEach: 2.9, Unit: "DZ", Channel: "federal", Source: "ABILITYONE_COM"},
		},
	}
	normalizeCommercialDisplayPrices(r)
	if r.PriceBasis != "each" {
		t.Fatalf("price_basis: got %q", r.PriceBasis)
	}
	if !strings.Contains(r.PriceShop, "/ea") {
		t.Fatalf("PriceShop should be per-each, got %q", r.PriceShop)
	}
	if !strings.Contains(r.Price, "/ea") {
		t.Fatalf("Price should be per-each, got %q", r.Price)
	}
	// Range should use 3–4 not 36–48
	if strings.Contains(r.PriceShop, "36") || strings.Contains(r.PriceShop, "48") {
		t.Fatalf("expected normalized dollars, got %q", r.PriceShop)
	}
	if !strings.Contains(r.PriceShop, "3.00") && !strings.Contains(r.PriceShop, "$3") {
		t.Fatalf("expected ~$3 low, got %q", r.PriceShop)
	}
}

func TestFormatPerEachDisplay(t *testing.T) {
	d, isR := formatPerEachDisplay([]float64{1.25, 2.00, 1.50})
	if !isR || !strings.Contains(d, "/ea") || !strings.Contains(d, "1.25") {
		t.Fatalf("got %q isRange=%v", d, isR)
	}
	d2, isR2 := formatPerEachDisplay([]float64{3.0})
	if isR2 || d2 != "$3.00 /ea" {
		t.Fatalf("single: got %q isRange=%v", d2, isR2)
	}
}
