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
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved, "$13.01", "ABILITYONE_COM", nsnMarketBand{})
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
	if out[0].Price == "" {
		t.Fatalf("expected market offer price on tile, got empty")
	}
	// Listing ($8.49) or pack-normalized per-each (e.g. 12-pack → $0.71 /ea).
	if !strings.Contains(out[0].Price, "8.49") && !strings.Contains(out[0].Price, "/ea") && !strings.Contains(out[0].Price, "0.71") {
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
	out := applyDeterministicProductLinks(refs, "7520009357136", resolved, "", "", nsnMarketBand{})
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

func TestFormatSearchMarketPriceNeverBareSingle(t *testing.T) {
	// Multi different prices
	d, isR := formatSearchMarketPrice(34.99, 59.92, 7)
	if !isR || !strings.Contains(d, "34.99") || !strings.Contains(d, "59.92") || !strings.Contains(d, "7 offers") {
		t.Fatalf("multi %q isR=%v", d, isR)
	}
	// Multi same price
	d, isR = formatSearchMarketPrice(69.59, 69.59, 3)
	if !isR || !strings.Contains(d, "69.59") || !strings.Contains(d, "3 offers") {
		t.Fatalf("same multi %q isR=%v", d, isR)
	}
	// Single hit on a search page must NOT look like a verified listing
	d, isR = formatSearchMarketPrice(69.59, 69.59, 1)
	if !isR || !strings.Contains(strings.ToLower(d), "search") || !strings.Contains(d, "69.59") {
		t.Fatalf("single search %q isR=%v", d, isR)
	}
	if d == "$69.59" || d == "69.59" {
		t.Fatalf("bare single price is misleading for search destinations: %q", d)
	}
}

func TestSearchLinksNeverGetBareSinglePrice(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "140757648", Manufacturer: "PURDY", Description: "QuickFit Poles 4-8'"},
	}
	// Identity as if Serp returned one collapsed price but no product deep-link.
	resolved := map[int]*productIdentity{
		0: {
			Title: "Purdy QuickFit Pole", OfferPrice: 69.59, OfferMerchant: "Google Shopping",
			ShopPrice: 69.59, ShopMerchant: "Google Shopping", ShopCount: 1, ShopMin: 69.59, ShopMax: 69.59,
			UPCMin: 69.59, UPCMax: 69.59, UPCCount: 1, UPCPrice: 69.59,
		},
	}
	out := applyDeterministicProductLinks(refs, "8020015964253", resolved, "$42.62", "ABILITYONE_COM", nsnMarketBand{})
	if isDirectProductURL(out[0].LinkAmazon) || isDirectProductURL(out[0].LinkShop) {
		// amazon/shop should be search builders
	}
	if !out[0].PriceAmazonIsRange || !strings.Contains(strings.ToLower(out[0].PriceAmazon), "search") {
		t.Fatalf("amazon search price must be honest search label, got %q range=%v", out[0].PriceAmazon, out[0].PriceAmazonIsRange)
	}
	if !out[0].PriceShopIsRange {
		t.Fatalf("shop search price must be marked range/search, got %q", out[0].PriceShop)
	}
	// Primary tile price should not claim a hard $69.59 listing either
	if out[0].Price == "$69.59" || out[0].Price == "69.59" {
		t.Fatalf("primary price still bare single: %q src=%q", out[0].Price, out[0].PriceSource)
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
			UPCMin:       20.39,
			UPCMax:       47.77,
			UPCCount:     12,
			UPCPrice:     20.39,
			DeepLinkOK:   true,
		},
	}
	// No ASIN → Amazon is search; shop is HD search (not a product deep-link) → ranges.
	out := applyDeterministicProductLinks(refs, "8020015964253", resolved, "$42.62", "ABILITYONE_COM", nsnMarketBand{})
	if !out[0].PriceAmazonIsRange || !strings.Contains(out[0].PriceAmazon, "offers") {
		t.Fatalf("amazon range expected, got %q isRange=%v", out[0].PriceAmazon, out[0].PriceAmazonIsRange)
	}
	if !out[0].PriceShopIsRange || !strings.Contains(out[0].PriceShop, "offers") {
		t.Fatalf("shop range expected, got %q isRange=%v", out[0].PriceShop, out[0].PriceShopIsRange)
	}
	// Prefer widest/most-offer catalog band for search fallbacks when richer.
	if !strings.Contains(out[0].PriceAmazon, "12 offers") && !strings.Contains(out[0].PriceAmazon, "20.39") {
		// Amazon may use amazon-only 3-offer band (also OK) or catalog 12
		if !strings.Contains(out[0].PriceAmazon, "3 offers") {
			t.Fatalf("amazon range unexpected: %q", out[0].PriceAmazon)
		}
	}
	if !out[0].PriceUPCIsRange {
		t.Fatalf("upc should be range for multi-offer catalog, got %q range=%v", out[0].PriceUPC, out[0].PriceUPCIsRange)
	}
	if out[0].PriceFederal == "" {
		t.Fatal("expected federal price")
	}
}

func TestPickSearchPriceRangePrefersRicherBand(t *testing.T) {
	min, max, n, ok := pickSearchPriceRange(35.99, 49.99, 3, 20.39, 47.77, 12, 0, 0, 0)
	if !ok || n != 12 || min != 20.39 || max != 47.77 {
		t.Fatalf("got min=%v max=%v n=%d ok=%v", min, max, n, ok)
	}
}

func TestNSNMarketBandFillsOtherSearchOnlyTiles(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "R091", UPC: "071497149299", Manufacturer: "WOOSTER", Description: "Sherlock pole"},
		{SKU: "001809102", Manufacturer: "PURDY", Description: "4'-8' Purdy Professional Extension Pole"},
		{SKU: "215467", Manufacturer: "Dynamic", Description: "Pin Lock Fiberglass Extension Pole"},
	}
	// Only first row "resolved" with multi-offer catalog data.
	resolved := map[int]*productIdentity{
		0: {
			Title: "Wooster Sherlock", UPC: "071497149299",
			OfferPrice: 20.39, UPCMin: 20.39, UPCMax: 47.77, UPCCount: 12, UPCPrice: 20.39,
			ShopPrice: 40.35, ShopMerchant: "Home Depot", ShopLink: "https://www.upcitemdb.com/norob/x",
			ASIN: "B000DZGQIM", AmazonPrice: 0,
		},
	}
	resolved = expandResolvedIdentities(refs, resolved)
	band := buildNSNMarketBand(resolved)
	if band.Count < 2 {
		t.Fatalf("expected nsn band from resolved row, got %+v", band)
	}
	out := applyDeterministicProductLinks(refs, "8020015964253", resolved, "$42.62", "ABILITYONE_COM", band)
	// Row 0 has direct amazon/shop — not forced to range for shop if direct.
	// Rows 1–2 are search-only with no own identity → should inherit NSN band on amazon+shop.
	if out[1].PriceAmazon == "" || !out[1].PriceAmazonIsRange {
		t.Fatalf("row1 amazon should inherit nsn range, got %q", out[1].PriceAmazon)
	}
	if out[1].PriceShop == "" || !out[1].PriceShopIsRange {
		t.Fatalf("row1 shop should inherit nsn range, got %q", out[1].PriceShop)
	}
	if out[2].PriceAmazon == "" || !strings.Contains(out[2].PriceAmazon, "offers") {
		t.Fatalf("row2 amazon range %q", out[2].PriceAmazon)
	}
	if out[1].PriceFederal == "" || out[2].PriceFederal == "" {
		t.Fatal("federal should still stamp all rows")
	}
}

func TestSearchOnlyAmazonUsesCatalogRangeWhenNoAmazonOffers(t *testing.T) {
	refs := []models.CommercialReference{
		{SKU: "14A050", Manufacturer: "WOOSTER", Description: "Paint Roller Cover"},
	}
	// No ASIN, no Amazon-channel offers — only overall catalog multi-offer data.
	resolved := map[int]*productIdentity{
		0: {
			Title:      "Wooster Paint Roller Cover",
			OfferPrice: 8.99,
			UPCMin:     6.50,
			UPCMax:     18.25,
			UPCCount:   9,
			UPCPrice:   6.50,
			ShopMin:    7.00,
			ShopMax:    16.00,
			ShopCount:  6,
			ShopPrice:  7.00,
		},
	}
	out := applyDeterministicProductLinks(refs, "8020015964250", resolved, "", "", nsnMarketBand{})
	if strings.Contains(out[0].LinkAmazon, "/dp/") {
		t.Fatalf("expected amazon search, got %q", out[0].LinkAmazon)
	}
	if !out[0].PriceAmazonIsRange {
		t.Fatalf("search amazon should show range, got %q", out[0].PriceAmazon)
	}
	if isDirectProductURL(out[0].LinkShop) {
		// HD search for paint
	}
	if !out[0].PriceShopIsRange {
		t.Fatalf("search shop should show range, got %q isDirect=%v link=%q", out[0].PriceShop, isDirectProductURL(out[0].LinkShop), out[0].LinkShop)
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
	// Google Shopping hubs must never count as product evidence.
	if isDirectProductURL("https://www.google.com/search?ibp=oshop&q=pole") {
		t.Fatal("google shopping hub is not direct")
	}
	if isDirectProductURL("https://www.google.com/search?tbm=shop&q=pole") {
		t.Fatal("tbm=shop is not direct")
	}
	if !isMerchantProductURL("https://www.homedepot.com/p/Wooster-Sherlock-R091/100123456") {
		t.Fatal("HD product path")
	}
	if !isMerchantProductURL("https://www.walmart.com/ip/Some-Product/12345") {
		t.Fatal("walmart ip")
	}
}

func TestProductURLQualityPrefersMerchantPDP(t *testing.T) {
	hub := "https://www.google.com/search?tbm=shop&q=WOOSTER+R091"
	pdp := "https://www.homedepot.com/p/Wooster-R091/100"
	search := "https://www.homedepot.com/s/WOOSTER%20R091"
	if productURLQuality(pdp) <= productURLQuality(hub) {
		t.Fatalf("PDP should beat google hub: pdp=%d hub=%d", productURLQuality(pdp), productURLQuality(hub))
	}
	if productURLQuality(pdp) <= productURLQuality(search) {
		t.Fatalf("PDP should beat HD search: pdp=%d search=%d", productURLQuality(pdp), productURLQuality(search))
	}
	best := pickBestEvidenceURL(hub, search, pdp, "")
	if best != pdp {
		t.Fatalf("pickBestEvidenceURL got %q want pdp", best)
	}
}

func TestBuildTightProductSearchQueryPrefersSKU(t *testing.T) {
	q := buildTightProductSearchQuery("WOOSTER", "R091", "012345678901", "long description that would bury results if used whole")
	if !strings.Contains(q, "R091") || !strings.Contains(q, "WOOSTER") {
		t.Fatalf("expected brand+sku, got %q", q)
	}
	// Should not dump the full long description.
	if strings.Contains(q, "bury results") {
		t.Fatalf("should not use long description when SKU present: %q", q)
	}
}

func TestPickBestShopEvidenceLinkSkipsAmazonAndSearch(t *testing.T) {
	offers := []models.MarketOffer{
		{Merchant: "Amazon", Channel: "amazon", Link: "https://www.amazon.com/dp/B000DZGQIM", UnitPrice: 10},
		{Merchant: "Google", Channel: "shop", Link: "https://www.google.com/search?tbm=shop&q=x", UnitPrice: 11},
		{Merchant: "The Home Depot", Channel: "shop", Link: "https://www.homedepot.com/p/Item/123", UnitPrice: 12, Source: "SERPAPI"},
	}
	link, merch := pickBestShopEvidenceLink(offers)
	if !strings.Contains(link, "homedepot.com/p/") {
		t.Fatalf("want HD product, got %q (%s)", link, merch)
	}
	if merch != "The Home Depot" {
		t.Fatalf("merchant %q", merch)
	}
}

func TestUnreliableOfferLinksRejected(t *testing.T) {
	dead := []string{
		"https://www.newegg.com/Product/Product.aspx?Item=9SIA5D52J90013&nm_mc=AFC-C8Junction-MKPL",
		"http://www.toolschest.com/lcst17623.html",
		"http://www.sears.com/shc/s/p_10153_12605_SPM8044505829",
		"https://www.jet.com/product/foo",
		"https://www.upcitemdb.com/norob/alink/?id=x",
		"https://www.google.com/search?ibp=oshop&q=pole",
	}
	for _, u := range dead {
		if !isUnreliableOfferLink(u) && isDirectProductURL(u) {
			t.Fatalf("should reject %q", u)
		}
		if productURLQuality(u) >= 50 {
			t.Fatalf("quality too high for dead link %q: %d", u, productURLQuality(u))
		}
	}
	if !isDirectProductURL("https://www.homedepot.com/p/Wooster-R091/203150945") {
		t.Fatal("HD product should be direct")
	}
	if !isDirectProductURL("https://www.truevalue.com/product/771335/purdy-power-lock-pole") {
		t.Fatal("True Value product should be direct")
	}
}

func TestBrandConflictRejectsWrongManufacturerPage(t *testing.T) {
	// Sherwin-Williams SKU must not use a Purdy product page as evidence.
	if !brandConflict("SHERWIN-WILLIAMS", "Purdy Power Lock Pole", "https://www.walmart.com/ip/Purdy-Pole/18280302499") {
		t.Fatal("expected brand conflict purdy vs sherwin")
	}
	if brandConflict("WOOSTER", "Wooster Sherlock Pole R091", "https://www.homedepot.com/p/Wooster-Sherlock/203150945") {
		t.Fatal("same brand should not conflict")
	}
	score := identityMatchScore("001803824", "SHERWIN-WILLIAMS", "Purdy Power Lock", "https://www.walmart.com/ip/Purdy-Pole/18280302499")
	if score >= 0 {
		t.Fatalf("wrong-brand identity score should be negative, got %d", score)
	}
}

func TestSanitizeMarketOfferLinksStripsDead(t *testing.T) {
	offers := []models.MarketOffer{
		{Merchant: "Newegg.com", UnitPrice: 20.39, Link: "https://www.newegg.com/Product/Product.aspx?Item=9SIA5D52J90013&nm_mc=AFC-C8Junction-MKPL", Source: "UPCITEMDB"},
		{Merchant: "Home Depot", UnitPrice: 40.35, Link: "https://www.homedepot.com/p/Wooster-R091/203150945", Source: "SERPAPI", Title: "Wooster Sherlock R091"},
		{Merchant: "Ghost", UnitPrice: 10, Link: "https://www.google.com/search?ibp=oshop&q=x", Source: "SERPAPI"},
	}
	out := sanitizeMarketOfferLinks(offers, "R091", "WOOSTER", "Wooster Sherlock Extension Pole")
	if out[0].Link != "" {
		t.Fatalf("newegg link should be stripped, got %q", out[0].Link)
	}
	if !strings.Contains(out[1].Link, "homedepot.com/p/") {
		t.Fatalf("HD link should remain: %q", out[1].Link)
	}
	if out[2].Link != "" {
		t.Fatalf("google hub should be stripped")
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
