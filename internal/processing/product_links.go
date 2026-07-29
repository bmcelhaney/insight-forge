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
	DeepLinkOK    bool // true when we have a direct product URL (Amazon /dp or UPC dossier)
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

	return applyDeterministicProductLinks(refs, entityID, resolved)
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
		if id != nil && id.Brand != "" {
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

		// --- Amazon: direct product page when ASIN known (resolver or SKU is already an ASIN) ---
		asin := ""
		if id != nil && id.ASIN != "" {
			asin = id.ASIN
		} else if a, ok := amazonASINFromSKU(sku); ok {
			asin = a
		}
		deepOK := false
		if asin != "" {
			r.LinkAmazon = "https://www.amazon.com/dp/" + asin
			deepOK = true
		} else {
			// Do not invent a weak Amazon search when we have a better deep link elsewhere.
			r.LinkAmazon = ""
		}

		// --- Shop: only when we have a strong product title/UPC; skip weak bare-SKU searches ---
		r.LinkShop = buildPreciseShopURL(mfr, sku, upc, title)
		// If we have an Amazon product page, prefer it as the primary commercial deep link
		// and keep shop only when title-based (product-like).
		if deepOK && title == "" && upc == "" {
			r.LinkShop = ""
		}

		// --- UPC identity page (human-readable product dossier) ---
		if upc != "" {
			r.LinkUPC = "https://www.upcitemdb.com/upc/" + upc
			deepOK = true
		}

		// --- Federal: AbilityOne.com with dashed NSN (or SKU) ---
		r.LinkGSA = buildFederalCatalogURL(sku, upc, digitsNSN)

		r.LinkWebsite = manufacturerHomepage(mfr)

		// Pull market offer price onto the tile only when a successful deep link exists.
		// Never overwrite AbilityOne.com / PartsBase-specific sources (those are other sections).
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

		if r.PriceURL == "" {
			if r.LinkAmazon != "" && strings.Contains(r.LinkAmazon, "/dp/") {
				r.PriceURL = r.LinkAmazon
			} else if r.LinkUPC != "" {
				r.PriceURL = r.LinkUPC
			} else if r.LinkShop != "" {
				r.PriceURL = r.LinkShop
			}
		}
		_ = deepOK
	}
	return refs
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
	id.DeepLinkOK = id.ASIN != "" || id.UPC != ""

	// Select a usable market offer price (prefer USD / unlabelled US merchants).
	if price, merchant, currency, ok := pickBestMarketOffer(it.Offers); ok {
		id.OfferPrice = price
		id.OfferMerchant = merchant
		id.OfferCurrency = currency
	}

	if id.Title == "" && id.ASIN == "" && id.UPC == "" {
		return productIdentity{}, false
	}
	return id, true
}

// pickBestMarketOffer chooses a displayable commercial offer price.
// Prefers USD (or blank currency), known US office retailers, New condition, and sane price bands.
func pickBestMarketOffer(offers []upcOffer) (price float64, merchant, currency string, ok bool) {
	type cand struct {
		price    float64
		merchant string
		currency string
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
		case strings.Contains(ml, "sam"):
			score += 20
		}
		if strings.EqualFold(strings.TrimSpace(o.Condition), "New") || o.Condition == "" {
			score += 10
		}
		// Prefer mid-range prices slightly (avoid extreme outliers among peers).
		cands = append(cands, cand{price: o.Price, merchant: merch, currency: cur, score: score})
	}
	if len(cands) == 0 {
		return 0, "", "", false
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
	for _, c := range cands {
		if c.score < bestScore-15 {
			continue
		}
		if bestPrice == 0 || c.price < bestPrice {
			bestPrice = c.price
			bestMerch = c.merchant
			bestCur = c.currency
		}
	}
	if bestPrice <= 0 {
		return 0, "", "", false
	}
	return bestPrice, bestMerch, bestCur, true
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
		return 12
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 12
	}
	if n > 25 {
		return 25
	}
	return n
}

func productLinkResolveBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("IF_PRODUCT_LINK_RESOLVE_MS"))
	if raw == "" {
		return 3500 * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 3500 * time.Millisecond
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
