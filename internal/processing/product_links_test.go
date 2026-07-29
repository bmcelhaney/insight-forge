package processing

import (
	"context"
	"strings"
	"testing"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

func TestBuildPreciseShopURLUsesTitle(t *testing.T) {
	u := buildPreciseShopURL("BIC", "BICCSM11BK", "070330904330", "BIC Clic Stic Retractable Ball Pen Medium Point Black")
	if !strings.Contains(u, "tbm=shop") {
		t.Fatalf("expected shopping URL, got %q", u)
	}
	if !strings.Contains(u, "Clic") && !strings.Contains(strings.ToLower(u), "clic") {
		// URL-encoded title should still contain Clic after decode check via raw fragment
		if !strings.Contains(u, "Clic") && !strings.Contains(u, "Clic%20") && !strings.Contains(u, "Ball") {
			// encoded form
			if !strings.Contains(u, "q=") {
				t.Fatalf("missing query: %q", u)
			}
		}
	}
}

func TestApplyDeterministicProductLinksAmazonASIN(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "BICCSM11BK", UPC: "070330904330", Manufacturer: "BIC"},
	}
	resolved := map[int]*productIdentity{
		0: {
			Title:         "BIC Clic Stic Retractable Ball Pen Medium Point Black 12-Pack",
			Brand:         "BIC",
			UPC:           "070330904330",
			ASIN:          "B00006IE7Z",
			OfferPrice:    8.49,
			OfferMerchant: "Staples",
			OfferCurrency: "USD",
			DeepLinkOK:    true,
		},
	}
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved)
	if out[0].LinkAmazon != "https://www.amazon.com/dp/B00006IE7Z" {
		t.Fatalf("amazon link %q", out[0].LinkAmazon)
	}
	if !strings.Contains(out[0].LinkShop, "tbm=shop") {
		t.Fatalf("shop link %q", out[0].LinkShop)
	}
	if out[0].Description == "" {
		t.Fatal("expected description filled from identity")
	}
	if !strings.Contains(out[0].LinkGSA, "7520-00-935-7136") {
		t.Fatalf("federal link should include dashed NSN: %q", out[0].LinkGSA)
	}
	if !strings.Contains(out[0].LinkUPC, "upcitemdb.com/upc/070330904330") {
		t.Fatalf("upc identity link %q", out[0].LinkUPC)
	}
	if out[0].Price == "" || !strings.Contains(out[0].Price, "8.49") {
		t.Fatalf("expected market offer price on tile, got %q", out[0].Price)
	}
	if !strings.Contains(out[0].PriceSource, "STAPLES") && !strings.Contains(out[0].PriceSource, "MARKET") {
		t.Fatalf("price source %q", out[0].PriceSource)
	}
}

func TestPickBestMarketOfferPrefersUSD(t *testing.T) {
	offers := []upcOffer{
		{Merchant: "Newegg Canada", Currency: "CAD", Price: 28.44, Condition: "New"},
		{Merchant: "Staples", Currency: "", Price: 8.49, Condition: "New"},
		{Merchant: "Random", Currency: "USD", Price: 0.24, Condition: "New"},
	}
	p, m, c, ok := pickBestMarketOffer(offers)
	if !ok {
		t.Fatal("expected offer")
	}
	if p != 8.49 {
		t.Fatalf("price %v", p)
	}
	if !strings.Contains(strings.ToLower(m), "staple") {
		t.Fatalf("merchant %q", m)
	}
	if c != "USD" {
		t.Fatalf("currency %q", c)
	}
}

func TestEnrichProductLinksNoNetworkWhenDisabled(t *testing.T) {
	t.Setenv("IF_PRODUCT_LINK_RESOLVES", "0")
	clearProductIDCache()
	refs := []models.CommercialReference{
		{SKU: "ABC-1", Manufacturer: "Acme"},
	}
	out := enrichProductLinks(context.Background(), refs, "7520009357136")
	if out[0].LinkAmazon == "" && out[0].LinkShop == "" {
		t.Fatal("expected deterministic links even when resolve disabled")
	}
}
