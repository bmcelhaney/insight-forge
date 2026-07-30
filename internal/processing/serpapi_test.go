package processing

import (
	"strings"
	"testing"
)

func TestIdentityFromShoppingHitsBuildsRangeAndLinks(t *testing.T) {
	hits := []shoppingHit{
		{Title: "Wooster Sherlock Extension Pole 4-8", Price: 40.35, Link: "https://www.homedepot.com/p/123", Source: "The Home Depot"},
		{Title: "Wooster Sherlock Gt Pole", Price: 35.99, Link: "https://www.amazon.com/dp/B000DZGQIM", Source: "Amazon.com"},
		{Title: "Wooster Extension Pole", Price: 47.77, Link: "https://www.walmart.com/ip/999", Source: "Walmart"},
		{Title: "Other pole", Price: 22.00, Link: "https://www.acehardware.com/x", Source: "Ace Hardware"},
	}
	id := identityFromShoppingHits(hits, "R091", "WOOSTER")
	if id.UPCCount < 4 || id.UPCMin != 22.00 || id.UPCMax != 47.77 {
		t.Fatalf("catalog range min=%v max=%v n=%d", id.UPCMin, id.UPCMax, id.UPCCount)
	}
	if id.AmazonPrice != 35.99 || id.ASIN != "B000DZGQIM" {
		t.Fatalf("amazon price=%v asin=%q", id.AmazonPrice, id.ASIN)
	}
	if id.ShopPrice <= 0 || id.ShopLink == "" {
		t.Fatalf("shop price/link missing: %+v", id)
	}
	if !id.DeepLinkOK {
		t.Fatal("expected DeepLinkOK")
	}
}

func TestMergeProductIdentityFillsGaps(t *testing.T) {
	a := productIdentity{Title: "A", ASIN: "B000DZGQIM"}
	b := productIdentity{
		Title: "B", OfferPrice: 20, ShopPrice: 22, ShopLink: "https://www.homedepot.com/p/1",
		UPCMin: 20, UPCMax: 40, UPCCount: 5, AmazonPrice: 19, AmazonMerchant: "Amazon",
	}
	m := mergeProductIdentity(a, b)
	if m.ASIN != "B000DZGQIM" {
		t.Fatalf("keep asin %q", m.ASIN)
	}
	if m.ShopPrice != 22 || m.UPCCount != 5 || m.AmazonPrice != 19 {
		t.Fatalf("merge fill failed: %+v", m)
	}
}

func TestParseMoneyToFloat(t *testing.T) {
	if parseMoneyToFloat("$1,234.56") != 1234.56 {
		t.Fatalf("got %v", parseMoneyToFloat("$1,234.56"))
	}
	if parseMoneyToFloat("40.35") != 40.35 {
		t.Fatal("plain")
	}
}

func TestSerpAPIDisabledWithoutKey(t *testing.T) {
	ConfigureSerpAPI("", 8)
	if SerpAPIEnabled() {
		t.Fatal("should be disabled")
	}
	if !strings.Contains(serpStatusMessage(), "not configured") {
		t.Fatal(serpStatusMessage())
	}
}
