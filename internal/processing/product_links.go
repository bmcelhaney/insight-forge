package processing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// productIdentity is a resolved commercial product key used for accurate deep links
// and optional market offer pricing pulled back onto the commercial tile.
type productIdentity struct {
	Title         string
	Brand         string
	Model         string
	UPC           string
	ASIN          string
	EAN           string
	OfferPrice    float64 // best overall USD (or unlabelled) offer price
	OfferMerchant string
	OfferCurrency string
	OfferLink     string // merchant/offer product URL from resolver (often via upcitemdb redirect)
	RetailURL     string // direct retailer product page inferred from catalog images/ids
	// Channel-specific prices from offer rows (shown next to each tile link).
	AmazonPrice    float64
	AmazonMerchant string
	AmazonMin      float64 // top-result range when no single Amazon product page
	AmazonMax      float64
	AmazonCount    int
	ShopPrice      float64
	ShopMerchant   string
	ShopLink       string // preferred non-Amazon offer link
	ShopMin        float64
	ShopMax        float64
	ShopCount      int
	UPCPrice       float64 // best catalog price suitable for UPC identity page
	UPCMerchant    string
	UPCMin         float64
	UPCMax         float64
	UPCCount       int
	DeepLinkOK     bool // true when we have a direct product URL (Amazon /dp, retail, offer, or UPC dossier)
	// Offers are atomic unit-price observations for data-capture export (not ranges).
	Offers []models.MarketOffer
}

type cachedProductIdentity struct {
	id     productIdentity
	expiry time.Time
	ok     bool // false = negative cache (not found / error)
}

var (
	productIDCacheMu sync.RWMutex
	productIDCache   = map[string]cachedProductIdentity{}
	productHTTPClient = &http.Client{Timeout: 8 * time.Second}

	// UPCItemDB paid plan (never log the raw key).
	upcitemdbMu     sync.RWMutex
	upcitemdbAPIKey string

	// Global rate limiter — DEV plan: ~1 lookup/2s, ~1 search/6s, ≤2 concurrent.
	// We serialize all UPCItemDB HTTP and space requests to stay under plan limits.
	upcRateMu       sync.Mutex
	upcLastLookup   time.Time
	upcLastSearch   time.Time
	upcBackoffUntil time.Time
	upcLastKind     string // "lookup" | "search"
)

// ConfigureUPCItemDB enables paid UPCitemdb /prod/v1 endpoints with user_key header.
// Empty key keeps the free trial endpoints (/prod/trial).
func ConfigureUPCItemDB(apiKey string) {
	upcitemdbMu.Lock()
	defer upcitemdbMu.Unlock()
	upcitemdbAPIKey = strings.TrimSpace(apiKey)
}

// UPCItemDBConfigured reports whether a paid API key is loaded.
func UPCItemDBConfigured() bool {
	upcitemdbMu.RLock()
	defer upcitemdbMu.RUnlock()
	return upcitemdbAPIKey != ""
}

func upcitemdbKey() string {
	upcitemdbMu.RLock()
	defer upcitemdbMu.RUnlock()
	return upcitemdbAPIKey
}

// upcitemdbBasePath returns "v1" for paid plans, "trial" for free.
func upcitemdbBasePath() string {
	if UPCItemDBConfigured() {
		return "v1"
	}
	return "trial"
}

// upcitemdbMinGap is the minimum spacing between requests of a given kind.
// DEV plan published limits: 1 lookup / 2s, 1 search / 6s (conservative).
func upcitemdbMinGap(kind string) time.Duration {
	paid := UPCItemDBConfigured()
	if kind == "search" {
		if paid {
			return 6500 * time.Millisecond
		}
		return 15 * time.Second
	}
	// lookup
	if paid {
		return 2100 * time.Millisecond
	}
	return 10 * time.Second
}

// upcitemdbAcquire waits for rate-limit spacing. kind is "lookup" or "search".
// Returns false if context cancelled or still cooling down after a 429.
func upcitemdbAcquire(ctx context.Context, kind string) bool {
	upcRateMu.Lock()
	defer upcRateMu.Unlock()

	now := time.Now()
	if !upcBackoffUntil.IsZero() && now.Before(upcBackoffUntil) {
		wait := upcBackoffUntil.Sub(now)
		upcRateMu.Unlock()
		select {
		case <-ctx.Done():
			upcRateMu.Lock()
			return false
		case <-time.After(wait):
		}
		upcRateMu.Lock()
		now = time.Now()
	}

	var last time.Time
	if kind == "search" {
		last = upcLastSearch
	} else {
		last = upcLastLookup
	}
	// Also space from the other kind slightly so we never double-fire.
	if !upcLastLookup.IsZero() && upcLastLookup.After(last) {
		// keep last as kind-specific; additional floor vs any prior call
	}
	gap := upcitemdbMinGap(kind)
	if !last.IsZero() {
		elapsed := now.Sub(last)
		if elapsed < gap {
			wait := gap - elapsed
			upcRateMu.Unlock()
			select {
			case <-ctx.Done():
				upcRateMu.Lock()
				return false
			case <-time.After(wait):
			}
			upcRateMu.Lock()
			now = time.Now()
		}
	}
	// Floor: never issue two UPCItemDB calls < 500ms apart (any kind).
	prevAny := upcLastLookup
	if upcLastSearch.After(prevAny) {
		prevAny = upcLastSearch
	}
	if !prevAny.IsZero() {
		if elapsed := now.Sub(prevAny); elapsed < 500*time.Millisecond {
			wait := 500*time.Millisecond - elapsed
			upcRateMu.Unlock()
			select {
			case <-ctx.Done():
				upcRateMu.Lock()
				return false
			case <-time.After(wait):
			}
			upcRateMu.Lock()
			now = time.Now()
		}
	}

	if kind == "search" {
		upcLastSearch = now
	} else {
		upcLastLookup = now
	}
	upcLastKind = kind
	return true
}

func upcitemdbNote429() {
	upcRateMu.Lock()
	defer upcRateMu.Unlock()
	// Cool off before more calls (DEV rolling windows are 30s).
	upcBackoffUntil = time.Now().Add(35 * time.Second)
}

// enrichProductLinks resolves UPC/SKU to real product identity (title, ASIN) and
// rewrites shop/Amazon/federal links to be as product-specific as possible.
// Soft-fails always; bounded by IF_PRODUCT_LINK_RESOLVES (default 16).
// federalPrice is optional AbilityOne.com NSN channel price stamped onto each tile's federal link.
func enrichProductLinks(ctx context.Context, refs []models.CommercialReference, entityID string) []models.CommercialReference {
	return enrichProductLinksWithFederal(ctx, refs, entityID, "", "")
}

func enrichProductLinksWithFederal(ctx context.Context, refs []models.CommercialReference, entityID, federalPrice, federalSource string) []models.CommercialReference {
	if len(refs) == 0 {
		return refs
	}
	limit := productLinkResolveLimit()
	if limit <= 0 {
		// Still rewrite links with deterministic rules (no network).
		return applyDeterministicProductLinks(refs, entityID, nil, federalPrice, federalSource, nsnMarketBand{})
	}

	// Independent budget so a spent parent analyze context cannot zero out deep-link work.
	budget := productLinkResolveBudget()
	base := ctx
	if base == nil {
		base = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(base), budget)
	defer cancel()

	// Rank candidates: has UPC first, then ASIN-as-SKU, then SKU+mfr+description.
	// Deduplicate by cache key so we only network-resolve each UPC/SKU once.
	type cand struct {
		idx   int
		score int
		key   string
		sku   string
		upc   string
		mfr   string
		title string
	}
	var cands []cand
	seenKey := map[string]bool{}
	for i, r := range refs {
		upc := normalizeUPCDigits(r.UPC)
		sku := strings.TrimSpace(r.SKU)
		if upc == "" && sku == "" {
			continue
		}
		key := productCacheKey(sku, upc)
		if seenKey[key] {
			// Still track first index only for network; expand step covers siblings.
			continue
		}
		seenKey[key] = true
		score := 0
		if upc != "" {
			score += 50
		}
		if _, ok := amazonASINFromSKU(sku); ok {
			score += 40 // ASIN deep-link + price resolve is high value
		}
		if sku != "" {
			score += 20
		}
		if strings.TrimSpace(r.Manufacturer) != "" {
			score += 5
		}
		if strings.TrimSpace(r.Description) != "" {
			score += 10 // description enables much better product search
		}
		// Prefer unpriced tiles so we spend budget where price backfill helps most.
		if strings.TrimSpace(r.Price) == "" {
			score += 8
		}
		cands = append(cands, cand{
			idx: i, score: score, key: key, sku: sku, upc: upc,
			mfr: strings.TrimSpace(r.Manufacturer), title: strings.TrimSpace(r.Description),
		})
	}
	if len(cands) > 1 {
		// sort by score desc
		for i := 0; i < len(cands); i++ {
			for j := i + 1; j < len(cands); j++ {
				if cands[j].score > cands[i].score {
					cands[i], cands[j] = cands[j], cands[i]
				}
			}
		}
	}
	if len(cands) > limit {
		cands = cands[:limit]
	}

	resolved := make(map[int]*productIdentity, len(cands))
	var mu sync.Mutex
	// Always serial for UPCItemDB HTTP (rate-limited DEV: 1 lookup/2s, 1 search/6s).
	// Parallelism here only burns quota and triggers 429s.
	sem := make(chan struct{}, 1)
	var wg sync.WaitGroup
	for _, c := range cands {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-probeCtx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			id, ok := resolveProductIdentity(probeCtx, c.sku, c.upc, c.mfr, c.title)
			// SerpAPI Google Shopping: fill prices/ranges/links for search tiles and
			// enrich weak UPCItemDB hits (paid; key via ConfigureSerpAPI).
			if SerpAPIEnabled() && ( !ok || needsSerpEnrich(id) ) {
				if sid, sok := resolveViaSerpShopping(probeCtx, c.sku, c.upc, c.mfr, c.title); sok {
					if ok {
						id = mergeProductIdentity(id, sid)
					} else {
						id = sid
					}
					ok = true
				}
			}
			if !ok {
				return
			}
			mu.Lock()
			resolved[c.idx] = &id
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Propagate resolved identity to every ref sharing the same UPC or SKU,
	// not only the indices we network-resolved (critical for multi-row ETS tiles).
	resolved = expandResolvedIdentities(refs, resolved)

	// NSN-level market band from any multi-offer resolve — used so search-only
	// ETS rows that never got their own API hit still show a useful range.
	nsnBand := buildNSNMarketBand(resolved)

	return applyDeterministicProductLinks(refs, entityID, resolved, federalPrice, federalSource, nsnBand)
}

// nsnMarketBand is a multi-offer price span observed for at least one commercial
// identity on this NSN analysis (used as fallback for other search-only tiles).
type nsnMarketBand struct {
	Min   float64
	Max   float64
	Count int
}

func buildNSNMarketBand(resolved map[int]*productIdentity) nsnMarketBand {
	var b nsnMarketBand
	for _, id := range resolved {
		if id == nil {
			continue
		}
		// Prefer full catalog span when available.
		min, max, n := id.UPCMin, id.UPCMax, id.UPCCount
		if n < 2 || min <= 0 {
			min, max, n = id.ShopMin, id.ShopMax, id.ShopCount
		}
		if n < 2 || min <= 0 {
			min, max, n = id.AmazonMin, id.AmazonMax, id.AmazonCount
		}
		if n < 2 || min <= 0 {
			continue
		}
		if b.Count == 0 {
			b = nsnMarketBand{Min: min, Max: max, Count: n}
			continue
		}
		// Merge: widest span, sum-ish count (use max count as representative sample size).
		if min < b.Min {
			b.Min = min
		}
		if max > b.Max {
			b.Max = max
		}
		if n > b.Count {
			b.Count = n
		}
	}
	return b
}

// expandResolvedIdentities copies a resolved identity onto sibling refs that
// share UPC, SKU, model, or ASIN but were not selected for a network resolve.
func expandResolvedIdentities(refs []models.CommercialReference, resolved map[int]*productIdentity) map[int]*productIdentity {
	if resolved == nil {
		resolved = map[int]*productIdentity{}
	}
	byUPC := map[string]*productIdentity{}
	bySKU := map[string]*productIdentity{}
	byASIN := map[string]*productIdentity{}
	for i, id := range resolved {
		if id == nil {
			continue
		}
		if u := normalizeUPCDigits(id.UPC); u != "" {
			byUPC[u] = id
		}
		if a := strings.ToUpper(strings.TrimSpace(id.ASIN)); a != "" {
			byASIN[a] = id
			bySKU[a] = id
		}
		if i >= 0 && i < len(refs) {
			if u := normalizeUPCDigits(refs[i].UPC); u != "" {
				byUPC[u] = id
			}
			if s := strings.ToUpper(strings.TrimSpace(refs[i].SKU)); s != "" {
				bySKU[s] = id
				if a, ok := amazonASINFromSKU(s); ok {
					byASIN[a] = id
				}
			}
		}
		if s := strings.ToUpper(strings.TrimSpace(id.Model)); s != "" {
			bySKU[s] = id
		}
	}
	out := make(map[int]*productIdentity, len(refs))
	for i, id := range resolved {
		if id != nil {
			out[i] = id
		}
	}
	for i, r := range refs {
		if out[i] != nil {
			continue
		}
		if u := normalizeUPCDigits(r.UPC); u != "" {
			if id := byUPC[u]; id != nil {
				out[i] = id
				continue
			}
		}
		s := strings.ToUpper(strings.TrimSpace(r.SKU))
		if s != "" {
			if id := bySKU[s]; id != nil {
				out[i] = id
				continue
			}
			if a, ok := amazonASINFromSKU(s); ok {
				if id := byASIN[a]; id != nil {
					out[i] = id
					continue
				}
			}
			// Compact match: BICCSM11BK vs BIC CSM11BK / CSM11BK
			compact := compactAlnum(s)
			if compact != "" {
				for k, id := range bySKU {
					kc := compactAlnum(k)
					if kc == compact || strings.HasSuffix(compact, kc) || strings.HasSuffix(kc, compact) {
						// Avoid tiny false positives (e.g. "BK")
						if len(kc) >= 5 && len(compact) >= 5 {
							out[i] = id
							break
						}
					}
				}
			}
		}
	}
	return out
}

func applyDeterministicProductLinks(refs []models.CommercialReference, entityID string, resolved map[int]*productIdentity, federalPrice, federalSource string, nsnBand nsnMarketBand) []models.CommercialReference {
	digitsNSN := digitsOnlyString(entityID)
	fedPrice := strings.TrimSpace(federalPrice)
	fedSrc := strings.TrimSpace(federalSource)
	if fedSrc == "" && fedPrice != "" {
		fedSrc = "ABILITYONE_COM"
	}
	for i := range refs {
		r := &refs[i]
		id := (*productIdentity)(nil)
		if resolved != nil {
			id = resolved[i]
		}

		// Enrich description from resolved identity when missing.
		if id != nil {
			if r.Description == "" && id.Title != "" {
				r.Description = id.Title
			}
			if r.Manufacturer == "" && id.Brand != "" {
				r.Manufacturer = id.Brand
			}
			if r.UPC == "" && id.UPC != "" {
				r.UPC = id.UPC
			}
		}

		upc := normalizeUPCDigits(r.UPC)
		if id != nil && id.UPC != "" {
			upc = normalizeUPCDigits(id.UPC)
		}
		sku := strings.TrimSpace(r.SKU)
		mfr := strings.TrimSpace(r.Manufacturer)
		if id != nil && id.Brand != "" && (mfr == "" || strings.EqualFold(mfr, "unknown")) {
			mfr = id.Brand
		}
		title := strings.TrimSpace(r.Description)
		// Prefer resolver title when ETS description is terse/catalog-coded.
		if id != nil && id.Title != "" {
			if title == "" || looksLikeCatalogCodeDescription(title) {
				title = id.Title
				r.Description = id.Title
			}
		}

		// Strong multi-field product query (brand + humanized description + SKU + UPC).
		searchQ := buildProductSearchQuery(mfr, sku, upc, title)

		// --- Amazon: /dp when ASIN known; otherwise rich product search ---
		asin := ""
		if id != nil && id.ASIN != "" {
			asin = id.ASIN
		} else if a, ok := amazonASINFromSKU(sku); ok {
			asin = a
		}
		amazonProduct := false
		if asin != "" {
			r.LinkAmazon = "https://www.amazon.com/dp/" + asin
			amazonProduct = true
		} else if searchQ != "" {
			r.LinkAmazon = "https://www.amazon.com/s?k=" + url.QueryEscape(searchQ)
		} else {
			r.LinkAmazon = buildAmazonProductSearchURL(mfr, sku, upc, title)
		}

		// --- Shop: true product URL first, then rich retailer/Google search ---
		r.LinkShop = ""
		if id != nil {
			if id.RetailURL != "" {
				r.LinkShop = id.RetailURL
			} else if id.OfferLink != "" {
				r.LinkShop = id.OfferLink
			}
		}
		if r.LinkShop == "" {
			r.LinkShop = buildBestShopURL(mfr, sku, upc, title, searchQ)
		}

		// --- UPC identity page (human-readable product dossier) ---
		if upc != "" {
			r.LinkUPC = "https://www.upcitemdb.com/upc/" + upc
		}

		// --- Federal: AbilityOne.com with dashed NSN (or SKU) ---
		r.LinkGSA = buildFederalCatalogURL(sku, upc, digitsNSN)

		r.LinkWebsite = manufacturerHomepage(mfr)

		// Successful deep / near-deep link for price attach.
		// True product pages count; also rich multi-token product searches (not bare SKU).
		strongSearch := searchQ != "" && (strings.Count(searchQ, " ") >= 2 || upc != "")
		deepOK := amazonProduct ||
			(id != nil && (id.RetailURL != "" || id.OfferLink != "" || id.DeepLinkOK)) ||
			r.LinkUPC != "" ||
			strongSearch

		// --- Per-link channel prices (shown next to Amazon / Shop / UPC / AbilityOne) ---
		// Direct product URL → single listing price.
		// Search-only (no direct Amazon/shop match) → range of top market results.
		shopIsDirect := isDirectProductURL(r.LinkShop)
		if id != nil {
			// Prefer shop link from non-Amazon offer when we have one (before pricing).
			if !shopIsDirect && id.ShopLink != "" && isDirectProductURL(id.ShopLink) {
				r.LinkShop = id.ShopLink
				shopIsDirect = true
			} else if r.LinkShop == "" && id.ShopLink != "" {
				r.LinkShop = id.ShopLink
				shopIsDirect = isDirectProductURL(r.LinkShop)
			}
			// Surface actual merchant name for UI (avoid generic "Retail product").
			if id.ShopMerchant != "" && !isGenericMerchant(id.ShopMerchant) {
				r.LinkShopMerchant = id.ShopMerchant
			} else if id.OfferMerchant != "" && !isGenericMerchant(id.OfferMerchant) &&
				!strings.Contains(strings.ToLower(id.OfferMerchant), "amazon") {
				r.LinkShopMerchant = id.OfferMerchant
			}

			// Amazon
			switch {
			case amazonProduct && id.AmazonPrice > 0:
				// Direct /dp/ with an Amazon offer → single verified listing price
				r.PriceAmazon = normalizePriceString(fmt.Sprintf("%.2f", id.AmazonPrice))
				r.PriceAmazonSrc = "MARKET:" + sanitizeMerchantTag(nonEmpty(id.AmazonMerchant, "AMAZON"))
				r.PriceAmazonIsRange = false
			case !amazonProduct:
				// Search page: NEVER show a bare single price (misleading vs multi-result pages).
				if min, max, n, ok := pickSearchPriceRange(
					id.AmazonMin, id.AmazonMax, id.AmazonCount,
					id.UPCMin, id.UPCMax, id.UPCCount,
					id.ShopMin, id.ShopMax, id.ShopCount,
				); ok {
					r.PriceAmazon, r.PriceAmazonIsRange = formatSearchMarketPrice(min, max, n)
					r.PriceAmazonSrc = "MARKET_RANGE:TOP_RESULTS"
				} else if id.AmazonPrice > 0 {
					r.PriceAmazon, r.PriceAmazonIsRange = formatSearchMarketPrice(id.AmazonPrice, id.AmazonPrice, 1)
					r.PriceAmazonSrc = "MARKET_RANGE:TOP_RESULTS"
				} else if id.OfferPrice > 0 {
					r.PriceAmazon, r.PriceAmazonIsRange = formatSearchMarketPrice(id.OfferPrice, id.OfferPrice, 1)
					r.PriceAmazonSrc = "MARKET_RANGE:TOP_RESULTS"
				}
			case amazonProduct && id.AmazonPrice <= 0 && id.OfferPrice > 0:
				// Direct /dp/ but no Amazon-tagged offer — leave blank; don't invent a search range on a product page
			}

			// Shop / retail
			switch {
			case shopIsDirect && id.ShopPrice > 0:
				r.PriceShop = normalizePriceString(fmt.Sprintf("%.2f", id.ShopPrice))
				r.PriceShopSrc = "MARKET:" + sanitizeMerchantTag(nonEmpty(id.ShopMerchant, "RETAIL"))
				r.PriceShopIsRange = false
			case shopIsDirect && id.ShopPrice <= 0 && id.OfferPrice > 0 && id.AmazonPrice <= 0:
				r.PriceShop = normalizePriceString(fmt.Sprintf("%.2f", id.OfferPrice))
				r.PriceShopSrc = "MARKET:" + sanitizeMerchantTag(nonEmpty(id.OfferMerchant, "OFFER"))
				r.PriceShopIsRange = false
			case !shopIsDirect:
				// Search page (HD/Google Shopping/etc.): always range / "from $X (search results)"
				if min, max, n, ok := pickSearchPriceRange(
					id.ShopMin, id.ShopMax, id.ShopCount,
					id.UPCMin, id.UPCMax, id.UPCCount,
					id.AmazonMin, id.AmazonMax, id.AmazonCount,
				); ok {
					r.PriceShop, r.PriceShopIsRange = formatSearchMarketPrice(min, max, n)
					r.PriceShopSrc = "MARKET_RANGE:TOP_RESULTS"
				} else if id.ShopPrice > 0 {
					r.PriceShop, r.PriceShopIsRange = formatSearchMarketPrice(id.ShopPrice, id.ShopPrice, 1)
					r.PriceShopSrc = "MARKET_RANGE:TOP_RESULTS"
				} else if id.OfferPrice > 0 {
					r.PriceShop, r.PriceShopIsRange = formatSearchMarketPrice(id.OfferPrice, id.OfferPrice, 1)
					r.PriceShopSrc = "MARKET_RANGE:TOP_RESULTS"
				}
			}

			// UPC identity is always multi-merchant catalog — prefer full offer range.
			if r.LinkUPC != "" {
				if id.UPCCount >= 2 && id.UPCMin > 0 {
					r.PriceUPC = formatPriceRange(id.UPCMin, id.UPCMax, id.UPCCount)
					r.PriceUPCSrc = "MARKET_RANGE:CATALOG"
					r.PriceUPCIsRange = true
				} else if id.UPCPrice > 0 {
					r.PriceUPC = normalizePriceString(fmt.Sprintf("%.2f", id.UPCPrice))
					r.PriceUPCSrc = "MARKET:" + sanitizeMerchantTag(nonEmpty(id.UPCMerchant, "CATALOG"))
				} else if id.OfferPrice > 0 {
					r.PriceUPC = normalizePriceString(fmt.Sprintf("%.2f", id.OfferPrice))
					r.PriceUPCSrc = "MARKET:" + sanitizeMerchantTag(nonEmpty(id.OfferMerchant, "CATALOG"))
				}
			}
		}

		// Search-only tiles that never got their own product resolve still show the
		// NSN-level multi-offer band (from other ETS rows that did resolve).
		if nsnBand.Count >= 1 && nsnBand.Min > 0 {
			band, isR := formatSearchMarketPrice(nsnBand.Min, nsnBand.Max, nsnBand.Count)
			if !amazonProduct && r.PriceAmazon == "" && r.LinkAmazon != "" {
				r.PriceAmazon = band
				r.PriceAmazonSrc = "MARKET_RANGE:NSN_TOP_RESULTS"
				r.PriceAmazonIsRange = isR
			}
			if !shopIsDirect && r.PriceShop == "" && r.LinkShop != "" {
				r.PriceShop = band
				r.PriceShopSrc = "MARKET_RANGE:NSN_TOP_RESULTS"
				r.PriceShopIsRange = isR
			}
		}

		// Primary tile price: if both commercial destinations are search pages, prefer
		// an honest range string over a single-offer headline that disagrees with the link.
		if !amazonProduct && !shopIsDirect {
			if r.PriceShopIsRange && r.PriceShop != "" {
				r.Price = r.PriceShop
				r.PriceSource = r.PriceShopSrc
				r.PriceAsOf = time.Now().UTC().Format("2006-01-02")
			} else if r.PriceAmazonIsRange && r.PriceAmazon != "" {
				r.Price = r.PriceAmazon
				r.PriceSource = r.PriceAmazonSrc
				r.PriceAsOf = time.Now().UTC().Format("2006-01-02")
			}
		}
		// AbilityOne.com NSN channel price on the federal link (same NSN for every commercial row).
		if fedPrice != "" && r.LinkGSA != "" {
			r.PriceFederal = normalizePriceString(fedPrice)
			r.PriceFederalSrc = fedSrc
		}

		// Atomic market offers for data-capture export (unit_price + quantity, no ranges).
		asOf := time.Now().UTC().Format("2006-01-02")
		if id != nil && len(id.Offers) > 0 {
			for i := range id.Offers {
				if id.Offers[i].AsOf == "" {
					id.Offers[i].AsOf = asOf
				}
				if id.Offers[i].Quantity <= 0 {
					id.Offers[i].Quantity = 1
				}
				// Prefer catalog title on the offer for pack/UOM parse.
				if id.Offers[i].Title == "" && id.Title != "" {
					id.Offers[i].Title = id.Title
				}
			}
			r.MarketOffers = appendMarketOffers(r.MarketOffers, id.Offers...)
		}
		// Single-price channels still get an atomic offer when offer rows were empty.
		if len(r.MarketOffers) == 0 {
			r.MarketOffers = appendMarketOffers(r.MarketOffers, singlePriceMarketOffers(*r, asOf)...)
		} else {
			// Always include AbilityOne federal list as its own atomic observation when present.
			if up, ok := parseSingleUnitPrice(r.PriceFederal); ok {
				r.MarketOffers = appendMarketOffers(r.MarketOffers, models.MarketOffer{
					UnitPrice: up,
					Quantity:  1,
					Currency:  "USD",
					Channel:   "federal",
					Merchant:  "AbilityOne.com",
					Source:    nonEmpty(r.PriceFederalSrc, "ABILITYONE_COM"),
					Link:      r.LinkGSA,
					AsOf:      asOf,
					Title:     strings.TrimSpace(r.Description),
				})
			}
		}
		// P0: pack size / UOM / price-per-each from ETS description + offer titles.
		enrichCommercialMarketOffers(r)
		// P0.5: rewrite card/link price ranges to per-each when pack is known.
		normalizeCommercialDisplayPrices(r)

		// Primary tile price: prefer shop, then amazon, then upc, then existing GSA/row price.
		if strings.TrimSpace(r.Price) == "" && id != nil && id.OfferPrice > 0 && (deepOK || id.DeepLinkOK || id.Title != "") {
			r.Price = normalizePriceString(fmt.Sprintf("%.2f", id.OfferPrice))
			src := "MARKET_OFFER"
			if id.OfferMerchant != "" {
				src = "MARKET:" + sanitizeMerchantTag(id.OfferMerchant)
			}
			r.PriceSource = src
			r.PriceAsOf = time.Now().UTC().Format("2006-01-02")
			if id.OfferMerchant != "" {
				note := "Market offer via " + id.OfferMerchant + " (resolved with product identity)"
				if r.Context == "" {
					r.Context = note
				} else if !strings.Contains(strings.ToLower(r.Context), "market offer") {
					r.Context = r.Context + " | " + note
				}
			}
		}
		// If primary still empty, promote the best channel price we have.
		if strings.TrimSpace(r.Price) == "" {
			switch {
			case r.PriceShop != "":
				r.Price, r.PriceSource = r.PriceShop, r.PriceShopSrc
			case r.PriceAmazon != "":
				r.Price, r.PriceSource = r.PriceAmazon, r.PriceAmazonSrc
			case r.PriceUPC != "":
				r.Price, r.PriceSource = r.PriceUPC, r.PriceUPCSrc
			}
			if r.Price != "" && r.PriceAsOf == "" {
				r.PriceAsOf = time.Now().UTC().Format("2006-01-02")
			}
		}

		// Always refresh PriceURL to the best product/verify link we now have
		// (earlier enrichment may have set a weak SKU-only Google URL).
		bestPriceURL := ""
		switch {
		case amazonProduct && r.PriceAmazon != "":
			bestPriceURL = r.LinkAmazon
		case r.PriceShop != "" && r.LinkShop != "":
			bestPriceURL = r.LinkShop
		case amazonProduct:
			bestPriceURL = r.LinkAmazon
		case id != nil && id.RetailURL != "":
			bestPriceURL = id.RetailURL
		case id != nil && id.OfferLink != "":
			bestPriceURL = id.OfferLink
		case r.LinkShop != "":
			bestPriceURL = r.LinkShop
		case r.LinkAmazon != "":
			bestPriceURL = r.LinkAmazon
		case r.LinkUPC != "":
			bestPriceURL = r.LinkUPC
		}
		if bestPriceURL != "" {
			// Overwrite weak prior price URLs (bare SKU searches).
			if r.PriceURL == "" || isWeakProductSearchURL(r.PriceURL) || !isWeakProductSearchURL(bestPriceURL) {
				r.PriceURL = bestPriceURL
			}
		}
	}
	return refs
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

// buildProductSearchQuery builds the strongest free-text product query we can:
// brand + humanized description + quoted SKU + UPC. This is far more specific than bare SKU.
func buildProductSearchQuery(manufacturer, sku, upc, title string) string {
	mfr := strings.TrimSpace(manufacturer)
	sku = strings.TrimSpace(sku)
	u := normalizeUPCDigits(upc)
	desc := humanizeProductDescription(title)

	var parts []string
	// Brand first when not already in description.
	if mfr != "" && (desc == "" || !strings.Contains(strings.ToLower(desc), strings.ToLower(mfr))) {
		parts = append(parts, mfr)
	}
	if desc != "" {
		// Cap description length for URL quality.
		if len(desc) > 80 {
			desc = strings.TrimSpace(desc[:80])
		}
		parts = append(parts, desc)
	}
	// Exact SKU in quotes for disambiguation (critical for multipacks / variants).
	if sku != "" && !strings.Contains(strings.ToUpper(desc), strings.ToUpper(sku)) {
		parts = append(parts, `"`+sku+`"`)
	}
	// UPC is highly specific when present.
	if u != "" {
		parts = append(parts, u)
	}
	q := strings.Join(parts, " ")
	q = strings.Join(strings.Fields(q), " ")
	return q
}

// humanizeProductDescription turns ETS catalog codes into search-friendly text.
// e.g. `9"ROLLERCOVER WOVEN NAPSIZE .5"` → `9" roller cover woven nap size .5`
func humanizeProductDescription(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Normalize common catalog compressions.
	repl := []struct{ old, new string }{
		{"ROLLERCOVER", "roller cover"},
		{"ROLLCOVER", "roller cover"},
		{"NAPSIZE", "nap size"},
		{"BALLPOINT", "ball point"},
		{"BALL-POINT", "ball point"},
		{"RETRACTABLE", "retractable"},
		{"MEDPT", "medium point"},
		{"MED POINT", "medium point"},
		{"PK12", "12 pack"},
		{"PK/12", "12 pack"},
		{"DOZ", "dozen"},
		{"WOVEN", "woven"},
		{"POLYESTER", "polyester"},
	}
	out := s
	upper := strings.ToUpper(out)
	for _, r := range repl {
		if strings.Contains(upper, r.old) {
			// Case-insensitive replace
			out = replaceInsensitive(out, r.old, r.new)
			upper = strings.ToUpper(out)
		}
	}
	// Insert spaces at letter/digit boundaries and after quotes: 9"ROLLER → 9" ROLLER
	var b strings.Builder
	runes := []rune(out)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			// digit/letter or letter/digit
			if (unicode.IsDigit(prev) && unicode.IsLetter(r)) || (unicode.IsLetter(prev) && unicode.IsDigit(r)) {
				b.WriteByte(' ')
			}
			// quote followed by letter
			if (prev == '"' || prev == '\'') && unicode.IsLetter(r) {
				b.WriteByte(' ')
			}
			// lower then upper (camel)
			if unicode.IsLower(prev) && unicode.IsUpper(r) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	out = b.String()
	// Commas to spaces for search, collapse whitespace
	out = strings.ReplaceAll(out, ",", " ")
	out = strings.ReplaceAll(out, ";", " ")
	out = strings.Join(strings.Fields(out), " ")
	return out
}

func replaceInsensitive(s, old, new string) string {
	if old == "" {
		return s
	}
	lower := strings.ToLower(s)
	oldL := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], oldL)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		b.WriteString(s[i:j])
		b.WriteString(new)
		i = j + len(old)
	}
	return b.String()
}

// buildBestShopURL prefers retailer product searches that rank specific SKUs highly.
func buildBestShopURL(manufacturer, sku, upc, title, searchQ string) string {
	if searchQ == "" {
		searchQ = buildProductSearchQuery(manufacturer, sku, upc, title)
	}
	u := normalizeUPCDigits(upc)
	// UPC-only Walmart is product-like when we lack a title.
	if searchQ == "" && u != "" {
		return "https://www.walmart.com/search?q=" + url.QueryEscape(u)
	}
	if searchQ == "" {
		return buildPreciseShopURL(manufacturer, sku, upc, title)
	}
	// Home Depot ranks industrial/paint/facility SKUs well; use rich query.
	// Analysts often find the exact cover/brush here faster than Google Shopping.
	if looksLikeFacilityOrPaintProduct(manufacturer, title, sku) {
		return "https://www.homedepot.com/s/" + url.PathEscape(searchQ)
	}
	// Google Shopping with full brand+description+SKU query.
	return "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(searchQ)
}

func looksLikeFacilityOrPaintProduct(mfr, title, sku string) bool {
	blob := strings.ToLower(mfr + " " + title + " " + sku)
	keys := []string{
		"paint", "roller", "brush", "nap", "primer", "coating",
		"wooster", "purdy", "sherwin", "premier", "homax",
		"mop", "broom", "towel", "cleaner", "janitor",
		"grainger", "uline", "tool",
	}
	for _, k := range keys {
		if strings.Contains(blob, k) {
			return true
		}
	}
	return false
}

func isWeakProductSearchURL(u string) bool {
	u = strings.ToLower(u)
	if u == "" {
		return true
	}
	// Bare SKU-ish Amazon / Google searches (few query tokens) are weak.
	if strings.Contains(u, "amazon.com/s?") || strings.Contains(u, "tbm=shop") || strings.Contains(u, "walmart.com/search") {
		// Count approximate query richness via encoded spaces / plus signs
		q := u
		if i := strings.Index(q, "q="); i >= 0 {
			q = q[i+2:]
		} else if i := strings.Index(q, "k="); i >= 0 {
			q = q[i+2:]
		}
		// Path-style Home Depot /s/QUERY
		if i := strings.Index(u, "homedepot.com/s/"); i >= 0 {
			return false // HD product search is preferred
		}
		plus := strings.Count(q, "+") + strings.Count(q, "%20")
		if plus < 2 && !strings.Contains(u, "upcitemdb.com/upc/") {
			return true
		}
	}
	return false
}

// buildAmazonProductSearchURL builds the strongest Amazon search we can without an ASIN.
// Always prefers brand + humanized description + quoted SKU over bare SKU.
func buildAmazonProductSearchURL(manufacturer, sku, upc, title string) string {
	if q := buildProductSearchQuery(manufacturer, sku, upc, title); q != "" {
		return "https://www.amazon.com/s?k=" + url.QueryEscape(q)
	}
	if u := normalizeUPCDigits(upc); u != "" {
		return "https://www.amazon.com/s?k=" + url.QueryEscape(u)
	}
	if sku = strings.TrimSpace(sku); sku != "" {
		q := sku
		if mfr := strings.TrimSpace(manufacturer); mfr != "" {
			q = mfr + " " + sku
		}
		return "https://www.amazon.com/s?k=" + url.QueryEscape(q)
	}
	return ""
}

// buildPreciseShopURL crafts a Google Shopping query that is much more specific
// than a bare manufacturer string.
func buildPreciseShopURL(manufacturer, sku, upc, title string) string {
	if q := buildProductSearchQuery(manufacturer, sku, upc, title); q != "" {
		return "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(q)
	}
	// 2) UPC alone
	if u := normalizeUPCDigits(upc); u != "" {
		return "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(u)
	}
	// 3) Exact SKU phrase + manufacturer
	if sku = strings.TrimSpace(sku); sku != "" {
		q := `"` + sku + `"`
		if mfr := strings.TrimSpace(manufacturer); mfr != "" {
			q += " " + mfr
		}
		return "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(q)
	}
	return buildShopSearchURL(manufacturer, sku, upc, title)
}

func productCacheKey(sku, upc string) string {
	if u := normalizeUPCDigits(upc); u != "" {
		return "upc:" + u
	}
	return "sku:" + strings.ToUpper(strings.TrimSpace(sku))
}

// needsSerpEnrich is true when UPCItemDB identity is missing deep links or multi-offer ranges.
func needsSerpEnrich(id productIdentity) bool {
	if id.ASIN == "" && id.RetailURL == "" && id.OfferLink == "" {
		return true
	}
	if id.OfferPrice <= 0 && id.UPCCount < 2 && id.ShopCount < 2 && id.AmazonCount < 2 {
		return true
	}
	// Have a product page but no usable prices yet.
	if id.AmazonPrice <= 0 && id.ShopPrice <= 0 && id.OfferPrice <= 0 {
		return true
	}
	return false
}

func resolveProductIdentity(ctx context.Context, sku, upc, mfr, title string) (productIdentity, bool) {
	key := productCacheKey(sku, upc)
	if hit, ok := getCachedProductID(key); ok {
		return hit.id, hit.ok
	}

	var id productIdentity
	var ok bool
	var transient bool // rate-limit / 5xx — do not long-cache as "not found"

	// Prefer UPC lookup (single definitive item) — one API call.
	// Avoid stacking search after lookup when rate-limited (429).
	if u := normalizeUPCDigits(upc); u != "" {
		id, ok, transient = upcitemdbLookup(ctx, u)
	}
	// ASIN-as-SKU: search by ASIN (expensive — only if no UPC hit).
	if !ok && !transient && ctx.Err() == nil {
		if asin, isASIN := amazonASINFromSKU(sku); isASIN {
			id, ok, transient = upcitemdbSearch(ctx, asin, sku)
		}
	}
	// Text search: ONE query only (brand+desc+SKU or brand+SKU).
	// Do not cascade multiple searches — DEV plan allows ~1 search / 6s.
	if !ok && !transient && ctx.Err() == nil {
		if _, isASIN := amazonASINFromSKU(sku); !isASIN {
			q := buildProductSearchQuery(mfr, sku, upc, title)
			if q == "" {
				q = strings.TrimSpace(mfr + " " + sku)
			}
			if q != "" {
				id, ok, transient = upcitemdbSearch(ctx, q, sku)
			}
		}
	}

	if transient {
		// Short soft-negative only; never poison the cache for hours after a gate storm.
		setCachedProductIDTTL(key, id, false, 45*time.Second)
		return id, false
	}
	setCachedProductID(key, id, ok)
	// Also cache under UPC if resolved.
	if ok && id.UPC != "" {
		setCachedProductID("upc:"+normalizeUPCDigits(id.UPC), id, true)
	}
	if ok && id.ASIN != "" {
		setCachedProductID("sku:"+strings.ToUpper(id.ASIN), id, true)
	}
	return id, ok
}

// productSearchQueryCascade returns ordered UPCItemDB search queries from most specific to least.
func productSearchQueryCascade(mfr, sku, upc, title string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.Join(strings.Fields(strings.TrimSpace(q)), " ")
		if q == "" || seen[q] {
			return
		}
		seen[q] = true
		out = append(out, q)
	}
	// 1) Full product query
	add(buildProductSearchQuery(mfr, sku, upc, title))
	// 2) Brand + humanized description (no SKU noise)
	desc := humanizeProductDescription(title)
	if mfr != "" && desc != "" {
		add(mfr + " " + desc)
	}
	// 3) Brand + SKU
	if mfr != "" && sku != "" {
		add(mfr + " " + sku)
	}
	// 4) SKU alone
	if sku != "" {
		add(sku)
	}
	// 5) Description alone
	if desc != "" {
		add(desc)
	}
	return out
}

func upcitemdbLookup(ctx context.Context, upc string) (id productIdentity, ok bool, transient bool) {
	if !upcitemdbAcquire(ctx, "lookup") {
		return productIdentity{}, false, true
	}
	u := "https://api.upcitemdb.com/prod/" + upcitemdbBasePath() + "/lookup?upc=" + url.QueryEscape(upc)
	return upcitemdbFetch(ctx, u, "")
}

func upcitemdbSearch(ctx context.Context, query, preferSKU string) (id productIdentity, ok bool, transient bool) {
	if !upcitemdbAcquire(ctx, "search") {
		return productIdentity{}, false, true
	}
	u := "https://api.upcitemdb.com/prod/" + upcitemdbBasePath() + "/search?s=" + url.QueryEscape(query)
	return upcitemdbFetch(ctx, u, preferSKU)
}

type upcOffer struct {
	Merchant     string      `json:"merchant"`
	Domain       string      `json:"domain"`
	Title        string      `json:"title"`
	Currency     string      `json:"currency"`
	Price        flexibleNum `json:"price"`
	// list_price deliberately omitted — trial API returns string or number.
	Condition    string `json:"condition"`
	Availability string `json:"availability"`
	Link         string `json:"link"`
}

// flexibleNum unmarshals JSON numbers or numeric strings into float64.
type flexibleNum float64

func (n *flexibleNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
		s = strings.TrimPrefix(s, "$")
		if s == "" {
			*n = 0
			return nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			*n = 0
			return nil // tolerate junk prices rather than fail the whole item
		}
		*n = flexibleNum(f)
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = flexibleNum(f)
	return nil
}

func (n flexibleNum) Float() float64 { return float64(n) }

func upcitemdbFetch(ctx context.Context, endpoint, preferSKU string) (id productIdentity, ok bool, transient bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return productIdentity{}, false, true
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+https://github.com/bmcelhaney/insight-forge)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	// Paid DEV/PRO plans: user_key + key_type on /prod/v1 (never log the key).
	if key := upcitemdbKey(); key != "" {
		req.Header.Set("user_key", key)
		req.Header.Set("key_type", "3scale")
	}

	resp, err := productHTTPClient.Do(req)
	if err != nil {
		recordUPCItemDBStatus(false, 0, err.Error(), "UPCItemDB request failed (network or timeout). Product identity resolve may be incomplete.")
		return productIdentity{}, false, true
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		recordUPCItemDBStatus(false, resp.StatusCode, err.Error(), "UPCItemDB response could not be read.")
		return productIdentity{}, false, true
	}

	// Shared error envelope (docs: code + message) — used for 4xx/5xx and some 200 bodies.
	var errEnv struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errEnv)
	codeUpper := strings.ToUpper(strings.TrimSpace(errEnv.Code))

	// 429 / TOO_FAST — plan rate-limit (not an invalid key).
	if resp.StatusCode == http.StatusTooManyRequests || codeUpper == "TOO_FAST" || codeUpper == "HTTP_TOO_MANY_REQUESTS" {
		upcitemdbNote429()
		recordUPCItemDBStatus(false, resp.StatusCode, nonEmpty(errEnv.Code, fmt.Sprintf("HTTP %d", resp.StatusCode)),
			"UPCItemDB rate limit hit (plan throttle). The key is likely valid — Insight Forge will slow requests. Commercial deep-links may be incomplete for this run; SerpAPI may still fill prices.")
		return productIdentity{}, false, true
	}
	if codeUpper == "EXCEED_LIMIT" {
		recordUPCItemDBStatus(false, resp.StatusCode, "EXCEED_LIMIT",
			"UPCItemDB daily request limit exceeded for this plan. Commercial catalog resolve will pause until the quota resets.")
		return productIdentity{}, false, true
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || codeUpper == "AUTH_ERR" {
		recordUPCItemDBStatus(false, resp.StatusCode, nonEmpty(errEnv.Code, fmt.Sprintf("HTTP %d", resp.StatusCode)),
			"UPCItemDB rejected the API key (unauthorized). Check IF_UPCITEMDB_KEY.")
		return productIdentity{}, false, false
	}
	if resp.StatusCode >= 500 || codeUpper == "SERVER_ERR" {
		recordUPCItemDBStatus(false, resp.StatusCode, nonEmpty(errEnv.Code, fmt.Sprintf("HTTP %d", resp.StatusCode)),
			"UPCItemDB is temporarily unavailable (server error). Product deep-link resolve may be incomplete.")
		return productIdentity{}, false, true
	}
	// 404 NOT_FOUND = no product match (or bad UPC) — API is fine; soft-miss this product only.
	// Docs: "No matched item was found or wrong endpoint path."
	if resp.StatusCode == http.StatusNotFound || codeUpper == "NOT_FOUND" || codeUpper == "INVALID_UPC" || codeUpper == "INVALID_QUERY" {
		// Do not flip global health to error — successful auth + live API.
		recordUPCItemDBStatus(true, resp.StatusCode, codeUpper,
			"UPCItemDB is live (some products return 404/not found — normal for unmatched SKUs/UPCs).")
		return productIdentity{}, false, false
	}
	if resp.StatusCode != 200 {
		recordUPCItemDBStatus(false, resp.StatusCode, fmt.Sprintf("HTTP %d %s", resp.StatusCode, errEnv.Code),
			"UPCItemDB returned an unexpected error. Product identity resolve may be incomplete.")
		return productIdentity{}, false, false
	}
	recordUPCItemDBStatus(true, 200, "", "UPCItemDB is live.")

	var payload struct {
		Code  string `json:"code"`
		Items []struct {
			Title                string     `json:"title"`
			Brand                string     `json:"brand"`
			Model                string     `json:"model"`
			UPC                  string     `json:"upc"`
			EAN                  string     `json:"ean"`
			ASIN                 string     `json:"asin"`
			Currency             string     `json:"currency"`
			LowestRecordedPrice  float64    `json:"lowest_recorded_price"`
			HighestRecordedPrice float64    `json:"highest_recorded_price"`
			Images               []string   `json:"images"`
			Offers               []upcOffer `json:"offers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		recordUPCItemDBStatus(false, 200, err.Error(), "UPCItemDB returned unreadable JSON.")
		return productIdentity{}, false, true
	}
	if len(payload.Items) == 0 {
		// Empty items with 200 — treat as miss, API still healthy.
		return productIdentity{}, false, false
	}

	// Prefer item whose model/title contains our SKU, else first with ASIN, else first.
	prefer := strings.ToUpper(strings.TrimSpace(preferSKU))
	bestIdx := 0
	bestScore := -1
	for i, it := range payload.Items {
		score := 0
		if it.ASIN != "" {
			score += 10
		}
		if prefer != "" {
			blob := strings.ToUpper(it.Model + " " + it.Title)
			if strings.Contains(blob, prefer) {
				score += 20
			}
			// Soft match without common separators
			compactPref := compactAlnum(prefer)
			if compactPref != "" && strings.Contains(compactAlnum(blob), compactPref) {
				score += 15
			}
		}
		if it.Title != "" {
			score += 1
		}
		if len(it.Offers) > 0 {
			score += 3
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	it := payload.Items[bestIdx]
	id = productIdentity{
		Title: strings.TrimSpace(it.Title),
		Brand: strings.TrimSpace(it.Brand),
		Model: strings.TrimSpace(it.Model),
		UPC:   normalizeUPCDigits(it.UPC),
		EAN:   digitsOnlyString(it.EAN),
		ASIN:  strings.TrimSpace(it.ASIN),
	}
	if id.UPC == "" && len(id.EAN) >= 12 {
		// Convert EAN-13 (0+UPC) to UPC-12 when applicable.
		if strings.HasPrefix(id.EAN, "0") && len(id.EAN) == 13 {
			id.UPC = id.EAN[1:]
		}
	}
	// Trial API often omits ASIN; recover from offer titles / image CDN URLs.
	if id.ASIN == "" {
		id.ASIN = extractASINFromOffersAndImages(it.Offers, it.Images)
	}
	// Direct retailer product page from image CDN patterns (Office Depot, etc.).
	id.RetailURL = extractRetailProductURL(it.Images)

	// Select overall + channel-specific offer prices (Amazon vs retail vs catalog ranges).
	ch := collectChannelOffers(it.Offers)
	if ch.BestPrice > 0 {
		id.OfferPrice = ch.BestPrice
		id.OfferMerchant = ch.BestMerchant
		id.OfferCurrency = ch.BestCurrency
		id.OfferLink = ch.BestLink
	}
	id.AmazonPrice = ch.AmazonPrice
	id.AmazonMerchant = ch.AmazonMerchant
	id.AmazonMin, id.AmazonMax, id.AmazonCount = ch.AmazonMin, ch.AmazonMax, ch.AmazonCount
	id.ShopPrice = ch.ShopPrice
	id.ShopMerchant = ch.ShopMerchant
	id.ShopLink = ch.ShopLink
	id.ShopMin, id.ShopMax, id.ShopCount = ch.ShopMin, ch.ShopMax, ch.ShopCount
	id.UPCPrice = ch.BestPrice
	id.UPCMerchant = ch.BestMerchant
	id.UPCMin, id.UPCMax, id.UPCCount = ch.AllMin, ch.AllMax, ch.AllCount
	if len(ch.Offers) > 0 {
		id.Offers = append(id.Offers, ch.Offers...)
	}
	if id.OfferLink == "" && ch.ShopLink != "" {
		id.OfferLink = ch.ShopLink
	}
	// Fallback: recorded low/high only when we have no live offer rows.
	// Do NOT replace a multi-offer range with lowest_recorded/highest_recorded —
	// those historical extremes are often absurd (pennies vs bulk) vs the live page.
	if id.UPCCount == 0 && id.OfferPrice <= 0 {
		low := it.LowestRecordedPrice
		high := it.HighestRecordedPrice
		if low >= 1.0 && low <= 2500 {
			if high <= 0 || high/low <= 40 {
				id.OfferPrice = low
				id.OfferMerchant = "catalog low"
				id.OfferCurrency = "USD"
				id.UPCPrice = low
				id.UPCMerchant = "catalog low"
				if high >= low && high <= 2500 && high/low <= 40 && high > low {
					id.UPCMin, id.UPCMax, id.UPCCount = low, high, 2
					if id.ShopCount < 2 && id.ShopPrice <= 0 {
						id.ShopMin, id.ShopMax, id.ShopCount = low, high, 2
					}
				} else {
					id.UPCCount = 1
				}
			}
		}
	}

	id.DeepLinkOK = id.ASIN != "" || id.UPC != "" || id.RetailURL != "" || id.OfferLink != "" || id.OfferPrice > 0

	if id.Title == "" && id.ASIN == "" && id.UPC == "" && id.RetailURL == "" && id.OfferPrice <= 0 {
		return productIdentity{}, false, false
	}
	return id, true, false
}

// channelOfferSet holds the best price per commercial destination plus top-result ranges.
type channelOfferSet struct {
	BestPrice      float64
	BestMerchant   string
	BestCurrency   string
	BestLink       string
	AmazonPrice    float64
	AmazonMerchant string
	AmazonMin      float64
	AmazonMax      float64
	AmazonCount    int
	ShopPrice      float64
	ShopMerchant   string
	ShopLink       string
	ShopMin        float64
	ShopMax        float64
	ShopCount      int
	AllMin         float64
	AllMax         float64
	AllCount       int
	// Offers is every usable atomic price row (qty=1) for data-capture export.
	Offers []models.MarketOffer
}

// maxTopOfferResults caps "top results" used for search-link ranges (Amazon/shop
// when no product deep-link). UPC identity uses the full offer set (see fullRange).
const maxTopOfferResults = 8

// collectChannelOffers splits offers into Amazon vs other retail channels and
// computes min/max ranges. UPC/catalog uses ALL usable offers; Amazon/shop search
// ranges use the top score band (up to maxTopOfferResults).
func collectChannelOffers(offers []upcOffer) channelOfferSet {
	var out channelOfferSet
	type scored struct {
		price    float64
		merchant string
		currency string
		link     string
		score    int
		channel  string // amazon | shop
	}
	var all []scored
	for _, o := range offers {
		price := o.Price.Float()
		if price <= 0 || price > 50000 || price < 0.5 {
			continue
		}
		if av := strings.ToLower(o.Availability); strings.Contains(av, "out of stock") {
			continue
		}
		cur := strings.ToUpper(strings.TrimSpace(o.Currency))
		score := 0
		switch cur {
		case "", "USD", "US$":
			score += 50
			cur = "USD"
		case "CAD":
			score += 5
		default:
			score += 10
		}
		merch := strings.TrimSpace(o.Merchant)
		if merch == "" {
			merch = strings.TrimSpace(o.Domain)
		}
		ml := strings.ToLower(merch + " " + o.Domain + " " + o.Link)
		channel := "shop"
		switch {
		case strings.Contains(ml, "amazon"):
			channel = "amazon"
			score += 40
		case strings.Contains(ml, "home depot"), strings.Contains(ml, "homedepot"):
			score += 38
		case strings.Contains(ml, "staples"):
			score += 35
		case strings.Contains(ml, "office depot"), strings.Contains(ml, "officedepot"):
			score += 35
		case strings.Contains(ml, "lowes"), strings.Contains(ml, "lowe's"):
			score += 34
		case strings.Contains(ml, "walmart"):
			score += 30
		case strings.Contains(ml, "grainger"):
			score += 30
		case strings.Contains(ml, "sherwin"):
			score += 28
		case strings.Contains(ml, "target"):
			score += 25
		case strings.Contains(ml, "uline"):
			score += 25
		case strings.Contains(ml, "shoplet"):
			score += 22
		case strings.Contains(ml, "sam"):
			score += 20
		}
		if strings.EqualFold(strings.TrimSpace(o.Condition), "New") || o.Condition == "" {
			score += 10
		}
		link := strings.TrimSpace(o.Link)
		if link != "" {
			score += 8
		}
		all = append(all, scored{price: price, merchant: merch, currency: cur, link: link, score: score, channel: channel})
	}

	// Prefer USD / blank currency for display ranges; keep foreign only if nothing else.
	filterUSD := func(list []scored) []scored {
		var usd []scored
		for _, c := range list {
			if c.currency == "USD" || c.currency == "" {
				usd = append(usd, c)
			}
		}
		if len(usd) > 0 {
			return usd
		}
		return list
	}

	// fullRange: every usable offer — used for UPC identity (matches catalog breadth better).
	fullRange := func(list []scored) (best scored, min, max float64, n int, ok bool) {
		list = filterUSD(list)
		if len(list) == 0 {
			return scored{}, 0, 0, 0, false
		}
		// Sort by price ascending for stable best = lowest among USD.
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				if list[j].price < list[i].price {
					list[i], list[j] = list[j], list[i]
				}
			}
		}
		min, max = list[0].price, list[0].price
		// Prefer best as lowest USD with a strong merchant score when possible.
		best = list[0]
		bestScore := list[0].score
		for _, c := range list {
			if c.price < min {
				min = c.price
			}
			if c.price > max {
				max = c.price
			}
			if c.score > bestScore || (c.score == bestScore && c.price < best.price) {
				bestScore = c.score
				best = c
			}
		}
		// Among high-score merchants, prefer lowest price as "best" single.
		for _, c := range list {
			if c.score >= bestScore-15 && c.price < best.price {
				best = c
			}
		}
		return best, min, max, len(list), true
	}

	// topRange: score band + cap — used for Amazon/shop search "top results" ranges.
	topRange := func(list []scored) (best scored, min, max float64, n int, ok bool) {
		list = filterUSD(list)
		if len(list) == 0 {
			return scored{}, 0, 0, 0, false
		}
		bestScore := list[0].score
		for _, c := range list {
			if c.score > bestScore {
				bestScore = c.score
			}
		}
		var band []scored
		for _, c := range list {
			if c.score >= bestScore-15 {
				band = append(band, c)
			}
		}
		for i := 0; i < len(band); i++ {
			for j := i + 1; j < len(band); j++ {
				if band[j].price < band[i].price {
					band[i], band[j] = band[j], band[i]
				}
			}
		}
		if len(band) > maxTopOfferResults {
			band = band[:maxTopOfferResults]
		}
		min, max = band[0].price, band[0].price
		for _, c := range band {
			if c.price < min {
				min = c.price
			}
			if c.price > max {
				max = c.price
			}
		}
		return band[0], min, max, len(band), true
	}

	var amazonList, shopList []scored
	for _, c := range all {
		switch c.channel {
		case "amazon":
			amazonList = append(amazonList, c)
		default:
			shopList = append(shopList, c)
		}
	}

	// Catalog / UPC: full offer set so range matches breadth of the UPC identity page.
	if b, min, max, n, ok := fullRange(all); ok {
		out.BestPrice, out.BestMerchant, out.BestCurrency, out.BestLink = b.price, b.merchant, b.currency, b.link
		out.AllMin, out.AllMax, out.AllCount = min, max, n
	}
	// Amazon / shop search ranges: top-result band (still useful when no deep-link).
	if b, min, max, n, ok := topRange(amazonList); ok {
		out.AmazonPrice, out.AmazonMerchant = b.price, b.merchant
		out.AmazonMin, out.AmazonMax, out.AmazonCount = min, max, n
	}
	if b, min, max, n, ok := topRange(shopList); ok {
		out.ShopPrice, out.ShopMerchant, out.ShopLink = b.price, b.merchant, b.link
		out.ShopMin, out.ShopMax, out.ShopCount = min, max, n
	}
	// When shop deep-link is missing, shop search range can use full non-Amazon set
	// so analysts see the same breadth as the UPC dossier for retail search.
	if _, min, max, n, ok := fullRange(shopList); ok && n > out.ShopCount {
		out.ShopMin, out.ShopMax, out.ShopCount = min, max, n
	}
	// Atomic offer rows for data-capture (one hit per merchant price; quantity = 1).
	usdAll := filterUSD(all)
	for _, c := range usdAll {
		ch := c.channel
		if ch == "" {
			ch = "shop"
		}
		if ch == "amazon" {
			// keep
		} else {
			ch = "catalog"
			if c.channel == "shop" {
				ch = "shop"
			}
		}
		out.Offers = append(out.Offers, models.MarketOffer{
			UnitPrice: c.price,
			Quantity:  1,
			Currency:  nonEmpty(c.currency, "USD"),
			Channel:   ch,
			Merchant:  c.merchant,
			Source:    "UPCITEMDB",
			Link:      c.link,
			// Title filled later from productIdentity when offers are attached to a ref.
		})
	}
	return out
}

// formatPriceRange renders "$12.99 – $24.50 (5 offers)" for multi-result catalog pricing.
func formatPriceRange(min, max float64, n int) string {
	if min <= 0 {
		return ""
	}
	lo := normalizePriceString(fmt.Sprintf("%.2f", min))
	if max <= min || n < 2 {
		return lo
	}
	hi := normalizePriceString(fmt.Sprintf("%.2f", max))
	if n > 0 {
		return fmt.Sprintf("%s – %s (%d offers)", lo, hi, n)
	}
	return fmt.Sprintf("%s – %s", lo, hi)
}

// formatSearchMarketPrice formats prices for SEARCH destinations (not product pages).
// Always returns isRange=true so the UI never treats a search hit as a single verified listing.
// Examples: "$34.99 – $59.92 (7 offers)", "$69.59 (3 offers)", "from $69.59 (search results)".
func formatSearchMarketPrice(min, max float64, n int) (display string, isRange bool) {
	if min <= 0 && max <= 0 {
		return "", false
	}
	if min <= 0 {
		min = max
	}
	if max < min {
		max = min
	}
	lo := normalizePriceString(fmt.Sprintf("%.2f", min))
	hi := normalizePriceString(fmt.Sprintf("%.2f", max))
	if n >= 2 && max > min {
		return fmt.Sprintf("%s – %s (%d offers)", lo, hi, n), true
	}
	if n >= 2 {
		// Multiple hits at the same price — still not a single deep-link listing.
		return fmt.Sprintf("%s (%d offers)", lo, n), true
	}
	// Only one priced hit observed for this query, but the URL is still a search page.
	return fmt.Sprintf("from %s (search results)", lo), true
}

// pickSearchPriceRange chooses the best multi-offer band for a search-only link.
// Prefers the primary channel's range; falls back to catalog / alternate channel
// so Amazon search and shop search still show a useful top-results spread.
func pickSearchPriceRange(aMin, aMax float64, aN int, bMin, bMax float64, bN int, cMin, cMax float64, cN int) (min, max float64, n int, ok bool) {
	type band struct {
		min, max float64
		n        int
	}
	// Prefer widest useful multi-offer band among candidates (catalog often has most offers).
	cands := []band{
		{aMin, aMax, aN},
		{bMin, bMax, bN},
		{cMin, cMax, cN},
	}
	best := band{}
	for _, b := range cands {
		if b.min <= 0 || b.n < 1 {
			continue
		}
		// Prefer multi-offer ranges with more samples; then wider span.
		if best.n == 0 || b.n > best.n || (b.n == best.n && (b.max-b.min) > (best.max-best.min)) {
			best = b
		}
	}
	if best.n == 0 || best.min <= 0 {
		return 0, 0, 0, false
	}
	if best.max < best.min {
		best.max = best.min
	}
	return best.min, best.max, best.n, true
}

// isDirectProductURL is true for product detail pages (not search result pages).
func isDirectProductURL(u string) bool {
	u = strings.ToLower(strings.TrimSpace(u))
	if u == "" {
		return false
	}
	// Explicit search / multi-result UIs — never treat as a single listing.
	if strings.Contains(u, "google.com/search") ||
		strings.Contains(u, "ibp=oshop") ||
		strings.Contains(u, "tbm=shop") ||
		strings.Contains(u, "udm=28") ||
		strings.Contains(u, "amazon.com/s?") ||
		strings.Contains(u, "amazon.com/s/") ||
		strings.Contains(u, "homedepot.com/s/") ||
		strings.Contains(u, "walmart.com/search") ||
		strings.Contains(u, "/search?") ||
		strings.Contains(u, "/search/") {
		return false
	}
	switch {
	case strings.Contains(u, "amazon.com/dp/"), strings.Contains(u, "amazon.com/gp/product/"):
		return true
	case strings.Contains(u, "officedepot.com/a/products/"):
		return true
	case strings.Contains(u, "walmart.com/ip/"):
		return true
	case strings.Contains(u, "upcitemdb.com/norob/"):
		return true // affiliate deep-link to a specific merchant listing
	case strings.Contains(u, "homedepot.com/p/"):
		return true
	case strings.Contains(u, "/product/"), strings.Contains(u, "/p/"):
		return true
	default:
		return false
	}
}

// pickBestMarketOffer chooses a displayable commercial offer price and optional product link.
// Prefers USD (or blank currency), known US office retailers, New condition, and sane price bands.
func pickBestMarketOffer(offers []upcOffer) (price float64, merchant, currency, link string, ok bool) {
	type cand struct {
		price    float64
		merchant string
		currency string
		link     string
		score    int
	}
	var cands []cand
	for _, o := range offers {
		price := o.Price.Float()
		if price <= 0 || price > 50000 {
			continue
		}
		// Skip absurd pennies / noise often present in "lowest_recorded".
		if price < 0.5 {
			continue
		}
		// Skip clearly unavailable listings.
		if av := strings.ToLower(o.Availability); strings.Contains(av, "out of stock") {
			continue
		}
		cur := strings.ToUpper(strings.TrimSpace(o.Currency))
		// Prefer USD / blank; deprioritize CAD/EUR but still allow if nothing else.
		score := 0
		switch cur {
		case "", "USD", "US$":
			score += 50
			cur = "USD"
		case "CAD":
			score += 5
		default:
			score += 10
		}
		merch := strings.TrimSpace(o.Merchant)
		if merch == "" {
			merch = strings.TrimSpace(o.Domain)
		}
		ml := strings.ToLower(merch + " " + o.Domain)
		switch {
		case strings.Contains(ml, "amazon"):
			score += 40
		case strings.Contains(ml, "staples"):
			score += 35
		case strings.Contains(ml, "office depot"), strings.Contains(ml, "officedepot"):
			score += 35
		case strings.Contains(ml, "walmart"):
			score += 30
		case strings.Contains(ml, "target"):
			score += 25
		case strings.Contains(ml, "home depot"), strings.Contains(ml, "homedepot"):
			score += 38
		case strings.Contains(ml, "lowes"), strings.Contains(ml, "lowe's"):
			score += 34
		case strings.Contains(ml, "grainger"):
			score += 30
		case strings.Contains(ml, "uline"):
			score += 25
		case strings.Contains(ml, "shoplet"):
			score += 22
		case strings.Contains(ml, "sherwin"):
			score += 28
		case strings.Contains(ml, "sam"):
			score += 20
		}
		if strings.EqualFold(strings.TrimSpace(o.Condition), "New") || o.Condition == "" {
			score += 10
		}
		offerLink := strings.TrimSpace(o.Link)
		if offerLink != "" {
			score += 8 // having a product link makes this offer more useful
		}
		cands = append(cands, cand{price: price, merchant: merch, currency: cur, link: offerLink, score: score})
	}
	if len(cands) == 0 {
		return 0, "", "", "", false
	}
	// Among top score band, pick lowest price.
	bestScore := cands[0].score
	for _, c := range cands {
		if c.score > bestScore {
			bestScore = c.score
		}
	}
	bestPrice := 0.0
	bestMerch := ""
	bestCur := ""
	bestLink := ""
	for _, c := range cands {
		if c.score < bestScore-15 {
			continue
		}
		if bestPrice == 0 || c.price < bestPrice {
			bestPrice = c.price
			bestMerch = c.merchant
			bestCur = c.currency
			bestLink = c.link
		}
	}
	if bestPrice <= 0 {
		return 0, "", "", "", false
	}
	// If the cheapest top-band offer lacks a link, prefer any same-band offer that has one.
	if bestLink == "" {
		for _, c := range cands {
			if c.score < bestScore-15 {
				continue
			}
			if c.link != "" {
				bestLink = c.link
				break
			}
		}
	}
	return bestPrice, bestMerch, bestCur, bestLink, true
}

// extractASINFromOffersAndImages recovers an Amazon ASIN when the API omitted the asin field.
func extractASINFromOffersAndImages(offers []upcOffer, images []string) string {
	for _, o := range offers {
		if a, ok := amazonASINFromSKU(scanASINToken(o.Title)); ok {
			return a
		}
		if a, ok := amazonASINFromSKU(scanASINToken(o.Link)); ok {
			return a
		}
		if a, ok := amazonASINFromSKU(scanASINToken(o.Domain)); ok {
			return a
		}
	}
	for _, img := range images {
		if a, ok := amazonASINFromSKU(scanASINToken(img)); ok {
			return a
		}
		// Amazon media: .../images/I/... or /dp/B0...
		if idx := strings.Index(strings.ToUpper(img), "/DP/"); idx >= 0 && len(img) >= idx+14 {
			cand := img[idx+4 : idx+14]
			if a, ok := amazonASINFromSKU(cand); ok {
				return a
			}
		}
	}
	return ""
}

// scanASINToken finds a B0xxxxxxxx-style token in free text.
func scanASINToken(s string) string {
	s = strings.ToUpper(s)
	for i := 0; i+10 <= len(s); i++ {
		if s[i] != 'B' || s[i+1] != '0' {
			continue
		}
		tok := s[i : i+10]
		ok := true
		for _, r := range tok {
			if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				ok = false
				break
			}
		}
		// Prefer tokens next to URL path markers when scanning free text.
		if ok {
			return tok
		}
	}
	return ""
}

// extractRetailProductURL builds a stable retailer product URL from CDN image paths.
// Office Depot: media.officedepot.com/.../products/{id}/ → /a/products/{id}/
// Only true product pages — not search results.
func extractRetailProductURL(images []string) string {
	for _, img := range images {
		low := strings.ToLower(img)
		// Office Depot product id
		if strings.Contains(low, "officedepot.com") && strings.Contains(low, "/products/") {
			if id := pathSegmentAfter(low, "/products/"); id != "" && isAllDigits(id) {
				return "https://www.officedepot.com/a/products/" + id + "/"
			}
		}
	}
	return ""
}

func pathSegmentAfter(path, marker string) string {
	idx := strings.Index(path, marker)
	if idx < 0 {
		return ""
	}
	rest := path[idx+len(marker):]
	// stop at next / or ? or end
	end := len(rest)
	for i, r := range rest {
		if r == '/' || r == '?' || r == '&' || r == '#' {
			end = i
			break
		}
	}
	seg := rest[:end]
	// strip trailing non-alnum
	seg = strings.Trim(seg, "/")
	return seg
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sanitizeMerchantTag(merchant string) string {
	merchant = strings.TrimSpace(merchant)
	if merchant == "" {
		return "OFFER"
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(merchant) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' || r == '.' {
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if len(s) > 24 {
		s = s[:24]
	}
	if s == "" {
		return "OFFER"
	}
	return s
}

// isGenericMerchant is true for labels that should not be shown as a specific vendor on cards.
func isGenericMerchant(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "", "retail", "shop", "google shopping", "google", "market", "offer", "catalog", "catalog low", "online":
		return true
	}
	return false
}

func compactAlnum(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func getCachedProductID(key string) (cachedProductIdentity, bool) {
	productIDCacheMu.RLock()
	hit, ok := productIDCache[key]
	productIDCacheMu.RUnlock()
	if !ok {
		return cachedProductIdentity{}, false
	}
	if time.Now().After(hit.expiry) {
		productIDCacheMu.Lock()
		delete(productIDCache, key)
		productIDCacheMu.Unlock()
		return cachedProductIdentity{}, false
	}
	return hit, true
}

func setCachedProductID(key string, id productIdentity, ok bool) {
	ttl := 24 * time.Hour
	if !ok {
		// Not-found only — keep short so a bad window during deploy gate does not stick.
		ttl = 20 * time.Minute
	}
	setCachedProductIDTTL(key, id, ok, ttl)
}

func setCachedProductIDTTL(key string, id productIdentity, ok bool, ttl time.Duration) {
	if key == "" || ttl <= 0 {
		return
	}
	productIDCacheMu.Lock()
	productIDCache[key] = cachedProductIdentity{id: id, expiry: time.Now().Add(ttl), ok: ok}
	productIDCacheMu.Unlock()
}

func productLinkResolveLimit() int {
	raw := strings.TrimSpace(os.Getenv("IF_PRODUCT_LINK_RESOLVES"))
	// Paid DEV: ~1 lookup/2s → keep default modest so one NSN stays under rate windows.
	def := 10
	if UPCItemDBConfigured() {
		def = 12
	}
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	max := 20
	if n > max {
		return max
	}
	return n
}

func productLinkResolveBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("IF_PRODUCT_LINK_RESOLVE_MS"))
	if raw == "" {
		// Allow rate-limit spacing (lookup 2s × ~12 products ≈ 24s+).
		if UPCItemDBConfigured() {
			return 45000 * time.Millisecond
		}
		return 12000 * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 12000 * time.Millisecond
	}
	if ms > 60000 {
		ms = 60000
	}
	return time.Duration(ms) * time.Millisecond
}

// clearProductIDCache is for tests.
func clearProductIDCache() {
	productIDCacheMu.Lock()
	productIDCache = map[string]cachedProductIdentity{}
	productIDCacheMu.Unlock()
}

// parseSingleUnitPrice extracts a single unit price. Returns false for ranges /
// "from $X" search estimates so data-capture never invents atomic rows from bands.
func parseSingleUnitPrice(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	lower := strings.ToLower(s)
	if strings.Contains(s, "–") || strings.Contains(s, "—") ||
		strings.Contains(s, " - ") || strings.Contains(lower, "from ") ||
		strings.Contains(lower, "offers") {
		// Explicit multi-offer / range display — skip for atomic export.
		// Also reject " $12.00-$15.00 " style.
		if strings.Contains(s, "–") || strings.Contains(s, "—") || strings.Contains(s, "-") && strings.Count(s, "$") >= 2 {
			return 0, false
		}
		if strings.Contains(lower, "from ") || strings.Contains(lower, "offers") {
			return 0, false
		}
	}
	// Single $12.50 or 12.50
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimPrefix(s, "$")
	// Strip trailing notes like " (3 offers)" already handled; leftover junk → fail
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v <= 0 || v > 500000 {
		return 0, false
	}
	return v, true
}

// appendMarketOffers de-dupes by channel+merchant+price and appends.
func appendMarketOffers(dst []models.MarketOffer, more ...models.MarketOffer) []models.MarketOffer {
	seen := map[string]struct{}{}
	for _, o := range dst {
		key := marketOfferKey(o)
		seen[key] = struct{}{}
	}
	for _, o := range more {
		if o.UnitPrice <= 0 {
			continue
		}
		if o.Quantity <= 0 {
			o.Quantity = 1
		}
		if o.Currency == "" {
			o.Currency = "USD"
		}
		if o.PricePerEach <= 0 && o.Quantity > 0 {
			o.PricePerEach = roundMoney(o.UnitPrice / float64(o.Quantity))
		}
		key := marketOfferKey(o)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, o)
	}
	return dst
}

func marketOfferKey(o models.MarketOffer) string {
	return fmt.Sprintf("%s|%s|%.4f|%d|%s",
		strings.ToLower(o.Channel),
		strings.ToLower(o.Merchant),
		o.UnitPrice,
		o.Quantity,
		o.Source,
	)
}

// singlePriceMarketOffers builds atomic offers from non-range channel price strings.
func singlePriceMarketOffers(r models.CommercialReference, asOf string) []models.MarketOffer {
	var out []models.MarketOffer
	type cand struct {
		raw, channel, merchant, source, link string
	}
	cands := []cand{
		{r.PriceShop, "shop", "", r.PriceShopSrc, r.LinkShop},
		{r.PriceAmazon, "amazon", "", r.PriceAmazonSrc, r.LinkAmazon},
		{r.PriceUPC, "catalog", "", r.PriceUPCSrc, r.LinkUPC},
		{r.PriceFederal, "federal", "AbilityOne.com", r.PriceFederalSrc, r.LinkGSA},
		{r.Price, "listing", "", r.PriceSource, r.PriceURL},
	}
	for _, c := range cands {
		if c.raw == "" {
			continue
		}
		// Skip known range flags / range strings
		if c.channel == "shop" && r.PriceShopIsRange {
			continue
		}
		if c.channel == "amazon" && r.PriceAmazonIsRange {
			continue
		}
		if c.channel == "catalog" && r.PriceUPCIsRange {
			continue
		}
		up, ok := parseSingleUnitPrice(c.raw)
		if !ok {
			continue
		}
		src := c.source
		if src == "" {
			src = "MARKET"
		}
		out = append(out, models.MarketOffer{
			UnitPrice: up,
			Quantity:  1,
			Currency:  "USD",
			Channel:   c.channel,
			Merchant:  c.merchant,
			Source:    src,
			Link:      c.link,
			AsOf:      asOf,
			Title:     strings.TrimSpace(r.Description),
		})
	}
	return out
}
