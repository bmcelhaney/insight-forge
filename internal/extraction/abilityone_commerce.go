package extraction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

const abilityOneCommerceSearchURL = "https://www.abilityone.com/ccstoreui/v1/search"

// AbilityOneCommerceExtractor pulls live AbilityOne.com catalog list prices.
// AbilityOne.com indexes primarily by dashed NSN (e.g. 7520-00-935-7136).
// This is currently the most reliable free federal-channel price source after
// GSA Advantage moved to a SPA that no longer supports our HTML scrape.
type AbilityOneCommerceExtractor struct {
	client *http.Client
}

func NewAbilityOneCommerceExtractor() *AbilityOneCommerceExtractor {
	return &AbilityOneCommerceExtractor{
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (a *AbilityOneCommerceExtractor) SourceCode() string { return "ABILITYONE_COMMERCE" }

func (a *AbilityOneCommerceExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	term := abilityOneSearchTerm(entityID)
	if term == "" {
		return []models.DataSnapshot{}, nil
	}

	hits, err := SearchAbilityOneCommerce(ctx, term)
	if err != nil {
		return []models.DataSnapshot{{
			EntityID:   entityID,
			SourceCode: a.SourceCode(),
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"note":       "AbilityOne.com commerce search failed",
				"error":      err.Error(),
				"search_term": term,
				"search_url": abilityOneCommercePublicURL(term),
			},
			QualityScore: 0.2,
			CreatedBy:    "abilityone-commerce-v0.1",
		}}, nil
	}
	if len(hits) == 0 {
		return []models.DataSnapshot{}, nil
	}

	// Prefer exact NSN match when present.
	digits := digitsOnly(entityID)
	var best AbilityOneCommerceHit
	exact := false
	for _, h := range hits {
		if abilityOneIDsMatch(h.SKU, digits) {
			best = h
			exact = true
			break
		}
	}
	if !exact {
		best = hits[0]
	}

	refs := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if h.Price <= 0 {
			continue
		}
		ref := map[string]any{
			"sku":          h.SKU,
			"price":        formatMoney(h.Price),
			"price_source": "ABILITYONE_COM",
			"manufacturer": h.Brand,
			"description":  h.Name,
			"context":      "AbilityOne.com catalog list price",
			"source":       a.SourceCode(),
		}
		if h.UPC != "" {
			ref["upc"] = h.UPC
		}
		refs = append(refs, ref)
	}

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: a.SourceCode(),
		SnapshotAt: time.Now(),
		Value:      best.Price,
		Currency:   "USD",
		RawResponse: map[string]any{
			"data_source":           "live_abilityone_com",
			"search_term":           term,
			"search_url":            abilityOneCommercePublicURL(term),
			"exact_nsn_match":       exact,
			"result_count":          len(hits),
			"best_sku":              best.SKU,
			"best_price":            best.Price,
			"best_name":             best.Name,
			"best_brand":            best.Brand,
			"price_as_of":           time.Now().UTC().Format("2006-01-02"),
			"prices_found":          hitsToMaps(hits),
			"commercial_references": refs,
			"note":                  "Live AbilityOne.com catalog pricing via ccstoreui search API. Best federal-channel list price for AbilityOne items.",
		},
		QualityScore: 0.95,
		CreatedBy:    "abilityone-commerce-v0.1",
	}
	return []models.DataSnapshot{snap}, nil
}

// AbilityOneCommerceHit is one catalog row from AbilityOne.com search.
type AbilityOneCommerceHit struct {
	SKU       string
	Name      string
	Brand     string
	UPC       string
	Price     float64
	ListPrice float64
}

// SearchAbilityOneCommerce queries AbilityOne.com guided search for a term
// (dashed NSN preferred, or manufacturer SKU when known).
func SearchAbilityOneCommerce(ctx context.Context, term string) ([]AbilityOneCommerceHit, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, fmt.Errorf("empty search term")
	}

	u, err := url.Parse(abilityOneCommerceSearchURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("Ntt", term)
	q.Set("Nrpp", "24")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; InsightForge/1.0; +https://github.com/bmcelhaney/insight-forge)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.abilityone.com/")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("abilityone.com search HTTP %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}

	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse abilityone.com JSON: %w", err)
	}
	return parseAbilityOneCommercePayload(payload), nil
}

func parseAbilityOneCommercePayload(payload map[string]any) []AbilityOneCommerceHit {
	var hits []AbilityOneCommerceHit
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			attrs, _ := t["attributes"].(map[string]any)
			if attrs != nil {
				price := firstFloatAttr(attrs, "sku.activePrice", "sku.listPrice")
				sku := firstStringAttr(attrs, "sku.repositoryId", "product.repositoryId", "sku.skuId")
				if price > 0 && sku != "" {
					hits = append(hits, AbilityOneCommerceHit{
						SKU:       sku,
						Name:      firstStringAttr(attrs, "sku.displayName", "product.displayName"),
						Brand:     cleanAbilityOneBrand(firstStringAttr(attrs, "product.brand", "sku.brand")),
						UPC:       firstStringAttr(attrs, "sku.upc", "product.upc", "sku.UPC"),
						Price:     price,
						ListPrice: firstFloatAttr(attrs, "sku.listPrice", "sku.activePrice"),
					})
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(payload)

	// Dedupe by SKU keeping first (usually best rank).
	seen := map[string]bool{}
	out := make([]AbilityOneCommerceHit, 0, len(hits))
	for _, h := range hits {
		key := strings.ToUpper(strings.TrimSpace(h.SKU))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, h)
	}
	return out
}

func abilityOneSearchTerm(entityID string) string {
	d := digitsOnly(entityID)
	if len(d) >= 13 {
		d = d[len(d)-13:]
		return formatDashedNSN(d)
	}
	// Non-NSN commercial SKU / free text
	return strings.TrimSpace(entityID)
}

func formatDashedNSN(d string) string {
	d = digitsOnly(d)
	if len(d) != 13 {
		return d
	}
	// FSC(4)-group(2)-part(3)-serial(4)
	return d[0:4] + "-" + d[4:6] + "-" + d[6:9] + "-" + d[9:13]
}

func abilityOneIDsMatch(skuOrNSN, digits13 string) bool {
	a := digitsOnly(skuOrNSN)
	b := digitsOnly(digits13)
	if a == "" || b == "" {
		return false
	}
	if len(b) >= 13 {
		b = b[len(b)-13:]
	}
	if len(a) >= 13 {
		a = a[len(a)-13:]
	}
	return a == b || strings.HasSuffix(a, b) || strings.HasSuffix(b, a)
}

func abilityOneCommercePublicURL(term string) string {
	return "https://www.abilityone.com/search?q=" + url.QueryEscape(term)
}

func firstStringAttr(attrs map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := attrs[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return strings.TrimSpace(t)
			}
		case []any:
			if len(t) > 0 {
				if s, ok := t[0].(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		case []string:
			if len(t) > 0 && strings.TrimSpace(t[0]) != "" {
				return strings.TrimSpace(t[0])
			}
		}
	}
	return ""
}

func firstFloatAttr(attrs map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := attrs[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			if t > 0 {
				return t
			}
		case json.Number:
			if f, err := t.Float64(); err == nil && f > 0 {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil && f > 0 {
				return f
			}
		case []any:
			if len(t) > 0 {
				switch x := t[0].(type) {
				case float64:
					if x > 0 {
						return x
					}
				case string:
					if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil && f > 0 {
						return f
					}
				}
			}
		case []string:
			if len(t) > 0 {
				if f, err := strconv.ParseFloat(strings.TrimSpace(t[0]), 64); err == nil && f > 0 {
					return f
				}
			}
		}
	}
	return 0
}

func cleanAbilityOneBrand(s string) string {
	s = strings.TrimSpace(s)
	// Strip HTML entities common in AbilityOne brand names.
	repl := []struct{ old, new string }{
		{"&reg;", ""}, {"&trade;", ""}, {"&amp;", "&"},
		{"®", ""}, {"™", ""},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return strings.TrimSpace(s)
}

func formatMoney(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func hitsToMaps(hits []AbilityOneCommerceHit) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"sku":        h.SKU,
			"price":      h.Price,
			"list_price": h.ListPrice,
			"name":       h.Name,
			"brand":      h.Brand,
			"upc":        h.UPC,
		})
	}
	return out
}

func truncateForLog(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
