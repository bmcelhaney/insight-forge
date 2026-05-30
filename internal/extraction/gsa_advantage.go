package extraction

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bmcelhaney/insight-forge/internal/models"
)

// GSAAdvantageExtractor scrapes GSA Advantage for real pricing data,
// with special focus on AbilityOne (JWOD) items using cat=ADV.JWOD.
type GSAAdvantageExtractor struct{}

func NewGSAAdvantageExtractor() *GSAAdvantageExtractor {
	return &GSAAdvantageExtractor{}
}

func (g *GSAAdvantageExtractor) SourceCode() string { return "GSA_ADVANTAGE" }

func (g *GSAAdvantageExtractor) Fetch(ctx context.Context, entityID string, params map[string]string) ([]models.DataSnapshot, error) {
	select {
	case <-time.After(250 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	prices, err := g.scrapePricing(ctx, entityID)
	if err != nil {
		// Return empty but valid snapshot on scrape failure (graceful for demo)
		return []models.DataSnapshot{{
			EntityID:   entityID,
			SourceCode: "GSA_ADVANTAGE",
			SnapshotAt: time.Now(),
			RawResponse: map[string]any{
				"note":  "GSA Advantage scrape failed or no results",
				"error": err.Error(),
			},
			QualityScore: 0.3,
			CreatedBy:    "gsa-advantage-scraper-v0.1",
		}}, nil
	}

	snap := models.DataSnapshot{
		EntityID:   entityID,
		SourceCode: "GSA_ADVANTAGE",
		SnapshotAt: time.Now(),
		RawResponse: map[string]any{
			"prices_found": prices,
			"note":         "Real-time pricing scraped from GSA Advantage (ADV.JWOD category)",
			"search_url":   "https://www.gsaadvantage.gov (POST with cat=ADV.JWOD)",
		},
		QualityScore: 0.9,
		CreatedBy:    "gsa-advantage-scraper-v0.1",
	}

	return []models.DataSnapshot{snap}, nil
}

func (g *GSAAdvantageExtractor) scrapePricing(ctx context.Context, nsn string) ([]map[string]any, error) {
	endpoint := "https://www.gsaadvantage.gov/advantage/search/searchAdv.do"

	form := url.Values{}
	form.Set("cat", "ADV.JWOD") // AbilityOne / JWOD category
	form.Set("searchText", nsn) // NSN as the search term
	form.Set("q", nsn)          // backup common param

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; InsightForge/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	htmlStr := string(body)

	// Extract prices using common patterns on GSA Advantage
	prices := extractPricesFromHTML(htmlStr)

	if len(prices) == 0 {
		return nil, fmt.Errorf("no pricing elements found for NSN %s in JWOD results", nsn)
	}

	return prices, nil
}

// extractPricesFromHTML uses regex + simple heuristics for price patterns
// (mimics CSS selectors .price, .unit-price, [data-price] etc.)
func extractPricesFromHTML(html string) []map[string]any {
	var results []map[string]any

	// Common price patterns on GSA Advantage
	priceRegex := regexp.MustCompile(`(?i)(?:\$|USD)?\s*([0-9,]+\.\d{2})`)

	// Look for elements that look like price containers
	// This is a lightweight approximation of CSS selector scraping
	priceContainers := regexp.MustCompile(`(?i)(?:class=["'][^"']*(?:price|unit-price|cost)[^"']*["']|data-price=["'][^"']*["'])`)

	matches := priceRegex.FindAllStringSubmatch(html, -1)
	containerMatches := priceContainers.FindAllString(html, -1)

	for i, m := range matches {
		if i >= 5 { // limit results
			break
		}
		priceStr := m[1]
		priceStr = strings.ReplaceAll(priceStr, ",", "")

		result := map[string]any{
			"price":    priceStr,
			"currency": "USD",
		}

		// Try to associate with nearby text (very basic)
		if i < len(containerMatches) {
			result["context"] = strings.TrimSpace(containerMatches[i])
		}

		results = append(results, result)
	}

	return results
}