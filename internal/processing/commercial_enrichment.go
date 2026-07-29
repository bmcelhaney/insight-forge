package processing

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/extraction"
	"github.com/bmcelhaney/insight-forge/internal/models"
)

const maxCommercialReferences = 200

// commercialPriceSearch is injectable for tests. Default prefers AbilityOne.com
// (reliable JSON API), then falls back to GSA Advantage (often broken after SPA rewrite).
var commercialPriceSearch = defaultCommercialPriceSearch

func defaultCommercialPriceSearch(ctx context.Context, term string) ([]map[string]any, error) {
	// 1) AbilityOne.com catalog (dashed NSN or SKU)
	if hits, err := extraction.SearchAbilityOneCommerce(ctx, term); err == nil && len(hits) > 0 {
		out := make([]map[string]any, 0, len(hits))
		for _, h := range hits {
			if h.Price <= 0 {
				continue
			}
			out = append(out, map[string]any{
				"price":        fmt.Sprintf("%.2f", h.Price),
				"mfr_part":     h.SKU,
				"sku":          h.SKU,
				"manufacturer": h.Brand,
				"description":  h.Name,
				"upc":          h.UPC,
				"price_source": "ABILITYONE_COM",
				"source":       "ABILITYONE_COM",
			})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	// 2) GSA Advantage — retained as degraded fallback (POST scrape frequently 405s).
	return extraction.SearchGSAAdvantage(ctx, term)
}

type cachedCommercialPrice struct {
	price  string
	source string
	asOf   string
	url    string
	expiry time.Time
}

var (
	commercialPriceCacheMu sync.RWMutex
	commercialPriceCache   = map[string]cachedCommercialPrice{}
)

// manufacturerHomepages maps normalized alias → homepage. Used only for stable
// company-home links (not SKU deep links, which frequently 404).
var manufacturerHomepages = []struct {
	aliases []string
	url     string
}{
	{[]string{"3m"}, "https://www.3m.com"},
	{[]string{"avery"}, "https://www.avery.com"},
	{[]string{"universal office", "universal"}, "https://www.universaloffice.com"},
	{[]string{"sanford"}, "https://www.newellbrands.com/our-brands/sanford"},
	{[]string{"pendaflex", "esselte"}, "https://www.pendaflex.com"},
	{[]string{"kimberly clark", "kimberly-clark", "kc"}, "https://www.kimberly-clark.com"},
	{[]string{"georgia pacific", "georgia-pacific", "gp"}, "https://www.gp.com"},
	{[]string{"boardwalk"}, "https://www.boardwalkbrand.com"},
	{[]string{"pilot"}, "https://pilotpen.us"},
	{[]string{"smead"}, "https://www.smead.com"},
	{[]string{"gojo"}, "https://www.gojo.com"},
	{[]string{"bic"}, "https://us.bic.com"},
	{[]string{"pentel"}, "https://www.pentel.com"},
	{[]string{"dixie"}, "https://www.dixie.com"},
	{[]string{"office depot"}, "https://www.officedepot.com"},
	{[]string{"rubbermaid"}, "https://www.rubbermaidcommercial.com"},
	{[]string{"staples"}, "https://www.staples.com"},
	{[]string{"fellowes"}, "https://www.fellowes.com"},
	{[]string{"brady"}, "https://www.bradyid.com"},
	{[]string{"dymo"}, "https://www.dymo.com"},
	{[]string{"stanley"}, "https://www.stanleytools.com"},
	{[]string{"zebra", "uni-ball", "uniball"}, "https://www.zebrapen.com"},
	{[]string{"tork"}, "https://www.torkusa.com"},
	{[]string{"uline", "u-line"}, "https://www.uline.com"},
	{[]string{"sharpie"}, "https://www.sharpie.com"},
	{[]string{"hammermill"}, "https://www.hammermill.com"},
	{[]string{"paper mate", "papermate"}, "https://www.papermate.com"},
	{[]string{"hp"}, "https://www.hp.com"},
	{[]string{"dewalt"}, "https://www.dewalt.com"},
	{[]string{"duracell"}, "https://www.duracell.com"},
	{[]string{"energizer"}, "https://www.energizer.com"},
	{[]string{"grainger"}, "https://www.grainger.com"},
	{[]string{"windsoft"}, "https://windsoft.com"},
	{[]string{"tops", "oxford"}, "https://www.tops-products.com"},
	{[]string{"swingline"}, "https://www.swingline.com"},
	{[]string{"c-line", "cline"}, "https://www.c-lineproducts.com"},
}

// enrichCommercialReferences attaches resilient discovery links and merges
// row-specific prices from GSA/PartsBase (and exact SKU matches only).
// AbilityOne.com NSN channel price is NOT applied as a default to commercial rows —
// that lives on InsightResult.AbilityOneChannelPrice instead.
func enrichCommercialReferences(entityID string, refs []models.CommercialReference, snaps []models.DataSnapshot) []models.CommercialReference {
	priceIndex := buildCommercialPriceIndex(snaps)
	now := time.Now().UTC().Format("2006-01-02")
	digitsNSN := digitsOnlyString(entityID)

	if len(refs) == 0 {
		return refs
	}

	out := make([]models.CommercialReference, 0, len(refs))
	for _, r := range refs {
		// Keep AbilityOne.com NSN catalog hits out of the commercial/ETS row list.
		if strings.EqualFold(r.Source, "ABILITYONE_COMMERCE") {
			continue
		}
		r.SKU = strings.TrimSpace(r.SKU)
		r.UPC = normalizeUPCDigits(r.UPC)
		r.GTIN = strings.TrimSpace(r.GTIN)
		r.Manufacturer = strings.TrimSpace(r.Manufacturer)
		r.Description = strings.TrimSpace(r.Description)
		r.Source = strings.TrimSpace(r.Source)
		r.Price = strings.TrimSpace(r.Price)

		// Merge live prices only when this row has a specific match (SKU/UPC),
		// never the NSN-level AbilityOne.com channel default.
		if r.Price == "" {
			if p, ok := priceIndex.lookup(r); ok && !strings.EqualFold(p.source, "ABILITYONE_COM") {
				r.Price = p.price
				r.PriceSource = p.source
				r.PriceAsOf = p.asOf
				if p.url != "" {
					r.PriceURL = p.url
				}
			}
		}
		if r.Price != "" && r.PriceSource == "" {
			switch strings.ToUpper(r.Source) {
			case "GSA_ADVANTAGE":
				r.PriceSource = "GSA_ADVANTAGE"
			case "PARTSBASE":
				r.PriceSource = "PARTSBASE"
			default:
				r.PriceSource = r.Source
			}
			if r.PriceAsOf == "" {
				r.PriceAsOf = now
			}
		}

		r.LinkShop = buildShopSearchURL(r.Manufacturer, r.SKU, r.UPC, r.Description)
		if r.UPC != "" {
			r.LinkUPC = "https://www.google.com/search?q=" + url.QueryEscape(r.UPC+" UPC")
		}
		r.LinkGSA = buildGSASearchURL(r.SKU, r.UPC, digitsNSN)
		r.LinkWebsite = manufacturerHomepage(r.Manufacturer)
		if r.PriceURL == "" {
			// Prefer GSA when price came from GSA; otherwise shop search for verification.
			if strings.EqualFold(r.PriceSource, "GSA_ADVANTAGE") && r.LinkGSA != "" {
				r.PriceURL = r.LinkGSA
			} else if r.LinkShop != "" {
				r.PriceURL = r.LinkShop
			}
		}

		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := commercialRefScore(out[i]), commercialRefScore(out[j])
		if si != sj {
			return si > sj
		}
		if out[i].Manufacturer != out[j].Manufacturer {
			return out[i].Manufacturer < out[j].Manufacturer
		}
		return out[i].SKU < out[j].SKU
	})

	if len(out) > maxCommercialReferences {
		out = out[:maxCommercialReferences]
	}
	return out
}

// probeCommercialPrices best-effort fills prices for top unpriced commercial refs via GSA.
// Soft-fails always; respects IF_COMMERCIAL_PRICE_PROBES (default 10, 0 disables) and
// IF_COMMERCIAL_PRICE_PROBE_MS (default 2500 overall budget).
func probeCommercialPrices(ctx context.Context, refs []models.CommercialReference) []models.CommercialReference {
	if len(refs) == 0 {
		return refs
	}
	k := commercialPriceProbeLimit()
	if k <= 0 {
		return refs
	}
	budget := commercialPriceProbeBudget()
	if budget <= 0 {
		return refs
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < budget {
			budget = remaining
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Candidate indices: unpriced, with UPC or SKU.
	type cand struct {
		idx   int
		score int
		term  string
		key   string
	}
	var cands []cand
	for i, r := range refs {
		if strings.TrimSpace(r.Price) != "" {
			continue
		}
		term, key := commercialProbeTerm(r)
		if term == "" {
			continue
		}
		// Prefer cache hits immediately without consuming probe budget slots.
		if hit, ok := getCachedCommercialPrice(key); ok {
			refs[i].Price = hit.price
			refs[i].PriceSource = hit.source
			refs[i].PriceAsOf = hit.asOf
			if hit.url != "" {
				refs[i].PriceURL = hit.url
			} else if refs[i].LinkGSA != "" {
				refs[i].PriceURL = refs[i].LinkGSA
			}
			continue
		}
		score := 0
		if r.UPC != "" {
			score += 50
		}
		if strings.EqualFold(r.Source, "ABILITYONE_ETS") {
			score += 20
		}
		if r.SKU != "" {
			score += 10
		}
		cands = append(cands, cand{idx: i, score: score, term: term, key: key})
	}
	if len(cands) == 0 {
		return refs
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].idx < cands[j].idx
	})
	if len(cands) > k {
		cands = cands[:k]
	}

	type result struct {
		idx  int
		key  string
		term string
		hit  cachedCommercialPrice
		ok   bool
	}
	results := make(chan result, len(cands))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, c := range cands {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-probeCtx.Done():
				results <- result{idx: c.idx, ok: false}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			// Re-check cache (another worker may have filled it).
			if hit, ok := getCachedCommercialPrice(c.key); ok {
				results <- result{idx: c.idx, key: c.key, hit: hit, ok: true}
				return
			}

			prices, err := commercialPriceSearch(probeCtx, c.term)
			if err != nil || len(prices) == 0 {
				results <- result{idx: c.idx, ok: false}
				return
			}
			price := ""
			source := "ABILITYONE_COM"
			priceURL := "https://www.abilityone.com/search?q=" + url.QueryEscape(c.term)
			for _, p := range prices {
				if v := firstNonEmptyString(p, "price"); v != "" {
					price = normalizePriceString(v)
					if ps := firstNonEmptyString(p, "price_source", "source"); ps != "" {
						source = strings.ToUpper(ps)
					}
					break
				}
			}
			if price == "" {
				results <- result{idx: c.idx, ok: false}
				return
			}
			if strings.Contains(source, "GSA") {
				priceURL = "https://www.gsaadvantage.gov/advantage?theme=adv19&store=ADVANTAGE"
				source = "GSA_ADVANTAGE"
			} else {
				source = "ABILITYONE_COM"
			}
			hit := cachedCommercialPrice{
				price:  price,
				source: source,
				asOf:   time.Now().UTC().Format("2006-01-02"),
				url:    priceURL,
				expiry: time.Now().Add(24 * time.Hour),
			}
			setCachedCommercialPrice(c.key, hit)
			results <- result{idx: c.idx, key: c.key, term: c.term, hit: hit, ok: true}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if !res.ok {
			continue
		}
		i := res.idx
		if i < 0 || i >= len(refs) {
			continue
		}
		if strings.TrimSpace(refs[i].Price) != "" {
			continue
		}
		refs[i].Price = res.hit.price
		refs[i].PriceSource = res.hit.source
		refs[i].PriceAsOf = res.hit.asOf
		if res.hit.url != "" {
			refs[i].PriceURL = res.hit.url
		} else if refs[i].LinkGSA != "" {
			refs[i].PriceURL = refs[i].LinkGSA
		}
	}

	// Re-sort so newly priced rows bubble up.
	sort.SliceStable(refs, func(i, j int) bool {
		si, sj := commercialRefScore(refs[i]), commercialRefScore(refs[j])
		if si != sj {
			return si > sj
		}
		if refs[i].Manufacturer != refs[j].Manufacturer {
			return refs[i].Manufacturer < refs[j].Manufacturer
		}
		return refs[i].SKU < refs[j].SKU
	})
	return refs
}

func commercialProbeTerm(r models.CommercialReference) (term, cacheKey string) {
	if upc := normalizeUPCDigits(r.UPC); upc != "" {
		return upc, "upc:" + upc
	}
	sku := strings.TrimSpace(r.SKU)
	if sku != "" && looksLikeProductSKU(sku) {
		return sku, "sku:" + strings.ToUpper(sku)
	}
	return "", ""
}

func commercialPriceProbeLimit() int {
	raw := strings.TrimSpace(os.Getenv("IF_COMMERCIAL_PRICE_PROBES"))
	if raw == "" {
		return 10
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 10
	}
	if n > 25 {
		return 25
	}
	return n
}

func commercialPriceProbeBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("IF_COMMERCIAL_PRICE_PROBE_MS"))
	if raw == "" {
		return 2500 * time.Millisecond
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 {
		return 2500 * time.Millisecond
	}
	if ms == 0 {
		return 0
	}
	if ms > 15000 {
		ms = 15000
	}
	return time.Duration(ms) * time.Millisecond
}

func getCachedCommercialPrice(key string) (cachedCommercialPrice, bool) {
	if key == "" {
		return cachedCommercialPrice{}, false
	}
	commercialPriceCacheMu.RLock()
	hit, ok := commercialPriceCache[key]
	commercialPriceCacheMu.RUnlock()
	if !ok {
		return cachedCommercialPrice{}, false
	}
	if time.Now().After(hit.expiry) {
		commercialPriceCacheMu.Lock()
		delete(commercialPriceCache, key)
		commercialPriceCacheMu.Unlock()
		return cachedCommercialPrice{}, false
	}
	return hit, true
}

func setCachedCommercialPrice(key string, hit cachedCommercialPrice) {
	if key == "" {
		return
	}
	commercialPriceCacheMu.Lock()
	commercialPriceCache[key] = hit
	commercialPriceCacheMu.Unlock()
}

func clearCommercialPriceCache() {
	commercialPriceCacheMu.Lock()
	commercialPriceCache = map[string]cachedCommercialPrice{}
	commercialPriceCacheMu.Unlock()
}

func commercialRefScore(r models.CommercialReference) int {
	score := 0
	src := strings.ToUpper(r.Source)
	switch src {
	case "ABILITYONE_ETS":
		score += 40
	case "GSA_ADVANTAGE":
		score += 30
	case "PARTSBASE":
		score += 10
	case "ABILITYONE":
		score += 20
	}
	if r.Price != "" {
		score += 25
	}
	if r.UPC != "" {
		score += 15
	}
	if r.SKU != "" {
		score += 10
	}
	if r.Description != "" {
		score += 5
	}
	if r.DateAdded != "" {
		score += 2
	}
	return score
}

type commercialPriceHit struct {
	price  string
	source string
	asOf   string
	url    string
}

type commercialPriceIndex struct {
	bySKU map[string]commercialPriceHit
	byUPC map[string]commercialPriceHit
}

func (idx commercialPriceIndex) lookup(r models.CommercialReference) (commercialPriceHit, bool) {
	if r.SKU != "" {
		if h, ok := idx.bySKU[strings.ToUpper(r.SKU)]; ok {
			return h, true
		}
	}
	if r.UPC != "" {
		if h, ok := idx.byUPC[r.UPC]; ok {
			return h, true
		}
	}
	return commercialPriceHit{}, false
}

type nsnChannelPrice struct {
	price string
	sku   string
	name  string
	brand string
	asOf  string
	url   string
}

func extractNSNChannelPrice(snaps []models.DataSnapshot, entityID string) nsnChannelPrice {
	digits := digitsOnlyString(entityID)
	asOf := time.Now().UTC().Format("2006-01-02")
	for _, s := range snaps {
		if s.SourceCode != "ABILITYONE_COMMERCE" {
			continue
		}
		price := firstStringFromAny(s.RawResponse["best_price"])
		if price == "" {
			if f := toFloatFromAny(s.RawResponse["best_price"]); f > 0 {
				price = fmt.Sprintf("%.2f", f)
			}
		}
		if price == "" && s.Value > 0 {
			price = fmt.Sprintf("%.2f", s.Value)
		}
		if price == "" {
			// Fall back to first priced commercial_reference on the snapshot.
			for _, r := range mapSliceFromAny(s.RawResponse["commercial_references"]) {
				if p := firstNonEmptyString(r, "price"); p != "" {
					price = p
					break
				}
			}
		}
		if strings.TrimSpace(price) == "" {
			continue
		}
		return nsnChannelPrice{
			price: normalizePriceString(price),
			sku:   firstStringFromAny(s.RawResponse["best_sku"]),
			name:  firstStringFromAny(s.RawResponse["best_name"]),
			brand: firstStringFromAny(s.RawResponse["best_brand"]),
			asOf:  nonEmptyOr(firstStringFromAny(s.RawResponse["price_as_of"]), asOf),
			url:   nonEmptyOr(firstStringFromAny(s.RawResponse["search_url"]), abilityOneSearchURL(digits)),
		}
	}
	return nsnChannelPrice{}
}

// buildAbilityOneChannelPrice returns the standalone NSN catalog price for the result payload.
func buildAbilityOneChannelPrice(snaps []models.DataSnapshot, entityID string) *models.ChannelPrice {
	ch := extractNSNChannelPrice(snaps, entityID)
	if ch.price == "" {
		return nil
	}
	return &models.ChannelPrice{
		Price:  ch.price,
		SKU:    ch.sku,
		Name:   ch.name,
		Brand:  ch.brand,
		Source: "ABILITYONE_COM",
		AsOf:   ch.asOf,
		URL:    ch.url,
		Note:   "AbilityOne.com catalog list price for this NSN (federal channel). Not a commercial SKU quote.",
	}
}

func abilityOneSearchURL(digitsOrTerm string) string {
	term := strings.TrimSpace(digitsOrTerm)
	d := digitsOnlyString(term)
	if len(d) >= 13 {
		d = d[len(d)-13:]
		term = d[0:4] + "-" + d[4:6] + "-" + d[6:9] + "-" + d[9:13]
	}
	return "https://www.abilityone.com/search?q=" + url.QueryEscape(term)
}

func buildCommercialPriceIndex(snaps []models.DataSnapshot) commercialPriceIndex {
	idx := commercialPriceIndex{
		bySKU: make(map[string]commercialPriceHit),
		byUPC: make(map[string]commercialPriceHit),
	}
	asOf := time.Now().UTC().Format("2006-01-02")

	for _, s := range snaps {
		switch s.SourceCode {
		// ABILITYONE_COMMERCE is intentionally NOT indexed into commercial row prices.
		// It is exposed separately as AbilityOneChannelPrice on the result.
		case "GSA_ADVANTAGE":
			gsaURL := firstStringFromAny(s.RawResponse["search_url"])
			for _, p := range mapSliceFromAny(s.RawResponse["prices_found"]) {
				price := firstNonEmptyString(p, "price")
				if price == "" {
					continue
				}
				hit := commercialPriceHit{
					price:  normalizePriceString(price),
					source: "GSA_ADVANTAGE",
					asOf:   asOf,
					url:    gsaURL,
				}
				if sku := firstNonEmptyString(p, "mfr_part", "sku", "manufacturer_part"); sku != "" {
					idx.bySKU[strings.ToUpper(sku)] = hit
				}
				if upc := normalizeUPCDigits(firstNonEmptyString(p, "upc", "gtin")); upc != "" {
					idx.byUPC[upc] = hit
				}
			}
			for _, r := range mapSliceFromAny(s.RawResponse["commercial_references"]) {
				price := firstNonEmptyString(r, "price")
				if price == "" {
					continue
				}
				hit := commercialPriceHit{
					price:  normalizePriceString(price),
					source: "GSA_ADVANTAGE",
					asOf:   asOf,
					url:    gsaURL,
				}
				if sku := firstNonEmptyString(r, "sku", "mfr_part"); sku != "" {
					idx.bySKU[strings.ToUpper(sku)] = hit
				}
				if upc := normalizeUPCDigits(firstNonEmptyString(r, "upc")); upc != "" {
					idx.byUPC[upc] = hit
				}
			}
		case "PARTSBASE":
			for _, r := range mapSliceFromAny(s.RawResponse["commercial_references"]) {
				price := firstNonEmptyString(r, "price")
				if price == "" {
					continue
				}
				// PartsBase "sku" is often a contract number — index only when a real UPC exists,
				// or when the sku looks product-like (has letters).
				sku := firstNonEmptyString(r, "sku", "mfr_part")
				upc := normalizeUPCDigits(firstNonEmptyString(r, "upc"))
				hit := commercialPriceHit{
					price:  normalizePriceString(price),
					source: "PARTSBASE",
					asOf:   asOf,
				}
				if upc != "" {
					idx.byUPC[upc] = hit
				}
				if sku != "" && looksLikeProductSKU(sku) {
					idx.bySKU[strings.ToUpper(sku)] = hit
				}
			}
			for _, p := range mapSliceFromAny(s.RawResponse["price_signals"]) {
				price := firstNonEmptyString(p, "unit_price", "price", "avg_price")
				if price == "" {
					if f := toFloatFromAny(p["unit_price"]); f > 0 {
						price = fmt.Sprintf("%.2f", f)
					}
				}
				if price == "" {
					continue
				}
				hit := commercialPriceHit{
					price:  normalizePriceString(price),
					source: "PARTSBASE",
					asOf:   asOf,
				}
				if upc := normalizeUPCDigits(firstNonEmptyString(p, "upc")); upc != "" {
					idx.byUPC[upc] = hit
				}
			}
		}
	}
	return idx
}

func buildShopSearchURL(manufacturer, sku, upc, description string) string {
	parts := make([]string, 0, 4)
	if manufacturer != "" {
		parts = append(parts, manufacturer)
	}
	if sku != "" {
		parts = append(parts, sku)
	}
	if upc != "" {
		parts = append(parts, upc)
	}
	if len(parts) == 0 && description != "" {
		// Fall back to a short description fragment.
		desc := description
		if len(desc) > 80 {
			desc = desc[:80]
		}
		parts = append(parts, desc)
	}
	if len(parts) == 0 {
		return ""
	}
	q := strings.Join(parts, " ")
	// Google Shopping is a resilient discovery surface (search results, not brittle deep links).
	return "https://www.google.com/search?tbm=shop&q=" + url.QueryEscape(q)
}

func buildGSASearchURL(sku, upc, nsn string) string {
	term := ""
	switch {
	case sku != "":
		term = sku
	case upc != "":
		term = upc
	case nsn != "":
		term = nsn
	default:
		return "https://www.gsaadvantage.gov/"
	}
	// Public search landing — works even when POST scrape fails.
	return "https://www.gsaadvantage.gov/advantage/s/search.do?q=1:4" + url.QueryEscape(term) + "&q=1:5" + url.QueryEscape(term) + "&db=0&searchType=1"
}

func manufacturerHomepage(name string) string {
	norm := normalizeSupplierKey(name)
	if norm == "" || strings.Contains(norm, "unknown") {
		return ""
	}
	for _, entry := range manufacturerHomepages {
		for _, alias := range entry.aliases {
			if strings.Contains(norm, normalizeSupplierKey(alias)) {
				return entry.url
			}
		}
	}
	return ""
}

func normalizeSupplierKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	prevSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func normalizeUPCDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if d == "" {
		return ""
	}
	switch len(d) {
	case 11:
		return "0" + d
	case 12, 13, 14:
		return d
	default:
		if len(d) > 14 {
			return d[len(d)-14:]
		}
		return d
	}
}

func normalizePriceString(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "$")
	p = strings.ReplaceAll(p, ",", "")
	if p == "" {
		return ""
	}
	// Keep simple numeric-looking prices; prefix with $ for display consistency.
	if strings.HasPrefix(p, "$") {
		return p
	}
	return "$" + p
}

func looksLikeProductSKU(sku string) bool {
	sku = strings.TrimSpace(sku)
	if len(sku) < 3 || len(sku) > 40 {
		return false
	}
	hasLetter := false
	for _, r := range sku {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			hasLetter = true
			break
		}
	}
	// Pure long numeric strings are often contract/PIID numbers, not product SKUs.
	if !hasLetter && len(sku) >= 10 {
		return false
	}
	return true
}
