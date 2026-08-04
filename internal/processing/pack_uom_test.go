package processing

import (
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
			{UnitPrice: 36.00, Quantity: 1, Source: "SERPAPI", Title: "Legal Pads 12 Pack"},
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
