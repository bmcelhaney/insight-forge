package processing

import (
	"strings"
	"testing"
)

func TestIdentityFromShoppingHitsBuildsRangeAndLinks(t *testing.T) {
	hits := []shoppingHit{
		{Title: "Wooster Sherlock Extension Pole 4-8 R091", Price: 40.35, Link: "https://www.homedepot.com/p/123", Source: "The Home Depot"},
		{Title: "Wooster Sherlock Gt Pole R091", Price: 35.99, Link: "https://www.amazon.com/dp/B000DZGQIM", Source: "Amazon.com"},
		{Title: "Wooster Extension Pole R091", Price: 47.77, Link: "https://www.walmart.com/ip/999", Source: "Walmart"},
		{Title: "Unrelated bulk pole kit", Price: 598.00, Link: "https://www.example.com/bulk", Source: "BulkCo"},
	}
	id := identityFromShoppingHits(hits, "R091", "WOOSTER")
	if id.UPCCount < 2 || id.UPCMax > 100 {
		t.Fatalf("catalog range should drop bulk outlier min=%v max=%v n=%d", id.UPCMin, id.UPCMax, id.UPCCount)
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
	ConfigureSerpAPI("", 8, true)
	if SerpAPIEnabled() {
		t.Fatal("should be disabled")
	}
	if SerpAPIImmersiveEnabled() {
		t.Fatal("immersive should be off without key")
	}
	if !strings.Contains(serpStatusMessage(), "not configured") {
		t.Fatal(serpStatusMessage())
	}
}

func TestSerpAPIImmersiveKillSwitch(t *testing.T) {
	ConfigureSerpAPI("test-key", 8, false)
	if !SerpAPIEnabled() {
		t.Fatal("key should enable Serp")
	}
	if SerpAPIImmersiveEnabled() {
		t.Fatal("immersive kill-switch should disable P2")
	}
	if !strings.Contains(serpStatusMessage(), "shopping only") {
		t.Fatalf("status=%s", serpStatusMessage())
	}
	ConfigureSerpAPI("test-key", 8, true)
	if !SerpAPIImmersiveEnabled() {
		t.Fatal("immersive should be on")
	}
	if !strings.Contains(serpStatusMessage(), "immersive") {
		t.Fatalf("status=%s", serpStatusMessage())
	}
	// Clean up so other tests don't see a fake key.
	ConfigureSerpAPI("", 8, true)
}

func TestPickBestImmersiveToken(t *testing.T) {
	hits := []shoppingHit{
		{Title: "Generic pole", Price: 10, ImmersiveToken: "tok-generic"},
		{Title: "Wooster Sherlock R091", Price: 40, ImmersiveToken: "tok-r091"},
		{Title: "No token Wooster R091", Price: 41},
	}
	got := pickBestImmersiveToken(hits, "R091", "WOOSTER")
	if got != "tok-r091" {
		t.Fatalf("want tok-r091 got %q", got)
	}
	if pickBestImmersiveToken(hits[:1], "R091", "WOOSTER") != "tok-generic" {
		t.Fatal("fallback to only available token")
	}
	if pickBestImmersiveToken(nil, "R091", "WOOSTER") != "" {
		t.Fatal("empty hits")
	}
}

func TestMergeImmersiveStoreHitsDedupes(t *testing.T) {
	base := []shoppingHit{
		{Title: "Pole", Price: 40.35, Source: "The Home Depot", Link: "https://hd.example/1"},
	}
	stores := []shoppingHit{
		{Title: "Pole", Price: 40.35, Source: "The Home Depot", Link: "https://hd.example/1"}, // dup
		{Title: "Pole", Price: 35.99, Source: "Amazon.com", Link: "https://amazon.com/dp/x"},
		{Title: "Pole", Price: 47.77, Source: "Walmart", Link: "https://walmart.com/ip/y"},
	}
	merged := mergeImmersiveStoreHits(base, stores)
	if len(merged) != 3 {
		t.Fatalf("want 3 unique offers got %d", len(merged))
	}
	id := identityFromShoppingHits(merged, "R091", "WOOSTER")
	if id.UPCCount < 3 {
		t.Fatalf("expected multi-store range n=%d", id.UPCCount)
	}
	if id.AmazonPrice != 35.99 {
		t.Fatalf("amazon from immersive %v", id.AmazonPrice)
	}
}
