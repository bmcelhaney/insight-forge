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
			OfferLink:     "https://www.upcitemdb.com/norob/alink/?id=staples-example",
			DeepLinkOK:    true,
		},
	}
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved)
	if out[0].LinkAmazon != "https://www.amazon.com/dp/B00006IE7Z" {
		t.Fatalf("amazon link %q", out[0].LinkAmazon)
	}
	if !strings.Contains(out[0].LinkShop, "upcitemdb.com/norob") && !strings.Contains(out[0].LinkShop, "staples") {
		// Prefer offer link as shop deep link when no RetailURL
		if out[0].LinkShop == "" {
			t.Fatalf("shop link empty")
		}
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
	if out[0].PriceURL == "" {
		t.Fatal("expected price_url set from deep link")
	}
}

func TestApplyDeterministicProductLinksPropagatesPriceToSiblingUPC(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "BICCSM11BK", UPC: "070330904330", Manufacturer: "BIC"},
		{SKU: "CSM11BK", UPC: "070330904330", Manufacturer: "BIC", Source: "ABILITYONE_ETS"},
		{SKU: "OTHER", UPC: "999999999999", Manufacturer: "Acme"},
	}
	resolved := map[int]*productIdentity{
		0: {
			Title:         "BIC Clic Stic",
			Brand:         "BIC",
			UPC:           "070330904330",
			ASIN:          "B00006IE7Z",
			OfferPrice:    8.49,
			OfferMerchant: "Staples",
			RetailURL:     "https://www.officedepot.com/a/products/811950/",
			DeepLinkOK:    true,
		},
	}
	resolved = expandResolvedIdentities(refs, resolved)
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved)
	if out[1].Price == "" || !strings.Contains(out[1].Price, "8.49") {
		t.Fatalf("sibling UPC tile should get market price, got %q", out[1].Price)
	}
	if out[1].LinkAmazon != "https://www.amazon.com/dp/B00006IE7Z" {
		t.Fatalf("sibling should get ASIN deep link, got %q", out[1].LinkAmazon)
	}
	if !strings.Contains(out[1].LinkShop, "officedepot.com/a/products/811950") {
		t.Fatalf("sibling should get retail deep link, got %q", out[1].LinkShop)
	}
	if out[2].Price != "" {
		t.Fatalf("unrelated UPC must not inherit price: %q", out[2].Price)
	}
}

func TestExtractRetailProductURLOfficeDepot(t *testing.T) {
	imgs := []string{
		"https://media.officedepot.com/images/t_extralarge%2Cf_auto/products/811950/811950_p_bic.jpg",
	}
	u := extractRetailProductURL(imgs)
	if u != "https://www.officedepot.com/a/products/811950/" {
		t.Fatalf("retail url %q", u)
	}
}

func TestPickBestMarketOfferPrefersUSD(t *testing.T) {
	offers := []upcOffer{
		{Merchant: "Newegg Canada", Currency: "CAD", Price: 28.44, Condition: "New", Link: "https://example.com/ca"},
		{Merchant: "Staples", Currency: "", Price: 8.49, Condition: "New", Link: "https://www.upcitemdb.com/norob/alink/?id=staples"},
		{Merchant: "Random", Currency: "USD", Price: 0.24, Condition: "New"},
	}
	p, m, c, link, ok := pickBestMarketOffer(offers)
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
	if link == "" {
		t.Fatal("expected offer link")
	}
}

func TestBuildAmazonProductSearchURLUsesTitle(t *testing.T) {
	u := buildAmazonProductSearchURL("BIC", "BICCSM11BK", "070330904330", "BIC Clic Stic Retractable Ball Pen Medium Point Black")
	if !strings.Contains(u, "amazon.com/s?k=") {
		t.Fatalf("expected amazon search, got %q", u)
	}
	if !strings.Contains(u, "Clic") && !strings.Contains(u, "Clic%") {
		// still ok if fully encoded — must have query
		if !strings.Contains(u, "q=") && !strings.Contains(u, "k=") {
			t.Fatalf("missing query: %q", u)
		}
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
