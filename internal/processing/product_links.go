package processing

import (
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
	OfferPrice    float64 // best USD (or unlabelled) offer price
	OfferMerchant string
	OfferCurrency string
	OfferLink     string // merchant/offer product URL from resolver (often via upcitemdb redirect)
	RetailURL     string // direct retailer product page inferred from catalog images/ids
	DeepLinkOK    bool   // true when we have a direct product URL (Amazon /dp, retail, offer, or UPC dossier)
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
)

// enrichProductLinks resolves UPC/SKU to real product identity (title, ASIN) and
// rewrites shop/Amazon/federal links to be as product-specific as possible.
// Soft-fails always; bounded by IF_PRODUCT_LINK_RESOLVES (default 12).
func enrichProductLinks(ctx context.Context, refs []models.CommercialReference, entityID string) []models.CommercialReference {
	if len(refs) == 0 {
		return refs
	}
	limit := productLinkResolveLimit()
	if limit <= 0 {
		// Still rewrite links with deterministic rules (no network).
		return applyDeterministicProductLinks(refs, entityID, nil)
	}

	budget := productLinkResolveBudget()
	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Rank candidates: has UPC first, then SKU+mfr, cap to limit.
	type cand struct {
		idx   int
		score int
		key   string
		sku   string
		upc   string
		mfr   string
	}
	var cands []cand
	for i, r := range refs {
		upc := normalizeUPCDigits(r.UPC)
		sku := strings.TrimSpace(r.SKU)
		if upc == "" && sku == "" {
			continue
		}
		key := productCacheKey(sku, upc)
		score := 0
		if upc != "" {
			score += 50
		}
		if sku != "" {
			score += 20
		}
		if strings.TrimSpace(r.Manufacturer) != "" {
			score += 5
		}
		cands = append(cands, cand{idx: i, score: score, key: key, sku: sku, upc: upc, mfr: strings.TrimSpace(r.Manufacturer)})
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
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
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

			id, ok := resolveProductIdentity(probeCtx, c.sku, c.upc, c.mfr)
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

	return applyDeterministicProductLinks(refs, entityID, resolved)
}

// expandResolvedIdentities copies a resolved identity onto sibling refs that
// share UPC or SKU but were not selected for a network resolve.
func expandResolvedIdentities(refs []models.CommercialReference, resolved map[int]*productIdentity) map[int]*productIdentity {
	if resolved == nil {
		resolved = map[int]*productIdentity{}
	}
	byUPC := map[string]*productIdentity{}
	bySKU := map[string]*productIdentity{}
	for i, id := range resolved {
		if id == nil {
			continue
		}
		if u := normalizeUPCDigits(id.UPC); u != "" {
			byUPC[u] = id
		}
		if i >= 0 && i < len(refs) {
			if u := normalizeUPCDigits(refs[i].UPC); u != "" {
				byUPC[u] = id
			}
			if s := strings.ToUpper(strings.TrimSpace(refs[i].SKU)); s != "" {
				bySKU[s] = id
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
		if s := strings.ToUpper(strings.TrimSpace(r.SKU)); s != "" {
			if id := bySKU[s]; id != nil {
				out[i] = id
				continue
			}
			// Compact match: BICCSM11BK vs BIC CSM11BK
			compact := compactAlnum(s)
			for k, id := range bySKU {
				if compact != "" && compactAlnum(k) == compact {
					out[i] = id
					break
				}
			}
		}
	}
	return out
}

func applyDeterministicProductLinks(refs []models.CommercialReference, entityID string, resolved map[int]*productIdentity) []models.CommercialReference {
	digitsNSN := digitsOnlyString(entityID)
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

		// --- Amazon: /dp when ASIN known; otherwise title/UPC search (never bare empty) ---
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
		} else {
			r.LinkAmazon = buildAmazonProductSearchURL(mfr, sku, upc, title)
		}

		// --- Shop: prefer true retailer product / offer link over Google search ---
		r.LinkShop = ""
		if id != nil {
			if id.RetailURL != "" {
				r.LinkShop = id.RetailURL
			} else if id.OfferLink != "" {
				r.LinkShop = id.OfferLink
			}
		}
		if r.LinkShop == "" && upc != "" {
			// Walmart UPC search is usually more product-specific than bare Google Shopping SKU.
			r.LinkShop = "https://www.walmart.com/search?q=" + url.QueryEscape(upc)
		}
		if r.LinkShop == "" {
			r.LinkShop = buildPreciseShopURL(mfr, sku, upc, title)
		}

		// --- UPC identity page (human-readable product dossier) ---
		if upc != "" {
			r.LinkUPC = "https://www.upcitemdb.com/upc/" + upc
		}

		// --- Federal: AbilityOne.com with dashed NSN (or SKU) ---
		r.LinkGSA = buildFederalCatalogURL(sku, upc, digitsNSN)

		r.LinkWebsite = manufacturerHomepage(mfr)

		// Successful deep link = Amazon product page, retailer product page, offer link, or UPC dossier.
		deepOK := amazonProduct ||
			(id != nil && (id.RetailURL != "" || id.OfferLink != "")) ||
			r.LinkUPC != "" ||
			(id != nil && id.DeepLinkOK)

		// Pull market offer price onto the tile only when a successful deep link exists.
		// Never overwrite an existing SKU/UPC-specific price (GSA etc.).
		if deepOK && strings.TrimSpace(r.Price) == "" && id != nil && id.OfferPrice > 0 {
			r.Price = normalizePriceString(fmt.Sprintf("%.2f", id.OfferPrice))
			src := "MARKET_OFFER"
			if id.OfferMerchant != "" {
				src = "MARKET:" + sanitizeMerchantTag(id.OfferMerchant)
			}
			r.PriceSource = src
			r.PriceAsOf = time.Now().UTC().Format("2006-01-02")
			if id.OfferMerchant != "" {
				note := "Market offer via " + id.OfferMerchant + " (resolved with product deep link)"
				if r.Context == "" {
					r.Context = note
				} else if !strings.Contains(strings.ToLower(r.Context), "market offer") {
					r.Context = r.Context + " | " + note
				}
			}
		}

		// Prefer price verification URL that matches the deep link we actually set.
		if r.PriceURL == "" {
			switch {
			case amazonProduct:
				r.PriceURL = r.LinkAmazon
			case id != nil && id.RetailURL != "":
				r.PriceURL = id.RetailURL
			case id != nil && id.OfferLink != "":
				r.PriceURL = id.OfferLink
			case r.LinkUPC != "":
				r.PriceURL = r.LinkUPC
			case r.LinkShop != "":
				r.PriceURL = r.LinkShop
			}
		}
	}
	return refs
}

// buildAmazonProductSearchURL builds the strongest Amazon search we can without an ASIN.
// Prefer full product title + brand; then UPC; then quoted SKU + brand. Bare empty SKU searches are last resort.
func buildAmazonProductSearchURL(manufacturer, sku, upc, title string) string {
	if t := strings.TrimSpace(title); t != "" && !looksLikeCatalogCodeDescription(t) {
		if len(t) > 100 {
			t = t[:100]
		}
		q := t
		mfr := strings.TrimSpace(manufacturer)
		if mfr != "" && !strings.Contains(strings.ToLower(t), strings.ToLower(mfr)) {
			q = mfr + " " + t
		}
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
	// 1) Full product title from resolver is the best shopping query.
	if t := strings.TrimSpace(title); t != "" {
		// Keep it short enough for URL quality; include brand if not already present.
		if len(t) > 90 {
			t = t[:90]
		}
		q := t
		mfr := strings.TrimSpace(manufacturer)
		if mfr != "" && !strings.Contains(strings.ToLower(t), strings.ToLower(mfr)) {
			q = mfr + " " + t
		}
		// Append exact SKU in quotes when available for disambiguation.
		if sku = strings.TrimSpace(sku); sku != "" && !strings.Contains(t, sku) {
			q += ` "` + sku + `"`
		}
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

func resolveProductIdentity(ctx context.Context, sku, upc, mfr string) (productIdentity, bool) {
	key := productCacheKey(sku, upc)
	if hit, ok := getCachedProductID(key); ok {
		return hit.id, hit.ok
	}

	var id productIdentity
	var ok bool

	// Prefer UPC lookup (single definitive item).
	if u := normalizeUPCDigits(upc); u != "" {
		id, ok = upcitemdbLookup(ctx, u)
	}
	// Fall back to text search by SKU (+ brand).
	if !ok && strings.TrimSpace(sku) != "" {
		q := strings.TrimSpace(sku)
		if mfr != "" {
			q = mfr + " " + q
		}
		id, ok = upcitemdbSearch(ctx, q, sku)
	}

	setCachedProductID(key, id, ok)
	// Also cache under UPC if resolved.
	if ok && id.UPC != "" {
		setCachedProductID("upc:"+normalizeUPCDigits(id.UPC), id, true)
	}
	return id, ok
}

func upcitemdbLookup(ctx context.Context, upc string) (productIdentity, bool) {
	u := "https://api.upcitemdb.com/prod/trial/lookup?upc=" + url.QueryEscape(upc)
	return upcitemdbFetch(ctx, u, "")
}

func upcitemdbSearch(ctx context.Context, query, preferSKU string) (productIdentity, bool) {
	u := "https://api.upcitemdb.com/prod/trial/search?s=" + url.QueryEscape(query)
	return upcitemdbFetch(ctx, u, preferSKU)
}

type upcOffer struct {
	Merchant     string  `json:"merchant"`
	Domain       string  `json:"domain"`
	Title        string  `json:"title"`
	Currency     string  `json:"currency"`
	Price        float64 `json:"price"`
	ListPrice    string  `json:"list_price"`
	Condition    string  `json:"condition"`
	Availability string  `json:"availability"`
	Link         string  `json:"link"`
}

func upcitemdbFetch(ctx context.Context, endpoint, preferSKU string) (productIdentity, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return productIdentity{}, false
	}
	req.Header.Set("User-Agent", "InsightForge/1.0 (+https://github.com/bmcelhaney/insight-forge)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := productHTTPClient.Do(req)
	if err != nil {
		return productIdentity{}, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 {
		return productIdentity{}, false
	}

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
		return productIdentity{}, false
	}
	if len(payload.Items) == 0 {
		return productIdentity{}, false
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
	id := productIdentity{
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

	// Select a usable market offer price + offer deep link.
	if price, merchant, currency, link, ok := pickBestMarketOffer(it.Offers); ok {
		id.OfferPrice = price
		id.OfferMerchant = merchant
		id.OfferCurrency = currency
		id.OfferLink = link
	}

	id.DeepLinkOK = id.ASIN != "" || id.UPC != "" || id.RetailURL != "" || id.OfferLink != ""

	if id.Title == "" && id.ASIN == "" && id.UPC == "" && id.RetailURL == "" {
		return productIdentity{}, false
	}
	return id, true
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
		if o.Price <= 0 || o.Price > 50000 {
			continue
		}
		// Skip absurd pennies / noise often present in "lowest_recorded".
		if o.Price < 0.5 {
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
		case strings.Contains(ml, "grainger"):
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
		offerLink := strings.TrimSpace(o.Link)
		if offerLink != "" {
			score += 8 // having a product link makes this offer more useful
		}
		cands = append(cands, cand{price: o.Price, merchant: merch, currency: cur, link: offerLink, score: score})
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
		if s[i] != 'B' {
			continue
		}
		// B0… ASIN
		if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			tok := s[i : i+10]
			ok := true
			for _, r := range tok {
				if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
					ok = false
					break
				}
			}
			if ok {
				return tok
			}
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
	if key == "" {
		return
	}
	ttl := 24 * time.Hour
	if !ok {
		ttl = 2 * time.Hour // negative cache shorter
	}
	productIDCacheMu.Lock()
	productIDCache[key] = cachedProductIdentity{id: id, expiry: time.Now().Add(ttl), ok: ok}
	productIDCacheMu.Unlock()
}

func productLinkResolveLimit() int {
	raw := strings.TrimSpace(os.Getenv("IF_PRODUCT_LINK_RESOLVES"))
	if raw == "" {
		return 16
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 16
	}
	if n > 30 {
		return 30
	}
	return n
}

func productLinkResolveBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("IF_PRODUCT_LINK_RESOLVE_MS"))
	if raw == "" {
		return 5000 * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 5000 * time.Millisecond
	}
	if ms > 15000 {
		ms = 15000
	}
	return time.Duration(ms) * time.Millisecond
}

// clearProductIDCache is for tests.
func clearProductIDCache() {
	productIDCacheMu.Lock()
	productIDCache = map[string]cachedProductIdentity{}
	productIDCacheMu.Unlock()
}
