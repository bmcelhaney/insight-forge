package processing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SerpAPI Google Shopping integration for commercial product prices and links.
// Key is loaded at process start via ConfigureSerpAPI (never log the raw key).

var (
	serpMu     sync.RWMutex
	serpAPIKey string
	serpNum    = 8
	serpClient = &http.Client{Timeout: 12 * time.Second}

	serpCacheMu sync.RWMutex
	serpCache   = map[string]cachedSerpResult{}
)

type cachedSerpResult struct {
	hits   []shoppingHit
	expiry time.Time
	ok     bool
}

// shoppingHit is one Google Shopping result.
type shoppingHit struct {
	Title    string
	Price    float64
	Link     string
	Source   string // merchant name
	ProductID string
}

// ConfigureSerpAPI enables SerpAPI-backed Google Shopping lookups.
func ConfigureSerpAPI(apiKey string, numResults int) {
	serpMu.Lock()
	defer serpMu.Unlock()
	serpAPIKey = strings.TrimSpace(apiKey)
	if numResults > 0 {
		if numResults > 20 {
			numResults = 20
		}
		serpNum = numResults
	}
}

// SerpAPIEnabled reports whether a key is configured.
func SerpAPIEnabled() bool {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpAPIKey != ""
}

func serpKey() string {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpAPIKey
}

func serpResultLimit() int {
	serpMu.RLock()
	defer serpMu.RUnlock()
	if serpNum <= 0 {
		return 8
	}
	return serpNum
}

// resolveViaSerpShopping queries Google Shopping and builds a productIdentity
// with channel prices, ranges, and best product links.
func resolveViaSerpShopping(ctx context.Context, sku, upc, mfr, title string) (productIdentity, bool) {
	if !SerpAPIEnabled() {
		return productIdentity{}, false
	}
	q := buildProductSearchQuery(mfr, sku, upc, title)
	if q == "" {
		return productIdentity{}, false
	}
	hits, ok := serpGoogleShopping(ctx, q)
	if !ok || len(hits) == 0 {
		// One simpler fallback
		if mfr != "" && sku != "" {
			hits, ok = serpGoogleShopping(ctx, strings.TrimSpace(mfr+" "+sku))
		}
		if !ok || len(hits) == 0 {
			return productIdentity{}, false
		}
	}
	return identityFromShoppingHits(hits, sku, mfr), true
}

// mergeProductIdentity prefers existing deep links/ASIN; fills prices/ranges/links from b.
func mergeProductIdentity(a, b productIdentity) productIdentity {
	out := a
	if out.Title == "" {
		out.Title = b.Title
	}
	if out.Brand == "" {
		out.Brand = b.Brand
	}
	if out.ASIN == "" {
		out.ASIN = b.ASIN
	}
	if out.UPC == "" {
		out.UPC = b.UPC
	}
	if out.RetailURL == "" {
		out.RetailURL = b.RetailURL
	}
	if out.OfferLink == "" {
		out.OfferLink = b.OfferLink
	}
	if out.ShopLink == "" {
		out.ShopLink = b.ShopLink
	}
	// Prices: prefer existing channel singles; fill gaps from Serp.
	if out.AmazonPrice <= 0 && b.AmazonPrice > 0 {
		out.AmazonPrice, out.AmazonMerchant = b.AmazonPrice, b.AmazonMerchant
	}
	if out.ShopPrice <= 0 && b.ShopPrice > 0 {
		out.ShopPrice, out.ShopMerchant, out.ShopLink = b.ShopPrice, b.ShopMerchant, b.ShopLink
	}
	if out.OfferPrice <= 0 && b.OfferPrice > 0 {
		out.OfferPrice, out.OfferMerchant, out.OfferCurrency, out.OfferLink =
			b.OfferPrice, b.OfferMerchant, b.OfferCurrency, b.OfferLink
	}
	// Ranges: prefer wider/more samples from Serp when stronger.
	if b.UPCCount > out.UPCCount {
		out.UPCMin, out.UPCMax, out.UPCCount = b.UPCMin, b.UPCMax, b.UPCCount
		if out.UPCPrice <= 0 {
			out.UPCPrice, out.UPCMerchant = b.UPCPrice, b.UPCMerchant
		}
	}
	if b.AmazonCount > out.AmazonCount {
		out.AmazonMin, out.AmazonMax, out.AmazonCount = b.AmazonMin, b.AmazonMax, b.AmazonCount
	}
	if b.ShopCount > out.ShopCount {
		out.ShopMin, out.ShopMax, out.ShopCount = b.ShopMin, b.ShopMax, b.ShopCount
	}
	if b.DeepLinkOK {
		out.DeepLinkOK = true
	}
	if out.ASIN != "" || out.RetailURL != "" || out.OfferLink != "" || out.OfferPrice > 0 || out.UPCCount >= 2 {
		out.DeepLinkOK = true
	}
	return out
}

func identityFromShoppingHits(hits []shoppingHit, preferSKU, mfr string) productIdentity {
	prefer := strings.ToUpper(strings.TrimSpace(preferSKU))
	// Prefer hits whose title mentions the SKU when possible.
	var ranked []shoppingHit
	var rest []shoppingHit
	for _, h := range hits {
		if prefer != "" && strings.Contains(strings.ToUpper(h.Title), prefer) {
			ranked = append(ranked, h)
		} else {
			rest = append(rest, h)
		}
	}
	ranked = append(ranked, rest...)
	if len(ranked) == 0 {
		return productIdentity{}
	}

	id := productIdentity{
		Title: strings.TrimSpace(ranked[0].Title),
		Brand: strings.TrimSpace(mfr),
	}

	var allPrices, amazonPrices, shopPrices []float64
	var bestShop shoppingHit
	var bestAmazon shoppingHit
	for _, h := range ranked {
		if h.Price <= 0 || h.Price > 50000 {
			continue
		}
		allPrices = append(allPrices, h.Price)
		src := strings.ToLower(h.Source + " " + h.Link)
		if strings.Contains(src, "amazon") {
			amazonPrices = append(amazonPrices, h.Price)
			if bestAmazon.Price <= 0 || h.Price < bestAmazon.Price {
				bestAmazon = h
			}
			if id.ASIN == "" {
				if a, ok := amazonASINFromSKU(scanASINToken(h.Link)); ok {
					id.ASIN = a
				} else if a, ok := amazonASINFromSKU(scanASINToken(h.Title)); ok {
					id.ASIN = a
				}
			}
		} else {
			shopPrices = append(shopPrices, h.Price)
			// Prefer direct product-looking links as shop destination.
			if bestShop.Price <= 0 || (isDirectProductURL(h.Link) && !isDirectProductURL(bestShop.Link)) ||
				(isDirectProductURL(h.Link) == isDirectProductURL(bestShop.Link) && h.Price < bestShop.Price) {
				bestShop = h
			}
		}
	}

	if len(allPrices) > 0 {
		minP, maxP := minMaxFloats(allPrices)
		id.OfferPrice = minP
		id.OfferMerchant = "Google Shopping"
		id.OfferCurrency = "USD"
		id.UPCPrice = minP
		id.UPCMerchant = "Google Shopping"
		id.UPCMin, id.UPCMax, id.UPCCount = minP, maxP, len(allPrices)
	}
	if len(amazonPrices) > 0 {
		minP, maxP := minMaxFloats(amazonPrices)
		id.AmazonPrice = minP
		id.AmazonMerchant = nonEmpty(bestAmazon.Source, "Amazon")
		id.AmazonMin, id.AmazonMax, id.AmazonCount = minP, maxP, len(amazonPrices)
	}
	if len(shopPrices) > 0 {
		minP, maxP := minMaxFloats(shopPrices)
		id.ShopPrice = minP
		id.ShopMerchant = nonEmpty(bestShop.Source, "Retail")
		id.ShopMin, id.ShopMax, id.ShopCount = minP, maxP, len(shopPrices)
		if bestShop.Link != "" {
			id.ShopLink = bestShop.Link
			id.OfferLink = bestShop.Link
			if isDirectProductURL(bestShop.Link) {
				id.RetailURL = bestShop.Link
			}
		}
	} else if bestShop.Link == "" && ranked[0].Link != "" {
		// Fall back to first hit link as shop destination.
		id.ShopLink = ranked[0].Link
		id.OfferLink = ranked[0].Link
		if isDirectProductURL(ranked[0].Link) {
			id.RetailURL = ranked[0].Link
		}
	}
	if id.ASIN != "" || id.RetailURL != "" || id.OfferLink != "" || id.OfferPrice > 0 {
		id.DeepLinkOK = true
	}
	return id
}

func minMaxFloats(vals []float64) (min, max float64) {
	min, max = vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

func serpGoogleShopping(ctx context.Context, query string) ([]shoppingHit, bool) {
	query = strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	if query == "" || !SerpAPIEnabled() {
		return nil, false
	}
	cacheKey := strings.ToLower(query)
	if hit, ok := getSerpCache(cacheKey); ok {
		return hit.hits, hit.ok
	}

	num := serpResultLimit()
	u := url.URL{
		Scheme: "https",
		Host:   "serpapi.com",
		Path:   "/search.json",
	}
	q := u.Query()
	q.Set("engine", "google_shopping")
	q.Set("q", query)
	q.Set("hl", "en")
	q.Set("gl", "us")
	q.Set("num", strconv.Itoa(num))
	q.Set("api_key", serpKey())
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		setSerpCache(cacheKey, nil, false, 2*time.Minute)
		return nil, false
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+https://github.com/bmcelhaney/insight-forge)")
	req.Header.Set("Accept", "application/json")

	resp, err := serpClient.Do(req)
	if err != nil {
		setSerpCache(cacheKey, nil, false, 2*time.Minute)
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		setSerpCache(cacheKey, nil, false, 2*time.Minute)
		return nil, false
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		setSerpCache(cacheKey, nil, false, 45*time.Second)
		return nil, false
	}
	if resp.StatusCode != 200 {
		setSerpCache(cacheKey, nil, false, 10*time.Minute)
		return nil, false
	}

	var payload struct {
		Error            string `json:"error"`
		ShoppingResults  []struct {
			Title           string  `json:"title"`
			Link            string  `json:"link"`
			ProductLink     string  `json:"product_link"`
			Source          string  `json:"source"`
			Price           string  `json:"price"`
			ExtractedPrice  float64 `json:"extracted_price"`
			ProductID       string  `json:"product_id"`
			OldPrice        string  `json:"old_price"`
		} `json:"shopping_results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		setSerpCache(cacheKey, nil, false, 5*time.Minute)
		return nil, false
	}
	if payload.Error != "" || len(payload.ShoppingResults) == 0 {
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}

	var hits []shoppingHit
	for _, r := range payload.ShoppingResults {
		price := r.ExtractedPrice
		if price <= 0 {
			price = parseMoneyToFloat(r.Price)
		}
		link := strings.TrimSpace(r.Link)
		if link == "" {
			link = strings.TrimSpace(r.ProductLink)
		}
		if price <= 0 && link == "" {
			continue
		}
		hits = append(hits, shoppingHit{
			Title:     strings.TrimSpace(r.Title),
			Price:     price,
			Link:      link,
			Source:    strings.TrimSpace(r.Source),
			ProductID: strings.TrimSpace(r.ProductID),
		})
	}
	if len(hits) == 0 {
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	setSerpCache(cacheKey, hits, true, 12*time.Hour)
	return hits, true
}

func parseMoneyToFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Keep digits, dot, comma then strip commas.
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == ',' {
			b.WriteRune(r)
		}
	}
	s = strings.ReplaceAll(b.String(), ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func getSerpCache(key string) (cachedSerpResult, bool) {
	serpCacheMu.RLock()
	hit, ok := serpCache[key]
	serpCacheMu.RUnlock()
	if !ok {
		return cachedSerpResult{}, false
	}
	if time.Now().After(hit.expiry) {
		serpCacheMu.Lock()
		delete(serpCache, key)
		serpCacheMu.Unlock()
		return cachedSerpResult{}, false
	}
	return hit, true
}

func setSerpCache(key string, hits []shoppingHit, ok bool, ttl time.Duration) {
	if key == "" || ttl <= 0 {
		return
	}
	serpCacheMu.Lock()
	serpCache[key] = cachedSerpResult{hits: hits, expiry: time.Now().Add(ttl), ok: ok}
	serpCacheMu.Unlock()
}

// clearSerpCache is for tests.
func clearSerpCache() {
	serpCacheMu.Lock()
	serpCache = map[string]cachedSerpResult{}
	serpCacheMu.Unlock()
}

// serpStatusMessage is a safe one-line status (never includes the key).
func serpStatusMessage() string {
	if !SerpAPIEnabled() {
		return "SerpAPI: not configured"
	}
	return fmt.Sprintf("SerpAPI: enabled (Google Shopping, num=%d)", serpResultLimit())
}
