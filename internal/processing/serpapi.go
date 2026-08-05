package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// SerpAPI Google Shopping + optional Immersive Product integration.
// Key is loaded at process start via ConfigureSerpAPI (never log the raw key).
//
// Quota: each google_shopping and google_immersive_product call costs a search credit.
// IF_SERPAPI_IMMERSIVE=false (or ConfigureSerpAPI immersive=false) restores shopping-only.

var (
	serpMu         sync.RWMutex
	serpAPIKey     string
	serpNum        = 8
	serpImmersive  = true // default on; kill-switch restores shopping-only
	// Google Shopping often needs 5–15s; keep client timeout above that.
	serpClient = &http.Client{Timeout: 30 * time.Second}

	serpCacheMu sync.RWMutex
	serpCache   = map[string]cachedSerpResult{}
)

// serpCallTimeout is the per-resolve budget for SerpAPI (shopping + optional immersive).
// Independent of the UPCItemDB resolve budget so rate-limit waits don't starve Serp.
const serpCallTimeout = 28 * time.Second

// maxImmersiveCallsPerResolve caps Immersive Product API usage (1 search credit each).
const maxImmersiveCallsPerResolve = 1

type cachedSerpResult struct {
	hits   []shoppingHit
	expiry time.Time
	ok     bool
}

// shoppingHit is one Google Shopping result (or a store row from Immersive Product).
type shoppingHit struct {
	Title          string
	Price          float64
	Link           string
	Source         string // merchant name
	ProductID      string
	ImmersiveToken string // page_token for engine=google_immersive_product
}

// ConfigureSerpAPI enables SerpAPI-backed Google Shopping lookups.
// immersive enables P2 multi-store enrichment (extra quota); false = shopping only.
func ConfigureSerpAPI(apiKey string, numResults int, immersive bool) {
	serpMu.Lock()
	defer serpMu.Unlock()
	serpAPIKey = strings.TrimSpace(apiKey)
	serpImmersive = immersive
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

// SerpAPIImmersiveEnabled reports the server default for Immersive Product (env / ConfigureSerpAPI).
// Per-request override uses WithSerpImmersive + SerpImmersiveForRequest.
func SerpAPIImmersiveEnabled() bool {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpAPIKey != "" && serpImmersive
}

// SerpAPIImmersiveDefault is the default for requests that omit serp_immersive (true when configured).
func SerpAPIImmersiveDefault() bool {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpImmersive
}

func serpKey() string {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpAPIKey
}

func serpImmersiveOn() bool {
	serpMu.RLock()
	defer serpMu.RUnlock()
	return serpImmersive
}

// Context key for per-request immersive override (API / UI control).
type ctxKeySerpImmersive struct{}

// WithSerpImmersive sets whether this analysis should use Immersive Product follow-up.
// When omitted from the request path, SerpImmersiveForRequest uses the server default.
func WithSerpImmersive(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, ctxKeySerpImmersive{}, on)
}

// SerpImmersiveForRequest returns whether Immersive Product is active for this call.
// Request override wins when present; otherwise server default (IF_SERPAPI_IMMERSIVE).
func SerpImmersiveForRequest(ctx context.Context) bool {
	if !SerpAPIEnabled() {
		return false
	}
	if ctx != nil {
		if v, ok := ctx.Value(ctxKeySerpImmersive{}).(bool); ok {
			return v
		}
	}
	return serpImmersiveOn()
}

// classifySerpNetError turns Go transport errors into analyst-facing messages.
func classifySerpNetError(err error) (message, detail string) {
	if err == nil {
		return "SerpAPI request failed.", ""
	}
	detail = err.Error()
	// Never leave full API key material in status (query string is redacted upstream too).
	if i := strings.Index(strings.ToLower(detail), "api_key="); i >= 0 {
		detail = detail[:i] + "api_key=[redacted]"
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(detail, "context deadline exceeded"):
		return "SerpAPI request timed out waiting for Google Shopping (often 5–15s). Analysis continues with other sources.", detail
	case errors.Is(err, context.Canceled):
		return "SerpAPI request was cancelled (analysis budget exhausted). Try again or reduce commercial resolve load.", detail
	case strings.Contains(detail, "Client.Timeout") || strings.Contains(detail, "Timeout exceeded"):
		return "SerpAPI HTTP client timed out. Google Shopping responses can be slow; Insight Forge uses a 30s client timeout.", detail
	default:
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return "SerpAPI network timeout. Check outbound HTTPS to serpapi.com from this host.", detail
		}
		return "SerpAPI request failed (network error). Commercial shopping prices may be incomplete.", detail
	}
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
	// Dedicated deadline: parent product-link ctx may already be nearly exhausted
	// after UPCItemDB rate-limit waits (2s+ per lookup). Without this, Serp fails
	// with context deadline and looks like a network timeout.
	serpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serpCallTimeout)
	defer cancel()

	hits, ok := serpGoogleShopping(serpCtx, q)
	if !ok || len(hits) == 0 {
		// One simpler fallback only if we still have budget.
		if mfr != "" && sku != "" && serpCtx.Err() == nil {
			hits, ok = serpGoogleShopping(serpCtx, strings.TrimSpace(mfr+" "+sku))
		}
		if !ok || len(hits) == 0 {
			return productIdentity{}, false
		}
	}
	// P2: one Immersive Product call when enabled for this request and a page_token is available.
	// Server default: IF_SERPAPI_IMMERSIVE (true). Per-request: serp_immersive on analyze/insight body.
	if SerpImmersiveForRequest(ctx) && serpCtx.Err() == nil {
		hits = enrichHitsWithImmersive(serpCtx, hits, sku, mfr)
	}
	return identityFromShoppingHits(hits, sku, mfr), true
}

// enrichHitsWithImmersive picks the best shopping hit that has an immersive token,
// fetches multi-store prices (≤maxImmersiveCallsPerResolve), and merges them in.
func enrichHitsWithImmersive(ctx context.Context, hits []shoppingHit, preferSKU, mfr string) []shoppingHit {
	if len(hits) == 0 || !SerpAPIEnabled() || !SerpImmersiveForRequest(ctx) {
		return hits
	}
	token := pickBestImmersiveToken(hits, preferSKU, mfr)
	if token == "" {
		return hits
	}
	storeHits, ok := serpImmersiveProduct(ctx, token)
	if !ok || len(storeHits) == 0 {
		return hits
	}
	return mergeImmersiveStoreHits(hits, storeHits)
}

// pickBestImmersiveToken prefers SKU/brand-matched hits that expose a page_token.
func pickBestImmersiveToken(hits []shoppingHit, preferSKU, mfr string) string {
	prefer := strings.ToUpper(strings.TrimSpace(preferSKU))
	preferCompact := compactAlnum(prefer)
	mfrL := strings.ToLower(strings.TrimSpace(mfr))

	bestScore := -1
	bestToken := ""
	for _, h := range hits {
		tok := strings.TrimSpace(h.ImmersiveToken)
		if tok == "" {
			continue
		}
		titleU := strings.ToUpper(h.Title)
		titleL := strings.ToLower(h.Title)
		sc := 1
		if prefer != "" && strings.Contains(titleU, prefer) {
			sc += 50
		}
		if preferCompact != "" && len(preferCompact) >= 4 && strings.Contains(compactAlnum(titleU), preferCompact) {
			sc += 40
		}
		if mfrL != "" && strings.Contains(titleL, mfrL) {
			sc += 20
		}
		if h.Price > 0 && h.Price <= 50000 {
			sc += 5
		}
		if sc > bestScore {
			bestScore = sc
			bestToken = tok
		}
	}
	return bestToken
}

// mergeImmersiveStoreHits appends store-level offers not already present by merchant+price.
func mergeImmersiveStoreHits(base, stores []shoppingHit) []shoppingHit {
	if len(stores) == 0 {
		return base
	}
	type key struct {
		src string
		p   int64 // price * 100
	}
	seen := map[key]bool{}
	for _, h := range base {
		if h.Price <= 0 {
			continue
		}
		seen[key{src: strings.ToLower(strings.TrimSpace(h.Source)), p: int64(h.Price*100 + 0.5)}] = true
	}
	out := append([]shoppingHit(nil), base...)
	for _, s := range stores {
		if s.Price <= 0 || s.Price > 50000 {
			continue
		}
		k := key{src: strings.ToLower(strings.TrimSpace(s.Source)), p: int64(s.Price*100 + 0.5)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
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
	// Preserve atomic offer rows from both UPCItemDB and Serp for data-capture.
	if len(b.Offers) > 0 {
		out.Offers = appendMarketOffers(out.Offers, b.Offers...)
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
	preferCompact := compactAlnum(prefer)
	mfrL := strings.ToLower(strings.TrimSpace(mfr))

	// Score hits for relevance; prefer SKU-in-title, then brand, drop loose matches when better ones exist.
	type scoredHit struct {
		h     shoppingHit
		score int
	}
	var scored []scoredHit
	for _, h := range hits {
		if h.Price <= 0 || h.Price > 50000 {
			continue
		}
		titleU := strings.ToUpper(h.Title)
		titleL := strings.ToLower(h.Title)
		sc := 0
		if prefer != "" && strings.Contains(titleU, prefer) {
			sc += 50
		}
		if preferCompact != "" && len(preferCompact) >= 4 && strings.Contains(compactAlnum(titleU), preferCompact) {
			sc += 40
		}
		if mfrL != "" && strings.Contains(titleL, mfrL) {
			sc += 20
		}
		// Soft brand aliases (home depot often drops hyphens)
		if mfrL != "" {
			for _, part := range strings.Fields(mfrL) {
				if len(part) >= 4 && strings.Contains(titleL, part) {
					sc += 8
				}
			}
		}
		if isDirectProductURL(h.Link) {
			sc += 5
		}
		scored = append(scored, scoredHit{h: h, score: sc})
	}
	if len(scored) == 0 {
		return productIdentity{}
	}
	// Sort by score desc, then price asc
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score || (scored[j].score == scored[i].score && scored[j].h.Price < scored[i].h.Price) {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}
	// If we have strong SKU matches, use only those; else keep top score band.
	bestScore := scored[0].score
	var ranked []shoppingHit
	if bestScore >= 40 {
		for _, s := range scored {
			if s.score >= 40 {
				ranked = append(ranked, s.h)
			}
		}
	} else {
		for _, s := range scored {
			if s.score >= bestScore-15 {
				ranked = append(ranked, s.h)
			}
		}
	}
	if len(ranked) == 0 {
		ranked = []shoppingHit{scored[0].h}
	}
	// Drop price outliers so one wrong "related" hit does not blow the range to $6–$600.
	ranked = filterShoppingPriceOutliers(ranked)

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
		channel := "shop"
		if strings.Contains(src, "amazon") {
			channel = "amazon"
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
			// Prefer highest-quality product URL; among equal quality, lower price.
			if bestShop.Link == "" && bestShop.Price <= 0 {
				bestShop = h
			} else {
				bq, hq := productURLQuality(bestShop.Link), productURLQuality(h.Link)
				if hq > bq || (hq == bq && (bestShop.Price <= 0 || (h.Price > 0 && h.Price < bestShop.Price))) {
					bestShop = h
				}
			}
		}
		// Atomic offer for data-capture export (pack/UOM filled later from title + ETS).
		id.Offers = append(id.Offers, models.MarketOffer{
			UnitPrice: h.Price,
			Quantity:  1,
			Currency:  "USD",
			Channel:   channel,
			Merchant:  nonEmpty(h.Source, "Google Shopping"),
			Source:    "SERPAPI",
			Link:      h.Link,
			Title:     strings.TrimSpace(h.Title),
		})
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
	}
	// Evidence URL: best merchant PDP across all ranked hits (not only cheapest).
	// Never promote Google Shopping hub/search pages as RetailURL/OfferLink.
	var bestEvidenceLink, bestEvidenceMerchant string
	bestEQ := 0
	for _, h := range ranked {
		if h.Link == "" {
			continue
		}
		// Skip Amazon for shop/retail evidence (Amazon uses /dp separately).
		src := strings.ToLower(h.Source + " " + h.Link)
		if strings.Contains(src, "amazon") {
			continue
		}
		q := productURLQuality(h.Link)
		if q > bestEQ {
			bestEQ = q
			bestEvidenceLink = h.Link
			bestEvidenceMerchant = h.Source
		}
	}
	if bestEvidenceLink != "" {
		id.ShopLink = bestEvidenceLink
		if bestEvidenceMerchant != "" && !isGenericMerchant(bestEvidenceMerchant) {
			id.ShopMerchant = bestEvidenceMerchant
		}
		if isDirectProductURL(bestEvidenceLink) {
			id.OfferLink = bestEvidenceLink
			if isMerchantProductURL(bestEvidenceLink) {
				id.RetailURL = bestEvidenceLink
			}
		}
	} else if bestShop.Link != "" && !isSearchOrHubURL(bestShop.Link) {
		id.ShopLink = bestShop.Link
	}
	// Amazon ASIN from any ranked hit with /dp — stronger than search.
	if id.ASIN == "" {
		for _, h := range ranked {
			if a, ok := amazonASINFromSKU(scanASINToken(h.Link)); ok {
				id.ASIN = a
				break
			}
		}
	}
	if id.ASIN != "" || id.RetailURL != "" || id.OfferLink != "" || id.OfferPrice > 0 {
		id.DeepLinkOK = isMerchantProductURL(id.RetailURL) || isDirectProductURL(id.OfferLink) || id.ASIN != "" || id.OfferPrice > 0
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

// filterShoppingPriceOutliers removes extreme prices that often come from
// unrelated Shopping hits (bulk packs, wrong size, accessories).
func filterShoppingPriceOutliers(hits []shoppingHit) []shoppingHit {
	if len(hits) < 3 {
		return hits
	}
	prices := make([]float64, 0, len(hits))
	for _, h := range hits {
		if h.Price > 0 {
			prices = append(prices, h.Price)
		}
	}
	if len(prices) < 3 {
		return hits
	}
	// Sort copy for median
	sorted := append([]float64(nil), prices...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	median := sorted[len(sorted)/2]
	if median <= 0 {
		return hits
	}
	var kept []shoppingHit
	for _, h := range hits {
		// Keep prices within ~3x of median (covers normal retail spread).
		if h.Price >= median/3 && h.Price <= median*3 {
			kept = append(kept, h)
		}
	}
	if len(kept) >= 2 {
		return kept
	}
	return hits
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
		msg, detail := classifySerpNetError(err)
		recordSerpAPIStatus(false, 0, detail, msg)
		setSerpCache(cacheKey, nil, false, 2*time.Minute)
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		recordSerpAPIStatus(false, resp.StatusCode, err.Error(), "SerpAPI response could not be read.")
		setSerpCache(cacheKey, nil, false, 2*time.Minute)
		return nil, false
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		recordSerpAPIStatus(false, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode),
			"SerpAPI is rate-limited or unavailable. Commercial shopping prices may be incomplete.")
		setSerpCache(cacheKey, nil, false, 45*time.Second)
		return nil, false
	}
	if resp.StatusCode != 200 {
		msg := "SerpAPI returned an error. Commercial shopping prices may be incomplete."
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			msg = "SerpAPI rejected the API key (unauthorized). Check IF_SERPAPI_KEY."
		}
		recordSerpAPIStatus(false, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode), msg)
		setSerpCache(cacheKey, nil, false, 10*time.Minute)
		return nil, false
	}

	var payload struct {
		Error           string `json:"error"`
		ShoppingResults []struct {
			Title                     string  `json:"title"`
			Link                      string  `json:"link"`
			ProductLink               string  `json:"product_link"`
			Source                    string  `json:"source"`
			Price                     string  `json:"price"`
			ExtractedPrice            float64 `json:"extracted_price"`
			ProductID                 string  `json:"product_id"`
			OldPrice                  string  `json:"old_price"`
			ImmersiveProductPageToken string  `json:"immersive_product_page_token"`
		} `json:"shopping_results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		recordSerpAPIStatus(false, 200, err.Error(), "SerpAPI returned unreadable JSON.")
		setSerpCache(cacheKey, nil, false, 5*time.Minute)
		return nil, false
	}
	if payload.Error != "" {
		// SerpAPI often returns 200 with {"error":"..."} for bad keys.
		msg := "SerpAPI error: " + payload.Error
		if strings.Contains(strings.ToLower(payload.Error), "invalid") || strings.Contains(strings.ToLower(payload.Error), "api key") {
			msg = "SerpAPI reported an invalid API key. Check IF_SERPAPI_KEY."
		}
		recordSerpAPIStatus(false, 200, payload.Error, msg)
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	if len(payload.ShoppingResults) == 0 {
		// Empty results for a query is not a global API failure — still mark live.
		recordSerpAPIStatus(true, 200, "", "SerpAPI is responding (no shopping hits for this query).")
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	recordSerpAPIStatus(true, 200, "", "SerpAPI Google Shopping is live.")

	var hits []shoppingHit
	for _, r := range payload.ShoppingResults {
		price := r.ExtractedPrice
		if price <= 0 {
			price = parseMoneyToFloat(r.Price)
		}
		// Prefer merchant PDP over Google Shopping product_link (usually a multi-result hub).
		link := preferSerpMerchantLink(r.Link, r.ProductLink)
		if price <= 0 && link == "" && strings.TrimSpace(r.ImmersiveProductPageToken) == "" {
			continue
		}
		hits = append(hits, shoppingHit{
			Title:          strings.TrimSpace(r.Title),
			Price:          price,
			Link:           link, // may be empty when only Google hub URLs — price still used for ranges
			Source:         strings.TrimSpace(r.Source),
			ProductID:      strings.TrimSpace(r.ProductID),
			ImmersiveToken: strings.TrimSpace(r.ImmersiveProductPageToken),
		})
	}
	if len(hits) == 0 {
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	setSerpCache(cacheKey, hits, true, 12*time.Hour)
	return hits, true
}

// serpImmersiveProduct fetches multi-store prices via engine=google_immersive_product.
// Each call costs one SerpAPI search credit. Results are cached by page_token.
func serpImmersiveProduct(ctx context.Context, pageToken string) ([]shoppingHit, bool) {
	pageToken = strings.TrimSpace(pageToken)
	if pageToken == "" || !SerpAPIEnabled() || !SerpImmersiveForRequest(ctx) {
		return nil, false
	}
	// Prefix cache keys so shopping and immersive never collide.
	cacheKey := "imm:" + pageToken
	if hit, ok := getSerpCache(cacheKey); ok {
		return hit.hits, hit.ok
	}

	u := url.URL{
		Scheme: "https",
		Host:   "serpapi.com",
		Path:   "/search.json",
	}
	q := u.Query()
	q.Set("engine", "google_immersive_product")
	q.Set("page_token", pageToken)
	// more_stores returns up to ~13 merchants (still one search credit).
	q.Set("more_stores", "true")
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
		// Soft-fail: keep prior shopping success status; immersive is additive.
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
		// Rate limit is real — surface it so the UI banner can warn.
		recordSerpAPIStatus(false, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode),
			"SerpAPI rate-limited during Immersive Product. Shopping enrichment may be incomplete.")
		setSerpCache(cacheKey, nil, false, 45*time.Second)
		return nil, false
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		recordSerpAPIStatus(false, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode),
			"SerpAPI rejected the API key (unauthorized). Check IF_SERPAPI_KEY.")
		setSerpCache(cacheKey, nil, false, 10*time.Minute)
		return nil, false
	}
	if resp.StatusCode != 200 {
		setSerpCache(cacheKey, nil, false, 10*time.Minute)
		return nil, false
	}

	var payload struct {
		Error          string `json:"error"`
		ProductResults struct {
			Title  string `json:"title"`
			Brand  string `json:"brand"`
			Stores []struct {
				Name           string  `json:"name"`
				Link           string  `json:"link"`
				Title          string  `json:"title"`
				Price          string  `json:"price"`
				ExtractedPrice float64 `json:"extracted_price"`
			} `json:"stores"`
		} `json:"product_results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		setSerpCache(cacheKey, nil, false, 5*time.Minute)
		return nil, false
	}
	if payload.Error != "" {
		if strings.Contains(strings.ToLower(payload.Error), "invalid") || strings.Contains(strings.ToLower(payload.Error), "api key") {
			recordSerpAPIStatus(false, 200, payload.Error, "SerpAPI reported an invalid API key. Check IF_SERPAPI_KEY.")
		}
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	if len(payload.ProductResults.Stores) == 0 {
		// No stores — shopping still valid; don't clobber healthy status.
		setSerpCache(cacheKey, nil, false, 6*time.Hour)
		return nil, false
	}
	recordSerpAPIStatus(true, 200, "", "SerpAPI Google Shopping + Immersive Product is live.")

	productTitle := strings.TrimSpace(payload.ProductResults.Title)
	var hits []shoppingHit
	for _, s := range payload.ProductResults.Stores {
		price := s.ExtractedPrice
		if price <= 0 {
			price = parseMoneyToFloat(s.Price)
		}
		if price <= 0 {
			continue
		}
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = productTitle
		}
		// Prefer merchant product URLs over Google Shopping aggregate links.
		link := strings.TrimSpace(s.Link)
		hits = append(hits, shoppingHit{
			Title:  title,
			Price:  price,
			Link:   link,
			Source: nonEmpty(strings.TrimSpace(s.Name), "Retail"),
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
	mode := "shopping only"
	if serpImmersiveOn() {
		mode = "shopping + immersive (default)"
	}
	return fmt.Sprintf("SerpAPI: enabled (%s, num=%d)", mode, serpResultLimit())
}

// SerpAPIAccountQuota is a redacted snapshot of SerpAPI account usage (free Account API).
type SerpAPIAccountQuota struct {
	Configured              bool    `json:"configured"`
	OK                      bool    `json:"ok"`
	PlanName                string  `json:"plan_name,omitempty"`
	PlanID                  string  `json:"plan_id,omitempty"`
	SearchesPerMonth        int     `json:"searches_per_month,omitempty"`
	ThisMonthUsage          int     `json:"this_month_usage,omitempty"`
	PlanSearchesLeft        int     `json:"plan_searches_left,omitempty"`
	TotalSearchesLeft       int     `json:"total_searches_left,omitempty"`
	ExtraCredits            int     `json:"extra_credits,omitempty"`
	ThisHourSearches        int     `json:"this_hour_searches,omitempty"`
	LastHourSearches        int     `json:"last_hour_searches,omitempty"`
	AccountRateLimitPerHour int     `json:"account_rate_limit_per_hour,omitempty"`
	PlanRenewalDate         string  `json:"plan_renewal_date,omitempty"`
	PlanMonthlyPrice        float64 `json:"plan_monthly_price,omitempty"`
	AccountStatus           string  `json:"account_status,omitempty"`
	// ImmersiveDefault is the server default for serp_immersive when the client omits it.
	ImmersiveDefault bool   `json:"immersive_default"`
	UsagePercent     float64 `json:"usage_percent,omitempty"` // this_month_usage / searches_per_month * 100
	CheckedAt        string  `json:"checked_at,omitempty"`
	Error            string  `json:"error,omitempty"`
	Note             string  `json:"note,omitempty"`
}

var (
	serpQuotaMu    sync.RWMutex
	serpQuotaCache SerpAPIAccountQuota
	serpQuotaAt    time.Time
)

const serpQuotaCacheTTL = 90 * time.Second

// FetchSerpAPIQuota calls SerpAPI Account API (free, does not burn search credits).
// Results are cached briefly so the UI can poll without hammering the account endpoint.
func FetchSerpAPIQuota(ctx context.Context, force bool) SerpAPIAccountQuota {
	out := SerpAPIAccountQuota{
		Configured:       SerpAPIEnabled(),
		ImmersiveDefault: SerpAPIImmersiveDefault(),
		Note:             "Account API is free and does not count against monthly search quota.",
	}
	if !out.Configured {
		out.Error = "SerpAPI key not configured"
		return out
	}
	if !force {
		serpQuotaMu.RLock()
		if !serpQuotaAt.IsZero() && time.Since(serpQuotaAt) < serpQuotaCacheTTL && serpQuotaCache.Configured {
			c := serpQuotaCache
			serpQuotaMu.RUnlock()
			return c
		}
		serpQuotaMu.RUnlock()
	}

	u := url.URL{Scheme: "https", Host: "serpapi.com", Path: "/account.json"}
	q := u.Query()
	q.Set("api_key", serpKey())
	u.RawQuery = q.Encode()

	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(reqCtx, 12*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		out.Error = "could not build account request"
		return out
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "InsightForge/1.0 (+https://github.com/bmcelhaney/insight-forge)")

	resp, err := serpClient.Do(req)
	if err != nil {
		out.Error = "account request failed: " + classifyNetDetail(err)
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out.Error = "account response unreadable"
		return out
	}
	if resp.StatusCode != 200 {
		out.Error = fmt.Sprintf("account API HTTP %d", resp.StatusCode)
		return out
	}

	var payload struct {
		AccountStatus           string  `json:"account_status"`
		PlanID                  string  `json:"plan_id"`
		PlanName                string  `json:"plan_name"`
		PlanMonthlyPrice        float64 `json:"plan_monthly_price"`
		PlanRenewalDate         string  `json:"plan_renewal_date"`
		SearchesPerMonth        int     `json:"searches_per_month"`
		PlanSearchesLeft        int     `json:"plan_searches_left"`
		ExtraCredits            int     `json:"extra_credits"`
		TotalSearchesLeft       int     `json:"total_searches_left"`
		ThisMonthUsage          int     `json:"this_month_usage"`
		ThisHourSearches        int     `json:"this_hour_searches"`
		LastHourSearches        int     `json:"last_hour_searches"`
		AccountRateLimitPerHour int     `json:"account_rate_limit_per_hour"`
		Error                   string  `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		out.Error = "account JSON unreadable"
		return out
	}
	if payload.Error != "" {
		out.Error = payload.Error
		return out
	}

	out.OK = true
	out.AccountStatus = payload.AccountStatus
	out.PlanID = payload.PlanID
	out.PlanName = payload.PlanName
	out.PlanMonthlyPrice = payload.PlanMonthlyPrice
	out.PlanRenewalDate = payload.PlanRenewalDate
	out.SearchesPerMonth = payload.SearchesPerMonth
	out.PlanSearchesLeft = payload.PlanSearchesLeft
	out.ExtraCredits = payload.ExtraCredits
	out.TotalSearchesLeft = payload.TotalSearchesLeft
	out.ThisMonthUsage = payload.ThisMonthUsage
	out.ThisHourSearches = payload.ThisHourSearches
	out.LastHourSearches = payload.LastHourSearches
	out.AccountRateLimitPerHour = payload.AccountRateLimitPerHour
	out.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	if out.SearchesPerMonth > 0 {
		out.UsagePercent = float64(out.ThisMonthUsage) / float64(out.SearchesPerMonth) * 100
	}

	serpQuotaMu.Lock()
	serpQuotaCache = out
	serpQuotaAt = time.Now()
	serpQuotaMu.Unlock()
	return out
}

func classifyNetDetail(err error) string {
	if err == nil {
		return ""
	}
	d := err.Error()
	if i := strings.Index(strings.ToLower(d), "api_key="); i >= 0 {
		d = d[:i] + "api_key=[redacted]"
	}
	return d
}

// BuildAPIQuotas assembles multi-source quota / burn status for the UI and /api/quotas.
func BuildAPIQuotas(ctx context.Context, forceSerp bool) map[string]any {
	serpQ := FetchSerpAPIQuota(ctx, forceSerp)
	upcPlan := "trial"
	if UPCItemDBConfigured() {
		upcPlan = "v1"
	}
	upcRT := getUPCItemDBStatus()
	upc := map[string]any{
		"configured": UPCItemDBConfigured(),
		"enabled":    true,
		"plan":       upcPlan,
		"ok":         !upcRT.Checked || upcRT.OK,
		"note": "UPCItemDB has no public remaining-quota API. Paid DEV plan is rate-limited " +
			"(~1 lookup / 2s, ~1 search / 6s). Daily caps depend on your UPCItemDB plan.",
	}
	if upcRT.Checked {
		upc["last_ok"] = upcRT.OK
		upc["last_http"] = upcRT.HTTPCode
		upc["last_message"] = upcRT.Message
		if upcRT.Error != "" {
			upc["last_error"] = upcRT.Error
		}
		if !upcRT.CheckedAt.IsZero() {
			upc["last_checked_at"] = upcRT.CheckedAt.Format(time.RFC3339)
		}
	}
	serpRT := getSerpAPIStatus()
	serp := map[string]any{
		"configured":        serpQ.Configured,
		"ok":                serpQ.OK,
		"plan_name":         serpQ.PlanName,
		"plan_id":           serpQ.PlanID,
		"searches_per_month": serpQ.SearchesPerMonth,
		"this_month_usage":  serpQ.ThisMonthUsage,
		"plan_searches_left": serpQ.PlanSearchesLeft,
		"total_searches_left": serpQ.TotalSearchesLeft,
		"extra_credits":     serpQ.ExtraCredits,
		"this_hour_searches": serpQ.ThisHourSearches,
		"last_hour_searches": serpQ.LastHourSearches,
		"account_rate_limit_per_hour": serpQ.AccountRateLimitPerHour,
		"plan_renewal_date": serpQ.PlanRenewalDate,
		"account_status":    serpQ.AccountStatus,
		"usage_percent":     serpQ.UsagePercent,
		"immersive_default": serpQ.ImmersiveDefault,
		"shopping_num":      serpResultLimit(),
		"checked_at":        serpQ.CheckedAt,
		"note":              serpQ.Note,
	}
	if serpQ.Error != "" {
		serp["error"] = serpQ.Error
	}
	if serpRT.Checked {
		serp["last_call_ok"] = serpRT.OK
		serp["last_call_message"] = serpRT.Message
		if !serpRT.CheckedAt.IsZero() {
			serp["last_call_at"] = serpRT.CheckedAt.Format(time.RFC3339)
		}
	}
	partsbase := map[string]any{
		"note": "PartsBase does not expose a public remaining-quota endpoint in Insight Forge. " +
			"Status reflects last GovData call outcome only.",
	}
	return map[string]any{
		"serpapi":   serp,
		"upcitemdb": upc,
		"partsbase": partsbase,
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	}
}
