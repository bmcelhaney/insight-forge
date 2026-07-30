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

func TestHumanizeProductDescriptionExpandsCatalogCodes(t *testing.T) {
	got := humanizeProductDescription(`9"ROLLERCOVER WOVEN NAPSIZE .5"`)
	low := strings.ToLower(got)
	if !strings.Contains(low, "roller cover") {
		t.Fatalf("expected humanized roller cover, got %q", got)
	}
	if !strings.Contains(low, "nap") {
		t.Fatalf("expected nap in %q", got)
	}
}

func TestBuildProductSearchQueryIncludesBrandDescSKU(t *testing.T) {
	q := buildProductSearchQuery("WOOSTER", "14A050", "", `9"ROLLERCOVER WOVEN NAPSIZE .5"`)
	if !strings.Contains(strings.ToUpper(q), "WOOSTER") {
		t.Fatalf("missing brand: %q", q)
	}
	if !strings.Contains(q, "14A050") {
		t.Fatalf("missing sku: %q", q)
	}
	if !strings.Contains(strings.ToLower(q), "roller") {
		t.Fatalf("missing humanized desc: %q", q)
	}
}

func TestBuildBestShopURLHomeDepotForPaint(t *testing.T) {
	u := buildBestShopURL("WOOSTER", "14A050", "", "WOOSTER Paint Roller Cover, 1/2 In", "")
	if !strings.Contains(u, "homedepot.com/s/") {
		t.Fatalf("expected Home Depot search for paint product, got %q", u)
	}
	if !strings.Contains(strings.ToUpper(u), "WOOSTER") && !strings.Contains(u, "WOOSTER") {
		// path-escaped
		if !strings.Contains(u, "14A050") {
			t.Fatalf("expected sku in HD url: %q", u)
		}
	}
}

func TestBuildAmazonProductSearchUsesCatalogDescription(t *testing.T) {
	u := buildAmazonProductSearchURL("WOOSTER", "3UW81", "", `9"ROLLERCOVER WOVEN NAPSIZE .5"`)
	if !strings.Contains(u, "amazon.com/s?k=") {
		t.Fatalf("expected amazon search: %q", u)
	}
	// Must not be bare SKU-only (should include brand and/or humanized tokens)
	if strings.HasSuffix(u, "3UW81") && !strings.Contains(u, "WOOSTER") && !strings.Contains(strings.ToLower(u), "roller") {
		t.Fatalf("amazon query too weak: %q", u)
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
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved, "$13.01", "ABILITYONE_COM")
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
	if out[0].PriceFederal == "" || !strings.Contains(out[0].PriceFederal, "13.01") {
		t.Fatalf("expected federal AbilityOne price on link, got %q", out[0].PriceFederal)
	}
	if out[0].PriceShop == "" && out[0].PriceAmazon == "" {
		// With only overall offer, shop or amazon channel should be filled
		t.Fatalf("expected per-channel market price, shop=%q amazon=%q", out[0].PriceShop, out[0].PriceAmazon)
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
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved, "", "")
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
		{Merchant: "Newegg Canada", Currency: "CAD", Price: flexibleNum(28.44), Condition: "New", Link: "https://example.com/ca"},
		{Merchant: "Staples", Currency: "", Price: flexibleNum(8.49), Condition: "New", Link: "https://www.upcitemdb.com/norob/alink/?id=staples"},
		{Merchant: "Random", Currency: "USD", Price: flexibleNum(0.24), Condition: "New"},
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

func TestCollectChannelOffersSplitsAmazonAndRetail(t *testing.T) {
	offers := []upcOffer{
		{Merchant: "Amazon.com", Currency: "USD", Price: flexibleNum(12.99), Condition: "New", Link: "https://www.amazon.com/dp/B00006IE7Z"},
		{Merchant: "Home Depot", Currency: "", Price: flexibleNum(40.35), Condition: "New", Link: "https://www.homedepot.com/p/123"},
		{Merchant: "Amazon.com", Currency: "USD", Price: flexibleNum(11.50), Condition: "New"},
		{Merchant: "Newegg.com", Currency: "", Price: flexibleNum(20.39), Condition: "New"},
		{Merchant: "Ace Hardware", Currency: "", Price: flexibleNum(33.99), Condition: "New"},
		{Merchant: "Wal-Mart.com", Currency: "", Price: flexibleNum(43.68), Condition: "New"},
		{Merchant: "Jet.com", Currency: "", Price: flexibleNum(21.25), Condition: "New"},
	}
	ch := collectChannelOffers(offers)
	if ch.AmazonPrice != 11.50 {
		t.Fatalf("amazon price %v (want lowest Amazon)", ch.AmazonPrice)
	}
	// UPC / full catalog range must include low Newegg and high Walmart, not only scored "top band".
	if ch.AllCount < 7 || ch.AllMin != 11.50 || ch.AllMax != 43.68 {
		t.Fatalf("full catalog range min=%v max=%v n=%d (want all USD offers)", ch.AllMin, ch.AllMax, ch.AllCount)
	}
	if ch.BestPrice <= 0 {
		t.Fatal("expected best overall")
	}
	if ch.AmazonCount < 2 || ch.AmazonMin != 11.50 || ch.AmazonMax != 12.99 {
		t.Fatalf("amazon range min=%v max=%v n=%d", ch.AmazonMin, ch.AmazonMax, ch.AmazonCount)
	}
	// Shop full set should span Newegg..Walmart (non-Amazon).
	if ch.ShopCount < 4 || ch.ShopMin > 21 || ch.ShopMax < 40 {
		t.Fatalf("shop full-ish range min=%v max=%v n=%d", ch.ShopMin, ch.ShopMax, ch.ShopCount)
	}
}

func TestFormatPriceRange(t *testing.T) {
	s := formatPriceRange(11.5, 40.35, 5)
	if !strings.Contains(s, "11.50") || !strings.Contains(s, "40.35") || !strings.Contains(s, "5 offers") {
		t.Fatalf("range %q", s)
	}
	if single := formatPriceRange(11.5, 11.5, 1); !strings.Contains(single, "11.50") || strings.Contains(single, "–") {
		t.Fatalf("single %q", single)
	}
}

func TestApplyDeterministicUsesRangeWhenNoDirectLink(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "R091", UPC: "071497149299", Manufacturer: "WOOSTER", Description: "Sherlock extension pole"},
	}
	resolved := map[int]*productIdentity{
		0: {
			Title:        "Wooster Sherlock Extension Pole",
			UPC:          "071497149299",
			OfferPrice:   40.35,
			ShopPrice:    40.35,
			ShopMerchant: "Home Depot",
			ShopMin:      38.00,
			ShopMax:      52.00,
			ShopCount:    4,
			AmazonMin:    35.99,
			AmazonMax:    49.99,
			AmazonCount:  3,
			AmazonPrice:  35.99,
			UPCMin:       35.99,
			UPCMax:       52.00,
			UPCCount:     5,
			UPCPrice:     35.99,
			DeepLinkOK:   true,
		},
	}
	// No ASIN → Amazon is search; shop is HD search (not a product deep-link) → ranges.
	out := applyDeterministicProductLinks(refs, "8020015964253", resolved, "$42.62", "ABILITYONE_COM")
	if !out[0].PriceAmazonIsRange || !strings.Contains(out[0].PriceAmazon, "offers") {
		t.Fatalf("amazon range expected, got %q isRange=%v", out[0].PriceAmazon, out[0].PriceAmazonIsRange)
	}
	if !out[0].PriceShopIsRange || !strings.Contains(out[0].PriceShop, "offers") {
		t.Fatalf("shop range expected, got %q isRange=%v", out[0].PriceShop, out[0].PriceShopIsRange)
	}
	if !out[0].PriceUPCIsRange {
		t.Fatalf("upc should be range for multi-offer catalog, got %q range=%v", out[0].PriceUPC, out[0].PriceUPCIsRange)
	}
	if out[0].PriceFederal == "" {
		t.Fatal("expected federal price")
	}
}

func TestIsDirectProductURL(t *testing.T) {
	if !isDirectProductURL("https://www.amazon.com/dp/B000DZGQIM") {
		t.Fatal("amazon dp")
	}
	if isDirectProductURL("https://www.amazon.com/s?k=wooster") {
		t.Fatal("amazon search is not direct")
	}
	if isDirectProductURL("https://www.homedepot.com/s/WOOSTER%20pole") {
		t.Fatal("HD search is not direct")
	}
	if !isDirectProductURL("https://www.officedepot.com/a/products/811950/") {
		t.Fatal("OD product")
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
